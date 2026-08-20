# 安全与生产化契约(Frozen)

对应架构文档第 14 节。所有模块据此实现。这是**冻结契约**,改动需同步各模块。

## 1. 身份认证(OIDC/JWT)

- 生产:企业 OIDC/SSO 签发 JWT(RS256),控制面用 JWKS 验签。
- 开发:控制面内置**本地签发端点**用 HS256(密钥 `AIOPS_AUTH_HS256_SECRET`),便于无 IdP 也能端到端跑。
- 统一走 `Authorization: Bearer <jwt>`。

JWT claims(最小集):
```jsonc
{
  "sub": "alice",                 // 用户名
  "email": "alice@corp.example",
  "roles": ["sre", "oncall"],     // 角色 → 权限
  "clusters": ["prod-cn-1"],      // ABAC:可访问集群
  "namespaces": ["payment","*"],  // ABAC:可访问命名空间(* 全部)
  "exp": 1900000000, "iat": ..., "iss": "aiops-dev", "aud": "aiops"
}
```

### 认证配置(env)
```
AIOPS_AUTH_MODE=hs256            # hs256(开发内置签发) | oidc(生产) | disabled(仅限本地测试)
AIOPS_AUTH_HS256_SECRET=<dev-secret>
AIOPS_OIDC_ISSUER=              # oidc 模式:IdP issuer
AIOPS_OIDC_JWKS_URL=            # oidc 模式:JWKS
AIOPS_OIDC_AUDIENCE=aiops
```

### 认证端点(仅 hs256 开发模式启用)
```
POST /v1/auth/login   { "username":"alice", "password":"..." }  → { "token": "<jwt>", "expires_in": 3600 }
GET  /v1/auth/me      (Bearer)                                   → { user claims }
```
开发用户库:内置若干演示账号(见实现),生产此端点关闭,由 IdP 负责。

## 2. 授权(RBAC + ABAC)

角色 → 动作权限(RBAC):

| 角色 | 权限 |
|---|---|
| `viewer` | 读 incidents/investigations/evidence |
| `oncall` | viewer + 启动/取消调查 + 反馈/确认/关闭 |
| `sre` | oncall + 全部命名空间 |
| `admin` | 全部 + 未来管理动作 |

范围(ABAC):对涉及具体 Incident 的操作,校验
```
用户可访问范围(clusters/namespaces) ∩ Agent 服务权限 ∩ Incident 调查范围(cluster/namespace)
```
三者交集为空 → 403。实现见 `internal/auth`。

### 受保护端点矩阵
| 端点 | 要求 |
|---|---|
| `POST /v1/signals` | 独立 webhook 鉴权(HMAC,见 §4),非用户 JWT |
| `GET /v1/incidents*` | `viewer`,且 ABAC 过滤到可见集群/命名空间 |
| `GET /v1/investigations*`,`/v1/evidence*` | `viewer` + ABAC |
| `POST /v1/incidents/{id}/investigations` | `oncall` + ABAC(Incident 范围) |
| `POST /v1/investigations/{id}/{cancel,feedback}` | `oncall` + ABAC |
| `/v1/auth/login` | 公开(仅 hs256) |
| `/healthz`,`/metrics` | 公开(metrics 生产应内网/加保护) |
| 内部 API `:8090/internal/*` | 仅集群内;`AIOPS_INTERNAL_TOKEN` 共享密钥头 `X-Internal-Token` |

## 3. mTLS(Tool Gateway ↔ Cluster Agent)

- cluster-agent 作为 TLS 服务端,校验客户端证书(control-plane 持客户端证书)。
- 通过 env 开关,开发可关闭(明文),生产必开。
```
# cluster-agent
AIOPS_AGENT_TLS_ENABLED=true
AIOPS_AGENT_TLS_CERT=/certs/agent.crt
AIOPS_AGENT_TLS_KEY=/certs/agent.key
AIOPS_AGENT_TLS_CLIENT_CA=/certs/ca.crt     # 校验调用方
# control-plane(作为客户端)
AIOPS_AGENT_MTLS_ENABLED=true
AIOPS_AGENT_CLIENT_CERT=/certs/client.crt
AIOPS_AGENT_CLIENT_KEY=/certs/client.key
AIOPS_AGENT_CA=/certs/ca.crt
AIOPS_CLUSTER_AGENT_URL=https://cluster-agent:9100
```
`deploy/certs/` 提供本地自签脚本 `gen-certs.sh`(仅开发)。

## 4. Webhook 签名(Signal Ingress)

`POST /v1/signals` 校验 HMAC-SHA256:
```
X-AIOPS-Signature: sha256=<hex(hmac(secret, raw_body))>
AIOPS_WEBHOOK_SECRET=<secret>
```
未配置 secret 时开发放行并记审计告警;配置后强制校验,失败 401。

## 5. 幂等

- `POST /v1/incidents/{id}/investigations` 的 `Idempotency-Key` 落 `idempotency_keys` 表:
  首次记录 (key, 结果 investigation_id);重复请求直接返回首个结果,不重复启动。

## 6. 证据原始快照(对象存储)

Tool Gateway 将脱敏后的 raw 证据 PutObject 到 MinIO(`s3://aiops-evidence/<investigation_id>/<evidence_id>.json`),
`evidence.raw_ref` 记录 object key。摘要入库,原文留对象存储(最小化进模型内容)。

