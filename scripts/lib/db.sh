#!/usr/bin/env bash
# 验收脚本查库的统一入口。被 source,不单独执行。
#
# 存在的原因有两个,第二个是缺陷:
#
# 1. 连库方式此前硬编码 `docker compose exec postgres psql`。本机 5432 被别的
#    项目占用时 compose 起不来,整套脚本没法跑 —— 而它是 ACCEPTANCE.md 里
#    验收结论的唯一证据来源。
#
# 2. **那些 q() 把 stderr 吞进 /dev/null。** 容器不存在、compose 文件找不到、
#    库没起来,这些情况下 psql 的报错全部消失,断言收到的是**空字符串**。
#    实测 check-two-tier.sh:16 条断言全部显示"期望 1 实得 ",不说原因。
#    它没有假装通过,但同样的沉默也会让"照着库里残留数据打分"看起来正常 ——
#    而 ACCEPTANCE.md 开头恰好警告过 curl 路径的这个坑,psql 路径一直还在。
#
# 所以这里的 dbq/dbx 在**任何**失败上立刻 exit 1 并打印原因:
# 连不上、SQL 语法错、容器没了,都不会让断言拿到空串继续跑下去。

# 连库方式按优先级解析,第一个可用的赢:
#   AIOPS_PSQL        完整命令前缀,如 "docker exec -i my-pg psql -U aiops -d aiops"
#                     或 "psql postgres://..."。给 CI 与非常规环境用。
#   AIOPS_PG_CONTAINER  已在跑的独立容器名
#   deploy/docker-compose.yml 里的 postgres 服务(既有默认路径)
#   本机 psql + AIOPS_DB_DSN
_db_resolve() {
  if [ -n "${AIOPS_PSQL:-}" ]; then
    _DB_CMD="$AIOPS_PSQL"
    _DB_MODE="custom"
    return 0
  fi
  if [ -n "${AIOPS_PG_CONTAINER:-}" ]; then
    if docker exec "$AIOPS_PG_CONTAINER" true >/dev/null 2>&1; then
      _DB_CMD="docker exec -i $AIOPS_PG_CONTAINER psql -U aiops -d aiops"
      _DB_MODE="container:$AIOPS_PG_CONTAINER"
      return 0
    fi
    echo "FATAL: AIOPS_PG_CONTAINER=$AIOPS_PG_CONTAINER 指定的容器不可用" >&2
    return 1
  fi
  local compose="${ROOT:-.}/deploy/docker-compose.yml"
  if [ -f "$compose" ] && docker compose -f "$compose" ps postgres 2>/dev/null | grep -q 'Up\|running'; then
    _DB_CMD="docker compose -f $compose exec -T postgres psql -U aiops -d aiops"
    _DB_MODE="compose"
    return 0
  fi
  if command -v psql >/dev/null 2>&1 && [ -n "${AIOPS_DB_DSN:-}" ]; then
    _DB_CMD="psql $AIOPS_DB_DSN"
    _DB_MODE="local-psql"
    return 0
  fi
  echo "FATAL: 找不到可用的 PostgreSQL 连接方式。请任选其一:" >&2
  echo "  export AIOPS_PG_CONTAINER=<已在跑的容器名>" >&2
  echo "  export AIOPS_PSQL='<完整 psql 命令前缀>'" >&2
  echo "  cd deploy && make up   # 走默认的 compose 栈" >&2
  return 1
}

if ! _db_resolve; then
  exit 1
fi

# 调用脚本自身的 PID。source 不 fork,所以这里的 $$ 就是它。
#
# 为什么需要:dbq 通常写成 `v=$(dbq '...')`,而命令替换开的是**子 shell** ——
# 里面的 `exit 1` 只杀掉那个子 shell,调用方拿着空串继续往下跑,
# 正是本文件要消掉的失效方式。而这些脚本都没开 `set -e`
# (实测 11 个 `set -uo pipefail` + 8 个 `set -u`),退出码也不会自动传播。
# 所以失败时显式给主 shell 发 TERM。
_DB_MAIN_PID=$$

# _db_die 打印原因并**终止整个脚本**,不是只终止子 shell。
_db_die() {
  echo "$1" >&2
  shift
  for line in "$@"; do echo "$line" >&2; done
  kill -TERM "$_DB_MAIN_PID" 2>/dev/null
  exit 1
}

# dbq 取单值/多行标量。失败立刻 exit,**绝不返回空串** ——
# 空串会让断言以为"库里就是没有",而真相是查询根本没跑成。
dbq() {
  local out rc
  out=$($_DB_CMD -tAc "$1" 2>&1)
  rc=$?
  if [ $rc -ne 0 ]; then
    _db_die "FATAL: 查询失败(rc=$rc, mode=$_DB_MODE)" "  SQL: $1" "  输出: $out"
  fi
  printf '%s' "$out" | tr -d ' ' | sed '/^$/d'
}

