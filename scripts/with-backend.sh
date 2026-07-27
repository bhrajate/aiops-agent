#!/usr/bin/env bash
# 起一套后端(cluster-agent + control-plane),跑指定脚本,然后收拾干净。
#
# 存在的原因:check-*.sh 里有几个不自己起后端,依赖 8088 上已有实例。
# 忘了起就会让 curl 静默失败、断言照着库里残留数据打分——通过和失败都没意义。
# 现在那些脚本会 fail-fast,本包装脚本负责提供它们需要的环境。
#
#   用法: ./scripts/with-backend.sh scripts/check-two-tier.sh [更多脚本...]
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOGDIR=$(mktemp -d)

for p in 8088 8090 9100; do
  command -v fuser >/dev/null 2>&1 && fuser -k "$p/tcp" >/dev/null 2>&1
done
sleep 1

echo "=== 构建(验证当前源码)==="
( cd "$ROOT/control-plane" && go build -o bin/control-plane ./cmd/control-plane ) || exit 1
( cd "$ROOT/cluster-agent" && go build -o bin/cluster-agent ./cmd/cluster-agent ) || exit 1

wait_http(){ for _ in $(seq "${2:-20}"); do curl -sf "$1" >/dev/null 2>&1 && return 0; sleep 0.5; done; return 1; }

PIDS=()
cleanup(){
  for pid in "${PIDS[@]:-}"; do kill "$pid" 2>/dev/null; done
  wait 2>/dev/null
}
trap cleanup EXIT

export AIOPS_ENV=development
export AIOPS_WEBHOOK_SECRET=webhook-dev-secret
export AIOPS_INTERNAL_TOKEN=dev-token
export AIOPS_DB_DSN="postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable"

"$ROOT/cluster-agent/bin/cluster-agent" >"$LOGDIR/agent.log" 2>&1 & PIDS+=($!)
wait_http http://localhost:9100/healthz 20 || { echo "cluster-agent 未就绪"; tail -20 "$LOGDIR/agent.log"; exit 1; }
"$ROOT/control-plane/bin/control-plane" >"$LOGDIR/cp.log" 2>&1 & PIDS+=($!)
wait_http http://localhost:8088/healthz 30 || { echo "control-plane 未就绪"; tail -20 "$LOGDIR/cp.log"; exit 1; }
echo "=== 后端就绪 ==="

rc=0
for s in "$@"; do
  echo
  echo "########## $s ##########"
  if ! bash "$s"; then rc=1; fi
done

[ "$rc" -eq 0 ] || { echo; echo "--- control-plane 日志尾部 ---"; tail -25 "$LOGDIR/cp.log"; }
exit "$rc"
