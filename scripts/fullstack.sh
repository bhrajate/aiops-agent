#!/usr/bin/env bash
# 全栈联调:后端(cluster-agent + control-plane + ai-worker)+ 前端(Vite dev server),
# 通过前端 :5173 的代理验证 /v1 数据流与 SSE 时间线。用 PID 精确管理进程。
set -u
ROOT="/home/glory/code/ai-generate/aiops"
export GOPROXY=https://goproxy.cn,direct
export AIOPS_DB_DSN="postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable"
export AIOPS_KAFKA_BROKERS="localhost:19092"
export AIOPS_TEMPORAL_HOSTPORT="localhost:7233"
export AIOPS_CLUSTER_AGENT_URL="http://localhost:9100"
export AIOPS_CONTROL_INTERNAL_URL="http://localhost:8090"
export AIOPS_MODEL_PROVIDER="mock"
export UV_INDEX_URL="https://mirrors.aliyun.com/pypi/simple/"

LOGDIR=/tmp/aiops-fullstack
mkdir -p "$LOGDIR"
PIDS=()
cleanup() {
  echo "--- cleanup ---"
  for pid in "${PIDS[@]:-}"; do [ -n "$pid" ] && kill "$pid" 2>/dev/null; done
  # 关掉 vite(子进程)
  [ -n "${VITE_PID:-}" ] && pkill -P "$VITE_PID" 2>/dev/null
  [ -n "${VITE_PID:-}" ] && kill "$VITE_PID" 2>/dev/null
  wait 2>/dev/null
}
trap cleanup EXIT

wait_http() { local url="$1" tries="${2:-30}"; for i in $(seq 1 "$tries"); do curl -sf "$url" >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }

echo "=== 后端三件套 ==="
"$ROOT/cluster-agent/bin/cluster-agent" > "$LOGDIR/cluster-agent.log" 2>&1 & PIDS+=($!)
wait_http http://localhost:9100/healthz 15 && echo "cluster-agent ok" || { echo "agent FAIL"; exit 1; }
"$ROOT/control-plane/bin/control-plane" > "$LOGDIR/control-plane.log" 2>&1 & PIDS+=($!)
wait_http http://localhost:8088/healthz 15 && echo "control-plane ok" || { echo "cp FAIL"; exit 1; }
( cd "$ROOT/ai-worker" && exec uv run python -m aiops_worker.main ) > "$LOGDIR/ai-worker.log" 2>&1 & PIDS+=($!)
echo "ai-worker starting..."; sleep 8

echo "=== 注入 Signal 并等待诊断 ==="
curl -s localhost:8088/v1/signals -H 'Content-Type: application/json' -d '{"alerts":[{"status":"firing","labels":{"alertname":"HighErrorRate","severity":"critical","namespace":"payment","deployment":"checkout","cluster":"prod-cn-1","rule_id":"r-101"},"startsAt":"2026-07-26T10:00:00Z"}]}' >/dev/null
INV=""; INC=""
for i in $(seq 1 40); do
  INC=$(curl -s localhost:8088/v1/incidents | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d["incidents"][0]["incident_id"] if d.get("incidents") else "")' 2>/dev/null)
  [ -n "$INC" ] && INV=$(curl -s "localhost:8088/v1/incidents/$INC" | python3 -c 'import sys,json;d=json.load(sys.stdin);iv=d.get("investigations") or [];print(iv[0]["investigation_id"] if iv else "")' 2>/dev/null)
  [ -n "$INV" ] && break; sleep 1
done
echo "incident=$INC investigation=$INV"
for i in $(seq 1 30); do
  P=$(curl -s "localhost:8088/v1/investigations/$INV" | python3 -c 'import sys,json;print(json.load(sys.stdin)["investigation"]["phase"])' 2>/dev/null)
  case "$P" in concluded|needs_human|waiting_feedback|triage_published|closed) break;; esac; sleep 2
done
echo "final phase=$P"

echo "=== 启动 Vite dev server :5173 ==="
( cd "$ROOT/frontend" && exec npm run dev -- --host 127.0.0.1 --port 5173 ) > "$LOGDIR/vite.log" 2>&1 &
VITE_PID=$!
wait_http http://127.0.0.1:5173/ 40 && echo "vite ok" || { echo "vite FAIL"; tail -20 "$LOGDIR/vite.log"; exit 1; }

echo "=== 通过前端代理验证 /v1 数据流(前端 → :5173 → 代理 → :8088)==="
echo "-- 列表 --"
curl -s "http://127.0.0.1:5173/v1/incidents" | python3 -c 'import sys,json;d=json.load(sys.stdin);print("incidents via proxy:",len(d.get("incidents") or []))'
echo "-- 详情(前端会解包 incident/investigations)--"
curl -s "http://127.0.0.1:5173/v1/incidents/$INC" | python3 -c 'import sys,json;d=json.load(sys.stdin);print("has incident:",bool(d.get("incident")),"investigations:",len(d.get("investigations") or []))'
echo "-- 调查(前端解包 investigation/hypotheses/evidence/feedback)--"
curl -s "http://127.0.0.1:5173/v1/investigations/$INV" | python3 -c 'import sys,json;d=json.load(sys.stdin);print("phase:",d["investigation"]["phase"],"hyps:",len(d.get("hypotheses") or []),"evidence:",len(d.get("evidence") or []))'
echo "-- 首页 HTML --"
curl -s "http://127.0.0.1:5173/" | grep -o '<title>[^<]*</title>' | head -1
echo "-- SSE 时间线(前 5 行,经代理)--"
timeout 4 curl -sN "http://127.0.0.1:5173/v1/investigations/$INV/events" 2>/dev/null | head -8 || true

echo "-- 人工 confirm(经代理 POST)--"
curl -s "http://127.0.0.1:5173/v1/investigations/$INV/feedback" -H 'Content-Type: application/json' -d '{"author":"oncall-bob","action":"confirm","confirmed_root_cause":"连接池回归","comment":"UI 联调"}' | python3 -c 'import sys,json;d=json.load(sys.stdin);print("feedback review_status:",d.get("review_status"))'

echo "=== DONE. logs in $LOGDIR ==="
