#!/usr/bin/env bash
# 验证 P4:队列积压指标真的出现在 /metrics 上,且**数据库不可用时缺失而非为 0**。
#
# 后一半是本项的核心。上报 0 会被读成"队列是空的",恰好在最需要告警时给出
# 虚假的正常;只有缺失才能让告警规则的 absent() 把"监控本身坏了"表达为
# 一个独立于"队列健康"的状态。所以脚本真的停掉 postgres 来验证。
#
# 自起一个 control-plane,不要用 with-backend.sh 包裹。
set -uo pipefail
cd "$(dirname "$0")/.."
COMPOSE=deploy/docker-compose.yml
PUB=8388
INT=8390
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
info(){ echo "== $1"; }
scrape(){ curl -s --max-time 8 "http://127.0.0.1:$INT/metrics"; }

for p in $PUB $INT; do
  if command -v fuser >/dev/null 2>&1 && fuser "$p/tcp" >/dev/null 2>&1; then
    echo "  端口 $p 被占用,先清理" >&2; exit 2
  fi
done

info "构建"
( cd control-plane && go build -o /tmp/cp-qm ./cmd/control-plane ) || exit 1

DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}"
# 由脚本自身位置推导仓库根:比相对路径稳,任意 cwd 调用都对。
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 查库走 lib/db.sh:连不上或 SQL 出错时立刻终止,
# 而不是让断言收到空串然后照着空数据打分(见该文件顶部注释)。
source "$ROOT/scripts/lib/db.sh"
q(){ dbq "$1"; }
info "造积压:3 条 pending(最老 20 分钟)+ 1 条 dead + 2 条死信"
q "DELETE FROM outbox WHERE topic LIKE 'qm-%'; DELETE FROM dead_letters WHERE topic LIKE 'qm-%';"
q "INSERT INTO outbox (topic,key,payload,status,created_at) VALUES
     ('qm-signals','a','{}','pending', now() - interval '20 minutes'),
     ('qm-signals','b','{}','pending', now()),
     ('qm-incidents','c','{}','failed', now() - interval '3 minutes'),
     ('qm-incidents','d','{}','dead',   now() - interval '1 hour');"
q "INSERT INTO dead_letters (topic,key,payload,error,attempts) VALUES
     ('qm-signals','e','{}','boom',5), ('qm-signals','f','{}','boom',5);"

LOG=$(mktemp)
AIOPS_ENV=development AIOPS_ROLES="api,internal" \
AIOPS_PUBLIC_ADDR=":$PUB" AIOPS_INTERNAL_ADDR=":$INT" \
AIOPS_DB_DSN="$DSN" AIOPS_INTERNAL_TOKEN=dev-token \
AIOPS_RETENTION_ENABLED=false \
/tmp/cp-qm >"$LOG" 2>&1 &
CP_PID=$!
restore(){
  kill $CP_PID 2>/dev/null; wait $CP_PID 2>/dev/null
  db_start
  for _ in $(seq 30); do
    docker compose -f "$COMPOSE" exec -T postgres pg_isready -U aiops -d aiops >/dev/null 2>&1 && break
    sleep 1
  done
  q "DELETE FROM outbox WHERE topic LIKE 'qm-%'; DELETE FROM dead_letters WHERE topic LIKE 'qm-%';"
}
trap restore EXIT

ready=0
for _ in $(seq 40); do
  curl -sf --max-time 3 "http://127.0.0.1:$PUB/healthz" >/dev/null 2>&1 && { ready=1; break; }
  kill -0 $CP_PID 2>/dev/null || break
  sleep 0.5
done
if [ "$ready" != 1 ]; then
  echo "control-plane 未就绪,日志:" >&2; tail -25 "$LOG" >&2; exit 1
fi

info "1) 指标出现在 /metrics 上"
M=$(scrape)
echo "$M" | grep -q '^aiops_outbox_pending{' && ok "aiops_outbox_pending 已上报" \
  || bad "缺少 aiops_outbox_pending"
echo "$M" | grep -q '^aiops_outbox_oldest_pending_age_seconds ' && ok "oldest_pending_age_seconds 已上报" \
  || bad "缺少 oldest_pending_age_seconds"
echo "$M" | grep -q '^aiops_outbox_dead ' && ok "aiops_outbox_dead 已上报" || bad "缺少 aiops_outbox_dead"
echo "$M" | grep -q '^aiops_dead_letters_pending{' && ok "dead_letters_pending 已上报" \
  || bad "缺少 dead_letters_pending"
echo "$M" | grep -q '^aiops_queue_scrape_failed 0' && ok "scrape_failed=0" || bad "scrape_failed 应为 0"

info "2) 数值正确"
AGE=$(echo "$M" | awk '/^aiops_outbox_oldest_pending_age_seconds /{print int($2)}')
if [ -n "$AGE" ] && [ "$AGE" -ge 1100 ] && [ "$AGE" -le 1300 ]; then
  ok "最老待投递年龄约 20 分钟($AGE s)"
else
  bad "最老待投递年龄期望约 1200s,得 '$AGE'"
fi
# failed 必须计入待投递:与 DrainOutbox 取件条件一致,漏掉会让"卡在重试"不可见
PI=$(echo "$M" | awk -F'[ ]' '/^aiops_outbox_pending\{topic="qm-incidents"\}/{print int($2)}')
[ "$PI" = "1" ] && ok "failed 计入待投递(卡在重试可见)" || bad "qm-incidents 待投递期望 1,得 '$PI'"
DEAD=$(echo "$M" | awk '/^aiops_outbox_dead /{print int($2)}')
[ "$DEAD" -ge 1 ] 2>/dev/null && ok "dead 存量 >=1($DEAD)" || bad "dead 期望 >=1,得 '$DEAD'"

info "3) 数据库不可用 —— 本项核心:指标必须缺失而非为 0"
db_stop
sleep 3
M2=$(scrape)
if echo "$M2" | grep -q '^aiops_queue_scrape_failed 1'; then
  ok "scrape_failed=1"
else
  bad "scrape_failed 应为 1,实际:$(echo "$M2" | grep scrape_failed || echo '缺失')"
fi
GONE=1
for n in aiops_outbox_pending aiops_outbox_oldest_pending_age_seconds aiops_outbox_dead aiops_dead_letters_pending; do
  if echo "$M2" | grep -q "^$n"; then
    bad "$n 在查询失败时仍上报(0 会被读成'队列是空的')"
    GONE=0
  fi
done
[ "$GONE" = 1 ] && ok "四个队列 gauge 全部缺失(absent() 可用于告警)"
# 计数器类指标不该跟着消失,否则说明整个 /metrics 都挂了而非只是队列查询失败
echo "$M2" | grep -q '^aiops_signals_ingested_total\|^go_goroutines' \
  && ok "其余指标仍正常(只是队列查询失败,不是端点挂了)" \
  || bad "整个 /metrics 都不可用了"

info "4) 数据库恢复后指标回来"
db_start
BACK=0
for _ in $(seq 40); do
  if scrape | grep -q '^aiops_outbox_oldest_pending_age_seconds '; then BACK=1; break; fi
  sleep 1
done
[ "$BACK" = 1 ] && ok "指标自动恢复(无需重启进程)" || bad "指标未恢复"

echo ""
echo "RESULT: pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ] && echo "QUEUE-METRICS OK" || echo "FAILURES"
[ "$FAIL" = 0 ]
