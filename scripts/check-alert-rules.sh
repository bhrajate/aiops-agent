#!/usr/bin/env bash
# 校验告警规则(P5):渲染 chart → 抓真实 /metrics → 用真实 PromQL 解析器对账。
#
# 抓两类问题,目视检查都发现不了:
#   1. 语法错误        —— Prometheus 加载时会整组拒绝规则;
#   2. 引用不存在的 series —— **不报错、规则永不触发**,看起来有覆盖实则没有。
#      这一类比语法错误危险:它占着"我们有监控"的名分。
#
# 第 2 类的对账基准是**真实抓取到的 series 名**,不是代码里的 Go 常量名。
# 用常量名对账等于自己和自己核对,漏掉"常量定义了但从未被调用"的情形
# ——本脚本第一次运行就抓到了这个:IncIncident 定义了却没人调,
# aiops_incidents_created_total 这个 series 根本不存在。
set -uo pipefail
cd "$(dirname "$0")/.."
COMPOSE=deploy/docker-compose.yml
PUB=8588
INT=8590
# 每次运行用唯一 namespace。incidents_created 只在**新建** incident 时 +1;
# 若沿用固定 namespace,上一次运行留下的活跃 incident 会把本次信号相关性合并
# 进去而不新建,该 series 就不出现 —— 被对账误判为"未导出"。
# 清库解决不了:incident 是在本次运行**中途**创建的,清理发生在启动前。
NS="archk-$$"

need(){ command -v "$1" >/dev/null 2>&1 || { echo "缺少 $1" >&2; exit 2; }; }
need helm; need python3; need docker

for p in $PUB $INT; do
  if command -v fuser >/dev/null 2>&1 && fuser "$p/tcp" >/dev/null 2>&1; then
    echo "  端口 $p 被占用,先清理" >&2; exit 2
  fi
done

echo "== 1) 渲染 chart(打开 monitoring)"
helm template aiops deploy/helm/aiops -f deploy/helm/aiops/values-prod.yaml \
  --set monitoring.serviceMonitor.enabled=true \
  --set monitoring.prometheusRule.enabled=true > /tmp/ar-mon.yaml || exit 1

python3 - <<'PY' || exit 1
import yaml, sys
out=[]
for d in yaml.safe_load_all(open('/tmp/ar-mon.yaml')):
    if d and d.get('kind')=='PrometheusRule':
        for g in d['spec']['groups']:
            for r in g['rules']:
                out.append(r['alert']+"\x1f"+r['expr'].strip())
if not out:
    print("渲染结果里没有任何告警规则", file=sys.stderr); sys.exit(1)
open('/tmp/ar-exprs.txt','w').write("\x1e".join(out))
print("   抽出 %d 条规则" % len(out))
PY

echo "== 2) 起控制面并抓真实 /metrics"
( cd control-plane && go build -o /tmp/cp-ar ./cmd/control-plane ) || exit 1
docker compose -f "$COMPOSE" up -d postgres redpanda >/dev/null 2>&1
for _ in $(seq 40); do
  docker compose -f "$COMPOSE" exec -T postgres pg_isready -U aiops -d aiops >/dev/null 2>&1 && break
  sleep 1
done

# 由脚本自身位置推导仓库根:比相对路径稳,任意 cwd 调用都对。
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 查库走 lib/db.sh:连不上或 SQL 出错时立刻终止,
# 而不是让断言收到空串然后照着空数据打分(见该文件顶部注释)。
source "$ROOT/scripts/lib/db.sh"
q(){ dbq "$1"; }
# 造出**带 label 的** gauge:它们只在有行时才存在,不造就会被误判为"不存在"。
q "DELETE FROM outbox WHERE topic LIKE 'ar-%'; DELETE FROM dead_letters WHERE topic LIKE 'ar-%';"
# 顺带清理历史运行留下的 archk-* 命名空间数据,避免无界堆积。
q "DELETE FROM alert_groups WHERE namespace LIKE 'archk-%';
   DELETE FROM incidents WHERE correlation_key LIKE '%|archk-%';"
q "INSERT INTO outbox (topic,key,payload,status) VALUES ('ar-t','k','{}','pending');"
q "INSERT INTO dead_letters (topic,key,payload,error,attempts) VALUES ('ar-t','k','{}','x',5);"

LOG=$(mktemp)
AIOPS_ENV=development AIOPS_ROLES="all" \
AIOPS_PUBLIC_ADDR=":$PUB" AIOPS_INTERNAL_ADDR=":$INT" \
AIOPS_DB_DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}" \
AIOPS_KAFKA_BROKERS="localhost:19092" \
AIOPS_INTERNAL_TOKEN=dev-token AIOPS_RETENTION_ENABLED=false \
AIOPS_INGRESS_RATE_PER_SEC=1 AIOPS_INGRESS_BURST=2 \
/tmp/cp-ar >"$LOG" 2>&1 &
CP_PID=$!
cleanup(){
  kill $CP_PID 2>/dev/null; wait $CP_PID 2>/dev/null
  q "DELETE FROM outbox WHERE topic LIKE 'ar-%'; DELETE FROM dead_letters WHERE topic LIKE 'ar-%';"
  q "DELETE FROM alert_groups WHERE namespace='$NS';
     DELETE FROM incidents WHERE correlation_key LIKE '%|$NS';"
}
trap cleanup EXIT

ready=0
for _ in $(seq 40); do
  curl -sf --max-time 3 "http://127.0.0.1:$PUB/healthz" >/dev/null 2>&1 && { ready=1; break; }
  kill -0 $CP_PID 2>/dev/null || break
  sleep 0.5
done
[ "$ready" = 1 ] || { echo "control-plane 未就绪:" >&2; tail -20 "$LOG" >&2; exit 1; }

# 触发计数器类指标:**带 label 的 counter 在 +1 之前不会出现在 /metrics 上**,
# 不触发就会被对账误判为"该 series 不存在"。
# 前两条落库(burst=2),后续被限流 —— 顺带让 ingress_throttled 也出现。
post(){ curl -s -o /dev/null --max-time 5 -X POST "http://127.0.0.1:$PUB/v1/signals" \
  -H 'Content-Type: application/json' \
  -d "{\"alerts\":[{\"status\":\"firing\",\"labels\":{\"alertname\":\"$1\",\"namespace\":\"$NS\",\"deployment\":\"d\",\"severity\":\"critical\"}}]}"; }
# 第一条必须放行,用来造出 incidents_created(burst=2 时它落在配额内)
post ArChkIncident
sleep 5   # 等 ingest 消费 → 建 incident → 指标 +1
# 再连打若干条触发限流,让 ingress_throttled 出现
for i in 1 2 3 4 5 6; do post "ArChkThrottle$i"; done
sleep 4
curl -s --max-time 8 "http://127.0.0.1:$INT/metrics" | grep -oE '^aiops_[a-z_]+' | sort -u > /tmp/ar-known.txt
echo "   抓到 $(wc -l < /tmp/ar-known.txt) 个 aiops_* series"

echo "== 3) 用真实 PromQL 解析器对账"
( cd control-plane && go run ./cmd/promqlcheck /tmp/ar-exprs.txt /tmp/ar-known.txt )
rc=$?
echo ""
[ "$rc" = 0 ] && echo "ALERT-RULES OK" || echo "FAILURES"
exit $rc
