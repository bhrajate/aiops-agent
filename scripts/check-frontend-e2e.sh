#!/usr/bin/env bash
# 浏览器渲染验证:起后端 + vite dev,用 Playwright 打开真实页面。
#
# 补的是一个具体的盲区:此前"前端能用"的证据只有 tsc 通过 + 43 项纯函数单测 +
# 产物 CSS 里有 token class。这三样都不能回答"页面打开是什么样" ——
# 样式写错、运行时崩、无权入口没藏住,全都能通过构建。
#
# 不在 CI 里跑:要额外下 ~170MB Chromium。见 e2e/../playwright.config.ts 的说明。
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/lib/db.sh"

PUB="${AIOPS_E2E_PUB_PORT:-8088}"
VITE_PORT="${AIOPS_E2E_VITE_PORT:-5173}"

command -v curl >/dev/null 2>&1 || { echo "需要 curl" >&2; exit 2; }
if [ ! -d "$HOME/.cache/ms-playwright" ]; then
  echo "缺 Chromium。先跑:cd frontend && npx playwright install chromium" >&2
  exit 2
fi

PIDS=()
cleanup(){
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null; done
  wait 2>/dev/null
}
trap cleanup EXIT

echo "== 构建后端(验证当前源码)"
( cd "$ROOT/control-plane" && go build -o bin/control-plane ./cmd/control-plane ) || exit 1
( cd "$ROOT/cluster-agent" && go build -o bin/cluster-agent ./cmd/cluster-agent ) || exit 1

echo "== 起 cluster-agent + control-plane"
AIOPS_LISTEN_ADDR=":9100" "$ROOT/cluster-agent/bin/cluster-agent" > /tmp/e2e-agent.log 2>&1 &
PIDS+=($!)
AIOPS_ENV=development \
AIOPS_PUBLIC_ADDR=":$PUB" AIOPS_INTERNAL_ADDR=":8090" \
AIOPS_AUTH_MODE=hs256 AIOPS_AUTH_HS256_KEY=dev-secret \
AIOPS_WEBHOOK_SECRET=webhook-dev-secret AIOPS_INTERNAL_TOKEN=dev-token \
AIOPS_CORS_ORIGINS="http://127.0.0.1:$VITE_PORT" \
"$ROOT/control-plane/bin/control-plane" > /tmp/e2e-cp.log 2>&1 &
PIDS+=($!)

ready=0
for _ in $(seq 40); do
  [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$PUB/healthz")" = "200" ] \
    && { ready=1; break; }
  sleep 0.5
done
if [ "$ready" != 1 ]; then
  echo "control-plane 未就绪" >&2
  if grep -q 'bind: address already in use' /tmp/e2e-cp.log; then
    echo "  原因:端口被占用。换端口:AIOPS_E2E_PUB_PORT=18088 $0" >&2
  fi
  tail -20 /tmp/e2e-cp.log >&2
  exit 1
fi
echo "  control-plane ready (:$PUB, db=$(db_mode))"

echo "== 起 vite dev (:$VITE_PORT),代理指向 :$PUB"
# 必须把代理指到实际后端端口。vite.config.ts 默认 8088,而这里可能换了端口 ——
# 不传的话前端所有 /v1 请求都连不上,页面表现为一直加载中,而不是报错。
( cd "$ROOT/frontend" && AIOPS_API_TARGET="http://127.0.0.1:$PUB" \
    exec npx vite --host 127.0.0.1 --port "$VITE_PORT" --strictPort ) \
  > /tmp/e2e-vite.log 2>&1 &
PIDS+=($!)
vready=0
for _ in $(seq 60); do
  curl -sf "http://127.0.0.1:$VITE_PORT/" >/dev/null 2>&1 && { vready=1; break; }
  sleep 0.5
done
[ "$vready" = 1 ] || { echo "vite 未就绪:" >&2; tail -20 /tmp/e2e-vite.log >&2; exit 1; }
echo "  vite ready"

# 造一条 incident,否则总览与列表页全是空态,几个断言就失去意义。
# startsAt 用当前时刻:写死会在非全新库上被 F5 幂等去重(check-metrics 踩过)。
STARTS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
BODY="{\"alerts\":[{\"status\":\"firing\",\"labels\":{\"alertname\":\"HighErrorRate\",\"severity\":\"critical\",\"namespace\":\"payment\",\"deployment\":\"checkout\",\"cluster\":\"prod-cn-1\",\"rule_id\":\"r-e2e\"},\"startsAt\":\"$STARTS\"}]}"
SIG=$(python3 -c "import hmac,hashlib,sys;print('sha256='+hmac.new(b'webhook-dev-secret',sys.argv[1].encode(),hashlib.sha256).hexdigest())" "$BODY")
curl -s -o /dev/null "http://127.0.0.1:$PUB/v1/signals" \
  -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $SIG" -d "$BODY"
sleep 4

echo "== Playwright"
cd "$ROOT/frontend"
E2E_BASE_URL="http://127.0.0.1:$VITE_PORT" npx playwright test
rc=$?
[ $rc -eq 0 ] && echo "FRONTEND-E2E OK" || echo "FRONTEND-E2E FAILED"
exit $rc
