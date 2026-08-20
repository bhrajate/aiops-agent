#!/usr/bin/env bash
# 验证 N1:相关性合并受时间窗约束
#   窗口内 → 同 namespace 新故障合并进同一 incident
#   超窗   → 陈旧 incident 自动 resolved,新故障新建 incident(不再无限吸收)
set -u
cd /home/glory/code/ai-generate/aiops
BASE=http://localhost:8088
COMPOSE=deploy/docker-compose.yml
# 由脚本自身位置推导仓库根:比相对路径稳,任意 cwd 调用都对。
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 查库走 lib/db.sh:连不上或 SQL 出错时立刻终止,
# 而不是让断言收到空串然后照着空数据打分(见该文件顶部注释)。
source "$ROOT/scripts/lib/db.sh"
q(){ dbq "$1"; }
sig(){ # deployment
  local BODY="{\"alerts\":[{\"status\":\"firing\",\"labels\":{\"alertname\":\"HighLatency\",\"severity\":\"warning\",\"namespace\":\"payment\",\"deployment\":\"$1\",\"cluster\":\"prod-cn-1\",\"rule_id\":\"r-$1\"},\"startsAt\":\"2026-07-27T02:00:00Z\"}]}"
  local S=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
  curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $S" -d "$BODY"
}
pass=0; fail=0
ck(){ if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; pass=$((pass+1)); else echo "  ✗ $1 期望 $2 实得 $3"; fail=$((fail+1)); fi; }

# 前置检查 + 清库:本脚本不自己起后端;且断言用的是**绝对计数**,
# 若不清空,上一个脚本的残留会让计数整体偏移(表现为"逻辑回归"的假象)。
if ! curl -sf "$BASE/healthz" >/dev/null 2>&1; then
  echo "后端未在 $BASE 运行。用 ./scripts/with-backend.sh $0 运行本脚本。" >&2
  exit 2
fi
q "TRUNCATE signals, alert_groups, incidents, investigations, evidence, hypotheses,
    investigation_events, human_feedback, outbox, audit_log CASCADE;" >/dev/null

echo "=== 窗口内:checkout + cart 应合并为 1 个 incident ==="
sig checkout; sleep 2; sig cart; sleep 3
ck "incident 数 = 1(合并)" 1 "$(q 'select count(*) from incidents;')"
ck "blast.services = 2" 2 "$(q "select (blast_radius->>'services') from incidents where status='open';")"

echo "=== 人为把该 incident 的 updated_at 推到窗口外(模拟陈旧:处理时间)==="
dbx "UPDATE incidents SET updated_at = now() - interval '2 hours' WHERE status='open';"
dbx "UPDATE alert_groups SET updated_at = now() - interval '2 hours' WHERE status='open';"

echo "=== 超窗后新故障 orders:应新建 incident,旧的自动 resolved ==="
sig orders; sleep 3
ck "incident 总数 = 2(新建而非吸收)" 2 "$(q 'select count(*) from incidents;')"
ck "陈旧 incident 已 resolved" 1 "$(q "select count(*) from incidents where status='resolved';")"
ck "新 incident 活跃" 1 "$(q "select count(*) from incidents where status='open';")"
ck "新 incident 只含 orders(services=1)" 1 "$(q "select (blast_radius->>'services') from incidents where status='open';")"

echo ""; echo "RESULT: pass=$pass fail=$fail"; [ "$fail" = 0 ] && echo "CORRELATION-WINDOW OK" || echo "FAILURES"