#!/usr/bin/env bash
# 备份恢复演练。DEPLOY.md §8 要求"每季度执行**实际**恢复演练(真正拉起副本并
# 验证一次完整调查),而非仅检查备份任务状态" —— 但此前没有脚本能做这件事,
# 于是"演练"只能靠人临场拼命令,而临场拼的命令没人验证过。
#
# 这个脚本做的是**业务库**那一类(唯一事实源)。它验证三件事,顺序有讲究:
#
#   1. 备份能恢复出来        —— 最基本的
#   2. 恢复后**数据一致**    —— 外键完整、incident 与其调查/证据都在
#   3. 恢复后**应用能用**    —— 拉起 control-plane 并跑通一次读路径
#
# 第 3 步是重点。只验前两步的演练会漏掉一整类问题:schema 版本与镜像不匹配、
# 恢复出来的库缺扩展(pgvector)、序列没跟上导致主键冲突 ——
# 这些都能通过"数据看起来都在"的检查,然后在第一次真实请求时炸。
#
# 用法(不碰生产,起独立容器):
#   ./scripts/drill-backup-restore.sh
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

SRC_PORT="${DRILL_SRC_PORT:-55450}"
DST_PORT="${DRILL_DST_PORT:-55451}"
SRC=aiops-drill-src
DST=aiops-drill-dst
API_PORT="${DRILL_API_PORT:-18888}"
DUMP=$(mktemp /tmp/aiops-drill-XXXX.dump)

PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
bad(){ echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
info(){ echo "== $1"; }

cleanup(){
  pkill -f "$ROOT/control-plane/bin/cp-drill" 2>/dev/null
  docker rm -f "$SRC" "$DST" >/dev/null 2>&1
  rm -f "$DUMP"
}
trap cleanup EXIT

need(){ command -v "$1" >/dev/null 2>&1 || { echo "需要 $1" >&2; exit 2; }; }
need docker; need go

pg_up(){ # name port
  docker rm -f "$1" >/dev/null 2>&1
  docker run -d --name "$1" -e POSTGRES_USER=aiops -e POSTGRES_PASSWORD=aiops \
    -e POSTGRES_DB=aiops -p "$2:5432" pgvector/pgvector:pg16 >/dev/null
  for _ in $(seq 60); do
    docker exec "$1" pg_isready -U aiops -d aiops >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

info "1) 起源库并建 schema"
pg_up "$SRC" "$SRC_PORT" || { echo "源库起不来" >&2; exit 1; }
SRC_DSN="postgres://aiops:aiops@localhost:$SRC_PORT/aiops?sslmode=disable"
( cd "$ROOT/control-plane" && AIOPS_DB_DSN="$SRC_DSN" go run ./cmd/control-plane migrate up ) \
  >/dev/null 2>&1 || { echo "迁移失败" >&2; exit 1; }
for f in "$ROOT"/shared/seed/*.sql; do
  docker exec -i "$SRC" psql -U aiops -d aiops -q -f - < "$f" >/dev/null 2>&1
done
ok "源库 schema + 种子就绪"

info "2) 造一份有外键关系的业务数据(incident → investigation → evidence)"
docker exec -i "$SRC" psql -U aiops -d aiops -q >/dev/null 2>&1 <<'SQL'
INSERT INTO incidents (incident_id,tenant_id,cluster_id,version,grouping_key,status,severity,
  title,fault_category,affected_resources,blast_radius,signal_count)
VALUES ('inc-drill-1','default','prod-cn-1',2,'gk-drill','open','P1','drill incident',
  'pod_workload','[{"namespace":"payment","kind":"Deployment","name":"checkout"}]'::jsonb,
  '{"services":1,"namespaces":1}'::jsonb,3);
INSERT INTO investigations (investigation_id,tenant_id,incident_id,incident_version,phase,
  trigger_reason,budget,usage)
VALUES ('inv-drill-1','default','inc-drill-1',2,'concluded','drill','{}'::jsonb,'{}'::jsonb);
INSERT INTO evidence (evidence_id,tenant_id,investigation_id,type,source,tool_name,query,
  summary,content_hash)
VALUES ('ev-drill-1','default','inv-drill-1','metric','prometheus','query_metrics',
  '{}'::jsonb,'drill evidence','sha256:drill');
INSERT INTO hypotheses (hypothesis_id,tenant_id,investigation_id,rank,statement,confidence,
  supporting_evidence_ids,status)
VALUES ('hyp-drill-1','default','inv-drill-1',1,'drill root cause',0.9,
  '["ev-drill-1"]'::jsonb,'supported');
SQL
SRC_COUNTS=$(docker exec "$SRC" psql -U aiops -d aiops -tAc \
  "select (select count(*) from incidents)||'/'||(select count(*) from investigations)||'/'||(select count(*) from evidence)||'/'||(select count(*) from hypotheses)||'/'||(select count(*) from knowledge_items)")
ok "源库数据就绪 (incidents/investigations/evidence/hypotheses/knowledge = $SRC_COUNTS)"

info "3) 备份(pg_dump 自定义格式,与托管快照等价的逻辑备份)"
docker exec "$SRC" pg_dump -U aiops -d aiops -Fc > "$DUMP" 2>/dev/null
SZ=$(stat -c%s "$DUMP" 2>/dev/null || echo 0)
[ "$SZ" -gt 1000 ] && ok "备份产出 $((SZ/1024)) KB" || { bad "备份文件过小($SZ B)"; exit 1; }

info "4) 起一个空的目标库,从备份恢复"
pg_up "$DST" "$DST_PORT" || { echo "目标库起不来" >&2; exit 1; }
DST_DSN="postgres://aiops:aiops@localhost:$DST_PORT/aiops?sslmode=disable"
# 预建 pgvector 扩展。
#
# 对 pg_dump -Fc 这条路**不是必需的** —— dump 里自带 CREATE EXTENSION
# (实测:去掉这一行,演练照样 14/14 通过)。留着是为了覆盖另一条恢复路径:
# 物理备份 / PITR 恢复到一个新集群时,若该集群没装 pgvector 二进制,
# 带 vector 列的表会建不出来。那时这一行会**先失败**,比在恢复中途失败好定位。
#
# 这条注释原先写的是"缺扩展会让恢复部分失败",那对 pg_dump 路径是错的 ——
# 变异验证时发现的。
docker exec "$DST" psql -U aiops -d aiops -q -c 'CREATE EXTENSION IF NOT EXISTS vector' >/dev/null 2>&1
if docker exec -i "$DST" pg_restore -U aiops -d aiops --no-owner 2>/tmp/drill-restore.err < "$DUMP"; then
  ok "pg_restore 成功"
else
  # pg_restore 对已存在对象会报 warning 并返回非零,这里区分"真错"与"警告"
  if grep -qiE 'error' /tmp/drill-restore.err; then
    bad "pg_restore 报错:$(head -2 /tmp/drill-restore.err | tr '\n' ' ')"
  else
    ok "pg_restore 完成(仅有 warning)"
  fi
fi

info "5) 数据一致性:条数 + 外键完整性"
DST_COUNTS=$(docker exec "$DST" psql -U aiops -d aiops -tAc \
  "select (select count(*) from incidents)||'/'||(select count(*) from investigations)||'/'||(select count(*) from evidence)||'/'||(select count(*) from hypotheses)||'/'||(select count(*) from knowledge_items)")
[ "$SRC_COUNTS" = "$DST_COUNTS" ] && ok "各表条数一致 ($DST_COUNTS)" \
  || bad "条数不一致:源 $SRC_COUNTS vs 恢复 $DST_COUNTS"

# 外键完整性:光看条数不够 —— 条数对而引用断了同样是坏数据,
# 且它只在应用查关联时才暴露。
ORPHAN=$(docker exec "$DST" psql -U aiops -d aiops -tAc \
  "select (select count(*) from investigations iv left join incidents i on i.incident_id=iv.incident_id where i.incident_id is null)
        + (select count(*) from evidence e left join investigations iv on iv.investigation_id=e.investigation_id where iv.investigation_id is null)
        + (select count(*) from hypotheses h left join investigations iv on iv.investigation_id=h.investigation_id where iv.investigation_id is null)")
[ "$ORPHAN" = "0" ] && ok "无孤儿引用(外键完整)" || bad "有 $ORPHAN 条孤儿引用"

# schema 版本:恢复出来的库若 migration 版本落后于镜像,应用会在第一次查询时炸。
SV=$(docker exec "$DST" psql -U aiops -d aiops -tAc "select max(version) from schema_migrations" 2>/dev/null)
[ -n "$SV" ] && ok "schema 版本已恢复 (version=$SV)" || bad "schema_migrations 缺失或为空"

info "6) 应用可用性:拉起 control-plane 指向恢复库,跑通读路径"
# 这一步是演练的重点。前面几步全过但这一步失败的情形是真实存在的:
# 缺扩展、序列未跟上、schema 版本不匹配 —— 都能通过条数检查。
( cd "$ROOT/control-plane" && go build -o bin/cp-drill ./cmd/control-plane ) || { bad "构建失败"; exit 1; }
AIOPS_ENV=development AIOPS_DB_DSN="$DST_DSN" \
AIOPS_PUBLIC_ADDR=":$API_PORT" AIOPS_INTERNAL_ADDR=":$((API_PORT+1))" \
AIOPS_AUTH_MODE=hs256 AIOPS_AUTH_HS256_SECRET=drill-secret \
AIOPS_RETENTION_ENABLED=false \
"$ROOT/control-plane/bin/cp-drill" > /tmp/drill-cp.log 2>&1 &
for _ in $(seq 40); do
  [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:$API_PORT/healthz")" = "200" ] && break
  sleep 0.5
done
RDY=$(curl -s --max-time 5 "http://127.0.0.1:$API_PORT/readyz")
echo "$RDY" | grep -q '"status":"ready"' && ok "/readyz 就绪(库连通且 schema 匹配)" \
  || bad "/readyz 未就绪:$RDY"

TOKEN=$(curl -s --max-time 5 -X POST "http://127.0.0.1:$API_PORT/v1/auth/login" \
  -H 'Content-Type: application/json' -d '{"username":"alice","password":"alice-pass"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))' 2>/dev/null)
[ -n "$TOKEN" ] && ok "登录成功" || bad "登录失败"

# 读回恢复的 incident 及其关联对象 —— 验证应用层能真的用这份数据,
# 而不只是"表里有行"。
GOT=$(curl -s --max-time 5 "http://127.0.0.1:$API_PORT/v1/incidents/inc-drill-1" \
  -H "Authorization: Bearer $TOKEN")
echo "$GOT" | grep -q 'inc-drill-1' && ok "读回 incident" || bad "读不到 incident:$(echo "$GOT"|head -c 120)"
echo "$GOT" | grep -q 'inv-drill-1' && ok "关联的 investigation 一并返回" \
  || bad "investigation 未随 incident 返回(关联查询坏了)"

INV=$(curl -s --max-time 5 "http://127.0.0.1:$API_PORT/v1/investigations/inv-drill-1" \
  -H "Authorization: Bearer $TOKEN")
echo "$INV" | grep -q 'ev-drill-1' && ok "证据可读回" || bad "证据读不到"
echo "$INV" | grep -q 'drill root cause' && ok "假设可读回" || bad "假设读不到"

# 写路径:恢复后必须能继续写入。序列/主键若没跟上,这里会主键冲突 ——
# 而那种失败在只读检查里完全看不到。
ACK=$(curl -s --max-time 5 -X POST "http://127.0.0.1:$API_PORT/v1/incidents/inc-drill-1/status" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"status":"acknowledged"}')
echo "$ACK" | grep -q '"changed":true' && ok "恢复后写路径可用(认领成功)" \
  || bad "写入失败:$(echo "$ACK"|head -c 120)"

echo ""
echo "RESULT: pass=$PASS fail=$FAIL"
if [ "$FAIL" = 0 ]; then
  echo "BACKUP-RESTORE DRILL OK"
else
  echo "DRILL FAILED —— control-plane 日志尾部:"
  tail -15 /tmp/drill-cp.log
  exit 1
fi