## 7. DLQ

Signal/Incident 消费重试超过 `AIOPS_MAX_DELIVERY_ATTEMPTS`(默认 5)进入 DLQ 表 `dead_letters`,并审计告警。

## 8. 审计

所有写操作与拒绝事件记 `audit_log`,`actor` 为 JWT `sub`(非 `operator` 缺省);权限拒绝记 `result=denied` + 原因。

## 密钥轮换

所有密钥都**只在启动时从环境变量读取**,没有热重载。这是 K8s 下的常规形态
(改 Secret → 滚动重启),但 `AIOPS_WEBHOOK_SECRET` 有个额外的坑:

**滚动重启期间一半副本持旧密钥、一半持新密钥。** Alertmanager 无论用哪个签名,
都会被另一半副本以 401 拒绝。而 Signal Ingress 的 401 意味着**告警丢失** ——
Alertmanager 重试几次就放弃,那段时间的故障在本系统里完全不存在,
而两边的日志都只显示"签名校验失败",看不出是轮换造成的。

因此 `AIOPS_WEBHOOK_SECRET` 支持**逗号分隔的多个密钥**,任一匹配即通过。
轮换按三步走:

```bash
# 1) 加新密钥(新在前,旧在后),滚动重启 —— 此时两种签名都收
AIOPS_WEBHOOK_SECRET="new-secret,old-secret"

# 2) 把 Alertmanager / cluster-agent 切到 new-secret

# 3) 摘掉旧密钥,滚动重启 —— 旧的失效
AIOPS_WEBHOOK_SECRET="new-secret"
```

第 3 步不能省:只做前两步的话"轮换"只是**多了一个**密钥,泄漏的那个仍然可用。

几个实现细节:

- 比较对每个候选密钥都做完(不因某个匹配就提前 break),使比较次数与
  "第几个密钥匹配"无关。
- 全空白(`" , "`)会被解析成 0 个密钥,而 0 个密钥的行为是**放行且不校验**。
  生产启动校验按"未设"拒绝这种值 —— 否则配置看起来设了、启动不报错,
  而 Signal Ingress 实际上完全没有鉴权。
- 其余密钥(`AIOPS_AUTH_HS256_SECRET`、`AIOPS_INTERNAL_TOKEN`)没有多值支持。
  前者在 OIDC 生产模式下不使用(JWKS 轮换由 IdP 侧的 kid 机制处理,
  本系统会按 kid 自动拉取新公钥);后者只在控制面内部调用间使用,
  两端同时滚动重启的窗口内会有少量 5xx,由 Temporal 的 Activity 重试兜住。

> Vault / KMS 与自动轮换属于部署侧:把 Secret 的来源换成 External Secrets
> Operator 或 CSI driver 即可,应用侧不需要改 —— 它只读环境变量。
> 上面那个多密钥机制正是为了让这类自动轮换**不丢信号**。

## 接入企业 OIDC IdP(实测步骤)

以 Keycloak 26 为例。**这一节是实际跑通一遍后记下来的** —— 开箱配置会让
每个请求都 403,而症状指向错误的方向(见下)。

```bash
AIOPS_AUTH_MODE=oidc
AIOPS_OIDC_ISSUER=https://idp.corp.example/realms/aiops
AIOPS_OIDC_JWKS_URL=https://idp.corp.example/realms/aiops/protocol/openid-connect/certs
AIOPS_AUTH_AUDIENCE=aiops
```

IdP 侧需要三个 protocol mapper 与一处 realm 设置:

| 要什么 | 怎么配 | 不配的后果 |
|---|---|---|
| `aud` 含 `aiops` | audience mapper,`included.custom.audience=aiops` | 所有 token 因 audience 不匹配被拒(401) |
| `clusters` claim | user-attribute mapper,`multivalued=true`,`claim.name=clusters` | ABAC 范围为空 → **每个请求 403** |
| `namespaces` claim | 同上 | 同上 |
| 用户属性能写进 token | realm → users/profile → `unmanagedAttributePolicy=ENABLED` | Keycloak 26 默认禁用非托管属性,上面两个 mapper 会**静默输出空值** |

角色**不需要** mapper:本系统会在顶层 `roles` 缺失时回落读
`realm_access.roles`(Keycloak 放 realm 角色的默认位置)。

### 为什么这一节必须存在

`clusters`/`namespaces` 缺失与角色缺失的症状完全一样,而且**极具误导性**:

- token 校验**通过** —— 日志是"认证成功"
- 审计记的是"合法身份被拒"(`result=denied`)
- 指标只是 403 上升

看到这三样,运维会去查 RBAC 角色配置或 ABAC 范围表 —— 而问题在 IdP 侧的
mapper。为此控制面会在"认证通过但零角色"时单独打一条 WARN 并指名这个原因
(按 subject 节流 5 分钟)。

另外两处形态差异已在代码里吸收,不需要额外配置:

- `aud` 是**数组**(`["aiops","account"]`),不是字符串
- `sub` 是 **UUID**;本系统优先用 `preferred_username` 作为审计身份 ——
  审计里记 UUID 等于记不了责任人
