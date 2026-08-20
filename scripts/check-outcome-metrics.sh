#!/usr/bin/env bash
# 验证 F10:成效与成本指标真的出现在 /metrics 上。
#
# 原问题:investigations.usage 里一直有 tokens / cost_usd / elapsed_sec /
# tool_calls / ungrounded_downgrades,但**从未导出到 Prometheus**。
# 成本只能查库逐条累加,诊断结论的采纳率完全没有聚合视图。
# 队列可观测性(P4)回答"系统是否在工作",这一项回答"工作得值不值"。
#
# 走内部 API 直接回写 usage/diagnosis(与 AI Worker 相同的路径),
# 再走公共 API 提交人工反馈,然后抓 /metrics 断言。
# 不依赖 Temporal 与真实模型 —— 那些会让本脚本变成端到端测试而非指标测试。
#
# 自起 control-plane,不要用 with-backend.sh 包裹。
set -uo pipefail
cd "$(dirname "$0")/.."
COMPOSE=deploy/docker-compose.yml
PUB=8988
INT=8990
NS="f10-$$"
TOKEN=dev-token
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
info(){ echo "== $1"; }

for p in $PUB $INT; do
  if command -v fuser >/dev/null 2>&1 && fuser "$p/tcp" >/dev/null 2>&1; then
    echo "  端口 $p 被占用,先清理" >&2; exit 2
  fi
done

q(){ docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -tAc "$1" 2>/dev/null | tr -d ' ' | sed '/^$/d'; }
qx(){ docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -q -c "$1" >/dev/null 2>&1; }
scrape(){ curl -s --max-time 8 "http://127.0.0.1:$INT/metrics"; }
# val <metric-with-labels> —— 取指标值(取不到返回空)
val(){ scrape | awk -v k="$1" '$1==k{print $2; exit}'; }

info "构建"
( cd control-plane && go build -o /tmp/cp-f10 ./cmd/control-plane ) || exit 1

docker compose -f "$COMPOSE" up -d postgres redpanda >/dev/null 2>&1
for _ in $(seq 40); do
  docker compose -f "$COMPOSE" exec -T postgres pg_isready -U aiops -d aiops >/dev/null 2>&1 && break
  sleep 1
done

LOG=$(mktemp)
AIOPS_ENV=development AIOPS_ROLES="all" \
AIOPS_PUBLIC_ADDR=":$PUB" AIOPS_INTERNAL_ADDR=":$INT" \
AIOPS_DB_DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}" \
AIOPS_KAFKA_BROKERS="localhost:19092" \
AIOPS_AUTH_MODE=hs256 AIOPS_AUTH_HS256_SECRET=dev-secret \
AIOPS_INTERNAL_TOKEN=$TOKEN AIOPS_RETENTION_ENABLED=false \
/tmp/cp-f10 >"$LOG" 2>&1 &
CP_PID=$!
cleanup(){
  kill $CP_PID 2>/dev/null; wait $CP_PID 2>/dev/null
  qx "DELETE FROM signals WHERE labels->>'namespace' LIKE 'f10-%';
      DELETE FROM alert_groups WHERE namespace LIKE 'f10-%';
      DELETE FROM incidents WHERE correlation_key LIKE '%|f10-%';"
}
trap cleanup EXIT

ready=0
for _ in $(seq 40); do
  curl -sf --max-time 3 "http://127.0.0.1:$PUB/healthz" >/dev/null 2>&1 && { ready=1; break; }
  kill -0 $CP_PID 2>/dev/null || break
  sleep 0.5
done
[ "$ready" = 1 ] || { echo "control-plane 未就绪:" >&2; tail -20 "$LOG" >&2; exit 1; }

info "造一个 P1 incident(P1 必触发,拿到 investigation)"
curl -s -o /dev/null --max-time 6 -X POST "http://127.0.0.1:$PUB/v1/signals" \
  -H 'Content-Type: application/json' \
  -d "{\"alerts\":[{\"status\":\"firing\",\"fingerprint\":\"fp$NS\",\"startsAt\":\"2026-07-29T10:00:00Z\",
       \"labels\":{\"alertname\":\"ServiceDown\",\"severity\":\"critical\",\"namespace\":\"$NS\",\"deployment\":\"checkout\"}}]}"