# dbx 执行不取结果的语句(TRUNCATE / INSERT / UPDATE)。同样失败即 exit。
dbx() {
  local out rc
  out=$($_DB_CMD -q -c "$1" 2>&1)
  rc=$?
  if [ $rc -ne 0 ]; then
    _db_die "FATAL: 执行失败(rc=$rc, mode=$_DB_MODE)" "  SQL: $1" "  输出: $out"
  fi
}

# db_mode 供脚本打印当前连库方式,便于排查"跑的是哪个库"。
db_mode() { printf '%s' "$_DB_MODE"; }

# db_stop / db_start 停掉与拉起数据库,用于验证故障路径
# (check-probes 的 /readyz→503、check-queue-metrics 的 gauge 缺失)。
#
# 必须跟着 _db_resolve 的结果走。此前这两个脚本硬编码
# `docker compose stop postgres`:当数据库不是 compose 起的(比如本机 5432 被占
# 而用了独立容器),这句**静默成为空操作** —— 库根本没停,而那两个脚本要测的
# 恰恰是"库停了会怎样"。故障路径测试变成空转,却照样打出通过。
#
# 解析不到可停的目标时返回 1,让调用方跳过并明确说明,而不是假装测过了。
db_stop() {
  case "$_DB_MODE" in
    container:*) docker stop "${_DB_MODE#container:}" >/dev/null 2>&1 ;;
    compose) docker compose -f "${ROOT:-.}/deploy/docker-compose.yml" stop postgres >/dev/null 2>&1 ;;
    *)
      echo "SKIP: 当前连库方式($_DB_MODE)无法停库 —— 故障路径未被验证" >&2
      return 1
      ;;
  esac
}

db_start() {
  case "$_DB_MODE" in
    container:*) docker start "${_DB_MODE#container:}" >/dev/null 2>&1 ;;
    compose) docker compose -f "${ROOT:-.}/deploy/docker-compose.yml" start postgres >/dev/null 2>&1 ;;
    *) return 1 ;;
  esac
}

# db_purge_ns 按 namespace 的 LIKE 模式清掉脚本造的数据。
#
# 参数是**LIKE 模式**,所以精确("f7-abc")和通配("f7-%")都能用。
#
# 存在的原因是外键顺序:investigations 上有 investigations_incident_id_fkey,
# 各脚本此前直接 `DELETE FROM incidents`,于是**有调查的 incident 删不掉**。
# 那条报错被 `2>/dev/null` 吞掉后,数据留在库里,下一次运行的 correlation
# 会合并到旧 incident —— 断言于是对着上一次的数据打分。
#
# 抽到这里而不是各脚本各写一份:删除顺序是个容易写错且错了不报错的东西
# (漏一张从表就撞外键,而撞了也只是静默留数据)。一份实现,四个脚本共用。
db_purge_ns() {
  local like="$1"
  dbx "DELETE FROM golden_cases WHERE incident_id IN
         (SELECT incident_id FROM incidents WHERE correlation_key LIKE '%|${like}');
       DELETE FROM human_feedback WHERE investigation_id IN
         (SELECT investigation_id FROM investigations WHERE incident_id IN
            (SELECT incident_id FROM incidents WHERE correlation_key LIKE '%|${like}'));
       DELETE FROM investigation_events WHERE investigation_id IN
         (SELECT investigation_id FROM investigations WHERE incident_id IN
            (SELECT incident_id FROM incidents WHERE correlation_key LIKE '%|${like}'));
       DELETE FROM hypotheses WHERE investigation_id IN
         (SELECT investigation_id FROM investigations WHERE incident_id IN
            (SELECT incident_id FROM incidents WHERE correlation_key LIKE '%|${like}'));
       DELETE FROM evidence WHERE investigation_id IN
         (SELECT investigation_id FROM investigations WHERE incident_id IN
            (SELECT incident_id FROM incidents WHERE correlation_key LIKE '%|${like}'));
       DELETE FROM investigations WHERE incident_id IN
         (SELECT incident_id FROM incidents WHERE correlation_key LIKE '%|${like}');
       DELETE FROM signals WHERE labels->>'namespace' LIKE '${like}';
       DELETE FROM alert_groups WHERE namespace LIKE '${like}';
       DELETE FROM incidents WHERE correlation_key LIKE '%|${like}';"
}

# db_ready 判断库是否已可接受连接。停库后等待恢复时用。
# 跟着 _DB_MODE 走,理由同 db_stop:硬编码 compose 的 pg_isready 在库不是
# compose 起的时候永远失败,于是调用方白等完超时再继续,
# 而后续断言会对着一个还没就绪的库跑 —— 那种失败看起来像功能坏了。
db_ready() {
  case "$_DB_MODE" in
    container:*) docker exec "${_DB_MODE#container:}" pg_isready -U aiops -d aiops >/dev/null 2>&1 ;;
    compose) docker compose -f "${ROOT:-.}/deploy/docker-compose.yml" exec -T postgres pg_isready -U aiops -d aiops >/dev/null 2>&1 ;;
    *) $_DB_CMD -tAc 'select 1' >/dev/null 2>&1 ;;
  esac
}
