package datasource

import (
	"crypto/sha1"
	"encoding/hex"
)

// shortHash produces a deterministic 6-char id fragment from s.
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
