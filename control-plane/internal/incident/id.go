package incident

import (
	"crypto/rand"
	"encoding/hex"
)

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// 极少发生;退化为固定填充,保证不 panic
		for i := range b {
			b[i] = byte(i)
		}
	}
	return hex.EncodeToString(b)
}
