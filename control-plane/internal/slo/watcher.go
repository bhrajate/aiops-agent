package slo

// SLO 燃尽率监视循环。
//
// 检测到燃尽后**合成一条 signal 走既有入口**,而不是直接建 incident:
// 那样它自动获得两层聚合、触发策略、幂等去重、审计。一条新路径会把这些全绕过,
// 而且会产出一类"不像别的 incident"的 incident,前端与 RCA 都要额外处理。

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aiops/control-plane/internal/model"
	"github.com/aiops/control-plane/internal/obsquery"
)

// Querier 是监视所需的最小查询能力。
type Querier interface {
	InternalInstantQuery(ctx context.Context, expr, clusterID string) ([]obsquery.InstantSample, error)
	HasPrometheus() bool
}

// SignalSink 接收合成出来的 signal(实现为 store.InsertSignalWithOutbox)。
type SignalSink interface {
	InsertSignalWithOutbox(ctx context.Context, sig model.Signal) (bool, error)
}

// Metrics 记录监视结果。
type Metrics interface {
	ObserveSLOEvaluation(sli string, breached bool)
	IncSignal(source string)
}

// Watcher 周期性评估 SLO 燃尽率。
type Watcher struct {
	q         Querier
	sink      SignalSink
	slis      []SLI
	tiers     []Tier
	tenantID  string
	clusterID string
	interval  time.Duration
	metrics   Metrics
	log       *slog.Logger

	// episodeStart 记录每个 (sli, tier) 当前燃烧片段的起始时刻。
	//
	// 为什么需要它:合成 signal 的身份由 fingerprint + status + startsAt 决定(F5)。
	// 若每轮用 now() 作 startsAt,持续燃烧会每轮产出一条新 signal,
	// signal_count 暴涨并误触发 signal_burst;若用固定值,恢复后再次燃烧
	// 又会被当成重投递吃掉,丢掉第二次故障。
	// 用"片段起始时刻"两者兼得:同一片段内 startsAt 不变(去重),
	// 新片段有新 startsAt(新故障)。
	//
	// 进程内状态,重启即丢 —— 重启后同一片段会产出一条新 signal。
	// 可接受:两层聚合会把它并进同一个 alert_group(grouping_key 不含时间),
	// 只是 signal_count 多 1。修这个需要把片段状态落库,代价大于收益。
	episodeStart map[string]time.Time
}

func NewWatcher(q Querier, sink SignalSink, slis []SLI, tenantID, clusterID string,
	interval time.Duration, log *slog.Logger) *Watcher {
	if interval <= 0 {
		interval = time.Minute
	}
	return &Watcher{
		q: q, sink: sink, slis: slis, tiers: DefaultTiers(),
		tenantID: tenantID, clusterID: clusterID, interval: interval, log: log,
		episodeStart: map[string]time.Time{},
	}
}

// WithMetrics 注入指标记录器。
func (w *Watcher) WithMetrics(m Metrics) *Watcher {
	w.metrics = m
	return w
}

// WithTiers 覆盖档位(测试与特殊 SLO 周期用)。
func (w *Watcher) WithTiers(t []Tier) *Watcher {
	if len(t) > 0 {
		w.tiers = t
	}
	return w
}

// Run 阻塞运行监视循环。
//
// 启动时**不**立即评估:长窗口(最长 3 天)需要历史数据,刚启动就评估拿到的是
// 不完整窗口的结果 —— 那会在部署后立刻产出一批假故障。等一个周期再开始。
func (w *Watcher) Run(ctx context.Context) {
	w.log.Info("slo watcher started", "interval", w.interval,
		"slis", len(w.slis), "tiers", len(w.tiers))
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.evaluateAll(ctx)
		}
	}
}

func (w *Watcher) evaluateAll(ctx context.Context) {
	for _, sli := range w.slis {
		breach, ok := w.evaluate(ctx, sli)
		if w.metrics != nil {
			w.metrics.ObserveSLOEvaluation(sli.Name, ok)
		}
		if !ok {
			continue
		}
		if err := w.emit(ctx, breach); err != nil {
			w.log.Warn("emit slo signal failed", "sli", sli.Name, "err", err)
		}
	}
}

// evaluate 按档位从严到宽评估,返回**最严重**的越限。
//
// 从严到宽并在首个命中处停止:一次燃烧同时满足多个档位是常态(14.4× 必然也满足
// 1×),全部产出会让同一次故障发出三条 signal。只报最严重的那个 ——
// 这也是 SRE workbook 里"三条通知"问题的解法。
func (w *Watcher) evaluate(ctx context.Context, sli SLI) (Breach, bool) {
	budget := sli.ErrorBudget()
	for _, tier := range w.tiers {
		threshold := tier.BurnRate * budget
		longRate, ok := w.queryRatio(ctx, sli, tier.LongWindow)
		if !ok || longRate <= threshold {
			continue
		}
		// 长窗超了才查短窗:短窗只用于确认"仍在燃烧",长窗没超时查它是浪费。
		shortRate, ok := w.queryRatio(ctx, sli, tier.ShortWindow)
		if !ok || shortRate <= threshold {
			// 长窗超但短窗没超 = 燃烧已停止,长窗均值还没降下来。
			// 这正是多窗口要过滤掉的情形。
			w.resetEpisode(sli, tier)
			continue
		}
		return Breach{SLI: sli, Tier: tier, LongRate: longRate,
			ShortRate: shortRate, Threshold: threshold}, true
	}
	// 所有档位都没越限:清掉所有片段状态,下次燃烧算新片段。
	for _, tier := range w.tiers {
		w.resetEpisode(sli, tier)
	}
	return Breach{}, false
}

