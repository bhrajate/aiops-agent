// Package webhookauth 校验 Signal Ingress 的 HMAC-SHA256 签名(SECURITY §4)。
package webhookauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Verify 校验 X-AIOPS-Signature: sha256=<hex> 是否匹配 body。
// secret 为空时返回 (true, false):放行但标记未校验(开发)。
func Verify(secret string, signature string, body []byte) (ok bool, checked bool) {
	if secret == "" {
		return true, false
	}
	expected := Sign(secret, body)
	sig := strings.TrimSpace(signature)
	return hmac.Equal([]byte(strings.ToLower(sig)), []byte(expected)), true
}

// Sign 生成签名头值(供测试与文档)。
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
