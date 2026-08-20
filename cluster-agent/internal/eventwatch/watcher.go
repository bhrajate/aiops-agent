package eventwatch

// Watcher:watch K8s Event → 筛选 → 限流 → 推送到控制面 /v1/signals。
//
// 三条设计约束(与项目其他部分一致):
//
//  1. **失败只降级,不影响既有路径。** watch goroutine 出任何问题都不能影响
//     :9100 上的拉取式工具 —— 那是 agent 的主职责。
//  2. **复用 ingress。** 走 POST /v1/signals 而不是新开内部端点:一次性拿到
//     限流(F6)、幂等落库、两层聚合、触发策略、审计。新开端点等于把这五样
//     重写一遍,且必然漂移。
//  3. **不是持久管道。** 丢一次推送不是灾难(事件仍可被 get_kubernetes_events
//     查到)。所以队列有界、丢最旧、不落盘 —— 落盘换来的可靠性配不上它引入的状态。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// Stats 是可观测计数。丢弃**必须可见** —— 否则"事件怎么没上来"无从排查。
type Stats struct {
	Seen      atomic.Int64
	Filtered  atomic.Int64
	Throttled atomic.Int64
	Sent      atomic.Int64
	Failed    atomic.Int64
}

// Config 配置 Watcher。
type Config struct {
	ClusterID string
	TenantID  string
	// IngressURL 控制面 /v1/signals 的完整地址。
	IngressURL string
	// WebhookSecret 用于 HMAC 签名(与 ingress 的 webhookauth 对齐)。
	//
	// ⚠️ 这是威胁面的实质变化:agent 跑在业务集群里,比控制面更可能被攻陷,
	// 而拿到这个 secret 就能伪造 signal → 伪造 incident。缓解:按集群一份
	// (不与其他集群共用),且 ingress 侧逐条记审计。若 agent 的威胁模型变化,
	// 这是第一个该回退的决定 —— 与 obsquery 那条"控制面持有观测凭据"同构。
	WebhookSecret string
	Reasons       []string
	Namespaces    []string
	// RatePerSec 本地令牌桶速率。0 用默认值。
	//
	// 为什么源头也要限:节点挂掉会瞬间产生几百个 Pod 事件。ingress 侧有按租户
	// 令牌桶,但拿到 429 再退避是把压力转成两边的无效功。
	RatePerSec float64
	// ResyncPeriod informer 全量重放周期。0 表示不周期重放。
	//
	// 重放是安全的(同桶内载荷稳定 → 同 signal_id → ON CONFLICT 吃掉),
	// 但它会白耗 ingress 的配额,所以默认关掉。
	ResyncPeriod time.Duration
}

// Watcher 监听事件并上报。
type Watcher struct {
	cfg    Config
	client kubernetes.Interface
	filter *Filter
	http   *http.Client
	log    *slog.Logger
	stats  *Stats
	// tokens 是简易令牌桶:每次上报消耗一个,后台按速率补充。
	tokens chan struct{}
	now    func() time.Time
}

const defaultRatePerSec = 5.0

// New 构造 Watcher。
func New(client kubernetes.Interface, cfg Config, log *slog.Logger) *Watcher {
	rate := cfg.RatePerSec
	if rate <= 0 {
		rate = defaultRatePerSec
	}
	cfg.RatePerSec = rate
	// 桶容量取 1 秒的量(至少 1):容量太大等于没限流,
	// 因为风暴的前一瞬间会一次性放行整桶。
	capacity := int(rate)
	if capacity < 1 {
		capacity = 1
	}
	return &Watcher{
		cfg:    cfg,
		client: client,
		filter: NewFilter(cfg.Reasons, cfg.Namespaces),
		http:   &http.Client{Timeout: 10 * time.Second},
		log:    log,
		stats:  &Stats{},
		tokens: make(chan struct{}, capacity),
		now:    time.Now,
	}
}

// Stats 暴露计数供日志/指标使用。
func (w *Watcher) Stats() *Stats { return w.stats }

// Run 启动 watch 直到 ctx 结束。**永不返回 error** ——
// 调用方是 agent 的 main,而 event watch 的任何问题都不该影响拉取式工具。
func (w *Watcher) Run(ctx context.Context) {
	defer func() {
		// informer 的回调里 panic 会打挂整个进程,而那会连带停掉 :9100 上的
		// 只读工具服务 —— 违反"失败只降级"。这里兜住。
		if r := recover(); r != nil {
			w.log.Error("event watch panicked, 已停止(拉取式工具不受影响)", "panic", r)
		}
	}()

	go w.refillTokens(ctx)

	factory := informers.NewSharedInformerFactory(w.client, w.cfg.ResyncPeriod)
	inf := factory.Core().V1().Events().Informer()
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) { w.handle(ctx, obj) },
		// UpdateFunc 也要处理:kubelet 把重复事件聚合进同一对象并递增 count,
		// 每次递增是一个 UPDATE。同桶内会被幂等吃掉,跨桶推进 last_seen。
		UpdateFunc: func(_, obj any) { w.handle(ctx, obj) },
	}); err != nil {
		w.log.Error("注册事件处理器失败,event watch 未启动", "err", err)
		return
	}

	w.log.Info("event watch 启动",
		"cluster_id", w.cfg.ClusterID, "rate_per_sec", w.cfg.RatePerSec,
		"reasons", len(w.filter.reasons), "namespaces", len(w.filter.namespaces))

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())
	<-ctx.Done()
	w.log.Info("event watch 停止",
		"seen", w.stats.Seen.Load(), "filtered", w.stats.Filtered.Load(),
		"throttled", w.stats.Throttled.Load(), "sent", w.stats.Sent.Load(),
		"failed", w.stats.Failed.Load())
}

func (w *Watcher) refillTokens(ctx context.Context) {
	interval := time.Duration(float64(time.Second) / w.cfg.RatePerSec)
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			select {
			case w.tokens <- struct{}{}:
			default: // 桶满,丢弃补充
			}
		}
	}
}

// handle 处理一条事件。
func (w *Watcher) handle(ctx context.Context, obj any) {
	ev, ok := obj.(*corev1.Event)
	if !ok {
		return
	}
	w.stats.Seen.Add(1)
	if !w.filter.Allow(ev) {
		w.stats.Filtered.Add(1)
		return
	}
	// 限流:拿不到令牌直接丢,不排队。排队会在风暴时把内存吃掉,
	// 而这不是持久管道 —— 事件仍可被拉取式工具查到。
	select {
	case <-w.tokens:
	default:
		w.stats.Throttled.Add(1)
		return
	}
	sig := ToSignal(ev, w.cfg.ClusterID, w.cfg.TenantID, w.now())
	if err := w.post(ctx, sig); err != nil {
		w.stats.Failed.Add(1)
		// 只记日志不重试:重试会在控制面不可用时把 agent 变成重试风暴的源头,
		// 而丢一次推送的代价是可接受的(见文件头第 3 条)。
		w.log.Warn("上报事件 signal 失败(已丢弃,拉取式工具不受影响)",
			"reason", ev.Reason, "namespace", ev.InvolvedObject.Namespace, "err", err)
		return
	}
	w.stats.Sent.Add(1)
}

// post 把 signal 发到控制面 ingress,带 HMAC 签名。
func (w *Watcher) post(ctx context.Context, sig Signal) error {
	body, err := json.Marshal(sig)
	if err != nil {
		return fmt.Errorf("marshal signal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.IngressURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.cfg.WebhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(w.cfg.WebhookSecret))
		mac.Write(body)
		req.Header.Set("X-AIOPS-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ingress 返回 %d", resp.StatusCode)
	}
	return nil
}
