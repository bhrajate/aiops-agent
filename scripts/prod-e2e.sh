#!/usr/bin/env bash
# 生产化端到端:认证 + webhook 签名 + 完整 RCA + 人工闭环,全部经安全控制。
# 前置:deploy 基础设施已 up;cluster-agent / ai-worker / control-plane 二进制已构建。
set -u
ROOT="/home/glory/code/ai-generate/aiops"
BASE=http://localhost:8088
export GOPROXY=https://goproxy.cn,direct
export AIOPS_DB_DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}"
export AIOPS_KAFKA_BROKERS="localhost:19092" AIOPS_TEMPORAL_HOSTPORT="localhost:7233"
export AIOPS_CLUSTER_AGENT_URL="http://localhost:9100"
export AIOPS_CONTROL_INTERNAL_URL="http://localhost:8090"
export AIOPS_MODEL_PROVIDER="mock"
export UV_INDEX_URL="https://mirrors.aliyun.com/pypi/simple/"
# 安全开关全开
export AIOPS_AUTH_MODE="hs256" AIOPS_AUTH_HS256_SECRET="dev-secret"
export AIOPS_INTERNAL_TOKEN="internal-dev-token"
export AIOPS_WEBHOOK_SECRET="webhook-dev-secret"

LOGDIR=/tmp/aiops-prod-e2e; mkdir -p "$LOGDIR"
PIDS=()
cleanup(){ for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null; done; wait 2>/dev/null; }
trap cleanup EXIT
wait_http(){ for i in $(seq 1 "${2:-30}"); do curl -sf "$1" >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }

# 清理端口 + 库 + topics
for p in $(fuser 8088/tcp 8090/tcp 9100/tcp 2>/dev/null); do kill "$p" 2>/dev/null; done; sleep 1
# 查库走 lib/db.sh:连不上或 SQL 出错立刻终止,不让断言照着残留数据打分。
source "$ROOT/scripts/lib/db.sh"
dbx "TRUNCATE signals, incidents, investigations, evidence, hypotheses, investigation_events, human_feedback, outbox, audit_log, idempotency_keys, dead_letters CASCADE;"
docker compose -f "$ROOT/deploy/docker-compose.yml" exec -T redpanda rpk topic delete signals incidents investigations --brokers redpanda:29092 >/dev/null 2>&1
docker compose -f "$ROOT/deploy/docker-compose.yml" exec -T redpanda rpk topic create signals incidents investigations --brokers redpanda:29092 -p 1 -r 1 >/dev/null 2>&1

# 必须先构建:此前脚本直接跑 bin/ 下的**预编译**产物,一旦忘记手动重编,
# E2E 就会在旧代码上跑出绿色——验证了一个不存在的版本。
echo "=== 构建(确保验证的是当前源码)==="
( cd "$ROOT/control-plane" && go build -o bin/control-plane ./cmd/control-plane ) || exit 1
( cd "$ROOT/cluster-agent" && go build -o bin/cluster-agent ./cmd/cluster-agent ) || exit 1

echo "=== 启动后端(内部 token + 认证 + webhook 全开)==="
"$ROOT/cluster-agent/bin/cluster-agent" > "$LOGDIR/cluster-agent.log" 2>&1 & PIDS+=($!)
wait_http http://localhost:9100/healthz 15 && echo "agent ok" || { echo agent FAIL; exit 1; }
"$ROOT/control-plane/bin/control-plane" > "$LOGDIR/control-plane.log" 2>&1 & PIDS+=($!)
wait_http $BASE/healthz 15 && echo "control-plane ok" || { echo cp FAIL; exit 1; }
( cd "$ROOT/ai-worker" && exec uv run python -m aiops_worker.main ) > "$LOGDIR/ai-worker.log" 2>&1 & PIDS+=($!)
echo "ai-worker starting..."; sleep 8

