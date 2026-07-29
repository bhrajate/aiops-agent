package model

// Signal 身份推导(F5)。
//
// 原问题:`fill` 声称"幂等:同一 payload 短时间内重复投递生成相同前缀 + 时间片",
// 但实现是 `hex(payloadHash[:8]) + "-" + randHex(4)` —— **随机后缀保证每次重投递
// 都得到不同 signal_id**。于是:
//   - `INSERT ... ON CONFLICT (signal_id) DO NOTHING` 永远不冲突,重投递产生重复行;
//   - `incidents.signal_count` 随重投递虚增;
//   - 进而误触发 `EvaluateAuto` 的 `signal_count >= 3` 分支(F7),
//     即一条告警重投三次就被当成"信号突发"。
// 去重机制本身早就在库上就位了,是这个随机后缀把它废掉的。
//
// Alertmanager 至少一次投递,重投递是**预期行为**(见 alertmanager#2768:
// 重复的 resolved 通知带相同 startsAt/endsAt),不是异常。
//
// 身份取 `fingerprint + status + startsAt`,理由:
//
//	fingerprint  Alertmanager 对**标签集**的 fnv64a 哈希。它标识"哪条告警",
//	             不标识"哪一次通知" —— 官方文档明确多条 alert 可共享同一
//	             fingerprint,要求接收方自己处理。所以单靠它会把
//	             firing→resolved→firing 三次通知折叠成一条,丢掉恢复与再次故障。
//	status       区分 firing 与 resolved。同一 fingerprint 的这两者是不同事实。
//	startsAt     区分"同一告警的不同一次故障"。告警恢复后再次触发会得到新的
//	             startsAt,于是新一轮故障是新信号;而同一轮的重复投递
//	             startsAt 不变,被正确去重。
//
// 没有 fingerprint 时(非 Alertmanager 来源,或旧版不带该字段)退回 payload 哈希:
// 它对**完全相同**的 payload 稳定,足以吃掉重投递,只是不像 fingerprint 那样
// 能容忍无关字段(如 generatorURL)的变化。

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// signalIdentity 是推导 signal_id 的输入。
type SignalIdentity struct {
	// Fingerprint 来自 Alertmanager;为空时用 PayloadHash 兜底。
	Fingerprint string
	Status      string
	StartsAt    time.Time
	// PayloadHash 形如 "sha256:<hex>",fingerprint 缺失时作为身份基础。
	PayloadHash string
}

// deriveSignalID 由稳定身份推导 signal_id,**不含任何随机成分**。
//
// 同一告警的同一次通知重复投递会得到同一 ID,由
// `ON CONFLICT (signal_id) DO NOTHING` 吃掉;不同故障轮次得到不同 ID。
func DeriveSignalID(id SignalIdentity) string {
	base := strings.TrimSpace(id.Fingerprint)
	if base == "" {
		// 兜底:payload 哈希。去掉 "sha256:" 前缀只为让最终 ID 短一些。
		base = strings.TrimPrefix(strings.TrimSpace(id.PayloadHash), "sha256:")
	}
	// startsAt 用 RFC3339Nano 并统一到 UTC:同一时刻的不同时区表示必须得到同一 ID,
	// 否则上游换个时区上报就会被当成新信号。
	var starts string
	if !id.StartsAt.IsZero() {
		starts = id.StartsAt.UTC().Format(time.RFC3339Nano)
	}
	h := sha256.Sum256([]byte(base + "|" + strings.ToLower(strings.TrimSpace(id.Status)) + "|" + starts))
	return "sig-" + hex.EncodeToString(h[:10])
}
