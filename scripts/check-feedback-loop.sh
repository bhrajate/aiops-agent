#!/usr/bin/env bash
# 验证反馈闭环:人工反馈 → 待审 Golden Case → 审核 → 进入评测集。
#
# human_feedback 表从 000001 起就在收反馈,注释也写明"先进入审核队列,审核后才能
# 成为 Golden Case",但**从来没有实现那条通路**。反馈躺在表里,系统学不到任何东西。
# 而读取端(evaluation/store.py)一直按 review_status='approved' 过滤 ——
# 消费方早就准备好了,只缺生产方。
#
# 核心断言是**提升出来必须是 pending**:评测集决定发布质量门槛,一条错误标注会让
# 门槛失真,而这种失真极难发现(门槛照常通过或照常失败,只是标准错了)。
#
# 自起 control-plane,不要用 with-backend.sh 包裹。
set -uo pipefail
cd "$(dirname "$0")/.."
COMPOSE=deploy/docker-compose.yml
PUB=9088
INT=9090
# 必须用 bob 的 ABAC 范围内的 namespace(payment/cart),否则反馈会被
# "incident out of your access scope" 拒掉 —— 那是鉴权正确工作,不是缺陷。
# 用 payment 顺带验证了"oncall 在自己范围内可提交反馈"。
NS="payment"
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

# 由脚本自身位置推导仓库根:比相对路径稳,任意 cwd 调用都对。
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 查库走 lib/db.sh:连不上或 SQL 出错时立刻终止,
# 而不是让断言收到空串然后照着空数据打分(见该文件顶部注释)。
source "$ROOT/scripts/lib/db.sh"
q(){ dbq "$1"; }
qx(){ dbx "$1"; }
login(){ curl -s --max-time 6 "http://127.0.0.1:$PUB/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$1\",\"password\":\"$2\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))'; }

info "构建"
( cd control-plane && go build -o /tmp/cp-fl ./cmd/control-plane ) || exit 1

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
/tmp/cp-fl >"$LOG" 2>&1 &
CP_PID=$!
cleanup(){
  kill $CP_PID 2>/dev/null; wait $CP_PID 2>/dev/null
  qx "DELETE FROM golden_cases WHERE incident_id IN
        (SELECT incident_id FROM incidents WHERE correlation_key LIKE '%|$NS');
      DELETE FROM signals WHERE labels->>'namespace' = '$NS';
      DELETE FROM alert_groups WHERE namespace = '$NS';
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

# 前置清库:NS 固定为 payment,历史数据会让 correlation 合并到旧 incident,
# 从而拿到一个没有本次反馈的 investigation。
qx "DELETE FROM golden_cases WHERE incident_id IN
      (SELECT incident_id FROM incidents WHERE correlation_key LIKE '%|$NS');
    DELETE FROM signals WHERE labels->>'namespace' = '$NS';
    DELETE FROM alert_groups WHERE namespace = '$NS';
    DELETE FROM incidents WHERE correlation_key LIKE '%|$NS';"

info "造 P1 incident 并拿到 investigation"
curl -s -o /dev/null --max-time 6 -X POST "http://127.0.0.1:$PUB/v1/signals" \
  -H 'Content-Type: application/json' \
  -d "{\"alerts\":[{\"status\":\"firing\",\"fingerprint\":\"fp-fl-$$\",\"startsAt\":\"2026-07-29T10:00:00Z\",
       \"labels\":{\"alertname\":\"ServiceDown\",\"severity\":\"critical\",\"namespace\":\"$NS\",\"deployment\":\"checkout\"}}]}"
INV=""
for _ in $(seq 25); do
  INV=$(q "SELECT i.investigation_id FROM investigations i JOIN incidents c ON c.incident_id=i.incident_id
           WHERE c.correlation_key LIKE '%|$NS' LIMIT 1;")
  [ -n "$INV" ] && break; sleep 1
done
[ -n "$INV" ] || { echo "未拿到 investigation" >&2; tail -20 "$LOG" >&2; exit 1; }
echo "   investigation=$INV"

ALICE=$(login alice alice-pass)      # sre:可审核
BOB=$(login bob bob-pass)            # oncall:可反馈,不可审核
[ -n "$ALICE" ] && [ -n "$BOB" ] || { bad "登录失败"; echo "$LOG"; exit 1; }

info "1) confirm 反馈 → 自动提升为待审用例"
curl -s -o /dev/null --max-time 6 -X POST "http://127.0.0.1:$PUB/v1/investigations/$INV/feedback" \
  -H "Authorization: Bearer $BOB" -H 'Content-Type: application/json' \
  -d '{"action":"confirm","confirmed_root_cause":"连接池大小配置回归导致下游超时"}'
sleep 2
CID=$(q "SELECT case_id FROM golden_cases WHERE investigation_id='$INV';")
[ -n "$CID" ] && ok "已提升为用例($CID)" || bad "反馈未产出用例 —— 闭环未接通"

info "2) 必须是 pending(核心断言)"
ST=$(q "SELECT review_status FROM golden_cases WHERE investigation_id='$INV';")
[ "$ST" = "pending" ] && ok "review_status=pending" \
  || bad "review_status=$ST —— 未经审核直接进评测集会让质量门槛失真"

info "3) provenance 可追溯"
SRC=$(q "SELECT source FROM golden_cases WHERE investigation_id='$INV';")
PB=$(q "SELECT promoted_by FROM golden_cases WHERE investigation_id='$INV';")
[ "$SRC" = "human_feedback" ] && ok "source=$SRC" || bad "source=$SRC"
[ "$PB" = "bob" ] && ok "promoted_by=$PB" || bad "promoted_by=$PB(应为反馈提交者)"

info "4) 重复反馈不产生第二条用例"
curl -s -o /dev/null --max-time 6 -X POST "http://127.0.0.1:$PUB/v1/investigations/$INV/feedback" \
  -H "Authorization: Bearer $BOB" -H 'Content-Type: application/json' \
  -d '{"action":"correct","confirmed_root_cause":"其实是磁盘满"}'
sleep 2
N=$(q "SELECT count(*) FROM golden_cases WHERE investigation_id='$INV';")
[ "$N" = "1" ] && ok "仍只有 1 条用例" || bad "产生了 $N 条 —— 一次故障会在评测集里加权"

info "5) 待审队列可查(仅 sre/admin)"
CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 \
  "http://127.0.0.1:$PUB/v1/golden-cases?status=pending" -H "Authorization: Bearer $ALICE")
[ "$CODE" = "200" ] && ok "sre 可查待审队列" || bad "sre 查询得 $CODE"
CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 \
  "http://127.0.0.1:$PUB/v1/golden-cases?status=pending" -H "Authorization: Bearer $BOB")
[ "$CODE" = "403" ] && ok "oncall 不可查(决定'什么算正确答案'应由更少的人负责)" \
  || bad "oncall 查询得 $CODE,期望 403"

info "6) 审核:oncall 被拒,sre 通过"
CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 -X POST \
  "http://127.0.0.1:$PUB/v1/golden-cases/$CID/review" -H "Authorization: Bearer $BOB" \
  -H 'Content-Type: application/json' -d '{"status":"approved"}')