func (w *Watcher) queryRatio(ctx context.Context, sli SLI, window time.Duration) (float64, bool) {
	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	samples, err := w.q.InternalInstantQuery(qctx, sli.exprFor(window), w.clusterID)
	if err != nil {
		w.log.Warn("slo query failed", "sli", sli.Name,
			"window", promDuration(window), "err", err)
		return 0, false
	}
	if len(samples) == 0 {
		// 无数据 ≠ 没问题。但也不能当成越限:服务刚上线或指标名写错都会走到这里,
		// 当越限处理会产出假故障。记日志而非静默,让配错有线索。
		w.log.Debug("slo query returned no samples", "sli", sli.Name,
			"window", promDuration(window))
		return 0, false
	}
	// 取最大值:表达式可能按实例/路径分组返回多条序列,
	// 任一维度在燃尽就该触发 —— 取平均会让局部故障被健康维度稀释掉。
	maxV := samples[0].Value
	for _, s := range samples[1:] {
		if s.Value > maxV {
			maxV = s.Value
		}
	}
	return maxV, true
}

func episodeKey(sli SLI, tier Tier) string { return sli.Name + "|" + tier.Name }

func (w *Watcher) resetEpisode(sli SLI, tier Tier) {
	delete(w.episodeStart, episodeKey(sli, tier))
}

// episodeStartFor 返回当前燃烧片段的起始时刻(不存在则以此刻开始)。
func (w *Watcher) episodeStartFor(sli SLI, tier Tier) time.Time {
	k := episodeKey(sli, tier)
	if t, ok := w.episodeStart[k]; ok {
		return t
	}
	t := time.Now().UTC().Truncate(time.Second)
	w.episodeStart[k] = t
	return t
}

// emit 把越限合成为 signal 并走既有入口。
func (w *Watcher) emit(ctx context.Context, b Breach) error {
	starts := w.episodeStartFor(b.SLI, b.Tier)
	sig := w.buildSignal(b, starts)
	inserted, err := w.sink.InsertSignalWithOutbox(ctx, sig)
	if err != nil {
		return err
	}
	if inserted {
		w.log.Info("slo burn rate signal emitted", "sli", b.SLI.Name,
			"tier", b.Tier.Name, "long_rate", b.LongRate, "threshold", b.Threshold)
		if w.metrics != nil {
			w.metrics.IncSignal(SignalSource)
		}
	}
	return nil
}

// SignalSource 标记来自 SLO 监视的信号。
//
// 单独的 source 值(而非复用 alertmanager)让这类信号在审计与指标里可区分:
// "多少故障是我们主动发现的"是衡量主动检测价值的唯一方式。
const SignalSource = "slo-burn-rate"

// buildSignal 构造合成信号。
//
// 身份稳定性靠 fingerprint + startsAt(见 episodeStart 的注释):
// fingerprint 由 tenant/cluster/SLI/档位 派生 —— 不含时间,所以同一片段内
// 重复评估得到同一 signal_id 并被 ON CONFLICT 吃掉。
func (w *Watcher) buildSignal(b Breach, starts time.Time) model.Signal {
	fingerprint := fmt.Sprintf("slo-%s-%s-%s-%s",
		w.tenantID, w.clusterID, b.SLI.Name, b.Tier.Name)
	labels := map[string]string{
		"alertname": "SLOBurnRateHigh",
		"severity":  b.Tier.Severity,
		"namespace": b.SLI.Namespace,
		"slo":       b.SLI.Name,
		"slo_tier":  b.Tier.Name,
		// burn_rate / objective 进标签,使值班人员在信号层面就能判断严重性,
		// 不必回查 SLO 定义。
		"burn_rate":   fmt.Sprintf("%.1f", b.Tier.BurnRate),
		"objective":   fmt.Sprintf("%.4f", b.SLI.Objective),
		"long_window": promDuration(b.Tier.LongWindow),
		"detector":    SignalSource,
	}
	if b.SLI.Service != "" {
		// 用 service 而非 deployment:SLO 是服务级的,而 ClassifyFault 与
		// resourceFromAlertLabels 都认这个标签。
		labels["service"] = b.SLI.Service
	}
	// 本路径不经 HTTP ingress,故自行推导 signal_id。用与 webhook 路径**同一个**
	// model.DeriveSignalID:两条路径必须共用一套幂等规则,否则合成信号的去重行为
	// 会与告警信号不一致,而这种不一致只会在生产的重复数据里显现。
	sigID := model.DeriveSignalID(model.SignalIdentity{
		Fingerprint: fingerprint,
		Status:      "firing",
		StartsAt:    starts,
	})
	return model.Signal{
		SignalID:   sigID,
		TenantID:   w.tenantID,
		ClusterID:  w.clusterID,
		Source:     SignalSource,
		SignalType: "alert",
		Severity:   b.Tier.Severity,
		ResourceRef: model.ResourceRef{
			Namespace: b.SLI.Namespace,
			Kind:      "Service",
			Name:      b.SLI.Service,
		},
		Labels:     labels,
		StartsAt:   &starts,
		ReceivedAt: time.Now().UTC(),
		// PayloadHash 是表上的可空列,但填上便于排查(同一片段内应恒定)。
		PayloadHash: "slo:" + fingerprint,
		PayloadRef:  fingerprint,
	}
}
