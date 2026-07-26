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
