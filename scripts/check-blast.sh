#!/usr/bin/env bash
# 验证 P0 相关性影响面:同 namespace 内第二个服务故障 → blast_radius.services 增长。
set -u
ROOT="/home/glory/code/ai-generate/aiops"
BASE=http://localhost:8088
export AIOPS_DB_DSN="postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable" AIOPS_KAFKA_BROKERS="localhost:19092" AIOPS_TEMPORAL_HOSTPORT="localhost:7233" AIOPS_CLUSTER_AGENT_URL="http://localhost:9100" AIOPS_AUTH_MODE="hs256" AIOPS_AUTH_HS256_SECRET="dev-secret" AIOPS_INTERNAL_TOKEN="internal-dev-token" AIOPS_WEBHOOK_SECRET="webhook-dev-secret"

sig(){ # alertname deployment
  local BODY="{\"alerts\":[{\"status\":\"firing\",\"labels\":{\"alertname\":\"$1\",\"severity\":\"warning\",\"namespace\":\"payment\",\"deployment\":\"$2\",\"cluster\":\"prod-cn-1\",\"rule_id\":\"r-$2\"},\"startsAt\":\"2026-07-27T02:00:00Z\"}]}"
  local SIG=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
  curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $SIG" -d "$BODY"
}

echo "=== 服务1 checkout 故障 ==="; sig "HighLatency" "checkout"; sleep 3
echo "checkout incident blast:"
docker compose -f "$ROOT/deploy/docker-compose.yml" exec -T postgres psql -U aiops -d aiops -t -c \
  "select blast_radius from incidents where affected_resources->0->>'name'='checkout';" 2>/dev/null | sed '/^$/d'

echo "=== 服务2 cart 故障(同 namespace)==="; sig "HighLatency" "cart"; sleep 3
echo "cart incident blast(应 services>=2):"
docker compose -f "$ROOT/deploy/docker-compose.yml" exec -T postgres psql -U aiops -d aiops -t -c \
  "select affected_resources->0->>'name', blast_radius from incidents where affected_resources->0->>'name' in ('checkout','cart') order by 1;" 2>/dev/null | sed '/^$/d'

echo "=== 判定 ==="
SVC=$(docker compose -f "$ROOT/deploy/docker-compose.yml" exec -T postgres psql -U aiops -d aiops -t -c \
  "select (blast_radius->>'services')::int from incidents where affected_resources->0->>'name'='cart';" 2>/dev/null | tr -d ' \n')
if [ "${SVC:-0}" -ge 2 ]; then echo "PASS: cart incident blast.services=$SVC (影响面扩大被捕获)"; else echo "FAIL: services=$SVC"; fi