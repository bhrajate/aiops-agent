package store

import (
	"crypto/rand"
	"encoding/hex"
)

// randHexStore 生成随机十六进制 ID 片段(store 内部生成 incident/group ID 用)。
func randHexStore(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(i)
		}
	}
	return hex.EncodeToString(b)
}
