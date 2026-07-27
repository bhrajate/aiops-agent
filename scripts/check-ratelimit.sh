#!/usr/bin/env bash
# 验证信号入口限流(F6)在真实 HTTP 路径上的行为。
#
# 断言:
#   1. 突发容量内放行(202)
#   2. 超出后拒绝(429)
#   3. 429 带 Retry-After(否则客户端立刻重试会放大风暴)
#   4. 令牌随时间补充
#   5. 按**信号条数**计费,不是按请求数
#   6. 限流计入 aiops_ingress_throttled_total 指标
#
# 用固定端口起一个独立实例;开头强制检查端口空闲,避免断言打到别的残留进程上
# (曾因此得到一次假通过)。
set -uo pipefail
cd "$(dirname "$0")/.."

PUB_PORT=8188
INT_PORT=8190
PASS=0
FAIL=0

ok()   { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
info() { echo "== $1"; }

info "端口预检(避免打到残留进程)"
for p in $PUB_PORT $INT_PORT; do
  if command -v fuser >/dev/null 2>&1 && fuser "$p/tcp" >/dev/null 2>&1; then
    echo "  端口 $p 已被占用,先清理再跑本脚本" >&2
    exit 2
  fi
done
ok "端口 $PUB_PORT / $INT_PORT 空闲"

info "构建 control-plane"
( cd control-plane && go build -o /tmp/cp-ratelimit ./cmd/control-plane ) || exit 1

LOG=$(mktemp)
# burst=5 / rate=1/s:便于用少量请求触发限流。
# 需要 internal 角色才会在内部 API 暴露 /metrics。
AIOPS_ENV=development \
AIOPS_ROLES="api,internal" \
AIOPS_PUBLIC_ADDR=":$PUB_PORT" \
AIOPS_INTERNAL_ADDR=":$INT_PORT" \
AIOPS_INGRESS_RATE_PER_SEC=1 \
AIOPS_INGRESS_BURST=5 \
AIOPS_DB_DSN="postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable" \
AIOPS_INTERNAL_TOKEN=dev-token \
/tmp/cp-ratelimit >"$LOG" 2>&1 &
CP_PID=$!
trap 'kill $CP_PID 2>/dev/null; wait $CP_PID 2>/dev/null' EXIT

for _ in $(seq 30); do
  curl -sf "http://127.0.0.1:$PUB_PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.3
done
if ! curl -sf "http://127.0.0.1:$PUB_PORT/healthz" >/dev/null 2>&1; then
  echo "control-plane 未就绪,日志:" >&2; tail -20 "$LOG" >&2; exit 1
fi

post_one() { # $1=alertname -> 打印 HTTP 状态码
  curl -s -o /dev/null -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PUB_PORT/v1/signals" \
    -H 'Content-Type: application/json' \
    -d "{\"alerts\":[{\"status\":\"firing\",\"labels\":{\"alertname\":\"$1\",\"namespace\":\"rl\",\"deployment\":\"api\",\"severity\":\"warning\"}}]}"
}

info "1) 突发容量内放行"
allowed=0
for i in $(seq 5); do
  [ "$(post_one "rl-$i")" = "202" ] && allowed=$((allowed+1))
done
[ "$allowed" -eq 5 ] && ok "burst=5 内 5 次全部 202" || bad "burst 内只放行了 $allowed/5"

info "2) 超出突发容量后拒绝"
code=$(post_one "rl-over")
[ "$code" = "429" ] && ok "超限返回 429" || bad "超限返回 $code,应为 429"

info "3) 429 带 Retry-After"
retry=$(curl -s -D - -o /dev/null -X POST "http://127.0.0.1:$PUB_PORT/v1/signals" \
  -H 'Content-Type: application/json' \
  -d '{"alerts":[{"status":"firing","labels":{"alertname":"rl-hdr","namespace":"rl"}}]}' \
  | tr -d '\r' | awk 'tolower($1)=="retry-after:"{print $2}')
if [ -n "$retry" ] && [ "$retry" -ge 1 ] 2>/dev/null; then
  ok "Retry-After=$retry"
else
  bad "429 缺少可用的 Retry-After(得到 '${retry:-空}')"
fi

info "4) 令牌随时间补充"
sleep 3   # rate=1/s -> 约补 3 个
code=$(post_one "rl-refill")
[ "$code" = "202" ] && ok "等待后重新放行(令牌已补充)" || bad "补充后仍被拒($code)"

info "5) 按信号条数计费"
# 桶里此刻约剩 2 个令牌;一次投递 10 条应超额被拒(按请求计费则会放行)。
batch=$(python3 - <<'PY'
import json
alerts=[{"status":"firing","labels":{"alertname":f"rl-batch-{i}","namespace":"rl"}} for i in range(10)]
print(json.dumps({"alerts":alerts}))
PY
)
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$PUB_PORT/v1/signals" \
  -H 'Content-Type: application/json' -d "$batch")
[ "$code" = "429" ] && ok "10 条批量按条计费被拒(按请求计费会漏放)" \
  || bad "批量返回 $code,应为 429——限流可能按请求而非按条计费"

info "6) 限流指标已记录"
m=$(curl -s "http://127.0.0.1:$INT_PORT/metrics" | grep -c '^aiops_ingress_throttled_total{')
[ "${m:-0}" -ge 1 ] && ok "aiops_ingress_throttled_total 已上报" \
  || bad "未找到 aiops_ingress_throttled_total 指标"

echo
echo "结果: $PASS 通过 / $FAIL 失败"
[ "$FAIL" -eq 0 ] || { echo "--- control-plane 日志尾部 ---"; tail -20 "$LOG"; exit 1; }
