package api

// 值班总览端点 GET /v1/overview?hours=24
//
// 一个请求返回整块值班台首屏:未闭环计数、级别/状态/类别分布、调查阶段分布、
// 处置耗时、24h 趋势、反馈动作分布、证据类型分布、队列健康。
//
// 为什么合成一个端点而不是拆八个:首屏八个并发请求里任意一个失败,页面会呈现
// **部分真实**的状态 —— 比如"P1: 2"是真的但"进行中调查: 0"是加载失败的默认值,
// 而值班人员没法分辨哪个数字是坏的。一个端点要么整块成功要么整块报错。
//
// 聚合口径:ABAC 逐行过滤后累加,与 GET /v1/incidents 用同一个 EffectiveAccess。
// 若两处不一致,总览说有 3 个 P1、点进列表只有 1 个 —— 这种矛盾会让人不再信任面板。

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/aiops/control-plane/internal/auth"
	"github.com/aiops/control-plane/internal/httpx"
	"github.com/aiops/control-plane/internal/store"
)

// 非终态调查阶段(与 store.ListInvestigations / trigger 的口径保持一致)。
var terminalPhases = map[string]bool{
	"closed": true, "cancelled": true, "concluded": true,
	"needs_human": true, "triage_published": true,
}

// countPair 是 {key, count} 的通用形状,前端按数组顺序直接渲染。
//
// 用数组而不是 map:map 的键序在 JSON 里不保证,前端每次刷新柱状图顺序都会变,
// 看起来像数据在跳。数组由后端定序(计数降序),渲染稳定。
type countPair struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// trendBucket 是趋势图的一个时间桶。
type trendBucket struct {
	// Ts 是桶的**起始**时刻(RFC3339)。前端按它做 x 轴标签。
	Ts string `json:"ts"`
	// New 是该桶内首次出现的 incident 数(first_seen 落在桶内)。
	New int `json:"new"`
	// Resolved 是该桶内被解决或关闭的数量。
	Resolved int `json:"resolved"`
	// Investigations 是该桶内启动的调查数。
	Investigations int `json:"investigations"`
}

type overviewResponse struct {
	WindowHours int `json:"window_hours"`
	GeneratedAt string `json:"generated_at"`

	// ── 未闭环现状(不受时间窗约束)──
	// 一个 3 天前爆发、至今没人处理的 P1 必须出现在这里。
	OpenTotal        int `json:"open_total"`
	OpenP1           int `json:"open_p1"`
	OpenP2           int `json:"open_p2"`
	Unacknowledged   int `json:"unacknowledged"`
	ActiveInvestigat int `json:"active_investigations"`
	// StalledInvestigations 是活跃但已超出预算时长的调查数。
	// 这是"卡住"的主信号:阶段没变、事件不再增长,但状态仍是 collecting。
	StalledInvestigations int `json:"stalled_investigations"`

	// ── 窗口内分布 ──
	BySeverity      []countPair `json:"by_severity"`
	ByStatus        []countPair `json:"by_status"`
	ByFaultCategory []countPair `json:"by_fault_category"`
	ByPhase         []countPair `json:"by_phase"`
	ByDiagnosis     []countPair `json:"by_diagnosis"`
	ByEvidenceType  []countPair `json:"by_evidence_type"`
	ByFeedback      []countPair `json:"by_feedback"`

	// ── 窗口内量级与成本 ──
	IncidentsInWindow     int     `json:"incidents_in_window"`
	InvestigationsStarted int     `json:"investigations_started"`
	SignalsAggregated     int     `json:"signals_aggregated"`
	CostUSD               float64 `json:"cost_usd"`
	Tokens                int     `json:"tokens"`
	ToolCalls             int     `json:"tool_calls"`

	// ── 处置耗时(秒)。样本不足时为 null,不填 0 ──
	// 0 会被读成"秒级解决",而真相是"没有已解决的样本"。
	MTTRSeconds        *float64 `json:"mttr_seconds"`
	MTTRSampleSize     int      `json:"mttr_sample_size"`
	P95InvestigationSec *float64 `json:"p95_investigation_seconds"`
	InvestigationSample int      `json:"investigation_sample_size"`

	Trend []trendBucket `json:"trend"`

	// ── 管道健康 ──
	Queue *queueHealth `json:"queue"`
	// GoldenPending 待审评测用例数(反馈闭环的积压)。
	GoldenPending int64 `json:"golden_pending"`
}

