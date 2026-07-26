#!/usr/bin/env bash
# =============================================================================
# 自签 mTLS 证书生成(仅开发 / 测试)。对应 docs/SECURITY.md §3。
#
# 生成:
#   ca.crt / ca.key         —— 自签根 CA
#   agent.crt / agent.key   —— cluster-agent 服务端证书(SAN: cluster-agent / localhost / 127.0.0.1)
#   client.crt / client.key —— control-plane 客户端证书(clientAuth)
#
# 输出目录默认为脚本所在目录(deploy/certs/)。生成的 .crt/.key 已被 .gitignore 忽略,
# 仅提交本脚本。生产环境请改用企业 CA / Vault PKI 并启用轮换。
#
# 用法:
#   bash deploy/certs/gen-certs.sh                 # 默认输出到 deploy/certs/
#   OUT_DIR=/tmp/certs DAYS=825 bash gen-certs.sh  # 自定义
# =============================================================================
set -euo pipefail

OUT_DIR="${OUT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
DAYS="${DAYS:-825}"          # ≤825 天,贴近现代 TLS 有效期上限
CA_DAYS="${CA_DAYS:-3650}"
AGENT_CN="${AGENT_CN:-cluster-agent}"
# 服务端 SAN:集群内 Service DNS + 短名 + 本地(便于 docker/本地联调)
AGENT_SAN="${AGENT_SAN:-DNS:cluster-agent,DNS:cluster-agent.aiops.svc.cluster.local,DNS:localhost,IP:127.0.0.1}"

mkdir -p "$OUT_DIR"
cd "$OUT_DIR"
umask 077   # 私钥仅属主可读

echo ">> 输出目录: $OUT_DIR"

# ---- 1) 根 CA ----
if [[ -f ca.crt && -f ca.key ]]; then
  echo ">> 复用已存在的 CA (ca.crt/ca.key)"
else
  echo ">> 生成根 CA"
  openssl genrsa -out ca.key 4096
  openssl req -x509 -new -nodes -key ca.key -sha256 -days "$CA_DAYS" \
    -subj "/O=AIOps/CN=AIOps Dev Root CA" -out ca.crt
fi

# ---- 2) cluster-agent 服务端证书 ----
echo ">> 生成 cluster-agent 服务端证书 (SAN: $AGENT_SAN)"
openssl genrsa -out agent.key 2048
openssl req -new -key agent.key -subj "/O=AIOps/CN=${AGENT_CN}" -out agent.csr
cat > agent.ext <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=${AGENT_SAN}
EOF
openssl x509 -req -in agent.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -sha256 -days "$DAYS" -extfile agent.ext -out agent.crt

# ---- 3) control-plane 客户端证书 ----
echo ">> 生成 control-plane 客户端证书"
openssl genrsa -out client.key 2048
openssl req -new -key client.key -subj "/O=AIOps/CN=control-plane" -out client.csr
cat > client.ext <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=clientAuth
subjectAltName=DNS:control-plane,DNS:control-plane.aiops.svc.cluster.local,DNS:localhost
EOF
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -sha256 -days "$DAYS" -extfile client.ext -out client.crt

# ---- 清理中间产物 ----
rm -f agent.csr agent.ext client.csr client.ext ca.srl

echo ""
echo ">> 完成。生成文件:"
ls -1 ca.crt agent.crt agent.key client.crt client.key
echo ""
echo ">> 校验证书链:"
openssl verify -CAfile ca.crt agent.crt
openssl verify -CAfile ca.crt client.crt
echo ""
echo ">> 创建 K8s Secret(示例):"
echo "   kubectl -n aiops create secret generic aiops-agent-tls \\"
echo "     --from-file=ca.crt --from-file=agent.crt --from-file=agent.key"
echo "   kubectl -n aiops create secret generic aiops-client-tls \\"
echo "     --from-file=ca.crt --from-file=client.crt --from-file=client.key"
