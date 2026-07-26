#!/usr/bin/env bash
# 快速验证 control-plane /metrics 与 OTel 埋点。启动→注入→抓指标→关停。
set -u
ROOT="/home/glory/code/ai-generate/aiops"
export AIOPS_DB_DSN="postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable"
export AIOPS_KAFKA_BROKERS="localhost:19092" AIOPS_TEMPORAL_HOSTPORT="localhost:7233"
export AIOPS_CLUSTER_AGENT_URL="http://localhost:9100"
export AIOPS_AUTH_MODE="hs256" AIOPS_AUTH_HS256_SECRET="dev-secret"
export AIOPS_WEBHOOK_SECRET="webhook-dev-secret"

for p in $(fuser 8088/tcp 8090/tcp 2>/dev/null); do kill "$p" 2>/dev/null; done
sleep 1
"$ROOT/control-plane/bin/control-plane" > /tmp/cp-metrics.log 2>&1 &
CP=$!
trap 'kill $CP 2>/dev/null' EXIT
for i in $(seq 1 15); do curl -sf localhost:8088/healthz >/dev/null 2>&1 && break; sleep 1; done

# 注入带签名的 signal
BODY='{"alerts":[{"status":"firing","labels":{"alertname":"HighErrorRate","severity":"critical","namespace":"payment","deployment":"checkout","cluster":"prod-cn-1","rule_id":"r-101"},"startsAt":"2026-07-26T10:00:00Z"}]}'
SIG=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
curl -s -o /dev/null localhost:8088/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $SIG" -d "$BODY"
sleep 3

echo "=== /metrics 抓取(control-plane :8090)==="
curl -s localhost:8090/metrics | grep -E "^aiops_" | grep -vE "^#" | head -20
echo ""
echo "=== signal 计数应 >=1 ==="
curl -s localhost:8090/metrics | grep "aiops_signals_ingested_total"