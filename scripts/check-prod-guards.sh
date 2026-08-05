#!/usr/bin/env bash
# 校验生产护栏真的会生效 —— 而不是"代码里写了校验"。
#
# 背景(这条检查为什么存在):护栏由 AIOPS_ENV 开启,数据源由 AIOPS_DATASOURCE
# 选择,而这两项**从来没出现在任何部署清单里**。后果是:
#   1. AIOPS_ENV 缺失 → 代码默认 development → config.Validate 的整个生产分支
#      不执行。auth disabled、默认 HS256 密钥、缺 webhook secret、mock 观测源、
#      未配集群隔离,全部静默放行。10 个生产校验用例测的是永不触发的分支。
#   2. AIOPS_DATASOURCE 缺失 → cluster-agent 默认 mock → get_workload_state /
#      get_kubernetes_events / list_recent_changes / inspect_dependencies 四个
#      工具返回虚构但自洽的"故障故事",它们被冻结成 Evidence、拿到 Evidence ID、
#      进入诊断结论。evidence-grounding 只校验结论是否引用了证据,不校验证据
#      是否真实,所以值班人员看到"有据可查"的根因,底下是编造的。
#
# 两者都不报错、日志无异常、指标正常 —— 只有结论内容是假的。目视检查发现不了,
# 单测也发现不了(它们直接构造 Config,绕过清单和 Load)。所以这里对着**渲染后的
# 清单**做断言,并把渲染出的环境变量真的喂给二进制跑一遍校验。
#
# 用法:bash scripts/check-prod-guards.sh   (无需任何基础设施)
set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0
ok(){   PASS=$((PASS+1)); echo "   ✅ $1"; }
bad(){  FAIL=$((FAIL+1)); echo "   ❌ $1"; }
need(){ command -v "$1" >/dev/null 2>&1 || { echo "缺少 $1" >&2; exit 2; }; }
need helm; need python3; need go

RAW=deploy/k8s/10-configmap.yaml
PRODY=/tmp/pg-prod.yaml
DEVY=/tmp/pg-dev.yaml

echo "== 1) 渲染 chart(prod / dev)"
helm template aiops deploy/helm/aiops -f deploy/helm/aiops/values-prod.yaml > "$PRODY" || exit 1
helm template aiops deploy/helm/aiops -f deploy/helm/aiops/values-dev.yaml  > "$DEVY"  || exit 1
echo "   prod / dev 均渲染成功"

# cmkey <file> <key> —— 从渲染结果或原始清单里取 aiops-config 的某个键。
cmkey(){
  python3 - "$1" "$2" <<'PY'
import sys, yaml
path, key = sys.argv[1], sys.argv[2]
for d in yaml.safe_load_all(open(path)):
    if d and d.get('kind') == 'ConfigMap' and d['metadata']['name'] == 'aiops-config':
        print((d.get('data') or {}).get(key, ''))
        break
PY
}

echo "== 2) 清单必须显式声明 AIOPS_ENV 与 AIOPS_DATASOURCE"
for spec in "$PRODY:production:live:helm-prod" "$RAW:production:live:raw-k8s" "$DEVY:development:mock:helm-dev"; do
  f="${spec%%:*}"; rest="${spec#*:}"
  want_env="${rest%%:*}"; rest="${rest#*:}"
  want_ds="${rest%%:*}"; label="${rest#*:}"

  got_env="$(cmkey "$f" AIOPS_ENV)"
  got_ds="$(cmkey "$f" AIOPS_DATASOURCE)"

  [ -n "$got_env" ] && ok "$label: AIOPS_ENV 已声明($got_env)" \
                    || bad "$label: AIOPS_ENV 缺失 —— 生产校验将静默不执行"
  [ "$got_env" = "$want_env" ] && ok "$label: AIOPS_ENV = $want_env" \
                              || bad "$label: AIOPS_ENV = '$got_env',期望 $want_env"

  [ -n "$got_ds" ] && ok "$label: AIOPS_DATASOURCE 已声明($got_ds)" \
                   || bad "$label: AIOPS_DATASOURCE 缺失 —— cluster-agent 将默认 mock 假数据"
  [ "$got_ds" = "$want_ds" ] && ok "$label: AIOPS_DATASOURCE = $want_ds" \
                            || bad "$label: AIOPS_DATASOURCE = '$got_ds',期望 $want_ds"
done

echo "== 3) 构建二进制"
go build -C control-plane -o /tmp/pg-cp ./cmd/control-plane   || exit 1
go build -C cluster-agent -o /tmp/pg-ca ./cmd/cluster-agent   || exit 1
echo "   control-plane / cluster-agent 构建完成"

