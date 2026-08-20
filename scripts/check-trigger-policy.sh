#!/usr/bin/env bash
# 验证 F7:自动触发策略真的会拦,且拦掉的一定留痕。
#
# 旧实现 EvaluateAuto 四个分支**全部返回 true** —— 伪装成策略的常量。
# 每个 incident 都消耗一次 triage 模型调用,含 P4 单信号(磁盘到 80% 这类)。
#
# 两个方向都要验:
#   * P4 单信号被跳过,且写了审计 + 指标(跳过不留痕比不拦更糟:
#     "为什么这个故障没有诊断"将无从回答);
#   * P1 / 变更关联 / 突发 一定不被拦(收紧策略的风险是拦掉真问题)。
#
# 自起 control-plane,不要用 with-backend.sh 包裹。
set -uo pipefail
cd "$(dirname "$0")/.."
COMPOSE=deploy/docker-compose.yml
PUB=8888
INT=8890
NS="f7-$$"
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

info "构建"
( cd control-plane && go build -o /tmp/cp-f7 ./cmd/control-plane ) || exit 1

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
AIOPS_INTERNAL_TOKEN=dev-token AIOPS_RETENTION_ENABLED=false \
/tmp/cp-f7 >"$LOG" 2>&1 &
CP_PID=$!
cleanup(){
  kill $CP_PID 2>/dev/null; wait $CP_PID 2>/dev/null
  qx "DELETE FROM signals WHERE labels->>'namespace' LIKE 'f7-%';
      DELETE FROM alert_groups WHERE namespace LIKE 'f7-%';
      DELETE FROM incidents WHERE correlation_key LIKE '%|f7-%';"
}
trap cleanup EXIT

ready=0
for _ in $(seq 40); do
  curl -sf --max-time 3 "http://127.0.0.1:$PUB/healthz" >/dev/null 2>&1 && { ready=1; break; }
  kill -0 $CP_PID 2>/dev/null || break
  sleep 0.5
done
[ "$ready" = 1 ] || { echo "control-plane 未就绪:" >&2; tail -20 "$LOG" >&2; exit 1; }

echo "   策略配置:$(grep -o 'msg="auto trigger policy".*' "$LOG" | head -1)"

# post <name> <severity> <ns-suffix> [extra-labels-json]
post(){
  local extra="${4:-}"
  local labels="\"alertname\":\"$1\",\"severity\":\"$2\",\"namespace\":\"$NS-$3\",\"deployment\":\"svc$3\""
  [ -n "$extra" ] && labels="$labels,$extra"
  curl -s -o /dev/null --max-time 6 -X POST "http://127.0.0.1:$PUB/v1/signals" \
    -H 'Content-Type: application/json' \
    -d "{\"alerts\":[{\"status\":\"firing\",\"fingerprint\":\"fp$1$3\",\"startsAt\":\"2026-07-29T10:00:00Z\",\"labels\":{$labels}}]}"
}
invCount(){ q "SELECT count(*) FROM investigations i JOIN incidents c ON c.incident_id=i.incident_id
               WHERE c.correlation_key LIKE '%|$NS-$1';"; }
skipAudit(){ q "SELECT count(*) FROM audit_log a JOIN incidents c ON c.incident_id=a.target_id
                WHERE a.action='trigger_skipped' AND c.correlation_key LIKE '%|$NS-$1';"; }

info "1) P4 单信号 → 应跳过"
# severity=info → P4(warning 映射到 P3,见 NormalizeSeverity)。
# alertname 刻意避开 deploy/release/rollout/version 与 pod/cpu/timeout 等关键词,
# 否则会被 ClassifyFault 判成变更关联而必触发。
post DiskUsageElevated info low
sleep 8
SEV=$(q "SELECT severity FROM incidents WHERE correlation_key LIKE '%|$NS-low';")
FCL=$(q "SELECT fault_category FROM incidents WHERE correlation_key LIKE '%|$NS-low';")
echo "   severity=$SEV fault_category=$FCL"
if [ "$SEV" != "P4" ]; then
  bad "构造 P4 失败(得 $SEV),用例前提不成立"
elif [ "$FCL" = "release_regression" ]; then
  bad "被判为变更关联($FCL),会必触发 —— 用例前提不成立"
else
  [ "$(invCount low)" = "0" ] && ok "P4 单信号未起调查" || bad "P4 单信号仍起了 $(invCount low) 个调查"
  [ "$(skipAudit low)" -ge 1 ] 2>/dev/null && ok "跳过已写审计(trigger_skipped)" \
    || bad "跳过未留痕 —— '为什么没有诊断'将无从回答"
fi

info "2) P1 → 必须触发"
post ServiceDown critical crit
sleep 7
SEVC=$(q "SELECT severity FROM incidents WHERE correlation_key LIKE '%|$NS-crit';")
[ "$(invCount crit)" -ge 1 ] 2>/dev/null && ok "高危($SEVC)已起调查" || bad "高危($SEVC)未起调查 —— 策略拦掉了真问题"

info "3) 变更关联 → 必须触发(即使低级别 P4)"
post SomethingOdd info chg "\"reason\":\"ReleaseRollout\""
sleep 8
SEVG=$(q "SELECT severity FROM incidents WHERE correlation_key LIKE '%|$NS-chg';")
FC=$(q "SELECT fault_category FROM incidents WHERE correlation_key LIKE '%|$NS-chg';")
echo "   severity=$SEVG fault_category=$FC"
if [ "$FC" = "release_regression" ]; then
  [ "$(invCount chg)" -ge 1 ] 2>/dev/null && ok "P4 但变更关联,已起调查" || bad "变更关联未起调查 —— 策略拦掉了最易定位的一类根因"
else
  bad "reason=ReleaseRollout 应被判为 release_regression,得 $FC"
fi

info "4) 指标已上报决策(按 reason 分维度)"
M=$(curl -s --max-time 8 "http://127.0.0.1:$INT/metrics")
echo "$M" | grep -q '^aiops_trigger_decisions_total{' && ok "aiops_trigger_decisions_total 已上报" \
  || bad "缺少 aiops_trigger_decisions_total"
echo "$M" | grep -q 'triggered="false"' && ok "记录了被跳过的决策" || bad "未记录跳过决策"
echo "$M" | grep -q 'triggered="true"' && ok "记录了触发的决策" || bad "未记录触发决策"
echo "   决策分布:"; echo "$M" | grep '^aiops_trigger_decisions_total{' | sed 's/^/     /'

echo ""
echo "RESULT: pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ] && echo "TRIGGER-POLICY OK" || echo "FAILURES"
[ "$FAIL" = 0 ]
