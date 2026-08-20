#!/usr/bin/env bash
# 验证 K8s Event watch 的端到端契约(补 ARCHITECTURE 能力边界里的 "仅 pull")。
#
# 不起真实 K8s:用 agent 的 Signal 构造逻辑产出载荷,直接投给真实 control-plane
# ingress,验证**幂等**这一条最要紧的性质 —— 它错了不报错,只是 signal_count 虚增,
# 而那个数喂给触发策略判"信号突发",于是会多跑几轮 RCA。
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/lib/db.sh"

BASE="http://localhost:${AIOPS_EW_PORT:-8088}"
SECRET=webhook-dev-secret
NS="ew-$RANDOM"
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

curl -sf "$BASE/healthz" >/dev/null 2>&1 || {
  echo "control-plane($BASE)未就绪 —— 请用 scripts/with-backend.sh 运行本脚本" >&2
  exit 2
}
cleanup(){ db_purge_ns "$NS"; }
trap cleanup EXIT
db_purge_ns "$NS"

# post <starts_at> —— 构造与 eventwatch.ToSignal 同形的原生格式载荷。
# 刻意**不带 signal_id**:幂等键由控制面 DeriveSignalID 推导。
# 也刻意**不带 count**:它递增,会破坏同桶内的载荷稳定性。
post(){
  local starts="$1"
  local body
  body=$(printf '{"cluster_id":"prod-cn-1","tenant_id":"default","source":"kubernetes","signal_type":"alert","severity":"critical","starts_at":"%s","resource_ref":{"namespace":"%s","kind":"Pod","name":"checkout-abc"},"labels":{"alertname":"K8sEventOOMKilling","rule_id":"k8s-event-oomkilling","reason":"OOMKilling","namespace":"%s","detector":"kubernetes","event_uid":"uid-1"}}' "$starts" "$NS" "$NS")
  local sig
  sig=$(python3 -c "import hmac,hashlib,sys;print('sha256='+hmac.new(b'$SECRET',sys.argv[1].encode(),hashlib.sha256).hexdigest())" "$body")
  curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/signals" \
    -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $sig" -d "$body"
}

echo "== 1) 首次上报应被接收并聚合成 incident"
B1="2026-08-20T10:00:00Z"
[ "$(post "$B1")" = "202" ] && ok "首次上报 202" || bad "首次上报未返回 202"
sleep 4
N=$(dbq "select count(*) from signals where labels->>'namespace'='$NS'")
[ "$N" = "1" ] && ok "落库 1 条 signal" || bad "落库 $N 条(期望 1)"
INC=$(dbq "select count(*) from incidents where correlation_key like '%|$NS'")
[ "$INC" = "1" ] && ok "聚合成 1 个 incident" || bad "incident 数 = $INC(期望 1)"

echo "== 2) 同一时间桶内重复上报必须被去重(防 F12 虚增)"
# 模拟 informer 的 resync 与 count 递增:同一桶 → 同载荷 → 同 signal_id。
for _ in 1 2 3 4; do post "$B1" >/dev/null; done
sleep 4
N2=$(dbq "select count(*) from signals where labels->>'namespace'='$NS'")
[ "$N2" = "1" ] && ok "重复 4 次仍只 1 条 signal" || bad "重复后 $N2 条 —— 幂等失效"
SC=$(dbq "select signal_count from incidents where correlation_key like '%|$NS'")
[ "$SC" = "1" ] && ok "signal_count 未虚增(仍 1)" \
  || bad "signal_count = $SC —— 虚增会误触发 signal_burst 并多跑 RCA"

echo "== 3) 跨时间桶必须产生新 signal(让 last_seen 前移)"
# 桶宽 5 分钟,所以 10:07 落在下一个桶。
B2="2026-08-20T10:07:00Z"
post "$B2" >/dev/null
sleep 4
N3=$(dbq "select count(*) from signals where labels->>'namespace'='$NS'")
[ "$N3" = "2" ] && ok "跨桶产生第 2 条 signal" \
  || bad "跨桶后 $N3 条(期望 2)—— last_seen 不会前移,持续故障看起来已结束"

echo "== 4) 来源可区分(回答'多少故障是主动发现的')"
SRC=$(dbq "select count(*) from signals where labels->>'namespace'='$NS' and source='kubernetes'")
[ "$SRC" = "2" ] && ok "source=kubernetes 可区分" || bad "source 不可区分($SRC)"

echo "== 5) 缺签名必须被拒(agent 持有 secret 是新增的威胁面)"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/signals" \
  -H 'Content-Type: application/json' -d '{"cluster_id":"x","source":"kubernetes","signal_type":"alert"}')
[ "$CODE" = "401" ] && ok "无签名 → 401" || bad "无签名返回 $CODE(期望 401)"

echo ""
echo "RESULT: pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ] && echo "EVENT-WATCH OK" || { echo "FAILURES"; exit 1; }