[ "$CODE" = "403" ] && ok "oncall 审核被拒" || bad "oncall 审核得 $CODE,期望 403"
CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 -X POST \
  "http://127.0.0.1:$PUB/v1/golden-cases/$CID/review" -H "Authorization: Bearer $ALICE" \
  -H 'Content-Type: application/json' -d '{"status":"approved","note":"复核无误"}')
[ "$CODE" = "200" ] && ok "sre 审核通过" || bad "sre 审核得 $CODE"

info "7) 审核后进入评测集(read 端按 approved 过滤)"
ST=$(q "SELECT review_status FROM golden_cases WHERE case_id='$CID';")
RB=$(q "SELECT reviewed_by FROM golden_cases WHERE case_id='$CID';")
[ "$ST" = "approved" ] && ok "review_status=approved" || bad "review_status=$ST"
[ "$RB" = "alice" ] && ok "reviewed_by=alice(问责可追溯)" || bad "reviewed_by=$RB"

info "8) 不可翻转(审核是一次决定)"
CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 6 -X POST \
  "http://127.0.0.1:$PUB/v1/golden-cases/$CID/review" -H "Authorization: Bearer $ALICE" \
  -H 'Content-Type: application/json' -d '{"status":"rejected"}')
[ "$CODE" = "400" ] && ok "已审核的不可再改" || bad "重复审核得 $CODE,期望 400"

info "9) 指标已上报"
M=$(curl -s --max-time 8 "http://127.0.0.1:$INT/metrics")
echo "$M" | grep -q '^aiops_golden_cases_promoted_total' \
  && ok "aiops_golden_cases_promoted_total 已上报" || bad "缺少提升计数指标"

echo ""
echo "RESULT: pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ] && echo "FEEDBACK-LOOP OK" || echo "FAILURES"
[ "$FAIL" = 0 ]
