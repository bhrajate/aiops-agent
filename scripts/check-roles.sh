#!/usr/bin/env bash
# 验证按角色拆分:两个进程分别只跑 api / 后台角色,合起来功能完整。
#   进程A(roles=api,internal)      → 提供 :8088 / :8090,不消费 Kafka、不投 outbox
#   进程B(roles=ingest,trigger,outbox) → 只跑后台管道,不监听 HTTP
set -u
cd /home/glory/code/ai-generate/aiops
BASE=http://localhost:8088
COMPOSE=deploy/docker-compose.yml
q(){ docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -t -c "$1" 2>/dev/null | tr -d ' ' | sed '/^$/d'; }

for pid in $(pgrep -f 'control-plane/bin/control-plane'); do kill "$pid" 2>/dev/null; done
for p in $(fuser 8088/tcp 8090/tcp 2>/dev/null); do kill "$p" 2>/dev/null; done
sleep 2
docker compose -f "$COMPOSE" exec -T redpanda rpk topic delete signals incidents investigations --brokers redpanda:29092 >/dev/null 2>&1
docker compose -f "$COMPOSE" exec -T redpanda rpk topic create signals incidents investigations --brokers redpanda:29092 -p 1 -r 1 >/dev/null 2>&1
docker compose -f "$COMPOSE" exec -T postgres psql -U aiops -d aiops -c "TRUNCATE signals, incidents, investigations, evidence, hypotheses, investigation_events, human_feedback, outbox, audit_log, idempotency_keys, dead_letters, alert_groups CASCADE;" >/dev/null 2>&1

export AIOPS_DB_DSN="${AIOPS_DB_DSN:-postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable}" AIOPS_KAFKA_BROKERS="localhost:19092" AIOPS_TEMPORAL_HOSTPORT="localhost:7233" AIOPS_AUTH_MODE="hs256" AIOPS_AUTH_HS256_SECRET="dev-secret" AIOPS_INTERNAL_TOKEN="internal-dev-token" AIOPS_WEBHOOK_SECRET="webhook-dev-secret"

# 进程A:仅 API 角色
AIOPS_ROLES="api,internal" setsid ./control-plane/bin/control-plane > /tmp/cp-roleA.log 2>&1 < /dev/null &
for i in $(seq 1 20); do curl -sf $BASE/healthz >/dev/null 2>&1 && break; sleep 1; done
# 进程B:仅后台角色(不同 HTTP 端口以免冲突——但它不监听,故随意)
AIOPS_ROLES="ingest,trigger,outbox" AIOPS_PUBLIC_ADDR=":18088" AIOPS_INTERNAL_ADDR=":18090" \
  setsid ./control-plane/bin/control-plane > /tmp/cp-roleB.log 2>&1 < /dev/null &
sleep 5

pass=0; fail=0
ck(){ if [ "$2" = "$3" ]; then echo "  ✓ $1 ($3)"; pass=$((pass+1)); else echo "  ✗ $1 期望 $2 实得 $3"; fail=$((fail+1)); fi; }

echo "=== 角色日志 ==="
echo "A: $(grep -o 'enabled roles.*' /tmp/cp-roleA.log | head -1)"
echo "B: $(grep -o 'enabled roles.*' /tmp/cp-roleB.log | head -1)"

echo "=== A 只跑 API:不应有 consumer/outbox 日志 ==="
ck "A 无 signals consumer" 0 "$(grep -c 'consuming signals' /tmp/cp-roleA.log)"
ck "A 无 incidents consumer" 0 "$(grep -c 'consuming incidents' /tmp/cp-roleA.log)"
ck "A 有 public API" 1 "$(grep -c 'public API listening' /tmp/cp-roleA.log)"

echo "=== B 只跑后台:不应监听 HTTP ==="
ck "B 无 public API" 0 "$(grep -c 'public API listening' /tmp/cp-roleB.log)"
ck "B 无 internal API" 0 "$(grep -c 'internal API listening' /tmp/cp-roleB.log)"
ck "B 有 signals consumer" 1 "$(grep -c 'consuming signals' /tmp/cp-roleB.log)"
ck "B 有 incidents consumer" 1 "$(grep -c 'consuming incidents' /tmp/cp-roleB.log)"
ck "18088 未被监听(B 不起 HTTP)" "" "$(ss -ltn 2>/dev/null | grep -o ':18088' | head -1)"

echo "=== 端到端:A 收信号 → B 处理成 incident(跨进程协作)==="
BODY='{"alerts":[{"status":"firing","labels":{"alertname":"HighLatency","severity":"critical","namespace":"payment","deployment":"checkout","cluster":"prod-cn-1","rule_id":"r-1"},"startsAt":"2026-07-27T02:00:00Z"}]}'
SIG=$(python3 -c "import hmac,hashlib;print('sha256='+hmac.new(b'webhook-dev-secret','''$BODY'''.encode(),hashlib.sha256).hexdigest())")
curl -s -o /dev/null $BASE/v1/signals -H 'Content-Type: application/json' -H "X-AIOPS-Signature: $SIG" -d "$BODY"
sleep 5
ck "incident 由 B 创建" 1 "$(q 'select count(*) from incidents;')"
ck "alert_group 由 B 创建" 1 "$(q 'select count(*) from alert_groups;')"
ck "outbox 已由 B 投递" 0 "$(q "select count(*) from outbox where status='pending';")"

for pid in $(pgrep -f 'control-plane/bin/control-plane'); do kill "$pid" 2>/dev/null; done
echo ""; echo "RESULT: pass=$pass fail=$fail"; [ "$fail" = 0 ] && echo "ROLE-SPLIT OK" || echo "FAILURES"