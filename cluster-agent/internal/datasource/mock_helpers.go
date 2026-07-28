package datasource

import (
	"crypto/sha1"
	"encoding/hex"
)

// shortHash 从 s 生成确定性的 6 字符 id 片段。
func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:6]
}

func keyEventReason(category string) string {
	switch category {
	case CatReleaseRegression:
		return "新版本 Pod Readiness 探针间歇失败(上游超时)"
	case CatPodCrashLoop:
		return "OOMKilling / BackOff"
	case CatResourceBottle:
		return "CPUThrottlingHigh"
	default:
		return "依赖超时导致 Readiness 失败"
	}
}