INV=""
for _ in $(seq 25); do
  INV=$(q "SELECT i.investigation_id FROM investigations i JOIN incidents c ON c.incident_id=i.incident_id
           WHERE c.correlation_key LIKE '%|$NS' LIMIT 1;")
  [ -n "$INV" ] && break; sleep 1
done
[ -n "$INV" ] || { echo "未拿到 investigation,日志:" >&2; tail -20 "$LOG" >&2; exit 1; }
echo "   investigation=$INV"

info "1) 回写 usage → 成本指标"
curl -s -o /dev/null --max-time 6 -X POST "http://127.0.0.1:$INT/internal/investigations/$INV/usage" \
  -H "X-Internal-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"usage":{"elapsed_sec":42.5,"rounds":2,"tokens":18000,"cost_usd":0.35,"tool_calls":6,"ungrounded_downgrades":2}}'
sleep 1
T=$(val aiops_model_tokens_total); C=$(val aiops_model_cost_usd_total); U=$(val aiops_ungrounded_downgrades_total)
[ "${T%.*}" = "18000" ] && ok "tokens 累计 = $T" || bad "tokens 期望 18000,得 '$T'"
awk -v c="$C" 'BEGIN{exit !(c>0.34 && c<0.36)}' && ok "费用累计 = $C" || bad "费用期望 0.35,得 '$C'"
[ "${U%.*}" = "2" ] && ok "无证据降级数 = $U(模型质量信号)" || bad "降级数期望 2,得 '$U'"
[ "$(val aiops_investigation_tokens_count)" = "1" ] && ok "每次调查 token 直方图已观测" \
  || bad "token 直方图未观测"
[ "$(val aiops_investigation_cost_usd_count)" = "1" ] && ok "每次调查费用直方图已观测" \
  || bad "费用直方图未观测"

info "2) 回写 diagnosis → 时延与发布计数"
curl -s -o /dev/null --max-time 6 -X POST "http://127.0.0.1:$INT/internal/investigations/$INV/diagnosis" \
  -H "X-Internal-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"phase":"concluded","diagnosis":{"status":"root_cause_identified","summary":"s","hypotheses":[]}}'
sleep 1
D=$(scrape | awk '/^aiops_diagnosis_published_total\{status="root_cause_identified"\}/{print $2; exit}')
[ "${D%.*}" = "1" ] && ok "诊断发布计数(按 status)= $D" || bad "发布计数期望 1,得 '$D'"
L=$(val aiops_diagnosis_latency_seconds_count)
[ "$L" = "1" ] && ok "诊断时延已观测(纯系统耗时,非 MTTR)" || bad "时延未观测,得 '$L'"

info "3) 人工反馈 → 采纳率的分子分母"
TK=$(curl -s --max-time 6 "http://127.0.0.1:$PUB/v1/auth/login" -H 'Content-Type: application/json' \
      -d '{"username":"alice","password":"alice-pass"}' \
      | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))')
[ -n "$TK" ] || { bad "登录失败,无法验证反馈指标"; }
if [ -n "$TK" ]; then
  curl -s -o /dev/null --max-time 6 -X POST "http://127.0.0.1:$PUB/v1/investigations/$INV/feedback" \
    -H "Authorization: Bearer $TK" -H 'Content-Type: application/json' \
    -d '{"action":"confirm","confirmed_root_cause":"x"}'
  sleep 1
  F=$(scrape | awk '/^aiops_human_feedback_total\{action="confirm"\}/{print $2; exit}')
  [ "${F%.*}" = "1" ] && ok "反馈计数(按 action)= $F" || bad "反馈计数期望 1,得 '$F'"
  scrape | grep -q '^aiops_human_feedback_total{action=' \
    && ok "按 action 分维度(采纳率可在 PromQL 现算)" || bad "反馈未按 action 分维度"
fi

info "4) 九个 F10 指标全部注册"
M=$(scrape); miss=0
for n in aiops_model_tokens_total aiops_model_cost_usd_total aiops_investigation_tokens \
         aiops_investigation_cost_usd aiops_investigation_tool_calls \
         aiops_diagnosis_latency_seconds aiops_diagnosis_published_total \
         aiops_human_feedback_total aiops_ungrounded_downgrades_total; do
  echo "$M" | grep -q "^$n" || { bad "缺少 $n"; miss=1; }
done
[ "$miss" = 0 ] && ok "九个指标齐备"

echo ""
echo "RESULT: pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ] && echo "OUTCOME-METRICS OK" || echo "FAILURES"
[ "$FAIL" = 0 ]
