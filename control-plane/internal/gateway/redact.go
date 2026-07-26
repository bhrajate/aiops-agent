package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
)

// 脱敏规则(文档 9.2 / 14.2):对进入模型的内容擦除 Secret/Token/凭据/个人信息。
var redactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._\-]+`),                                               // Bearer token
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)[^\s"']+`),                                      // Authorization
	regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key)\s*[:=]\s*)[^\s"',}]+`),     // 凭据键值
	regexp.MustCompile(`eyJ[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+`),                       // JWT
	regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),                           // Email
	regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`),                                                // IPv4(内网地址也脱敏,最小化)
	regexp.MustCompile(`-----BEGIN [A-Z ]+PRIVATE KEY-----[\s\S]*?-----END [A-Z ]+PRIVATE KEY-----`), // 私钥
}

// Redact 返回脱敏后文本与是否发生脱敏。
func Redact(s string) (string, bool) {
	redacted := false
	out := s
	for i, p := range redactPatterns {
		switch i {
		case 0, 1, 2:
			// 保留键名,替换值
			if p.MatchString(out) {
				redacted = true
				out = p.ReplaceAllString(out, "${1}[REDACTED]")
			}
		default:
			if p.MatchString(out) {
				redacted = true
				out = p.ReplaceAllString(out, "[REDACTED]")
			}
		}
	}
	return out, redacted
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(i)
		}
	}
	return hex.EncodeToString(b)
}
