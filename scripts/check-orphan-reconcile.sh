#!/usr/bin/env bash
# 验证 A2:孤儿调查(phase=queued 且无 run_id)被对账补偿。
# 模拟崩溃窗口:直接插入一条 queued/无 run_id 的调查,看 Reconciler 是否拉起工作流。
set -u
cd /home/glory/code/ai-generate/aiops
BASE=http://localhost:8088
COMPOSE=deploy/docker-compose.yml
q(){ docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -t -c "$1" 2>/dev/null | tr -d ' ' | sed '/^$/d'; }

pass=0; fail=0
ck(){ if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; pass=$((pass+1)); else echo "  ✗ $1 期望 $2 实得 $3"; fail=$((fail+1)); fi; }

# 先造一个 incident(供孤儿调查引用)
BODY='{"alerts":[{"status":"firing","labels":{"alertname":"HighLatency","severity":"warning","namespace":"payment","deployment":"checkout","cluster":"prod-cn-1","rule_id":"r-orphan"},"startsAt":"2026-07-27T02:00:00Z"}]}'
S=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $S" -d "$BODY"
sleep 4
INC=$(q "select incident_id from incidents limit 1;")
echo "incident=$INC"
[ -z "$INC" ] && { echo "NO INCIDENT — abort"; exit 1; }

echo "=== 模拟崩溃窗口:插入 queued 且无 run_id 的孤儿调查(started_at 推到宽限期外)==="
docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -c \
  "INSERT INTO investigations (investigation_id, tenant_id, incident_id, incident_version, phase, budget, usage, started_at)
   VALUES ('inv-orphan-test','default','$INC',1,'queued','{\"max_duration_sec\":300,\"max_rounds\":3,\"max_tokens\":200000,\"max_cost_usd\":2,\"max_tool_calls\":20}','{}', now() - interval '10 minutes');" >/dev/null 2>&1

ck "孤儿已插入(queued,无 run_id)" 1 "$(q "select count(*) from investigations where investigation_id='inv-orphan-test' and phase='queued' and coalesce(run_id,'')='';")"

echo "=== 等待对账(间隔 10s,最多 40s)==="
for i in $(seq 1 8); do
  RUN=$(q "select coalesce(run_id,'') from investigations where investigation_id='inv-orphan-test';")
  [ -n "$RUN" ] && break
  sleep 5
done
ck "孤儿已被补偿(拿到 run_id)" 1 "$([ -n "$(q "select coalesce(run_id,'') from investigations where investigation_id='inv-orphan-test';")" ] && echo 1 || echo 0)"
ck "workflow_id 已回填" 1 "$([ -n "$(q "select coalesce(workflow_id,'') from investigations where investigation_id='inv-orphan-test';")" ] && echo 1 || echo 0)"
ck "审计有 investigation_reconcile ok" 1 "$(q "select count(*) from audit_log where action='investigation_reconcile' and result='ok';")"

echo ""; echo "RESULT: pass=$pass fail=$fail"; [ "$fail" = 0 ] && echo "ORPHAN-RECONCILE OK" || echo "FAILURES"