#!/usr/bin/env bash
# 校验 Workflow 超时相关的三项改动是端到端生效的,而不只是单测通过:
#   1. 控制面能带默认 run timeout 正常启动,并真的把它传给 Temporal
#   2. run timeout 配小时**拒绝启动**(而不是降级成一条 warn 后工作流永不启动)
#   3. Worker 侧心跳与超时参数一致
#
# 为什么必须端到端:第 2 项的失效方式是**静默** —— 若校验漏到 Dial 之后,
# Temporal 可达时会先返回连接成功,非法配置就永远不会被发现;Temporal 不可达时
# 又会被当成"可降级"只打一条 warn。两种情况在日志里都看不出配置错了。
set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }

BIN=$(mktemp -d)/control-plane
trap 'rm -rf "$(dirname "$BIN")" "${LOG:-}" 2>/dev/null || true' EXIT

echo "== 构建控制面 =="
if ! (cd control-plane && go build -o "$BIN" ./cmd/control-plane); then
  echo "构建失败"; exit 1
fi
ok "构建成功"

BASE_ENV=(
  AIOPS_DB_DSN="postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable"
  AIOPS_KAFKA_BROKERS="localhost:19092"
  AIOPS_TEMPORAL_HOSTPORT="localhost:7233"
  AIOPS_AUTH_MODE="hs256"
  AIOPS_AUTH_HS256_SECRET="dev-secret"
  AIOPS_INTERNAL_TOKEN="internal-dev-token"
  AIOPS_WEBHOOK_SECRET="webhook-dev-secret"
  AIOPS_PUBLIC_ADDR=":18099"
  AIOPS_INTERNAL_ADDR=":18098"
  AIOPS_CLUSTER_AGENT_ADDR=":19199"
  AIOPS_ROLES="api"
)

echo
echo "== 1) 默认 run timeout:应正常启动并连上 Temporal =="
LOG=$(mktemp)
env "${BASE_ENV[@]}" "$BIN" >"$LOG" 2>&1 &
PID=$!
for _ in $(seq 60); do
  grep -q "connected to temporal" "$LOG" 2>/dev/null && break
  kill -0 $PID 2>/dev/null || break
  sleep 0.25
done
if grep -q "connected to temporal" "$LOG"; then
  ok "带默认 run timeout 成功连接 Temporal"
else
  bad "未能连上 Temporal(日志见下)"; sed -n '1,25p' "$LOG"
fi
if kill -0 $PID 2>/dev/null; then
  ok "进程存活(未因新增配置崩溃)"
  kill $PID 2>/dev/null; wait $PID 2>/dev/null
else
  bad "进程已退出"
fi
rm -f "$LOG"

echo
echo "== 2) run timeout 配小:必须拒绝启动 =="
# 24h 对一次调查绰绰有余,但小于 Worker 的 48h 人工反馈等待 —— 最容易误配的量级。
LOG=$(mktemp)
env "${BASE_ENV[@]}" AIOPS_TEMPORAL_RUN_TIMEOUT_SEC=86400 "$BIN" >"$LOG" 2>&1
RC=$?
if [ $RC -ne 0 ]; then
  ok "以非零码退出(rc=$RC)"
else
  bad "居然启动成功了 —— 非法配置被静默接受"
fi
if grep -q "waiting_feedback" "$LOG"; then
  ok "错误信息说明了后果(卡在 waiting_feedback)"
else
  bad "错误信息未说明后果,运维只会看到一个数字"; sed -n '1,10p' "$LOG"
fi
if grep -q "invalid configuration" "$LOG"; then
  ok "走的是配置校验路径(fail-fast),非降级路径"
else
  bad "未走配置校验路径"
fi
if grep -qi "degraded" "$LOG"; then
  bad "出现了 degraded —— 配置错误被当成可降级依赖处理"
else
  ok "未降级(配置错误不该降级)"
fi
rm -f "$LOG"

echo
echo "== 3) 边界值:恰好等于下限应通过,少 1 秒应拒绝 =="
LOG=$(mktemp)
env "${BASE_ENV[@]}" AIOPS_TEMPORAL_RUN_TIMEOUT_SEC=216000 "$BIN" >"$LOG" 2>&1 &
PID=$!
for _ in $(seq 60); do
  grep -q "configuration validated" "$LOG" 2>/dev/null && break
  kill -0 $PID 2>/dev/null || break
  sleep 0.25
done
if grep -q "configuration validated" "$LOG"; then
  ok "下限值 216000(60h)通过校验"
else
  bad "下限值被误拒"; sed -n '1,10p' "$LOG"
fi
kill $PID 2>/dev/null; wait $PID 2>/dev/null
rm -f "$LOG"

LOG=$(mktemp)
env "${BASE_ENV[@]}" AIOPS_TEMPORAL_RUN_TIMEOUT_SEC=215999 "$BIN" >"$LOG" 2>&1
if [ $? -ne 0 ] && grep -q "waiting_feedback" "$LOG"; then
  ok "下限减 1 秒被拒绝(边界严格)"
else
  bad "边界不严格:215999 应被拒绝"
fi
rm -f "$LOG"

echo
echo "== 4) 未配置(0):应回落默认值而非报错 =="
LOG=$(mktemp)
env "${BASE_ENV[@]}" AIOPS_TEMPORAL_RUN_TIMEOUT_SEC=0 "$BIN" >"$LOG" 2>&1 &
PID=$!
for _ in $(seq 60); do
  grep -q "connected to temporal" "$LOG" 2>/dev/null && break
  kill -0 $PID 2>/dev/null || break
  sleep 0.25
done
if grep -q "connected to temporal" "$LOG"; then
  ok "0 被当作未配置,回落默认值"
else
  bad "0 未能回落默认值"; sed -n '1,10p' "$LOG"
fi
kill $PID 2>/dev/null; wait $PID 2>/dev/null
rm -f "$LOG"

echo
echo "== 5) Worker 侧参数自洽 =="
cd ai-worker
if .venv/bin/python -m pytest tests/test_heartbeat_and_timeouts.py -q >/dev/null 2>&1; then
  ok "心跳与超时不变式全部成立"
else
  bad "心跳/超时单测失败"
fi
# 死配置回归:AIOPS_MAX_ANALYZER_CONCURRENCY 曾经被读取但从未使用。
if grep -rq "max_analyzer_concurrency" aiops_worker/ 2>/dev/null; then
  bad "死配置 max_analyzer_concurrency 又回来了"
else
  ok "无死配置 max_analyzer_concurrency"
fi
# 新配置必须真的接到 Worker 上,否则又是一处静默失效。
if grep -q "max_concurrent_activities=settings.max_concurrent_activities" aiops_worker/main.py; then
  ok "max_concurrent_activities 已接到 Worker"
else
  bad "max_concurrent_activities 未接线"
fi
if .venv/bin/python -m pytest tests/test_cross_round_evidence.py -q >/dev/null 2>&1; then
  ok "跨轮证据累积生效"
else
  bad "跨轮证据测试失败"
fi
cd ..

echo
echo "================================"
echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ] && echo "RESULT: PASS" || echo "RESULT: FAIL"
exit $([ "$FAIL" -eq 0 ] && echo 0 || echo 1)
