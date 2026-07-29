#!/usr/bin/env bash
# 验证 P3:/readyz 与 /healthz 语义分离。
#
# 核心场景是"数据库断连":此前两个探针共用 /healthz 且状态码恒 200,
# 断连副本不会被摘出 Service endpoints,继续接流量然后每个请求 500。
# 本脚本真的把数据库停掉来验证,而不是只测正常路径 —— 正常路径此前也是通的,
# 测它证明不了任何东西。
#
# 自起一个 control-plane,不要用 with-backend.sh 包裹。
set -uo pipefail
cd "$(dirname "$0")/.."
COMPOSE=deploy/docker-compose.yml
PUB=8288
INT=8290
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
info(){ echo "== $1"; }
code(){ curl -s -o /dev/null -w '%{http_code}' --max-time 8 "$1"; }
body(){ curl -s --max-time 8 "$1"; }

for p in $PUB $INT; do
  if command -v fuser >/dev/null 2>&1 && fuser "$p/tcp" >/dev/null 2>&1; then
    echo "  端口 $p 被占用,先清理" >&2; exit 2
  fi
done

info "构建"
( cd control-plane && go build -o /tmp/cp-probes ./cmd/control-plane ) || exit 1

LOG=$(mktemp)
AIOPS_ENV=development \
AIOPS_ROLES="api,internal" \
AIOPS_PUBLIC_ADDR=":$PUB" AIOPS_INTERNAL_ADDR=":$INT" \
AIOPS_DB_DSN="postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable" \
AIOPS_INTERNAL_TOKEN=dev-token \
AIOPS_RETENTION_ENABLED=false \
/tmp/cp-probes >"$LOG" 2>&1 &
CP_PID=$!
# 无论以何种方式退出,都必须把数据库恢复起来,否则会把本机环境留在坏状态。
restore(){
  kill $CP_PID 2>/dev/null; wait $CP_PID 2>/dev/null
  docker compose -f "$COMPOSE" start postgres >/dev/null 2>&1
  for _ in $(seq 30); do
    docker compose -f "$COMPOSE" exec -T postgres pg_isready -U aiops -d aiops >/dev/null 2>&1 && break
    sleep 1
  done
}
trap restore EXIT

ready=0
for _ in $(seq 40); do
  [ "$(code http://127.0.0.1:$PUB/healthz)" = "200" ] && { ready=1; break; }
  # 进程已退出就没必要继续等 —— 直接把日志摊开,而不是等满 40 轮再报一堆 000
  kill -0 $CP_PID 2>/dev/null || break
  sleep 0.5
done
if [ "$ready" != 1 ]; then
  echo "control-plane 未就绪(所有断言会得到 000,先看日志):" >&2
  tail -25 "$LOG" >&2
  exit 1
fi

info "1) 依赖正常"
[ "$(code http://127.0.0.1:$PUB/readyz)" = "200" ] && ok "/readyz → 200" || bad "/readyz 期望 200,得 $(code http://127.0.0.1:$PUB/readyz)"
[ "$(code http://127.0.0.1:$PUB/healthz)" = "200" ] && ok "/healthz → 200" || bad "/healthz 期望 200"
echo "$(body http://127.0.0.1:$PUB/readyz)" | grep -q '"status":"ready"' \
  && ok "/readyz 报告 ready" || bad "/readyz 未报告 ready:$(body http://127.0.0.1:$PUB/readyz)"

info "2) 探针免鉴权(否则 kubelet 探不了)"
[ "$(code http://127.0.0.1:$INT/readyz)" = "200" ] && ok "内部 API /readyz 免 token" || bad "内部 API /readyz 需要 token"

info "3) 数据库断连 —— 本项修复的核心场景"
docker compose -f "$COMPOSE" stop postgres >/dev/null 2>&1
# 等连接池确实失败(Ping 有 2s 超时,探针有 3s 超时)
sleep 3
RC=$(code http://127.0.0.1:$PUB/readyz)
if [ "$RC" = "503" ]; then
  ok "/readyz → 503(副本会被摘出 Service endpoints)"
else
  bad "/readyz 期望 503,得 $RC —— 断连副本仍会接流量然后每请求 500"
fi
echo "$(body http://127.0.0.1:$PUB/readyz)" | grep -q '"status":"not_ready"' \
  && ok "/readyz 报告 not_ready" || bad "/readyz 未报告 not_ready"
echo "$(body http://127.0.0.1:$PUB/readyz)" | grep -q '"database"' \
  && ok "/readyz 指名是 database 挂了" || bad "/readyz 未指名故障依赖"

HC=$(code http://127.0.0.1:$PUB/healthz)
if [ "$HC" = "200" ]; then
  ok "/healthz 仍 200(不重启进程:重启修不了数据库)"
else
  bad "/healthz 得 $HC —— liveness 失败会让所有副本同时 CrashLoopBackOff"
fi

info "4) 数据库恢复后自动放回"
docker compose -f "$COMPOSE" start postgres >/dev/null 2>&1
RECOVERED=0
for _ in $(seq 40); do
  [ "$(code http://127.0.0.1:$PUB/readyz)" = "200" ] && { RECOVERED=1; break; }
  sleep 1
done
[ "$RECOVERED" = 1 ] && ok "/readyz 恢复 200(无需重启进程)" || bad "/readyz 未自动恢复"

echo ""
echo "RESULT: pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ] && echo "PROBES OK" || echo "FAILURES"
[ "$FAIL" = 0 ]
