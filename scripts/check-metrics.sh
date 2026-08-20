#!/usr/bin/env bash
# 快速验证 control-plane /metrics 与 OTel 埋点。启动→注入→抓指标→关停。
set -u
ROOT="/home/glory/code/ai-generate/aiops"
export AIOPS_DB_DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}"
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

# 注入带签名的 signal。
#
# startsAt 必须**每次运行都不同**。此前写死成 2026-07-26T10:00:00Z,与
# check-auth.sh 用的是同一个 body —— 于是在非全新库上这条信号被 F5 幂等去重
# (signal_id = f(fingerprint, status, startsAt)),而 ingress 刻意**不给重投递计数**
# (见 ingress.go:114:否则计数器会随重投递虚增)。
# 结果:aiops_signals_ingested_total 永不出现在 /metrics 里(Prometheus 的
# 带标签计数器只在首次 +1 后才出现),末尾那句 grep 返回 1,整脚本 exit 1。
#
# 这个脚本此前在 ACCEPTANCE 里记的是 PASS —— 那只在全新库上成立。
STARTS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
BODY="{\"alerts\":[{\"status\":\"firing\",\"labels\":{\"alertname\":\"HighErrorRate\",\"severity\":\"critical\",\"namespace\":\"payment\",\"deployment\":\"checkout\",\"cluster\":\"prod-cn-1\",\"rule_id\":\"r-101\"},\"startsAt\":\"$STARTS\"}]}"
SIG=$(python3 -c "import hmac,hashlib,sys;print('sha256='+hmac.new(b'webhook-dev-secret',sys.argv[1].encode(),hashlib.sha256).hexdigest())" "$BODY")
CODE=$(curl -s -o /dev/null -w '%{http_code}' localhost:8088/v1/signals \
  -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $SIG" -d "$BODY")
if [ "$CODE" != "202" ]; then
  echo "FAIL: 注入 signal 返回 $CODE(期望 202)—— 后续指标断言无意义" >&2
  exit 1
fi
sleep 3

echo "=== /metrics 抓取(control-plane :8090)==="
curl -s localhost:8090/metrics | grep -E "^aiops_" | grep -vE "^#" | head -20
echo ""
echo "=== signal 计数应 >=1 ==="
# 显式判定而不是靠裸 grep 的退出码兜底:裸 grep 找不到时 exit 1,
# 但输出里没有任何一行说明"为什么没找到"。
INGESTED=$(curl -s localhost:8090/metrics | grep "^aiops_signals_ingested_total" | head -1)
if [ -z "$INGESTED" ]; then
  echo "FAIL: /metrics 里没有 aiops_signals_ingested_total。" >&2
  echo "      带标签的 Prometheus 计数器只在首次 +1 后出现 —— 说明这条信号" >&2
  echo "      根本没被计数(最可能是被幂等去重了,检查 startsAt 是否唯一)。" >&2
  exit 1
fi
echo "  PASS  $INGESTED"