# 渲染出的 ConfigMap 键值导出成 env 参数。Secret 里的敏感项由下面单独补 ——
# 生产 secrets.create=false(外部注入),渲染结果里没有它们。
envargs(){
  python3 - "$1" <<'PY'
import sys, yaml, shlex
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get('kind') == 'ConfigMap' and d['metadata']['name'] == 'aiops-config':
        for k, v in (d.get('data') or {}).items():
            if k.startswith('AIOPS_'):
                print(f"{k}={v}")
        break
PY
}

echo "== 4) 渲染出的生产配置必须通过控制面启动校验"
# 若这一步失败,说明按 values-prod.yaml 部署会**起不来**——比护栏空转更糟。
# 外部 Secret 注入的四项在这里补齐(生产由 Vault/ESO 提供)。
mapfile -t PRODENV < <(envargs "$PRODY")
if env -i "${PRODENV[@]}" \
     AIOPS_OIDC_ISSUER=https://idp.corp.example/realms/aiops \
     AIOPS_OIDC_JWKS_URL=https://idp.corp.example/realms/aiops/protocol/openid-connect/certs \
     AIOPS_INTERNAL_TOKEN=internal-token-value \
     AIOPS_WEBHOOK_SECRET=webhook-secret-value \
     /tmp/pg-cp validate-config >/tmp/pg-valid.log 2>&1; then
  ok "生产清单渲染结果通过 validate-config"
  grep -q 'production=true' /tmp/pg-valid.log \
    && ok "校验确认运行在生产模式(严格分支已执行)" \
    || bad "校验未报告 production=true —— 严格分支没跑"
  grep -q 'obs_datasource=live' /tmp/pg-valid.log \
    && ok "观测数据源解析为 live(非 mock 假证据)" \
    || bad "观测数据源不是 live: $(grep -o 'obs_datasource=[^ ]*' /tmp/pg-valid.log)"
else
  bad "生产清单渲染结果**未通过** validate-config(照此部署会起不来):"
  sed 's/^/        /' /tmp/pg-valid.log
fi

echo "== 5) 护栏必须真的会拒绝(反向用例)"
# 逐项抽掉一个必需项,校验必须失败。若某项抽掉后仍通过,说明那条护栏是空转的。
reject_case(){ # <描述> <要清空的变量名>
  local desc="$1" var="$2"
  if env -i "${PRODENV[@]}" \
       AIOPS_OIDC_ISSUER=https://idp.corp.example/realms/aiops \
       AIOPS_OIDC_JWKS_URL=https://idp.corp.example/realms/aiops/protocol/openid-connect/certs \
       AIOPS_INTERNAL_TOKEN=internal-token-value \
       AIOPS_WEBHOOK_SECRET=webhook-secret-value \
       "$var=" \
       /tmp/pg-cp validate-config >/dev/null 2>&1; then
    bad "$desc —— 护栏未拦住(空转)"
  else
    ok "$desc —— 已被拒绝"
  fi
}
reject_case "缺 AIOPS_INTERNAL_TOKEN"  AIOPS_INTERNAL_TOKEN
reject_case "缺 AIOPS_WEBHOOK_SECRET"  AIOPS_WEBHOOK_SECRET

# 观测后端三项同时为空 → 必须拒绝(否则静默回退 mock)。
if env -i "${PRODENV[@]}" \
     AIOPS_OIDC_ISSUER=https://idp AIOPS_OIDC_JWKS_URL=https://idp/certs \
     AIOPS_INTERNAL_TOKEN=t AIOPS_WEBHOOK_SECRET=w \
     AIOPS_PROM_URL= AIOPS_LOKI_URL= AIOPS_TEMPO_URL= \
     /tmp/pg-cp validate-config >/dev/null 2>&1; then
  bad "观测后端全空 —— 护栏未拦住(将回退 mock 假证据)"
else
  ok "观测后端全空 —— 已被拒绝"
fi

# auth disabled 在生产必须拒绝。
if env -i "${PRODENV[@]}" \
     AIOPS_INTERNAL_TOKEN=t AIOPS_WEBHOOK_SECRET=w \
     AIOPS_AUTH_MODE=disabled \
     /tmp/pg-cp validate-config >/dev/null 2>&1; then
  bad "AIOPS_AUTH_MODE=disabled —— 护栏未拦住"
else
  ok "AIOPS_AUTH_MODE=disabled —— 已被拒绝"
