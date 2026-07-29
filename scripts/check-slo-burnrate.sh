#!/usr/bin/env bash
# 验证主动异常检测:SLO 燃尽率越限 → 合成 signal → 走既有管道 → 产出 incident。
#
# 此前系统完全**被动** —— 只在告警流入时才有反应。没有告警规则覆盖的缓慢退化
# (错误率从 0.05% 爬到 0.4%,不触发任何静态阈值)完全看不见,直到变成用户投诉。
#
# 本脚本起一个**真实 Prometheus**,喂给它一个自制指标(通过 static_configs 抓一个
# 本地 exporter),使错误率可控。这样燃尽率是确定的,不依赖任何外部环境。
#
# 核心断言:
#   1. 越限时合成 signal 并走既有入口(自动获得两层聚合/触发策略/幂等/审计);
#   2. 持续燃烧只产出一条 signal(身份稳定,否则 signal_count 暴涨误触发 burst);
#   3. 未越限时不产出(否则就是一个只会喊狼来了的检测器)。
set -uo pipefail
cd "$(dirname "$0")/.."
COMPOSE=deploy/docker-compose.yml
PUB=9288
INT=9290
PROM=9291
EXP=9292
NS="payment"          # 与既有 dev 用户的 ABAC 范围一致
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
info(){ echo "== $1"; }

for p in $PUB $INT $PROM $EXP; do
  if command -v fuser >/dev/null 2>&1 && fuser "$p/tcp" >/dev/null 2>&1; then
    echo "  端口 $p 被占用,先清理" >&2; exit 2
  fi
done
command -v docker >/dev/null || { echo "需要 docker" >&2; exit 2; }

q(){ docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -tAc "$1" 2>/dev/null | tr -d ' ' | sed '/^$/d'; }
qx(){ docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -q -c "$1" >/dev/null 2>&1; }

WORK=$(mktemp -d)
purge_ns(){
  # psql -c 是**单事务**:外键阻塞会让整批回滚,连 signals 的删除一起撤销
  # (实测正是如此 —— investigations 引用 incidents,清库静默失效,
  #  而断言拿着残留数据给出假结果:先假通过,修了关联断言后又假失败)。
  # 故按"子表先删"的顺序分多次执行,与其他检查脚本一致。
  qx "DELETE FROM signals WHERE source='slo-burn-rate';"
  qx "DELETE FROM evidence WHERE investigation_id IN
        (SELECT i.investigation_id FROM investigations i
           JOIN incidents c ON c.incident_id=i.incident_id
          WHERE c.correlation_key LIKE '%|$NS');
      DELETE FROM hypotheses WHERE investigation_id IN
        (SELECT i.investigation_id FROM investigations i
           JOIN incidents c ON c.incident_id=i.incident_id
          WHERE c.correlation_key LIKE '%|$NS');
      DELETE FROM investigation_events WHERE investigation_id IN
        (SELECT i.investigation_id FROM investigations i
           JOIN incidents c ON c.incident_id=i.incident_id
          WHERE c.correlation_key LIKE '%|$NS');
      DELETE FROM human_feedback WHERE investigation_id IN
        (SELECT i.investigation_id FROM investigations i
           JOIN incidents c ON c.incident_id=i.incident_id
          WHERE c.correlation_key LIKE '%|$NS');"
  qx "DELETE FROM golden_cases WHERE incident_id IN
        (SELECT incident_id FROM incidents WHERE correlation_key LIKE '%|$NS');
      DELETE FROM investigations WHERE incident_id IN
        (SELECT incident_id FROM incidents WHERE correlation_key LIKE '%|$NS');"
  qx "DELETE FROM signals WHERE labels->>'namespace' = '$NS';
      DELETE FROM alert_groups WHERE namespace = '$NS';
      DELETE FROM incidents WHERE correlation_key LIKE '%|$NS';"
}

