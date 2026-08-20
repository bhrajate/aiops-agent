#!/usr/bin/env bash
# 验证 F5:Alertmanager 重投递不产生重复 signal、不虚增 signal_count。
#
# Alertmanager 是至少一次投递,重投递是**预期行为**而非异常
# (alertmanager#2768:重复的 resolved 通知带相同 startsAt/endsAt)。
#
# 旧实现给 signal_id 附加 randHex(4),使每次重投递都得到新 ID,
# `ON CONFLICT (signal_id) DO NOTHING` 永不冲突。后果不只是表里多几行:
# incidents.signal_count 随之虚增,进而误触发 EvaluateAuto 的
# signal_count>=3 分支 —— **一条告警重投三次就被当成"信号突发"**。
#
# 反向属性同样要验:firing→resolved→firing 必须是三条不同信号,
# 否则会丢掉"恢复"与"再次故障"。
#
# 自起 control-plane,不要用 with-backend.sh 包裹。
set -uo pipefail
cd "$(dirname "$0")/.."
COMPOSE=deploy/docker-compose.yml
PUB=8788
INT=8790
NS="f5-$$"          # 唯一 namespace:避免上一次运行的活跃 incident 干扰相关性合并
FP="fp$$abcdef"     # 唯一 fingerprint
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
info(){ echo "== $1"; }

for p in $PUB $INT; do
  if command -v fuser >/dev/null 2>&1 && fuser "$p/tcp" >/dev/null 2>&1; then
    echo "  端口 $p 被占用,先清理" >&2; exit 2
  fi
done

# 由脚本自身位置推导仓库根:比相对路径稳,任意 cwd 调用都对。
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 查库走 lib/db.sh:连不上或 SQL 出错时立刻终止,
# 而不是让断言收到空串然后照着空数据打分(见该文件顶部注释)。
source "$ROOT/scripts/lib/db.sh"
q(){ dbq "$1"; }
qx(){ dbx "$1"; }
info "构建"
( cd control-plane && go build -o /tmp/cp-f5 ./cmd/control-plane ) || exit 1

docker compose -f "$COMPOSE" up -d postgres redpanda >/dev/null 2>&1
for _ in $(seq 40); do
  docker compose -f "$COMPOSE" exec -T postgres pg_isready -U aiops -d aiops >/dev/null 2>&1 && break
  sleep 1
done

LOG=$(mktemp)
AIOPS_ENV=development AIOPS_ROLES="all" \
AIOPS_PUBLIC_ADDR=":$PUB" AIOPS_INTERNAL_ADDR=":$INT" \
AIOPS_DB_DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}" \
AIOPS_KAFKA_BROKERS="localhost:19092" \
AIOPS_INTERNAL_TOKEN=dev-token AIOPS_RETENTION_ENABLED=false \
/tmp/cp-f5 >"$LOG" 2>&1 &
CP_PID=$!
cleanup(){
  kill $CP_PID 2>/dev/null; wait $CP_PID 2>/dev/null
  db_purge_ns "$NS"
}
trap cleanup EXIT

ready=0
for _ in $(seq 40); do
  curl -sf --max-time 3 "http://127.0.0.1:$PUB/healthz" >/dev/null 2>&1 && { ready=1; break; }
  kill -0 $CP_PID 2>/dev/null || break
  sleep 0.5
done
[ "$ready" = 1 ] || { echo "control-plane 未就绪:" >&2; tail -20 "$LOG" >&2; exit 1; }

# post <status> <startsAt> —— 模拟 Alertmanager webhook(含 fingerprint)
post(){
  curl -s -o /dev/null --max-time 6 -X POST "http://127.0.0.1:$PUB/v1/signals" \
    -H 'Content-Type: application/json' \
    -d "{\"alerts\":[{\"status\":\"$1\",\"startsAt\":\"$2\",\"endsAt\":\"0001-01-01T00:00:00Z\",
         \"fingerprint\":\"$FP\",
         \"labels\":{\"alertname\":\"F5Dup\",\"namespace\":\"$NS\",\"deployment\":\"checkout\",\"severity\":\"warning\"}}]}"
}

info "1) 同一通知重投递 5 次"
for _ in 1 2 3 4 5; do post firing "2026-07-29T10:00:00Z"; done
sleep 6
N=$(q "SELECT count(*) FROM signals WHERE labels->>'namespace'='$NS';")
if [ "$N" = "1" ]; then
  ok "5 次重投递只落 1 条 signal"
else
  bad "5 次重投递落了 $N 条(期望 1)—— ON CONFLICT 未生效"
fi

info "2) signal_count 未被虚增"
SC=$(q "SELECT signal_count FROM incidents WHERE correlation_key LIKE '%|$NS';")
if [ "$SC" = "1" ]; then
  ok "incident.signal_count=1"
else
  bad "incident.signal_count=$SC(期望 1)—— 虚增会误触发 signal_burst"
fi

info "3) 反向属性:firing→resolved→firing 是三条不同信号"
post resolved "2026-07-29T10:00:00Z"
sleep 4
post firing "2026-07-29T11:30:00Z"   # 恢复后再次故障:新 startsAt
sleep 6
N2=$(q "SELECT count(*) FROM signals WHERE labels->>'namespace'='$NS';")
if [ "$N2" = "3" ]; then
  ok "三个不同事实各成一条($N2 条)"
else
  bad "期望 3 条,得 $N2 —— 只按 fingerprint 去重会丢掉恢复与再次故障"
fi

info "4) signal_id 不含随机成分(重投递第二轮仍不新增)"
for _ in 1 2 3; do post firing "2026-07-29T11:30:00Z"; done
sleep 6
N3=$(q "SELECT count(*) FROM signals WHERE labels->>'namespace'='$NS';")
[ "$N3" = "3" ] && ok "再投 3 次仍是 3 条" || bad "再投后变成 $N3 条(期望 3)"

info "5) signal_id 形态可复现(同输入同 ID)"
IDS=$(q "SELECT signal_id FROM signals WHERE labels->>'namespace'='$NS' ORDER BY signal_id;")
UNIQ=$(echo "$IDS" | sort -u | wc -l)
TOTAL=$(echo "$IDS" | wc -l)
[ "$UNIQ" = "$TOTAL" ] && ok "3 条 signal_id 互不相同($UNIQ 个)" || bad "存在重复 ID"
echo "$IDS" | grep -qE '^sig-[0-9a-f]{20}$' && ok "ID 形态为 sig-<20 位 hex>(无随机段)" \
  || bad "ID 形态异常:$(echo "$IDS" | head -1)"

echo ""
echo "RESULT: pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ] && echo "SIGNAL-IDEMPOTENCY OK" || echo "FAILURES"
[ "$FAIL" = 0 ]
