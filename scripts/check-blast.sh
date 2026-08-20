#!/usr/bin/env bash
# 验证 P0 相关性影响面:同 namespace 内第二个服务故障 → blast_radius.services 增长。
set -u
ROOT="/home/glory/code/ai-generate/aiops"
BASE=http://localhost:8088
export AIOPS_DB_DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}" AIOPS_KAFKA_BROKERS="localhost:19092" AIOPS_TEMPORAL_HOSTPORT="localhost:7233" AIOPS_CLUSTER_AGENT_URL="http://localhost:9100" AIOPS_AUTH_MODE="hs256" AIOPS_AUTH_HS256_SECRET="dev-secret" AIOPS_INTERNAL_TOKEN="internal-dev-token" AIOPS_WEBHOOK_SECRET="webhook-dev-secret"

sig(){ # alertname deployment
  local BODY="{\"alerts\":[{\"status\":\"firing\",\"labels\":{\"alertname\":\"$1\",\"severity\":\"warning\",\"namespace\":\"payment\",\"deployment\":\"$2\",\"cluster\":\"prod-cn-1\",\"rule_id\":\"r-$2\"},\"startsAt\":\"2026-07-27T02:00:00Z\"}]}"
  local SIG=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
  curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $SIG" -d "$BODY"
}

if ! curl -sf "$BASE/healthz" >/dev/null 2>&1; then
  echo "后端未在 $BASE 运行。用 ./scripts/with-backend.sh $0 运行本脚本。" >&2
  exit 2
fi
# 断言用绝对计数,必须先清库,否则上一个脚本的残留会让判定失真。
# 查库走 lib/db.sh:连不上或 SQL 出错立刻终止,不让断言照着残留数据打分。
source "$ROOT/scripts/lib/db.sh"
dbx "TRUNCATE signals, alert_groups, incidents, investigations, evidence, hypotheses,
   investigation_events, human_feedback, outbox, audit_log CASCADE;"

echo "=== 服务1 checkout 故障 ==="; sig "HighLatency" "checkout"; sleep 3
echo "checkout incident blast:"
dbq "select blast_radius from incidents where affected_resources->0->>'name'='checkout';"

echo "=== 服务2 cart 故障(同 namespace)==="; sig "HighLatency" "cart"; sleep 3
echo "cart incident blast(应 services>=2):"
dbq "select affected_resources->0->>'name', blast_radius from incidents where affected_resources->0->>'name' in ('checkout','cart') order by 1;"

echo "=== 判定 ==="
# 注意:两层聚合模型(优化②)下 cart 会**合并进** checkout 所在的 incident,
# 不再各自成 incident——所以不能按 affected_resources[0]='cart' 去找。
# 该 namespace 下唯一的活跃 incident 就是判定对象。
SVC=$(dbq "select (blast_radius->>'services')::int from incidents
    where status in ('open','acknowledged') and correlation_key like '%|payment';" 2>/dev/null | tr -d ' \n')
if [ "${SVC:-0}" -ge 2 ]; then
  echo "PASS: incident blast.services=$SVC (影响面扩大被捕获)"
else
  echo "FAIL: services=${SVC:-空}"
  exit 1
fi

# F3:services 与 resources / groups 语义不同,不能同值糊过去。
# 这里两个服务各一个 Deployment 资源,故三者都应为 2;
# 真正要防的是"同一服务多个 Pod 被算成多服务",由 model.ServiceKey 单测覆盖。
BR=$(dbq "select blast_radius from incidents
    where status in ('open','acknowledged') and correlation_key like '%|payment';" 2>/dev/null | tr -d ' \n')
for k in services resources groups namespaces; do
  echo "$BR" | grep -q "\"$k\"" || { echo "FAIL: blast_radius 缺少维度 $k($BR)"; exit 1; }
done
echo "PASS: blast_radius 四个维度齐备 $BR"