#!/usr/bin/env bash
# 对一个已运行的 control-plane(:8088)做认证/RBAC/ABAC/幂等/webhook 检查。
set -u
BASE=http://localhost:8088
COMPOSE="$(dirname "$0")/../deploy/docker-compose.yml"

# 前置清库。本脚本的断言依赖"库里只有自己造的 incident":此前它取
# incidents[0] 就当成自己的 payment incident,于是别的脚本(如 check-ratelimit
# 造 namespace=rl 的 incident)留下的行会被当成断言对象 —— bob/viewer 对 rl
# 确实无权,于是报出 4 个"RBAC/ABAC 失败",而鉴权其实完全正确。
# 这是假失败,和之前几个脚本的假通过是同一类问题:断言前不确认前置状态。
# 由脚本自身位置推导仓库根:任意 cwd 调用都对。
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 查库走 lib/db.sh:连不上或 SQL 出错立刻终止,不让断言照着残留数据打分。
source "$ROOT/scripts/lib/db.sh"
dbx "TRUNCATE signals, alert_groups, incidents, investigations, evidence, hypotheses,
   investigation_events, human_feedback, outbox, audit_log, idempotency_keys,
   dead_letters CASCADE;"

# 后端就绪预检:curl 静默失败会让所有断言拿到空值,报出误导性结论。
curl -sf "$BASE/healthz" >/dev/null 2>&1 || {
  echo "control-plane(:8088)未就绪 —— 请用 scripts/with-backend.sh 运行本脚本" >&2
  exit 2
}
login(){ curl -s $BASE/v1/auth/login -H 'Content-Type: application/json' -d "{\"username\":\"$1\",\"password\":\"$2\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))'; }
code(){ curl -s -o /dev/null -w "%{http_code}" "$@"; }
jget(){ python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

ALICE=$(login alice alice-pass); BOB=$(login bob bob-pass); VIEWER=$(login viewer viewer-pass)

# 注入 signal(带正确 HMAC)
BODY='{"alerts":[{"status":"firing","labels":{"alertname":"HighErrorRate","severity":"critical","namespace":"payment","deployment":"checkout","cluster":"prod-cn-1","rule_id":"r-101"},"startsAt":"2026-07-26T10:00:00Z"}]}'
SIG=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $SIG" -d "$BODY"

# 等待聚合成 incident(最多 25s)
# 按 namespace 精确定位自己造的 incident。不用 incidents[0]:那取的是"最新一条",
# 库里有任何其他 incident 时就会张冠李戴。
INC=""
for i in $(seq 1 25); do
  INC=$(curl -s "$BASE/v1/incidents" -H "Authorization: Bearer $ALICE" | jget '
next((x["incident_id"] for x in d.get("incidents",[])
      if any(r.get("namespace")=="payment" for r in x.get("affected_resources",[]))), "")
' 2>/dev/null)
  [ -n "$INC" ] && break; sleep 1
done
echo "incident=$INC"
[ -z "$INC" ] && { echo "NO INCIDENT — abort"; exit 1; }

pass=0; fail=0
check(){ if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; pass=$((pass+1)); else echo "  ✗ $1 期望 $2 实得 $3"; fail=$((fail+1)); fi; }

echo "=== 认证 ==="
check "未认证 → 401" 401 "$(code $BASE/v1/incidents)"
check "带 token → 200" 200 "$(code $BASE/v1/incidents -H "Authorization: Bearer $ALICE")"
check "错误密码 → 401" 401 "$(code $BASE/v1/auth/login -H 'Content-Type: application/json' -d '{"username":"alice","password":"x"}')"
check "垃圾 token → 401" 401 "$(code $BASE/v1/incidents -H 'Authorization: Bearer garbage')"

echo "=== RBAC ==="
check "viewer 启动调查 → 403" 403 "$(code -X POST $BASE/v1/incidents/$INC/investigations -H "Authorization: Bearer $VIEWER")"
# 已有自动调查时手动启动返回既有(200);否则新建(202)——两者皆合法
BOBSTART=$(code -X POST $BASE/v1/incidents/$INC/investigations -H "Authorization: Bearer $BOB" -H 'Idempotency-Key: k1')
check "bob 启动调查 → 200或202" "1" "$([ "$BOBSTART" = 200 ] || [ "$BOBSTART" = 202 ] && echo 1 || echo 0)"

echo "=== ABAC ==="
check "viewer 读 payment incident → 200" 200 "$(code $BASE/v1/incidents/$INC -H "Authorization: Bearer $VIEWER")"
# 注入一个 viewer/bob 都无权访问的命名空间(inventory)的 incident,验证越权拦截
OBODY='{"alerts":[{"status":"firing","labels":{"alertname":"OOMKilled","severity":"critical","namespace":"inventory","deployment":"stock-api","cluster":"prod-cn-1","rule_id":"r-777"},"startsAt":"2026-07-26T10:00:00Z"}]}'
OSIG=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$OBODY'''.encode(),hashlib.sha256).hexdigest())")
curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $OSIG" -d "$OBODY"
OINC=""
for i in $(seq 1 20); do
  OINC=$(curl -s "$BASE/v1/incidents" -H "Authorization: Bearer $ALICE" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(next((x["incident_id"] for x in d.get("incidents",[]) if x["affected_resources"] and x["affected_resources"][0].get("namespace")=="inventory"),""))' 2>/dev/null)
  [ -n "$OINC" ] && break; sleep 1
done
echo "  inventory incident=$OINC"
check "bob 读 inventory incident → 403(越权)" 403 "$(code $BASE/v1/incidents/$OINC -H "Authorization: Bearer $BOB")"
check "alice(sre)读 inventory → 200" 200 "$(code $BASE/v1/incidents/$OINC -H "Authorization: Bearer $ALICE")"
# viewer 列表不应看到 inventory(ABAC 过滤)
VLIST=$(curl -s "$BASE/v1/incidents" -H "Authorization: Bearer $VIEWER" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d.get("incidents",[]) if x["affected_resources"] and x["affected_resources"][0].get("namespace")=="inventory"))')
check "viewer 列表已过滤掉 inventory" "0" "$VLIST"

echo "=== 幂等 ==="
R1=$(curl -s -X POST $BASE/v1/incidents/$INC/investigations -H "Authorization: Bearer $BOB" -H 'Idempotency-Key: k1' | jget 'd.get("investigation_id","")')
R2=$(curl -s -X POST $BASE/v1/incidents/$INC/investigations -H "Authorization: Bearer $BOB" -H 'Idempotency-Key: k1' | jget 'd.get("investigation_id","")')
check "同 key 返回同一调查(非空)" "1" "$([ -n "$R1" ] && [ "$R1" = "$R2" ] && echo 1 || echo 0)"

echo "=== webhook ==="
check "无签名 signal → 401" 401 "$(code -X POST $BASE/v1/signals -H 'Content-Type: application/json' -d '{"alerts":[]}')"

echo "=== 内部 API token ==="
check "内部 API 无 token → 401" 401 "$(code -X POST http://localhost:8090/internal/investigations/$R1/phase -H 'Content-Type: application/json' -d '{"phase":"planning"}')"
check "内部 API 带 token → 200" 200 "$(code -X POST http://localhost:8090/internal/investigations/$R1/phase -H "X-Internal-Token: ${AIOPS_INTERNAL_TOKEN:-dev-token}" -H 'Content-Type: application/json' -d '{"phase":"planning"}')"

echo ""; echo "RESULT: pass=$pass fail=$fail"; [ "$fail" = 0 ] && echo "ALL PASSED" || echo "FAILURES"