cleanup(){
  [ -n "${CP_PID:-}" ] && kill $CP_PID 2>/dev/null
  docker rm -f aiops-slo-prom aiops-slo-exp >/dev/null 2>&1
  docker network rm aiops-slo-net >/dev/null 2>&1
  # 退出清理复用同一个函数(定义在下方,此处为函数体内引用,调用时已定义)
  purge_ns 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

info "起自制 exporter(容器,与 Prometheus 同网络)"
# exporter 也跑成容器并与 Prometheus 共用一个 docker 网络:
# host.docker.internal 在 WSL 环境下不可靠(实测抓不到),而同网络内按容器名
# 互访是确定的。错误率写在挂载的文件里,改文件即改错误率。
cat > "$WORK/metrics.txt" <<'EOF'
# HELP slo_test_error_ratio Controlled error ratio for SLO watcher test
# TYPE slo_test_error_ratio gauge
slo_test_error_ratio 0
EOF
cat > "$WORK/serve.py" <<'EOF'
import http.server, socketserver
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        try:
            body = open('/data/metrics.txt', 'rb').read()
        except OSError:
            body = b''
        self.send_response(200)
        self.send_header('Content-Type', 'text/plain; version=0.0.4')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(('0.0.0.0', 8000), H) as s:
    s.serve_forever()
EOF
docker network create aiops-slo-net >/dev/null 2>&1 || true
docker run -d --rm --name aiops-slo-exp --network aiops-slo-net \
  -v "$WORK:/data:ro" python:3.11-alpine \
  python /data/serve.py >/dev/null 2>&1 || { echo "exporter 容器启动失败" >&2; exit 1; }
expok=0
for _ in $(seq 40); do
  docker run --rm --network aiops-slo-net curlimages/curl:latest \
    -sf --max-time 2 "http://aiops-slo-exp:8000/metrics" >/dev/null 2>&1 && { expok=1; break; }
  sleep 1
done
[ "$expok" = 1 ] && ok "exporter 就绪" || { bad "exporter 未就绪"; exit 1; }

info "起真实 Prometheus 抓它"
cat > "$WORK/prometheus.yml" <<'EOF'
global:
  scrape_interval: 1s
  evaluation_interval: 1s
scrape_configs:
  - job_name: slo-test
    static_configs:
      - targets: ['aiops-slo-exp:8000']
EOF
docker run -d --rm --name aiops-slo-prom \
  --network aiops-slo-net \
  -p "$PROM:9090" \
  -v "$WORK/prometheus.yml:/etc/prometheus/prometheus.yml:ro" \
  prom/prometheus:latest \
  --config.file=/etc/prometheus/prometheus.yml \
  --storage.tsdb.retention.time=1h >/dev/null 2>&1 || { echo "Prometheus 启动失败" >&2; exit 1; }
ready=0
for _ in $(seq 60); do
  curl -sf --max-time 2 "http://127.0.0.1:$PROM/-/ready" >/dev/null 2>&1 && { ready=1; break; }
  sleep 1
done
[ "$ready" = 1 ] && ok "Prometheus 就绪" || { bad "Prometheus 未就绪"; exit 1; }
# 先确认抓取目标是 up —— 抓不到时所有断言都会失败,但根因在这里,
# 直接摊开 lastError 比让 7 条断言各报一次空值有用得多。
for _ in $(seq 30); do
  h=$(curl -s --max-time 3 "http://127.0.0.1:$PROM/api/v1/targets" \
      | python3 -c 'import sys,json;ts=json.load(sys.stdin)["data"]["activeTargets"];print(ts[0]["health"] if ts else "none")' 2>/dev/null || echo none)
  [ "$h" = "up" ] && break; sleep 1
done
if [ "${h:-none}" != "up" ]; then
  bad "Prometheus 抓取目标不健康(health=$h)"
  curl -s --max-time 3 "http://127.0.0.1:$PROM/api/v1/targets" \
    | python3 -c 'import sys,json
ts=json.load(sys.stdin)["data"]["activeTargets"]
for t in ts: print("   url=",t["scrapeUrl"],"err=",t.get("lastError","")[:160])' 2>/dev/null
  exit 1
fi
ok "抓取目标 up"

# 等它抓到至少一个样本
for _ in $(seq 30); do
  n=$(curl -s --max-time 3 "http://127.0.0.1:$PROM/api/v1/query?query=slo_test_error_ratio" \
      | python3 -c 'import sys,json;print(len(json.load(sys.stdin)["data"]["result"]))' 2>/dev/null || echo 0)
  [ "$n" -ge 1 ] 2>/dev/null && break; sleep 1
done
ok "指标已被抓取"

info "构建并起控制面(SLO 监视开启,间隔 3s)"
( cd control-plane && go build -o /tmp/cp-slo ./cmd/control-plane ) || exit 1
docker compose -f "$COMPOSE" up -d postgres redpanda >/dev/null 2>&1
for _ in $(seq 40); do
  docker compose -f "$COMPOSE" exec -T postgres pg_isready -U aiops -d aiops >/dev/null 2>&1 && break
  sleep 1
done
# 前置清库。必须在控制面启动**之前**:上一轮运行若被打断,残留的 slo-burn-rate
# signal 会让"持续燃烧只产出 1 条"这条断言假通过或假失败 ——
# 实测两种都撞到过(先假通过,修了关联断言后又假失败)。
purge_ns
LEFT=$(q "SELECT count(*) FROM signals WHERE source='slo-burn-rate';")
[ "${LEFT:-0}" = "0" ] && ok "前置清库完成(0 条残留)" \
  || { bad "清库未生效,仍有 $LEFT 条残留 —— 后续断言不可信"; exit 1; }

# SLI:objective 0.999 → 错误预算 0.001 → fast 档阈值 = 14.4 × 0.001 = 0.0144
# 表达式里 "+ 0 * ...min_over_time..." 那一项恒为 0,加它只为让 $WINDOW 出现**两次**
# —— 顺带验证占位符被全部替换(只替换第一处会让分子分母用不同窗口,
# 算出的比率毫无意义却仍是个数字,不会报错)。
# 表达式用 max_over_time 保留 $WINDOW 语义(两处,验证全部替换)。
SLIS=$(cat <<'EOF'
[{"name":"slo-test","namespace":"payment","service":"checkout","objective":0.999,
  "error_ratio_expr":"max(max_over_time(slo_test_error_ratio[$WINDOW])) + 0 * max(min_over_time(slo_test_error_ratio[$WINDOW]))"}]
EOF
)
LOG=$(mktemp)
AIOPS_ENV=development AIOPS_ROLES="all" \
AIOPS_PUBLIC_ADDR=":$PUB" AIOPS_INTERNAL_ADDR=":$INT" \
AIOPS_DB_DSN="postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable" \
AIOPS_KAFKA_BROKERS="localhost:19092" \
AIOPS_PROM_URL="http://127.0.0.1:$PROM" \
AIOPS_CLUSTER_LABEL_DISABLED=true \
AIOPS_INTERNAL_TOKEN=dev-token AIOPS_RETENTION_ENABLED=false \
AIOPS_TOPOLOGY_ENABLED=false \
AIOPS_SLO_ENABLED=true AIOPS_SLO_INTERVAL_SEC=3 \
AIOPS_SLO_DEFINITIONS="$SLIS" \
/tmp/cp-slo >"$LOG" 2>&1 &
CP_PID=$!
ready=0
for _ in $(seq 40); do
  curl -sf --max-time 3 "http://127.0.0.1:$PUB/healthz" >/dev/null 2>&1 && { ready=1; break; }
  kill -0 $CP_PID 2>/dev/null || break
  sleep 0.5
done
[ "$ready" = 1 ] || { echo "control-plane 未就绪:" >&2; tail -25 "$LOG" >&2; exit 1; }
grep -q "slo watcher enabled" "$LOG" && ok "SLO 监视已启用" || bad "SLO 监视未启用:$(grep -i slo "$LOG"|head -2)"

info "1) 错误率为 0 → 不应产出 signal"
sleep 9
N=$(q "SELECT count(*) FROM signals WHERE source='slo-burn-rate';")
[ "$N" = "0" ] && ok "未越限时不产出(0 条)" \
  || bad "未越限却产出了 $N 条 —— 只会喊狼来了的检测器比没有更糟"

info "2) 错误率拉到 5%(远超 fast 档阈值 1.44%)→ 应产出 signal"
cat > "$WORK/metrics.txt" <<'EOF'
# HELP slo_test_error_ratio Controlled error ratio for SLO watcher test
# TYPE slo_test_error_ratio gauge
slo_test_error_ratio 0.05
EOF
FOUND=0
for _ in $(seq 30); do
  N=$(q "SELECT count(*) FROM signals WHERE source='slo-burn-rate';")
  [ "${N:-0}" -ge 1 ] 2>/dev/null && { FOUND=1; break; }
  sleep 1
done
[ "$FOUND" = 1 ] && ok "越限后产出 signal" || { bad "越限后未产出 signal"; grep -i slo "$LOG"|tail -5; }

info "3) 走既有管道:severity/来源/资源引用正确"
SEV=$(q "SELECT severity FROM signals WHERE source='slo-burn-rate' LIMIT 1;")
[ "$SEV" = "critical" ] && ok "severity=critical(14.4× 档 → P1)" || bad "severity=$SEV"
SVC=$(q "SELECT resource_ref->>'name' FROM signals WHERE source='slo-burn-rate' LIMIT 1;")
[ "$SVC" = "checkout" ] && ok "资源引用指向具体服务" || bad "资源引用=$SVC"
TIER=$(q "SELECT labels->>'slo_tier' FROM signals WHERE source='slo-burn-rate' LIMIT 1;")
[ "$TIER" = "fast" ] && ok "命中最严重档(slo_tier=fast)" || bad "slo_tier=$TIER"

info "4) 聚合为 incident(证明走的是既有管道,不是另开一条路)"
# 必须断言 incident 是**由这条 SLO signal 产生**的,而不是"namespace 下存在
# 某个 incident" —— 后者会被其他脚本的残留数据满足(实测撞到过一次假通过:
# 拿到的 incident_id 与上一轮失败运行的完全相同)。
# 经 signals.incident_id 反查是唯一可靠的关联。
INC=""
for _ in $(seq 25); do
  INC=$(q "SELECT incident_id FROM signals
            WHERE source='slo-burn-rate' AND incident_id IS NOT NULL LIMIT 1;")
  [ -n "$INC" ] && break; sleep 1
done
[ -n "$INC" ] && ok "SLO signal 已归属 incident($INC)" \
  || bad "SLO signal 未归属任何 incident —— 没走既有聚合管道"
if [ -n "$INC" ]; then
  ISEV=$(q "SELECT severity FROM incidents WHERE incident_id='$INC';")
  [ "$ISEV" = "P1" ] && ok "incident severity=P1(critical 归一化正确)" || bad "incident severity=$ISEV"
  ICAT=$(q "SELECT fault_category FROM incidents WHERE incident_id='$INC';")
  echo "   fault_category=$ICAT"
fi

info "5) 持续燃烧只产出一条 signal(身份稳定)"
sleep 10
N=$(q "SELECT count(*) FROM signals WHERE source='slo-burn-rate';")
[ "$N" = "1" ] && ok "持续燃烧仍是 1 条" \
  || bad "产出了 $N 条 —— signal_count 暴涨会误触发 signal_burst"

info "6) 审计与指标"
A=$(q "SELECT count(*) FROM audit_log WHERE action='signal_ingest';")
M=$(curl -s --max-time 8 "http://127.0.0.1:$INT/metrics")
echo "$M" | grep -q '^aiops_slo_evaluations_total{' && ok "aiops_slo_evaluations_total 已上报" \
  || bad "缺少 SLO 评估指标"
echo "$M" | grep -q 'breached="true"' && ok "记录了越限评估" || bad "未记录越限"
echo "$M" | grep -q 'aiops_signals_ingested_total{source="slo-burn-rate"}' \
  && ok "信号来源可区分(能回答'多少故障是主动发现的')" \
  || bad "signals_ingested 未按 slo-burn-rate 分维度"

echo ""
echo "RESULT: pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ] && echo "SLO-BURNRATE OK" || echo "FAILURES"
[ "$FAIL" = 0 ]
