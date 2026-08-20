#!/usr/bin/env bash
# 认证/RBAC/ABAC/幂等/webhook 安全测试。PID 精确管理,避免 pkill 自匹配。
set -u
ROOT="/home/glory/code/ai-generate/aiops"
BASE=http://localhost:8088
export GOPROXY=https://goproxy.cn,direct
export AIOPS_DB_DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}"
export AIOPS_KAFKA_BROKERS="localhost:19092" AIOPS_TEMPORAL_HOSTPORT="localhost:7233"
export AIOPS_CLUSTER_AGENT_URL="http://localhost:9100"
export AIOPS_AUTH_MODE="hs256" AIOPS_AUTH_HS256_SECRET="dev-secret"
export AIOPS_INTERNAL_TOKEN="internal-dev-token" AIOPS_WEBHOOK_SECRET="webhook-dev-secret"

# 杀掉旧 control-plane(按可执行路径精确匹配,pgrep -f 但排除本脚本)
for p in $(pgrep -f "$ROOT/control-plane/bin/control-plane"); do kill "$p" 2>/dev/null; done
sleep 2

# 清库
# 查库走 lib/db.sh:连不上或 SQL 出错立刻终止,不让断言照着残留数据打分。
source "$ROOT/scripts/lib/db.sh"
dbx "TRUNCATE signals, incidents, investigations, evidence, hypotheses, investigation_events, human_feedback, outbox, audit_log, idempotency_keys, dead_letters CASCADE;"

# 启动
"$ROOT/control-plane/bin/control-plane" > /tmp/cp-auth.log 2>&1 &
CP=$!
for i in $(seq 1 15); do curl -sf $BASE/healthz >/dev/null 2>&1 && break; sleep 1; done
cleanup(){ kill $CP 2>/dev/null; }
trap cleanup EXIT

login(){ curl -s $BASE/v1/auth/login -H 'Content-Type: application/json' -d "{\"username\":\"$1\",\"password\":\"$2\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))'; }
code(){ curl -s -o /dev/null -w "%{http_code}" "$@"; }

ALICE=$(login alice alice-pass)
BOB=$(login bob bob-pass)
VIEWER=$(login viewer viewer-pass)

echo "=== 注入 signal(payment)==="
BODY='{"alerts":[{"status":"firing","labels":{"alertname":"HighErrorRate","severity":"critical","namespace":"payment","deployment":"checkout","cluster":"prod-cn-1","rule_id":"r-101"},"startsAt":"2026-07-26T10:00:00Z"}]}'
SIG=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $SIG" -d "$BODY"
sleep 3
INC=$(curl -s "$BASE/v1/incidents" -H "Authorization: Bearer $ALICE" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d["incidents"][0]["incident_id"] if d["incidents"] else "")')
echo "incident=$INC"

pass=0; fail=0
check(){ # desc expected actual
  if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; pass=$((pass+1)); else echo "  ✗ $1 期望 $2 实得 $3"; fail=$((fail+1)); fi
}

echo "=== 认证 ==="
check "未认证访问 → 401" 401 "$(code $BASE/v1/incidents)"
check "带 token 访问 → 200" 200 "$(code $BASE/v1/incidents -H "Authorization: Bearer $ALICE")"
check "错误密码 → 401" 401 "$(code $BASE/v1/auth/login -H 'Content-Type: application/json' -d '{"username":"alice","password":"x"}')"

echo "=== RBAC ==="
check "viewer 启动调查 → 403" 403 "$(code -X POST $BASE/v1/incidents/$INC/investigations -H "Authorization: Bearer $VIEWER")"
check "bob(oncall)启动调查 → 202" 202 "$(code -X POST $BASE/v1/incidents/$INC/investigations -H "Authorization: Bearer $BOB" -H 'Idempotency-Key: k1')"

echo "=== ABAC(bob 仅 payment/cart)==="
check "viewer 读 payment incident → 200" 200 "$(code $BASE/v1/incidents/$INC -H "Authorization: Bearer $VIEWER")"

echo "=== 幂等 ==="
R1=$(curl -s -X POST $BASE/v1/incidents/$INC/investigations -H "Authorization: Bearer $BOB" -H 'Idempotency-Key: k1' | python3 -c 'import sys,json;print(json.load(sys.stdin).get("investigation_id"))')
R2=$(curl -s -X POST $BASE/v1/incidents/$INC/investigations -H "Authorization: Bearer $BOB" -H 'Idempotency-Key: k1' | python3 -c 'import sys,json;print(json.load(sys.stdin).get("investigation_id"))')
check "同 Idempotency-Key 返回同一调查" "$R1" "$R2"

echo "=== webhook 签名 ==="
check "无签名 signal → 401" 401 "$(code -X POST $BASE/v1/signals -H 'Content-Type: application/json' -d '{"alerts":[]}')"

echo "=== 内部 API token ==="
check "内部 API 无 token → 401" 401 "$(code -X POST http://localhost:8090/internal/investigations/x/phase -H 'Content-Type: application/json' -d '{"phase":"planning"}')"
check "内部 API 带 token → 非401" 200 "$(code -X POST http://localhost:8090/internal/investigations/$R1/phase -H 'X-Internal-Token: internal-dev-token' -H 'Content-Type: application/json' -d '{"phase":"planning"}')"

echo ""
echo "RESULT: pass=$pass fail=$fail"
[ "$fail" = 0 ] && echo "ALL AUTH TESTS PASSED" || echo "SOME TESTS FAILED"