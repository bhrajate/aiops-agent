package eventwatch

// 幂等契约的断言。
//
// 这里每一条覆盖的都是**错了不报错**的地方:signal 照常发出、控制面照常返回
// 202、库里照常有行,只是数量不对。而 signal_count 喂给触发策略判"信号突发",
// 于是数量不对会变成"多跑了几轮 RCA",在结论上看不出来。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// payloadHash 复刻控制面 ingress.fill 的算法:对整个 JSON 载荷取 sha256。
// signal_id 由它 + signal_type + starts_at 推导,所以**同哈希 + 同桶 = 同 ID**。
func payloadHash(t *testing.T, s Signal) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func evt(uid, reason string, first, last time.Time, count int32) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e-" + uid, Namespace: "payment", UID: types.UID(uid)},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Count:          count,
		FirstTimestamp: metav1.Time{Time: first},
		LastTimestamp:  metav1.Time{Time: last},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Name: "checkout-abc", Namespace: "payment", UID: types.UID("obj-" + uid),
		},
	}
}

func TestReplayProducesIdenticalPayload(t *testing.T) {
	// informer 在 agent 重启或 resync 时会把现存事件全量重放。
	// 重放必须得到逐字节相同的载荷 —— 否则控制面算出新 signal_id,
	// ON CONFLICT 不冲突,库里多一行、signal_count +1。
	now := time.Date(2026, 8, 20, 10, 3, 0, 0, time.UTC)
	e := evt("u1", "OOMKilling", now.Add(-time.Minute), now, 1)

	a := ToSignal(e, "prod-cn-1", "default", now)
	b := ToSignal(e, "prod-cn-1", "default", now.Add(37*time.Second))

	if payloadHash(t, a) != payloadHash(t, b) {
		t.Fatal("同一事件重放得到不同载荷 —— 控制面会算出不同 signal_id,重复落库")
	}
}

func TestCountIncrementWithinBucketKeepsPayloadStable(t *testing.T) {
	// 这条防 F12 回归。kubelet 把重复事件聚合进同一个 Event 对象并递增 count,
	// informer 为每次递增推一个 UPDATE。若 count 或未归一化的时间戳进了载荷,
	// 一次 OOMKill 循环(count 1→200)就是 200 条 signal ——
	// 表里"去重了"但 signal_count 虚增到 200,而那个数判"信号突发"。
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	first := base.Add(-time.Hour)

	a := ToSignal(evt("u1", "BackOff", first, base.Add(30*time.Second), 1), "c", "t", base)
	b := ToSignal(evt("u1", "BackOff", first, base.Add(4*time.Minute), 87), "c", "t", base)

	if payloadHash(t, a) != payloadHash(t, b) {
		t.Fatal("同一时间桶内 count 递增改变了载荷 —— signal_count 会虚增(F12)")
	}
}

func TestCountIncrementAcrossBucketChangesPayload(t *testing.T) {
	// 反面:跨桶必须变。否则 incident.last_seen 永不前移,
	// 一个正在持续的故障在值班界面上看起来"很久以前的事"。
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	first := base.Add(-time.Hour)

	a := ToSignal(evt("u1", "BackOff", first, base.Add(1*time.Minute), 1), "c", "t", base)
	b := ToSignal(evt("u1", "BackOff", first, base.Add(7*time.Minute), 20), "c", "t", base)

	if payloadHash(t, a) == payloadHash(t, b) {
		t.Fatal("跨时间桶载荷未变 —— last_seen 不会前移,持续故障看起来已经结束")
	}
}

func TestBucketWidthBoundsSignalsPerHour(t *testing.T) {
	// 上界必须确定。桶宽 5 分钟 → 每事件每小时最多 12 条。
	// 这个数字是刻意选的,写死在断言里防止有人把桶调窄而不自知后果。
	if bucketWidth != 5*time.Minute {
		t.Fatalf("桶宽 = %v。改它会同时改变每小时 signal 上界(现为 %d 条/事件),"+
			"太窄退化成 F12 虚增、太宽让 last_seen 迟滞到无意义",
			bucketWidth, int(time.Hour/bucketWidth))
	}
	if got := int(time.Hour / bucketWidth); got != 12 {
		t.Fatalf("每小时上界 = %d, want 12", got)
	}
}

func TestStartsAtIsBucketedAndUTC(t *testing.T) {
	// starts_at 参与 signal_id 推导,而控制面用 RFC3339Nano 格式化它。
	// 非 UTC 会让同一时刻的不同时区表示得到不同 ID(见 signalid.go)。
	shanghai := time.FixedZone("CST", 8*3600)
	local := time.Date(2026, 8, 20, 18, 7, 33, 0, shanghai)
	s := ToSignal(evt("u1", "Evicted", local, local, 1), "c", "t", local)

	if s.StartsAt == nil {
		t.Fatal("starts_at 不能为空 —— 它是幂等键的一部分")
	}
	got := *s.StartsAt
	if got.Location() != time.UTC {
		t.Errorf("starts_at 时区 = %v, want UTC", got.Location())
	}
	if got.Second() != 0 || got.Minute()%5 != 0 {
		t.Errorf("starts_at = %v 未归一化到 5 分钟桶", got)
	}
}

