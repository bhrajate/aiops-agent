#!/usr/bin/env bash
# 负载下的护栏验证 + 吞吐基线。包装 loadtest-ingress.py 并补上查库断言。
#
# 补的是 ACCEPTANCE 里"对接真实后端并**压测**"那条 —— 它此前没有任何可跑的东西。
# 绝对容量数字确实需要生产硬件,但两类问题在任何硬件上都能验,
# 而它们恰好是压测真正要回答的:
#
#   1. 限流是否真的按配置值生效(放行一切的限流器能通过所有功能测试)
#   2. **并发**下幂等是否仍成立(串行重投递已有覆盖,并发会撞 ON CONFLICT 的竞态)
#
# 第 2 条的后果:重复落库让 incidents.signal_count 虚增,而那个数喂给触发策略
# 判"信号突发"(burst_signals=3),于是一条告警被读成影响面扩大、拉起多轮 RCA。
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/lib/db.sh"

BASE="http://localhost:${AIOPS_LT_PORT:-8088}"
SECRET=webhook-dev-secret
NS="loadtest"
RATE="${AIOPS_LT_RATE:-50}"
DUR="${AIOPS_LT_DURATION:-12}"

PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

curl -sf "$BASE/healthz" >/dev/null 2>&1 || {
  echo "control-plane($BASE)未就绪 —— 请用 scripts/with-backend.sh 运行本脚本" >&2
  exit 2
}
cleanup(){ db_purge_ns "$NS"; db_purge_ns "${NS}-conc"; }
trap cleanup EXIT
db_purge_ns "$NS"
db_purge_ns "${NS}-conc"

echo "== 跑负载(限流配置 ${RATE}/s,时长 ${DUR}s)"
if python3 "$ROOT/scripts/loadtest-ingress.py" --base "$BASE" --secret "$SECRET" \
     --rate "$RATE" --duration "$DUR" --namespace "$NS"; then
  ok "负载下护栏成立(限流生效 + 速率不超 3x + 429 带 Retry-After)"
else
  bad "负载脚本报告护栏失效(详见上方输出)"
fi

echo ""
echo "== 等投递管道排空(否则断言会跑在 incident 还没建出来的时候)"
# 吞吐阶段会在 outbox 里留下上千条待投递,relay 需要时间排空。
# 不等的话并发幂等那条断言会看到"signal 落了但 incident 不存在" ——
# 那是**测量时机**的问题,不是幂等失效。第一次跑就踩到了这个:
# signal_count 报 0(而非虚增),因为 incident 那时还没被创建。
for i in $(seq 60); do
  P=$(dbq "select count(*) from outbox where status in ('pending','failed')")
  [ "$P" = "0" ] && break
  sleep 1
done
echo "  信息  等待 ${i}s 后 outbox 待投递 = $P 条"

echo ""
echo "== 并发幂等的落库断言"
# 上一步末尾用 200 个并发投了**完全相同**的一条信号(固定 startsAt)。
# 幂等成立时它只该落 1 条 —— 而不是 200 条,也不是 2 条。
#
# 查的是 rule_id='lt-999999' 那条:loadtest-ingress.py 用这个 seq 做并发用例。
# 并发阶段用独立 namespace(loadtest-conc),与吞吐阶段隔离 ——
# 否则 signal_count 里混着上千条吞吐流量,无法断言"这一条只贡献了 1"。
CNS="${NS}-conc"
N=$(dbq "select count(*) from signals where labels->>'namespace'='$CNS'")
if [ "$N" = "1" ]; then
  ok "200 并发投同一条信号只落 1 条(ON CONFLICT 无竞态)"
else
  bad "落了 $N 条(期望 1)—— 并发下幂等失效"
fi

# signal_count 必须**恰好** 1,不是 ">=1"。
# 这一条与上面不同:signals 表去重了,而 incident 的计数仍可能被每次投递 +1
# —— 那正是 F12 的失效模式(表里 1 行、计数却是 200),而这个数喂给触发策略
# 判"信号突发"(burst_signals=3),于是一条告警被读成影响面扩大、拉起多轮 RCA。
SC=$(dbq "select coalesce(max(signal_count),0) from incidents where correlation_key like '%|$CNS'")
if [ "$SC" = "1" ]; then
  ok "incident.signal_count 恰好为 1(并发投递未虚增 —— 防 F12 回归)"
else
  bad "incident.signal_count = $SC(期望恰好 1)—— 200 次并发投递把计数推高了"
fi

# outbox 不该有大量积压残留:relay 跟不上会让 signal 落库但 incident 不增长,
# 而那是最危险的静默失败(见 store/queuestats.go 的注释)。
PEND=$(dbq "select count(*) from outbox where status in ('pending','failed')")
echo "  信息  负载结束后 outbox 待投递 = $PEND 条"
DEAD=$(dbq "select count(*) from outbox where status='dead'")
if [ "$DEAD" = "0" ]; then
  ok "无 dead 记录(投递未被打垮)"
else
  bad "有 $DEAD 条 dead 记录 —— 负载下投递重试耗尽"
fi

echo ""
echo "RESULT: pass=$PASS fail=$FAIL"
[ "$FAIL" = 0 ] && echo "LOADTEST OK" || { echo "FAILURES"; exit 1; }
