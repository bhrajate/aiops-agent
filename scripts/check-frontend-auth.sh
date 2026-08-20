#!/usr/bin/env bash
# 验证前端 dev server 经代理与认证后端打通:登录 + 带 token 拉数据 + SSE。
set -u
ROOT="/home/glory/code/ai-generate/aiops"
export AIOPS_DB_DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}"
export AIOPS_KAFKA_BROKERS="localhost:19092" AIOPS_TEMPORAL_HOSTPORT="localhost:7233"
export AIOPS_CLUSTER_AGENT_URL="http://localhost:9100"
export AIOPS_AUTH_MODE="hs256" AIOPS_AUTH_HS256_SECRET="dev-secret"
export AIOPS_WEBHOOK_SECRET="webhook-dev-secret"

for p in $(fuser 8088/tcp 8090/tcp 5173/tcp 2>/dev/null); do kill "$p" 2>/dev/null; done; sleep 1
"$ROOT/control-plane/bin/control-plane" > /tmp/fe-auth-cp.log 2>&1 & CP=$!
VITE=""
cleanup(){ kill $CP 2>/dev/null; [ -n "$VITE" ] && kill $VITE 2>/dev/null; pkill -f "vite.*5173" 2>/dev/null; wait 2>/dev/null; }
trap cleanup EXIT
for i in $(seq 1 15); do curl -sf localhost:8088/healthz >/dev/null 2>&1 && break; sleep 1; done

( cd "$ROOT/frontend" && exec npm run dev -- --host 127.0.0.1 --port 5173 ) > /tmp/fe-auth-vite.log 2>&1 & VITE=$!
for i in $(seq 1 40); do curl -sf http://127.0.0.1:5173/ >/dev/null 2>&1 && break; sleep 1; done

P=http://127.0.0.1:5173
pass=0; fail=0
check(){ if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; pass=$((pass+1)); else echo "  ✗ $1 期望 $2 实得 $3"; fail=$((fail+1)); fi; }

echo "=== 经前端代理的认证链路 ==="
check "首页 200" 200 "$(curl -s -o /dev/null -w '%{http_code}' $P/)"
check "未认证 /v1/incidents → 401" 401 "$(curl -s -o /dev/null -w '%{http_code}' $P/v1/incidents)"
TOK=$(curl -s $P/v1/auth/login -H 'Content-Type: application/json' -d '{"username":"alice","password":"alice-pass"}' | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))')
check "经代理登录拿到 token" "1" "$([ -n "$TOK" ] && echo 1 || echo 0)"
check "带 token 经代理 → 200" 200 "$(curl -s -o /dev/null -w '%{http_code}' $P/v1/incidents -H "Authorization: Bearer $TOK")"
check "/v1/auth/me 经代理 → 200" 200 "$(curl -s -o /dev/null -w '%{http_code}' $P/v1/auth/me -H "Authorization: Bearer $TOK")"

echo ""; echo "RESULT: pass=$pass fail=$fail"; [ "$fail" = 0 ] && echo "FRONTEND-AUTH OK" || echo "FAIL"