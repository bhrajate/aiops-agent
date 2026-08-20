#!/usr/bin/env bash
# 验证 A2:孤儿调查(phase=queued 且无 run_id)被对账补偿。
# 模拟崩溃窗口:直接插入一条 queued/无 run_id 的调查,看 Reconciler 是否拉起工作流。
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
# 前置清库:本脚本第 37 行用**绝对计数**断言审计行数,上一次运行留下的记录
# 会让它变成 2 而误判失败。其余脚本都清库,这个漏了。
dbx "TRUNCATE signals, alert_groups, incidents, investigations, evidence, hypotheses,
   investigation_events, human_feedback, outbox, audit_log, idempotency_keys,
   dead_letters CASCADE;"

curl -sf "$BASE/healthz" >/dev/null 2>&1 || {
  echo "control-plane(:8088)未就绪 —— 请用 scripts/with-backend.sh 运行本脚本" >&2
  exit 2
}

pass=0; fail=0
ck(){ if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; pass=$((pass+1)); else echo "  ✗ $1 期望 $2 实得 $3"; fail=$((fail+1)); fi; }

# 先造一个 incident(供孤儿调查引用)
BODY='{"alerts":[{"status":"firing","labels":{"alertname":"HighLatency","severity":"warning","namespace":"payment","deployment":"checkout","cluster":"prod-cn-1","rule_id":"r-orphan"},"startsAt":"2026-07-27T02:00:00Z"}]}'
S=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $S" -d "$BODY"
sleep 4
# 按创建时间取,不用裸 limit 1(无 ORDER BY 时返回哪条由执行计划决定)
INC=$(q "select incident_id from incidents order by created_at desc limit 1;")
echo "incident=$INC"
[ -z "$INC" ] && { echo "NO INCIDENT — abort"; exit 1; }

echo "=== 模拟崩溃窗口:插入 queued 且无 run_id 的孤儿调查(started_at 推到宽限期外)==="
dbx "INSERT INTO investigations (investigation_id, tenant_id, incident_id, incident_version, phase, budget, usage, started_at)
   VALUES ('inv-orphan-test','default','$INC',1,'queued','{\"max_duration_sec\":300,\"max_rounds\":3,\"max_tokens\":200000,\"max_cost_usd\":2,\"max_tool_calls\":20}','{}', now() - interval '10 minutes');"

# 只断言"孤儿行已建立",不要求它此刻仍未被补偿:对账间隔仅 5s,
# 完全可能在这条断言执行前就已补偿完 —— 那是**成功**,不该判失败。
# 这条的作用是确认前置数据造好了,补偿本身由下面三条断言负责。
ck "孤儿行已建立" 1 "$(q "select count(*) from investigations where investigation_id='inv-orphan-test';")"

echo "=== 等待对账(with-backend.sh 设间隔 5s,最多等 40s)==="
for i in $(seq 1 8); do
  RUN=$(q "select coalesce(run_id,'') from investigations where investigation_id='inv-orphan-test';")
  [ -n "$RUN" ] && break
  sleep 5
done
ck "孤儿已被补偿(拿到 run_id)" 1 "$([ -n "$(q "select coalesce(run_id,'') from investigations where investigation_id='inv-orphan-test';")" ] && echo 1 || echo 0)"
ck "workflow_id 已回填" 1 "$([ -n "$(q "select coalesce(workflow_id,'') from investigations where investigation_id='inv-orphan-test';")" ] && echo 1 || echo 0)"
# 用 >=1 而非 ==1:一次运行内对账可能扫到多条(或重跑遗留),
# 断言的语义是"补偿被审计到了",不是"恰好一条"。
ck "审计有 investigation_reconcile ok" 1 "$([ "$(q "select count(*) from audit_log where action='investigation_reconcile' and result='ok';")" -ge 1 ] && echo 1 || echo 0)"

echo ""; echo "RESULT: pass=$pass fail=$fail"; [ "$fail" = 0 ] && echo "ORPHAN-RECONCILE OK" || echo "FAILURES"