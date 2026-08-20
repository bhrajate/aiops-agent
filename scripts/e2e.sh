#!/usr/bin/env bash
# 端到端联调脚本:启动 cluster-agent + control-plane + ai-worker,注入 Signal,验证全链路。
# 用 PID 精确管理进程(避免 pkill -f 误杀本脚本)。
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 查库走 lib/db.sh:连不上或 SQL 出错立刻终止,不让输出静默变空。
source "$ROOT/scripts/lib/db.sh"
export GOPROXY=https://goproxy.cn,direct
export AIOPS_DB_DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}"
export AIOPS_KAFKA_BROKERS="localhost:19092"
export AIOPS_TEMPORAL_HOSTPORT="localhost:7233"
export AIOPS_TEMPORAL_NAMESPACE="default"
export AIOPS_CLUSTER_AGENT_URL="http://localhost:9100"
export AIOPS_CONTROL_INTERNAL_URL="http://localhost:8090"
export AIOPS_MODEL_PROVIDER="mock"
export UV_INDEX_URL="https://mirrors.aliyun.com/pypi/simple/"

LOGDIR=/tmp/aiops-e2e
mkdir -p "$LOGDIR"
PIDS=()

cleanup() {
  echo "--- cleanup ---"
  for pid in "${PIDS[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null
  done
  wait 2>/dev/null
}
trap cleanup EXIT

start_bg() { # name cmd...
  local name="$1"; shift
  ( "$@" ) > "$LOGDIR/$name.log" 2>&1 &
  local pid=$!
  PIDS+=("$pid")
  echo "[start] $name pid=$pid log=$LOGDIR/$name.log"
}

wait_http() { # url tries
  local url="$1" tries="${2:-30}"
  for i in $(seq 1 "$tries"); do
    if curl -sf "$url" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

echo "=== 1) cluster-agent :9100 ==="
start_bg cluster-agent "$ROOT/cluster-agent/bin/cluster-agent"
wait_http http://localhost:9100/healthz 15 && echo "cluster-agent healthy" || { echo "cluster-agent FAILED"; exit 1; }

echo "=== 2) control-plane :8088/:8090 ==="
start_bg control-plane "$ROOT/control-plane/bin/control-plane"
wait_http http://localhost:8088/healthz 15 && echo "control-plane healthy" || { echo "control-plane FAILED"; exit 1; }

echo "=== 3) ai-worker (Temporal worker) ==="
( cd "$ROOT/ai-worker" && exec uv run python -m aiops_worker.main ) > "$LOGDIR/ai-worker.log" 2>&1 &
WPID=$!; PIDS+=("$WPID")
echo "[start] ai-worker pid=$WPID"
sleep 8   # worker 连 Temporal + 注册

echo "=== 4) inject Signal ==="
curl -s localhost:8088/v1/signals -H 'Content-Type: application/json' -d '{
  "alerts":[{"status":"firing","labels":{
    "alertname":"HighErrorRate","severity":"critical",
    "namespace":"payment","deployment":"checkout",
    "cluster":"prod-cn-1","rule_id":"r-101"
  },"startsAt":"2026-07-26T10:00:00Z"}]
}'
echo ""

echo "=== 5) 等待调查推进(最多 40s) ==="
INC=""
for i in $(seq 1 40); do
  INC=$(curl -s 'localhost:8088/v1/incidents' | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d["incidents"][0]["incident_id"] if d["incidents"] else "")' 2>/dev/null)
  [ -n "$INC" ] && break
  sleep 1
done
echo "incident: $INC"
[ -z "$INC" ] && { echo "NO INCIDENT"; exit 1; }

# 找到调查
INV=$(curl -s "localhost:8088/v1/incidents/$INC" | python3 -c 'import sys,json;d=json.load(sys.stdin);iv=d["investigations"];print(iv[0]["investigation_id"] if iv else "")')
echo "investigation: $INV"

# 轮询调查阶段直到终态
for i in $(seq 1 40); do
  PHASE=$(curl -s "localhost:8088/v1/investigations/$INV" | python3 -c 'import sys,json;print(json.load(sys.stdin)["investigation"]["phase"])' 2>/dev/null)
  echo "  phase=$PHASE"
  case "$PHASE" in
    concluded|needs_human|triage_published|closed|cancelled) break;;
  esac
  sleep 2
done

echo "=== 6) 最终调查结果 ==="
curl -s "localhost:8088/v1/investigations/$INV" > "$LOGDIR/final.json"
python3 - "$LOGDIR/final.json" <<'PY'
import sys, json
d = json.load(open(sys.argv[1]))
inv = d["investigation"]
print("phase:", inv["phase"])
print("usage:", inv.get("usage"))
diag = inv.get("diagnosis")
if diag:
    print("diagnosis.status:", diag.get("status"))
    print("hypotheses:", len(diag.get("hypotheses") or []))
    print("next_actions:", diag.get("next_actions"))
    print("remediation_proposal (must be null):", diag.get("remediation_proposal"))
print("evidence count:", len(d.get("evidence") or []))
print("hypotheses count:", len(d.get("hypotheses") or []))
for h in (d.get("hypotheses") or [])[:3]:
    print(f"  - [{h['status']}] conf={h['confidence']:.2f} {h['statement'][:60]} sup={h['supporting_evidence_ids']}")
PY

echo "=== 7) 证据接口 GET /v1/evidence/{id} ==="
EVID=$(python3 -c 'import json;d=json.load(open("'"$LOGDIR"'/final.json"));e=d.get("evidence") or [];print(e[0]["evidence_id"] if e else "")')
if [ -n "$EVID" ]; then
  curl -s "localhost:8088/v1/evidence/$EVID" | python3 -c 'import sys,json;d=json.load(sys.stdin);print("evidence",d["evidence_id"],"type=",d["type"],"source=",d["source"],"redaction=",d["redaction_status"])'
fi

echo "=== 8) 人工反馈:confirm ==="
curl -s "localhost:8088/v1/investigations/$INV/feedback" -H 'Content-Type: application/json' \
  -d '{"author":"oncall-alice","action":"confirm","confirmed_root_cause":"新版本连接池配置回归","comment":"已核实"}' \
  | python3 -c 'import sys,json;d=json.load(sys.stdin);print("feedback",d.get("feedback_id"),"review=",d.get("review_status"))'

echo "=== 9) 人工反馈:close(应关闭 Incident)==="
curl -s "localhost:8088/v1/investigations/$INV/feedback" -H 'Content-Type: application/json' \
  -d '{"author":"oncall-alice","action":"close","comment":"处置完成"}' >/dev/null
sleep 1
curl -s "localhost:8088/v1/incidents/$INC" | python3 -c 'import sys,json;d=json.load(sys.stdin);print("incident status:",d["incident"]["status"]);print("investigation phase:",d["investigations"][0]["phase"] if d["investigations"] else "?")'

echo "=== 10) 审计日志抽样 ==="
dbq "select action||' | '||coalesce(result,'-')||' | '||count(*) from audit_log group by action, result order by 1;"

echo "done. logs in $LOGDIR"
echo "=== ai-worker log tail (errors only) ==="
grep -iE "error|traceback|exception" "$LOGDIR/ai-worker.log" | tail -5 || echo "(no worker errors)"