// queueHealth 是投递管道的存量视图。nil 表示查询失败 ——
// 绝不填 0:0 会被读成"队列是空的",恰好掩盖了 outbox 卡死这类静默失败。
type queueHealth struct {
	OutboxPending       int64  `json:"outbox_pending"`
	OutboxDead          int64  `json:"outbox_dead"`
	OldestPendingAgeSec int64  `json:"oldest_pending_age_sec"`
	DeadLetters         int64  `json:"dead_letters"`
	// Health 是给前端的判定结论:ok / lagging / stuck。
	// 在后端判定而不是让前端比较阈值 —— 阈值属于运维语义,
	// 散在前端会与告警规则里的阈值悄悄漂移。
	Health string `json:"health"`
}

// 卡住判定:最老待投递记录的年龄。按条数判定会在告警风暴时误报
// (积压几千条但几秒排空是正常的),在真卡住时漏报(只积压 3 条却卡了 20 分钟)。
const (
	outboxLaggingSec = 60
	outboxStuckSec   = 300
)

func (a *PublicAPI) getOverview(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if !p.Can(auth.ActionReadIncident) {
		httpx.Error(w, http.StatusForbidden, "forbidden", "missing read permission")
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*30 {
		hours = 24 * 30
	}

	ctx := r.Context()
	incRows, err := a.store.IncidentStatRows(ctx, hours)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	invRows, err := a.store.InvestigationStatRows(ctx, hours)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	now := time.Now()
	windowStart := now.Add(-time.Duration(hours) * time.Hour)
	out := overviewResponse{
		WindowHours: hours,
		GeneratedAt: now.UTC().Format(time.RFC3339),
	}

	sevCount := map[string]int{}
	statusCount := map[string]int{}
	faultCount := map[string]int{}
	var mttrSum float64
	var mttrN int

	for _, row := range incRows {
		// ABAC:与 listIncidents 同一口径(用户 ∩ Agent ∩ Incident)
		if !auth.EffectiveAccess(p, a.agentScope, row.ClusterID, row.Namespace) {
			continue
		}
		openish := row.Status == "open" || row.Status == "acknowledged"
		if openish {
			out.OpenTotal++
			switch row.Severity {
			case "P1":
				out.OpenP1++
			case "P2":
				out.OpenP2++
			}
			if row.Status == "open" {
				out.Unacknowledged++
			}
		}
		// 分布与量级只统计窗口内有活动的,避免"未闭环的老 incident"把
		// 窗口分布拉偏 —— 那两个问题分开回答。
		if row.LastSeen.After(windowStart) {
			out.IncidentsInWindow++
			out.SignalsAggregated += row.SignalCount
			sevCount[row.Severity]++
			statusCount[row.Status]++
			if row.FaultCategory != "" {
				faultCount[row.FaultCategory]++
			}
		}
		// MTTR:首次出现 → 解决(优先 resolved_at,退回 closed_at)。
		// 只统计窗口内完成的,否则历史均值会把"今天变快了"这个信号抹平。
		if end := resolutionTime(row); end != nil && end.After(windowStart) {
			mttrSum += end.Sub(row.FirstSeen).Seconds()
			mttrN++
		}
	}

	phaseCount := map[string]int{}
	diagCount := map[string]int{}
	var durations []float64
	for _, row := range invRows {
		if !auth.EffectiveAccess(p, a.agentScope, row.ClusterID, row.Namespace) {
			continue
		}
		active := !terminalPhases[row.Phase]
		if active {
			out.ActiveInvestigat++
			// 卡住:活跃且已运行超过预算上限。用挂钟时间而不是 usage.elapsed_sec ——
			// 后者由 worker 上报,worker 挂了它就不再更新,而那正是要检测的情况。
			if now.Sub(row.StartedAt) > stallThreshold {
				out.StalledInvestigations++
			}
		}
		if row.StartedAt.After(windowStart) {
			out.InvestigationsStarted++
			out.CostUSD += row.CostUSD
			out.Tokens += row.Tokens
			out.ToolCalls += row.ToolCalls
			phaseCount[row.Phase]++
			if row.DiagnosisStatus != "" {
				diagCount[row.DiagnosisStatus]++
			}
			if row.EndedAt != nil {
				durations = append(durations, row.EndedAt.Sub(row.StartedAt).Seconds())
			}
		}
	}

	out.BySeverity = sortedPairs(sevCount, severityOrder)
	out.ByStatus = sortedPairs(statusCount, nil)
	out.ByFaultCategory = sortedPairs(faultCount, nil)
	out.ByPhase = sortedPairs(phaseCount, nil)
	out.ByDiagnosis = sortedPairs(diagCount, nil)

	if mttrN > 0 {
		avg := mttrSum / float64(mttrN)
		out.MTTRSeconds = &avg
	}
	out.MTTRSampleSize = mttrN
	if len(durations) > 0 {
		p95 := percentile(durations, 0.95)
		out.P95InvestigationSec = &p95
	}
	out.InvestigationSample = len(durations)

	out.Trend = buildTrend(incRows, invRows, p, a.agentScope, windowStart, now, hours)

	// 以下三项是**增强**,失败不影响主体:总览的核心是 incident/调查现状,
	// 证据分布查不出来不该让整个首屏 500。
	if evs, e := a.store.EvidenceTypeCounts(ctx, hours); e == nil {
		out.ByEvidenceType = pairsFromEvidence(evs)
	}
	if fbs, e := a.store.FeedbackActionCounts(ctx, hours); e == nil {
		out.ByFeedback = pairsFromFeedback(fbs)
	}
	if n, e := a.store.CountGoldenCases(ctx, "pending"); e == nil {
		out.GoldenPending = n
	}
	// 队列健康只给有审计权限的角色(sre/admin):它反映的是平台内部管道状态,
	// 值班人员看了也无从处置,反而会把"outbox 积压"误读成自己负责的故障。
	if p.Can(auth.ActionReadAudit) {
		if qs, e := a.store.QueueStats(ctx); e == nil {
			out.Queue = summarizeQueue(qs)
		}
	}

	httpx.JSON(w, http.StatusOK, out)
}

// resolutionTime 返回 incident 的闭环时刻:优先 resolved_at,退回 closed_at。
// 都为空(仍未闭环)返回 nil。
func resolutionTime(row store.IncidentStatRow) *time.Time {
	if row.ResolvedAt != nil {
		return row.ResolvedAt
	}
	return row.ClosedAt
}

// stallThreshold 是"这次调查跑太久了"的判定线,**刻意是个常量**。
//
// 默认预算是 300s(model.DefaultBudget),这里取 2 倍并向上取到 10 分钟:
// 判定太紧会把正常的长调查标成卡住,而误报几次之后没人再看这个数字。
//
// 为什么不按每次调查自己的 budget.max_duration_sec 算:这条线在三处出现
// (本函数、前端 lib/phase.ts 的 STALL_MS、frontend/README 的说明),
// 改成按调查动态取值就必须让前端也能拿到每行的 budget 并复刻同一套算法。
// 那是"两处实现同一个判定"的经典漂移源 —— 而漂移的表现是总览说 3 个卡住、
// 列表标出 5 个,没有任何报错。常量换来的是三处可以用同一个数字对齐。
//
// 要改成动态,得先把判定挪到后端**唯一**产出(比如在 InvestigationListItem 上
// 加一个 stalled 布尔字段),让前端只渲染不计算。
const stallThreshold = 10 * time.Minute

var severityOrder = []string{"P1", "P2", "P3", "P4"}

// sortedPairs 把计数 map 转成定序数组。
// 给定 order 时按其排列(级别要 P1→P4,不是计数降序);否则按计数降序、同数按键名。
func sortedPairs(m map[string]int, order []string) []countPair {
	out := make([]countPair, 0, len(m))
	if len(order) > 0 {
		for _, k := range order {
			if n, ok := m[k]; ok {
				out = append(out, countPair{Key: k, Count: n})
			}
		}
		// 不在预设顺序里的键(脏数据/新枚举)追加在后,不静默丢弃
		for k, n := range m {
			if !containsStr(order, k) {
				out = append(out, countPair{Key: k, Count: n})
			}
		}
		return out
	}
	for k, n := range m {
		out = append(out, countPair{Key: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func containsStr(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func pairsFromEvidence(in []store.EvidenceTypeCount) []countPair {
	out := make([]countPair, 0, len(in))
	for _, c := range in {
		out = append(out, countPair{Key: c.Type, Count: c.Count})
	}
	return out
}

func pairsFromFeedback(in []store.FeedbackActionCount) []countPair {
	out := make([]countPair, 0, len(in))
	for _, c := range in {
		out = append(out, countPair{Key: c.Action, Count: c.Count})
	}
	return out
}

// percentile 返回排序后样本的分位值(最近秩法)。样本为空时返回 0 ——
// 调用方负责在 len==0 时不使用该值(见 InvestigationSample)。
func percentile(vals []float64, q float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := make([]float64, len(vals))
	copy(s, vals)
	sort.Float64s(s)
	idx := int(float64(len(s)-1) * q)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

// buildTrend 把窗口切成等宽时间桶。
//
// 桶数固定 24 个而不是"每小时一个":窗口可以是 1h 也可以是 30d,
// 前者需要 2.5 分钟粒度,后者需要 30 小时粒度,同一个图形都能读。
func buildTrend(incRows []store.IncidentStatRow, invRows []store.InvestigationStatRow,
	p auth.Principal, agentScope auth.AgentServiceScope,
	windowStart, now time.Time, hours int) []trendBucket {
	const buckets = 24
	span := now.Sub(windowStart)
	if span <= 0 {
		span = time.Duration(hours) * time.Hour
	}
	step := span / buckets
	if step <= 0 {
		step = time.Minute
	}
	out := make([]trendBucket, buckets)
	for i := range out {
		out[i].Ts = windowStart.Add(time.Duration(i) * step).UTC().Format(time.RFC3339)
	}
	idxOf := func(t time.Time) int {
		if t.Before(windowStart) {
			return -1
		}
		i := int(t.Sub(windowStart) / step)
		if i < 0 || i >= buckets {
			// 落在最后一桶边界外(now 与 windowStart+24*step 的舍入差)归入末桶,
			// 不丢弃 —— 刚发生的事件恰好最该被看见。
			if i >= buckets {
				return buckets - 1
			}
			return -1
		}
		return i
	}
	for _, row := range incRows {
		if !auth.EffectiveAccess(p, agentScope, row.ClusterID, row.Namespace) {
			continue
		}
		if i := idxOf(row.FirstSeen); i >= 0 {
			out[i].New++
		}
		if end := resolutionTime(row); end != nil {
			if i := idxOf(*end); i >= 0 {
				out[i].Resolved++
			}
		}
	}
	for _, row := range invRows {
		if !auth.EffectiveAccess(p, agentScope, row.ClusterID, row.Namespace) {
			continue
		}
		if i := idxOf(row.StartedAt); i >= 0 {
			out[i].Investigations++
		}
	}
	return out
}

// summarizeQueue 把 store.QueueStats 压成前端要的形状 + 一个健康判定。
func summarizeQueue(qs store.QueueStats) *queueHealth {
	q := &queueHealth{
		OutboxDead:          qs.OutboxDead,
		OldestPendingAgeSec: int64(qs.OutboxOldestPendingAge.Seconds()),
	}
	for _, d := range qs.OutboxPending {
		q.OutboxPending += d.Count
	}
	for _, d := range qs.DeadLetters {
		q.DeadLetters += d.Count
	}
	switch {
	// 没有待投递时年龄恒为 0,不该因为 age==0 判成 ok 之外的任何状态。
	case q.OutboxPending == 0 && q.DeadLetters == 0 && q.OutboxDead == 0:
		q.Health = "ok"
	case q.OldestPendingAgeSec >= outboxStuckSec || q.OutboxDead > 0:
		q.Health = "stuck"
	case q.OldestPendingAgeSec >= outboxLaggingSec || q.DeadLetters > 0:
		q.Health = "lagging"
	default:
		q.Health = "ok"
	}
	return q
}