fi

echo "== 6) cluster-agent 必须拒绝以 mock 数据源在生产启动"
# 漏配与显式配 mock 是同一种失败(默认值就是 mock),两者都要拦。
ca_should_fail(){ # <描述> [额外环境变量...]
  local desc="$1"; shift
  if timeout 10 env -i AIOPS_ENV=production \
       AIOPS_CLUSTER_AGENT_ADDR=127.0.0.1:19199 \
       AIOPS_CLUSTER_AGENT_HEALTH_ADDR=127.0.0.1:19198 \
       "$@" /tmp/pg-ca >/tmp/pg-ca.log 2>&1; then
    bad "$desc —— cluster-agent 竟然启动了(将产出虚假 K8s 证据)"
  else
    grep -q 'invalid datasource configuration' /tmp/pg-ca.log \
      && ok "$desc —— 已拒绝启动" \
      || bad "$desc —— 退出了但不是因为数据源校验: $(head -c 200 /tmp/pg-ca.log)"
  fi
}
ca_should_fail "生产 + 漏配 AIOPS_DATASOURCE"
ca_should_fail "生产 + 显式 mock" AIOPS_DATASOURCE=mock

# 反向:非生产下 mock 必须可用(零基础设施演示与全部离线测试依赖它)。
if timeout 5 env -i AIOPS_ENV=development AIOPS_DATASOURCE=mock \
     AIOPS_CLUSTER_AGENT_ADDR=127.0.0.1:19197 \
     AIOPS_CLUSTER_AGENT_HEALTH_ADDR=127.0.0.1:19196 \
     /tmp/pg-ca >/tmp/pg-ca-dev.log 2>&1; then
  # 正常启动后被 timeout 杀掉 → 退出码 124;真正的失败会更早退出。
  bad "开发模式 mock 意外退出: $(head -c 200 /tmp/pg-ca-dev.log)"
else
  if [ $? -eq 124 ] || grep -q 'cluster-agent starting' /tmp/pg-ca-dev.log; then
    ok "开发模式 + mock —— 正常启动(未被误拦)"
  else
    bad "开发模式 mock 未能启动: $(head -c 200 /tmp/pg-ca-dev.log)"
  fi
fi

echo "== 7) ai-worker 必须拒绝以 mock provider 在生产启动"
if [ -x ai-worker/.venv/bin/python ]; then
  PY=ai-worker/.venv/bin/python
  if (cd ai-worker && env AIOPS_ENV=production AIOPS_MODEL_PROVIDER=mock \
        .venv/bin/python -c 'from aiops_worker.config import load_settings; load_settings().validate()' \
        >/tmp/pg-aw.log 2>&1); then
    bad "生产 + mock provider —— 未被拦住(将产出编造的诊断结论)"
  else
    grep -q 'ConfigError' /tmp/pg-aw.log && ok "生产 + mock provider —— 已拒绝" \
      || bad "退出了但不是 ConfigError: $(tail -c 200 /tmp/pg-aw.log)"
  fi
  for real in anthropic pydantic-ai; do
    if (cd ai-worker && env AIOPS_ENV=production "AIOPS_MODEL_PROVIDER=$real" \
          .venv/bin/python -c 'from aiops_worker.config import load_settings; load_settings().validate()' \
          >/dev/null 2>&1); then
      ok "生产 + $real provider —— 放行"
    else
      bad "生产 + $real provider 被误拦"
    fi
  done
  # 拼错的 provider 名此前能通过启动校验,直到 build_provider 才抛 ValueError ——
  # 那时已连上 Temporal,且错误信息里没有"合法取值是什么"。
  if (cd ai-worker && env AIOPS_ENV=production AIOPS_MODEL_PROVIDER=anthropc \
        .venv/bin/python -c 'from aiops_worker.config import load_settings; load_settings().validate()' \
        >/tmp/pg-aw2.log 2>&1); then
    bad "拼错的 provider 名 —— 未被拦住"
  else
    grep -q '未知的 AIOPS_MODEL_PROVIDER' /tmp/pg-aw2.log \
      && ok "拼错的 provider 名 —— 已拒绝且列出合法取值" \
      || bad "拼错的 provider 名退出了,但错误信息没说清合法取值"
  fi
else
  echo "   ⏭  跳过(ai-worker/.venv 不存在,先 cd ai-worker && make install)"
fi

echo
echo "===== 结果:$PASS 通过 / $FAIL 失败 ====="
[ "$FAIL" -eq 0 ] || exit 1