login(){ curl -s $BASE/v1/auth/login -H 'Content-Type: application/json' -d "{\"username\":\"$1\",\"password\":\"$2\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))'; }
ALICE=$(login alice alice-pass)
[ -z "$ALICE" ] && { echo "LOGIN FAIL"; exit 1; }
echo "logged in as alice (sre)"

echo "=== 注入 signal(HMAC 签名)==="
BODY='{"alerts":[{"status":"firing","labels":{"alertname":"HighErrorRate","severity":"critical","namespace":"payment","deployment":"checkout","cluster":"prod-cn-1","rule_id":"r-101"},"startsAt":"2026-07-26T10:00:00Z"}]}'
SIG=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $SIG" -d "$BODY"

echo "=== 等待自动调查得出诊断(带 token 轮询)==="
INV=""
for i in $(seq 1 40); do
  INC=$(curl -s "$BASE/v1/incidents" -H "Authorization: Bearer $ALICE" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d["incidents"][0]["incident_id"] if d.get("incidents") else "")' 2>/dev/null)
  [ -n "$INC" ] && INV=$(curl -s "$BASE/v1/incidents/$INC" -H "Authorization: Bearer $ALICE" | python3 -c 'import sys,json;d=json.load(sys.stdin);iv=d.get("investigations") or [];print(iv[0]["investigation_id"] if iv else "")' 2>/dev/null)
  [ -n "$INV" ] && break; sleep 1
done
echo "incident=$INC investigation=$INV"
[ -z "$INV" ] && { echo "NO INVESTIGATION"; tail -20 "$LOGDIR/ai-worker.log"; exit 1; }

for i in $(seq 1 40); do
  P=$(curl -s "$BASE/v1/investigations/$INV" -H "Authorization: Bearer $ALICE" | python3 -c 'import sys,json;print(json.load(sys.stdin)["investigation"]["phase"])' 2>/dev/null)
  case "$P" in concluded|needs_human|waiting_feedback|closed) break;; esac; sleep 2
done
echo "phase=$P"

echo "=== 诊断结果(带证据快照 raw_ref)==="
curl -s "$BASE/v1/investigations/$INV" -H "Authorization: Bearer $ALICE" > "$LOGDIR/final.json"
python3 - "$LOGDIR/final.json" <<'PY'
import sys, json
d = json.load(open(sys.argv[1]))
inv = d["investigation"]; diag = inv.get("diagnosis") or {}
print("phase:", inv["phase"])
print("diagnosis.status:", diag.get("status"))
print("hypotheses:", len(d.get("hypotheses") or []))
print("evidence:", len(d.get("evidence") or []))
ev = d.get("evidence") or []
withref = [e for e in ev if e.get("raw_ref")]
print("evidence with S3 raw_ref:", len(withref), "/", len(ev))
if withref: print("  sample raw_ref:", withref[0]["raw_ref"])
print("remediation_proposal (must be null):", diag.get("remediation_proposal"))
PY

echo "=== 人工确认 + 关闭(带 token)==="
curl -s "$BASE/v1/investigations/$INV/feedback" -H "Authorization: Bearer $ALICE" -H 'Content-Type: application/json' \
  -d '{"action":"confirm","confirmed_root_cause":"连接池配置回归","comment":"prod-e2e"}' >/dev/null
curl -s "$BASE/v1/investigations/$INV/feedback" -H "Authorization: Bearer $ALICE" -H 'Content-Type: application/json' \
  -d '{"action":"close","comment":"done"}' >/dev/null
sleep 1
curl -s "$BASE/v1/incidents/$INC" -H "Authorization: Bearer $ALICE" | python3 -c 'import sys,json;d=json.load(sys.stdin);print("incident status:",d["incident"]["status"])'

echo "=== 指标抓取 ==="
curl -s localhost:8090/metrics | grep -E "aiops_(signals|tool|investigations)" | grep -v "^#" | head

echo "=== 审计(含认证身份 alice)==="
# 拼成单列输出(dbq 走 -tAc,多列会带 | 分隔符)。
# group/order 必须用列名而非序号 —— 拼接后 select 列表只有 1 列,
# 沿用 `group by 1,2,3 order by 2` 会报 "ORDER BY position 2 is not in select list"。
dbq "select actor||' | '||action||' | '||coalesce(result,'-')||' | '||count(*)
       from audit_log group by actor, action, result order by action;" | head -20

echo ""; echo "=== PROD E2E DONE ==="