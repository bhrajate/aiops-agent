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
//
// secret 支持**逗号分隔的多个值**,任一匹配即通过。这是为了让密钥轮换
// 不丢信号 —— 详见 VerifyAny 的说明。
func Verify(secret string, signature string, body []byte) (ok bool, checked bool) {
	return VerifyAny(ParseSecrets(secret), signature, body)
}

// ParseSecrets 解析逗号分隔的密钥列表,去空白与空项。
func ParseSecrets(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// VerifyAny 用任一密钥校验签名。
//
// 为什么需要多密钥:密钥只在启动时从环境读取(无热重载),所以轮换靠滚动重启。
// 但滚动重启期间**一半副本持旧密钥、一半持新密钥** —— Alertmanager 无论用哪个
// 签名,都会被另一半副本以 401 拒绝。而 Signal Ingress 的 401 意味着**告警丢失**:
// Alertmanager 会重试几次然后放弃,那段时间的故障在本系统里完全不存在。
//
// 轮换流程因此是三步(见 docs/SECURITY.md):
//  1. 配 "新,旧" 两个密钥,滚动重启 —— 两种签名都收
//  2. 把 Alertmanager 切到新密钥
//  3. 配 "新" 一个,滚动重启 —— 旧密钥失效
//
// 逐个比较而不是先找出"哪个密钥对":后者会因为提前返回而产生可测量的时间差。
// 这里对每个候选都做 hmac.Equal(常数时间),且不因某个匹配就提前跳出循环。
func VerifyAny(secrets []string, signature string, body []byte) (ok bool, checked bool) {
	if len(secrets) == 0 {
		// 未配密钥:放行但标记未校验。生产模式下 config.Validate 会拒绝这种配置
		// (必须设 AIOPS_WEBHOOK_SECRET),所以这条路径只在开发环境生效。
		return true, false
	}
	sig := strings.ToLower(strings.TrimSpace(signature))
	matched := false
	for _, s := range secrets {
		// 刻意不 break:让比较次数与"第几个密钥匹配"无关。
		if hmac.Equal([]byte(sig), []byte(Sign(s, body))) {
			matched = true
		}
	}
	return matched, true
}

// Sign 生成签名头值(供测试与文档)。
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