func TestSignalIDLeftEmpty(t *testing.T) {
	// agent 绝不自己算 signal_id:那会形成第二套幂等规则,
	// 而两套规则的分歧只在生产的重复数据里显现。
	s := ToSignal(evt("u1", "OOMKilling", time.Now(), time.Now(), 1), "c", "t", time.Now())
	if s.SignalID != "" {
		t.Errorf("signal_id = %q,应留空由控制面 DeriveSignalID 推导", s.SignalID)
	}
}

func TestPodNotReducedToWorkload(t *testing.T) {
	// 归约到工作负载是控制面 model.WorkloadName 的职责,且 blast_radius.services
	// 依赖它(F3)。agent 也做会让两处在某个 Kind 上分叉,而分叉表现为影响面算错。
	now := time.Now()
	s := ToSignal(evt("u1", "OOMKilling", now, now, 1), "c", "t", now)
	if s.ResourceRef.Kind != "Pod" {
		t.Errorf("Kind = %q, want Pod(忠实上报,不在 agent 侧归约)", s.ResourceRef.Kind)
	}
	if s.ResourceRef.Name != "checkout-abc" {
		t.Errorf("Name = %q, want 原始 Pod 名", s.ResourceRef.Name)
	}
}

func TestSignalJSONShapeMatchesControlPlane(t *testing.T) {
	// cluster-agent 是独立 module,不能 import control-plane 的 model。
	// 字段名靠这条用例钉住:少一个 json tag,控制面 fromNative 会把它解析成
	// 零值 —— 比如 signal_type 空会让整条 signal 被丢弃(return nil),
	// 而 agent 侧只看到 202,以为成功了。
	now := time.Now()
	b, err := json.Marshal(ToSignal(evt("u1", "OOMKilling", now, now, 1), "c1", "t1", now))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{
		"cluster_id", "tenant_id", "source", "signal_type",
		"resource_ref", "severity", "starts_at", "labels",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("载荷缺字段 %q —— 控制面会解析成零值", k)
		}
	}
	if _, ok := m["signal_id"]; ok {
		t.Error("载荷不该含 signal_id(omitempty 应生效)")
	}
	// count 绝不能出现:它递增,会破坏同桶内的载荷稳定性。
	if _, ok := m["count"]; ok {
		t.Error("载荷含 count —— 它递增,会让同一事件每次上报都得到新 signal_id")
	}
	rr, _ := m["resource_ref"].(map[string]any)
	if rr["kind"] != "Pod" {
		t.Errorf("resource_ref.kind = %v, want Pod", rr["kind"])
	}
}

func TestSeverityDefaultsToWarningNotCritical(t *testing.T) {
	// critical 在触发策略里是"必触发"(always=[P1 P2])。默认给 critical
	// 会让一次节点抖动的每个 Pod 事件都消耗一次分诊模型调用。
	now := time.Now()
	if s := ToSignal(evt("u1", "FailedMount", now, now, 1), "c", "t", now); s.Severity != "warning" {
		t.Errorf("FailedMount severity = %q, want warning", s.Severity)
	}
	// 但真正致命的那几个要给 critical
	if s := ToSignal(evt("u2", "OOMKilling", now, now, 1), "c", "t", now); s.Severity != "critical" {
		t.Errorf("OOMKilling severity = %q, want critical", s.Severity)
	}
}

func TestEventTimeFallbackDoesNotUseNow(t *testing.T) {
	// 时间戳全空时必须回退到 firstTimestamp,而不是悄悄用 now:
	// 后者会让同一事件在每次 resync 落进新桶 —— 又回到 F12。
	first := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	e := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e", Namespace: "payment", UID: types.UID("u")},
		Type:           corev1.EventTypeWarning,
		Reason:         "Evicted",
		FirstTimestamp: metav1.Time{Time: first},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p", Namespace: "payment"},
	}
	now := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)
	a := ToSignal(e, "c", "t", now)
	b := ToSignal(e, "c", "t", now.Add(20*time.Minute))
	if payloadHash(t, a) != payloadHash(t, b) {
		t.Fatal("缺时间戳时用了 now —— 每次 resync 都会落进新桶(F12)")
	}
	if a.StartsAt.Before(first.Add(-time.Minute)) || a.StartsAt.After(first.Add(bucketWidth)) {
		t.Errorf("starts_at = %v,应落在 firstTimestamp 的桶里", a.StartsAt)
	}
}
