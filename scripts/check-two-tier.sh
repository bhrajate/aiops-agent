#!/usr/bin/env bash
# 验证两层聚合模型(优化②):
#   1. 同资源重复告警 → 同一 alert_group(去重),incident 不新增
#   2. 同 namespace 不同资源 → 合并进同一 incident,services 增长(聚合)
#   3. 单个资源 resolved → 只关掉该 group,incident 仍活跃
#   4. 全部 resolved → incident 才 resolved
set -u
ROOT="/home/glory/code/ai-generate/aiops"
BASE=http://localhost:8088
COMPOSE="$ROOT/deploy/docker-compose.yml"

sig(){ # deployment [status] [startsAt]
  # 第三个参数是 startsAt:同一资源的**不同故障轮次**用不同 startsAt 区分。
  # 完全相同的 startsAt 表示同一次通知的重投递,会被 F5 的幂等去重吃掉 ——
  # 那是正确行为,所以要测"重投不涨"就别改 startsAt,要测"新一轮要涨"就改。
  local st="${2:-firing}"
  local starts="${3:-2026-07-27T02:00:00Z}"
  local BODY="{\"alerts\":[{\"status\":\"$st\",\"labels\":{\"alertname\":\"HighLatency\",\"severity\":\"warning\",\"namespace\":\"payment\",\"deployment\":\"$1\",\"cluster\":\"prod-cn-1\",\"rule_id\":\"r-$1\"},\"startsAt\":\"$starts\",\"endsAt\":\"2026-07-27T03:00:00Z\"}]}"
  local SIG=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
  curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $SIG" -d "$BODY"
}
q(){ docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -t -c "$1" 2>/dev/null | tr -d ' ' | sed '/^$/d'; }

pass=0; fail=0
ck(){ if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; pass=$((pass+1)); else echo "  ✗ $1 期望 $2 实得 $3"; fail=$((fail+1)); fi; }

# 前置检查:本脚本**不自己起后端**,依赖已在 8088 运行的实例。
# 若没有,curl 会静默失败,而断言仍会照着库里的**残留数据**打分——
# 那样得到的通过/失败都没有意义(曾因此误判为代码回归)。
if ! curl -sf "$BASE/healthz" >/dev/null 2>&1; then
  echo "后端未在 $BASE 运行。先跑 ./scripts/prod-e2e.sh 或 make cp-run,再执行本脚本。" >&2
  exit 2
fi
# 清空相关表,确保断言基于本次运行而非上一次的残留。
q "TRUNCATE signals, alert_groups, incidents, investigations, evidence, hypotheses,
    investigation_events, human_feedback, outbox, audit_log CASCADE;" >/dev/null

echo "=== 1) checkout 首次告警 ==="
sig checkout; sleep 3
ck "incident 数 = 1" 1 "$(q 'select count(*) from incidents;')"
ck "alert_group 数 = 1" 1 "$(q 'select count(*) from alert_groups;')"
ck "blast.services = 1" 1 "$(q "select (blast_radius->>'services') from incidents;")"

echo "=== 2) checkout 重复告警(去重:group 不增、incident 不增)==="
# 完全相同的通知重投一次。F5 之后这会被幂等去重吃掉,signal_count **不涨** ——
# 原断言期望 2,那记录的是修复前的行为:随重投递虚增的计数会误触发
# EvaluateAuto 的 signal_burst 判据。
sig checkout; sleep 3
ck "incident 仍 1" 1 "$(q 'select count(*) from incidents;')"
ck "alert_group 仍 1" 1 "$(q 'select count(*) from alert_groups;')"
ck "重投递不涨 signal_count(F5 幂等)" 1 "$(q "select signal_count from alert_groups where resource_ref->>'name'='checkout';")"

echo "=== 2b) checkout 新一轮故障(不同 startsAt)→ signal_count 应涨 ==="
# 反向属性:去重不能过度。同资源的新一轮故障是新事实,必须计入。
sig checkout firing "2026-07-27T05:00:00Z"; sleep 3
ck "alert_group 仍 1(同资源同规则)" 1 "$(q 'select count(*) from alert_groups;')"
ck "新一轮故障使 signal_count = 2" 2 "$(q "select signal_count from alert_groups where resource_ref->>'name'='checkout';")"

echo "=== 3) cart 告警(聚合:同 namespace 合并进同一 incident)==="
sig cart; sleep 3
ck "incident 仍 1(合并而非新建)" 1 "$(q 'select count(*) from incidents;')"
ck "alert_group 增到 2" 2 "$(q 'select count(*) from alert_groups;')"
ck "blast.services = 2(影响面扩大可见)" 2 "$(q "select (blast_radius->>'services') from incidents;")"
ck "affected_resources 含 2 个资源" 2 "$(q "select jsonb_array_length(affected_resources) from incidents;")"

echo "=== 4) cart 恢复(只关该 group,incident 仍活跃)==="
sig cart resolved; sleep 3
ck "incident 仍 open" open "$(q 'select status from incidents;')"
ck "cart group resolved" resolved "$(q "select status from alert_groups where resource_ref->>'name'='cart';")"
ck "blast.services 回落到 1" 1 "$(q "select (blast_radius->>'services') from incidents;")"

echo "=== 5) checkout 也恢复(incident 随之 resolved)==="
sig checkout resolved; sleep 3
ck "incident resolved" resolved "$(q 'select status from incidents;')"

echo ""; echo "RESULT: pass=$pass fail=$fail"; [ "$fail" = 0 ] && echo "TWO-TIER OK" || echo "FAILURES"