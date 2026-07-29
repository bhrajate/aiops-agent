package api

import (
	"strings"
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestDeriveSignalID_NoRandomness 是 F5 的核心断言。
//
// 旧实现附加 randHex(4),使每次重投递都得到新 signal_id ——
// `ON CONFLICT (signal_id) DO NOTHING` 永不冲突,重复行虚增 signal_count,
// 进而误触发 EvaluateAuto 的 signal_count>=3 分支(一条告警重投三次
// 就被当成"信号突发")。
func TestDeriveSignalID_NoRandomness(t *testing.T) {
	id := signalIdentity{
		Fingerprint: "57c6d9296de2ad39",
		Status:      "firing",
		StartsAt:    ts("2026-07-29T10:00:00Z"),
	}
	first := deriveSignalID(id)
	for i := 0; i < 20; i++ {
		if got := deriveSignalID(id); got != first {
			t.Fatalf("同一身份必须得到同一 ID(否则重投递去重失效):第 %d 次得到 %s,首次 %s", i, got, first)
		}
	}
	if !strings.HasPrefix(first, "sig-") {
		t.Errorf("ID 应带 sig- 前缀: %s", first)
	}
}

// TestDeriveSignalID_RefireIsDistinct 这条同样重要,方向相反。
//
// 只用 fingerprint 会把 firing→resolved→firing 折叠成一条,丢掉"恢复"与
// "再次故障"两个事实。告警恢复后再次触发会得到新的 startsAt,必须是新信号。
func TestDeriveSignalID_RefireIsDistinct(t *testing.T) {
	fp := "57c6d9296de2ad39"
	firing1 := deriveSignalID(signalIdentity{Fingerprint: fp, Status: "firing", StartsAt: ts("2026-07-29T10:00:00Z")})
	resolved := deriveSignalID(signalIdentity{Fingerprint: fp, Status: "resolved", StartsAt: ts("2026-07-29T10:00:00Z")})
	firing2 := deriveSignalID(signalIdentity{Fingerprint: fp, Status: "firing", StartsAt: ts("2026-07-29T11:30:00Z")})

	if firing1 == resolved {
		t.Error("同一 fingerprint 的 firing 与 resolved 是不同事实,必须是不同信号")
	}
	if firing1 == firing2 {
		t.Error("恢复后再次故障(新 startsAt)必须是新信号,否则丢掉第二轮故障")
	}
	if resolved == firing2 {
		t.Error("resolved 与后续 firing 必须区分")
	}
}

// TestDeriveSignalID_TimezoneNormalized 同一时刻的不同时区表示必须得到同一 ID。
// 否则上游换个时区上报就会被当成新信号,重投递去重再次失效。
func TestDeriveSignalID_TimezoneNormalized(t *testing.T) {
	utc := deriveSignalID(signalIdentity{Fingerprint: "abc", Status: "firing",
		StartsAt: ts("2026-07-29T10:00:00Z")})
	shifted := deriveSignalID(signalIdentity{Fingerprint: "abc", Status: "firing",
		StartsAt: ts("2026-07-29T18:00:00+08:00")}) // 同一时刻
	if utc != shifted {
		t.Errorf("同一时刻的不同时区表示应得到同一 ID: %s vs %s", utc, shifted)
	}
}

// TestDeriveSignalID_StatusCaseInsensitive 上游大小写不一致不该产生新信号。
func TestDeriveSignalID_StatusCaseInsensitive(t *testing.T) {
	a := deriveSignalID(signalIdentity{Fingerprint: "abc", Status: "firing", StartsAt: ts("2026-07-29T10:00:00Z")})
	b := deriveSignalID(signalIdentity{Fingerprint: "abc", Status: "FIRING", StartsAt: ts("2026-07-29T10:00:00Z")})
	if a != b {
		t.Error("status 大小写不同不应产生不同信号")
	}
}

// TestDeriveSignalID_FingerprintBeatsPayloadHash fingerprint 在时应优先于 payload 哈希。
//
// 意义在于容忍无关字段变化:Alertmanager 重投递时 payload 里可能带上不同的
// generatorURL / annotations,payload 哈希会变而 fingerprint 不变。
func TestDeriveSignalID_FingerprintBeatsPayloadHash(t *testing.T) {
	base := signalIdentity{Fingerprint: "fp-1", Status: "firing", StartsAt: ts("2026-07-29T10:00:00Z")}
	withHashA := base
	withHashA.PayloadHash = "sha256:aaaa"
	withHashB := base
	withHashB.PayloadHash = "sha256:bbbb"
	if deriveSignalID(withHashA) != deriveSignalID(withHashB) {
		t.Error("有 fingerprint 时,payload 变化不应改变身份(否则无关字段变化就绕过去重)")
	}
}

// TestDeriveSignalID_FallsBackToPayloadHash 无 fingerprint 时退回 payload 哈希。
func TestDeriveSignalID_FallsBackToPayloadHash(t *testing.T) {
	a := deriveSignalID(signalIdentity{Status: "alert", PayloadHash: "sha256:aaaa"})
	b := deriveSignalID(signalIdentity{Status: "alert", PayloadHash: "sha256:aaaa"})
	c := deriveSignalID(signalIdentity{Status: "alert", PayloadHash: "sha256:bbbb"})
	if a != b {
		t.Error("无 fingerprint 时同一 payload 应得到同一 ID")
	}
	if a == c {
		t.Error("不同 payload 应得到不同 ID")
	}
}

// TestDeriveSignalID_DifferentAlertsDistinct 不同告警不能碰撞。
func TestDeriveSignalID_DifferentAlertsDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, fp := range []string{"fp-a", "fp-b", "fp-c"} {
		id := deriveSignalID(signalIdentity{Fingerprint: fp, Status: "firing", StartsAt: ts("2026-07-29T10:00:00Z")})
		if prev, dup := seen[id]; dup {
			t.Errorf("不同 fingerprint 碰撞: %s 与 %s 都得到 %s", fp, prev, id)
		}
		seen[id] = fp
	}
}

// TestDeriveSignalID_ZeroStartsAt startsAt 缺失时仍应稳定(不少上游不带该字段)。
func TestDeriveSignalID_ZeroStartsAt(t *testing.T) {
	id := signalIdentity{Fingerprint: "fp", Status: "firing"}
	if deriveSignalID(id) != deriveSignalID(id) {
		t.Error("startsAt 缺失时 ID 仍应稳定")
	}
	withTime := signalIdentity{Fingerprint: "fp", Status: "firing", StartsAt: ts("2026-07-29T10:00:00Z")}
	if deriveSignalID(id) == deriveSignalID(withTime) {
		t.Error("有无 startsAt 应区分")
	}
}
