#!/usr/bin/env bash
# 验证 N1:相关性合并受时间窗约束
#   窗口内 → 同 namespace 新故障合并进同一 incident
#   超窗   → 陈旧 incident 自动 resolved,新故障新建 incident(不再无限吸收)
set -u
cd /home/glory/code/ai-generate/aiops
BASE=http://localhost:8088
COMPOSE=deploy/docker-compose.yml
q(){ docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -t -c "$1" 2>/dev/null | tr -d ' ' | sed '/^$/d'; }
sig(){ # deployment
  local BODY="{\"alerts\":[{\"status\":\"firing\",\"labels\":{\"alertname\":\"HighLatency\",\"severity\":\"warning\",\"namespace\":\"payment\",\"deployment\":\"$1\",\"cluster\":\"prod-cn-1\",\"rule_id\":\"r-$1\"},\"startsAt\":\"2026-07-27T02:00:00Z\"}]}"
  local S=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
  curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $S" -d "$BODY"
}
pass=0; fail=0
ck(){ if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; pass=$((pass+1)); else echo "  ✗ $1 期望 $2 实得 $3"; fail=$((fail+1)); fi; }

echo "=== 窗口内:checkout + cart 应合并为 1 个 incident ==="
sig checkout; sleep 2; sig cart; sleep 3
ck "incident 数 = 1(合并)" 1 "$(q 'select count(*) from incidents;')"
ck "blast.services = 2" 2 "$(q "select (blast_radius->>'services') from incidents where status='open';")"

echo "=== 人为把该 incident 的 updated_at 推到窗口外(模拟陈旧:处理时间)==="
docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -c \
  "UPDATE incidents SET updated_at = now() - interval '2 hours' WHERE status='open';" >/dev/null 2>&1
docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -c \
  "UPDATE alert_groups SET updated_at = now() - interval '2 hours' WHERE status='open';" >/dev/null 2>&1

echo "=== 超窗后新故障 orders:应新建 incident,旧的自动 resolved ==="
sig orders; sleep 3
ck "incident 总数 = 2(新建而非吸收)" 2 "$(q 'select count(*) from incidents;')"
ck "陈旧 incident 已 resolved" 1 "$(q "select count(*) from incidents where status='resolved';")"
ck "新 incident 活跃" 1 "$(q "select count(*) from incidents where status='open';")"
ck "新 incident 只含 orders(services=1)" 1 "$(q "select (blast_radius->>'services') from incidents where status='open';")"

echo ""; echo "RESULT: pass=$pass fail=$fail"; [ "$fail" = 0 ] && echo "CORRELATION-WINDOW OK" || echo "FAILURES"