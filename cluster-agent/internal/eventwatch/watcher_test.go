package eventwatch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recorder 收下 agent 发来的 signal,供断言。
type recorder struct {
	mu     sync.Mutex
	bodies [][]byte
	sigs   []string
	status int
	srv    *httptest.Server
	secret string
}

func newRecorder(status int, secret string) *recorder {
	r := &recorder{status: status, secret: secret}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, b)
		r.sigs = append(r.sigs, req.Header.Get("X-AIOPS-Signature"))
		r.mu.Unlock()
		w.WriteHeader(r.status)
	}))
	return r
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *recorder) close() { r.srv.Close() }

func warnEvent(name, reason, ns string, last time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name)},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		FirstTimestamp: metav1.Time{Time: last},
		LastTimestamp:  metav1.Time{Time: last},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p-" + name, Namespace: ns},
	}
}

// runWatcher 起一个 Watcher,等它把预置事件处理完,然后停掉。
func runWatcher(t *testing.T, objs []*corev1.Event, cfg Config, status int) (*Watcher, *recorder) {
	t.Helper()
	if status == 0 {
		status = 202
	}
	rec := newRecorder(status, cfg.WebhookSecret)
	t.Cleanup(rec.close)
	cfg.IngressURL = rec.srv.URL

	converted := make([]any, 0, len(objs))
	for _, o := range objs {
		converted = append(converted, o)
	}
	client := fake.NewSimpleClientset(toRuntime(converted)...)
	w := New(client, cfg, quietLog())
	// 预填满令牌桶,避免用例受补充速率影响(限流单独有用例)。
	for i := 0; i < cap(w.tokens); i++ {
		w.tokens <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	// 等到有上报或超时
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if rec.count() >= len(objs) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	cancel()
	<-done
	return w, rec
}

func TestWatcherSendsWhitelistedEvent(t *testing.T) {
	now := time.Now()
	_, rec := runWatcher(t, []*corev1.Event{
		warnEvent("a", "OOMKilling", "payment", now),
	}, Config{ClusterID: "c1", TenantID: "t1"}, 202)

	if rec.count() != 1 {
		t.Fatalf("上报数 = %d, want 1", rec.count())
	}
	var sig map[string]any
	if err := json.Unmarshal(rec.bodies[0], &sig); err != nil {
		t.Fatalf("上报载荷不是合法 JSON: %v", err)
	}
	if sig["source"] != SourceKubernetes {
		t.Errorf("source = %v, want %s", sig["source"], SourceKubernetes)
	}
	if _, ok := sig["signal_id"]; ok {
		t.Error("不该带 signal_id —— 幂等键由控制面推导")
	}
}

func TestWatcherDropsNonWhitelisted(t *testing.T) {
	now := time.Now()
	w, rec := runWatcher(t, []*corev1.Event{
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "n1", Namespace: "payment"},
			Type:           corev1.EventTypeNormal,
			Reason:         "Scheduled",
			LastTimestamp:  metav1.Time{Time: now},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p", Namespace: "payment"},
		},
	}, Config{ClusterID: "c1"}, 202)

	if rec.count() != 0 {
		t.Errorf("Normal 事件被上报了 %d 条", rec.count())
	}
	if w.stats.Filtered.Load() == 0 {
		t.Error("Filtered 计数未增加 —— 丢弃必须可见,否则无从排查'事件怎么没上来'")
	}
}

func TestWatcherRateLimitBoundsOutput(t *testing.T) {
	// 节点挂掉会瞬间产生几百个 Pod 事件。限流必须在源头生效并且**有上界**,
	// 否则 ingress 侧会返 429,而那只是把压力转成两边的无效功。
	now := time.Now()
	events := make([]*corev1.Event, 0, 60)
	for i := 0; i < 60; i++ {
		events = append(events, warnEvent("s"+string(rune('a'+i%26))+string(rune('0'+i/26)),
			"OOMKilling", "payment", now))
	}
	rec := newRecorder(202, "")
	t.Cleanup(rec.close)

	converted := make([]any, 0, len(events))
	for _, e := range events {
		converted = append(converted, e)
	}
	client := fake.NewSimpleClientset(toRuntime(converted)...)
	// 速率 10/s。预填 3 个令牌:informer 在 cache sync 时几乎瞬间投完 60 条,
	// 所以放行量 ≈ 预填 + 窗口内补充,远小于 60。
	w := New(client, Config{ClusterID: "c1", IngressURL: rec.srv.URL, RatePerSec: 10}, quietLog())
	for i := 0; i < 3; i++ {
		w.tokens <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	<-ctx.Done()
	cancel()
	<-done

	sent := rec.count()
	throttled := int(w.stats.Throttled.Load())
	// 上界:必须远小于事件总数,否则限流没生效。
	if sent >= len(events) {
		t.Errorf("上报 %d/%d 条 —— 限流没生效", sent, len(events))
	}
	// 下界同样重要:一个**永久卡死**的令牌桶也能满足上界。
	// 没有这条,把 refillTokens 整个删掉用例照样通过 —— 那就是空转。
	if sent == 0 {
		t.Error("上报 0 条 —— 限流器把流量永久掐死了,而它应该按速率放行")
	}
	if throttled == 0 {
		t.Error("Throttled 计数为 0 —— 被丢弃的量必须可见,否则'事件怎么没上来'无从排查")
	}
	if sent+throttled < len(events) {
		t.Errorf("放行 %d + 丢弃 %d < 总数 %d —— 有事件既没上报也没计数,凭空消失了",
			sent, throttled, len(events))
	}
	t.Logf("限流后上报 %d 条,丢弃 %d 条(总 %d)", sent, throttled, len(events))
}

func TestWatcherSurvivesIngressFailure(t *testing.T) {
	// 控制面返 5xx 时 agent 必须继续活着 —— 它的主职责是 :9100 上的拉取式工具,
	// 而"失败只降级、不影响既有路径"是全项目一致的约束。
	now := time.Now()
	w, rec := runWatcher(t, []*corev1.Event{
		warnEvent("f1", "OOMKilling", "payment", now),
	}, Config{ClusterID: "c1"}, 500)

	_ = rec
	if w.stats.Failed.Load() == 0 {
		t.Error("Failed 计数未增加 —— 上报失败必须可见")
	}
	// Run 已正常返回(runWatcher 里 <-done 成功),即没有 panic 逃出去。
}

func TestWatcherSignsPayload(t *testing.T) {
	// ingress 用 webhook HMAC 鉴权(非用户 JWT)。签名错会被 401 拒,
	// 而那时 agent 只看到一个非 2xx,不会知道是签名问题 —— 所以这里钉住算法。
	now := time.Now()
	secret := "test-secret"
	_, rec := runWatcher(t, []*corev1.Event{
		warnEvent("g1", "Evicted", "payment", now),
	}, Config{ClusterID: "c1", WebhookSecret: secret}, 202)

	if rec.count() != 1 {
		t.Fatalf("上报数 = %d, want 1", rec.count())
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rec.bodies[0])
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if rec.sigs[0] != want {
		t.Errorf("签名 = %q, want %q", rec.sigs[0], want)
	}
}

func TestWatcherNamespaceScope(t *testing.T) {
	now := time.Now()
	_, rec := runWatcher(t, []*corev1.Event{
		warnEvent("in", "OOMKilling", "payment", now),
		warnEvent("out", "OOMKilling", "kube-system", now),
	}, Config{ClusterID: "c1", Namespaces: []string{"payment"}}, 202)

	if rec.count() != 1 {
		t.Errorf("上报数 = %d, want 1(kube-system 应被范围排除)", rec.count())
	}
}
