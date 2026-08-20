package api

// 总览聚合的纯函数契约。
//
// 这些用例针对的都是"错了也看起来正常"的地方:数字在那里、也在变,只是答的
// 不是你问的问题。没有断言的话,这类缺陷只能靠值班人员某天发现总览与列表矛盾。

import (
	"testing"
	"time"

	"github.com/aiops/control-plane/internal/auth"
	"github.com/aiops/control-plane/internal/store"
)

func TestSortedPairsSeverityKeepsP1First(t *testing.T) {
	// 级别分布必须按 P1→P4 排,不能按计数降序:
	// P4 有 20 个、P1 有 1 个时,计数序会把 P4 排在最前,而值班台第一眼
	// 要看的是有没有 P1。图例顺序每次刷新还会变(map 键序不定)。
	got := sortedPairs(map[string]int{"P4": 20, "P1": 1, "P3": 5}, severityOrder)
	want := []countPair{{"P1", 1}, {"P3", 5}, {"P4", 20}}
	if len(got) != len(want) {
		t.Fatalf("长度 = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSortedPairsKeepsUnknownKeys(t *testing.T) {
	// 预设顺序外的键(新枚举值 / 脏数据)必须保留。静默丢弃会让总览
	// 少算一部分 incident,而"少了几个"没有任何提示。
	got := sortedPairs(map[string]int{"P1": 1, "SEV0": 3}, severityOrder)
	var sum int
	for _, p := range got {
		sum += p.Count
	}
	if sum != 4 {
		t.Errorf("总数 = %d, want 4;未知级别被丢弃了: %+v", sum, got)
	}
}

func TestSortedPairsStableOnTies(t *testing.T) {
	// 同计数按键名定序 —— 否则同一份数据两次请求返回不同顺序,
	// 前端图表看起来像在自己跳动。
	for i := 0; i < 5; i++ {
		got := sortedPairs(map[string]int{"b": 2, "a": 2, "c": 2}, nil)
		if got[0].Key != "a" || got[1].Key != "b" || got[2].Key != "c" {
			t.Fatalf("同计数未按键名定序: %+v", got)
		}
	}
}

func TestPercentile(t *testing.T) {
	vals := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if p := percentile(vals, 0.95); p != 90 {
		t.Errorf("p95 = %v, want 90", p)
	}
	if p := percentile(vals, 0); p != 10 {
		t.Errorf("p0 = %v, want 10", p)
	}
	// 不改动入参:调用方后面还要用这个切片算别的
	unsorted := []float64{50, 10, 30}
	_ = percentile(unsorted, 0.95)
	if unsorted[0] != 50 {
		t.Errorf("percentile 修改了入参切片: %+v", unsorted)
	}
}

func TestSummarizeQueueEmptyIsOk(t *testing.T) {
	// 空队列时 OldestPendingAge 恒为 0,不能因此判成异常。
	q := summarizeQueue(store.QueueStats{})
	if q.Health != "ok" {
		t.Errorf("空队列 Health = %q, want ok", q.Health)
	}
}

func TestSummarizeQueueStuckByAgeNotCount(t *testing.T) {
	// 关键契约:按**年龄**判卡住,不按条数。
	// 告警风暴下积压几千条但几秒排空是正常的;只积压 3 条却卡了 20 分钟才是故障。
	storm := summarizeQueue(store.QueueStats{
		OutboxPending:          []store.QueueDepth{{Topic: "signals", Count: 5000}},
		OutboxOldestPendingAge: 2 * time.Second,
	})
	if storm.Health != "ok" {
		t.Errorf("风暴但排空快 Health = %q, want ok(按条数判定会在这里误报)", storm.Health)
	}

	stuck := summarizeQueue(store.QueueStats{
		OutboxPending:          []store.QueueDepth{{Topic: "signals", Count: 3}},
		OutboxOldestPendingAge: 20 * time.Minute,
	})
	if stuck.Health != "stuck" {
		t.Errorf("仅 3 条但卡 20 分钟 Health = %q, want stuck(按条数判定会在这里漏报)", stuck.Health)
	}
}

func TestSummarizeQueueDeadIsStuck(t *testing.T) {
	// status='dead' 表示重试耗尽、已放弃投递 —— 无论年龄都是需要人工处理的状态。
	q := summarizeQueue(store.QueueStats{OutboxDead: 1})
	if q.Health != "stuck" {
		t.Errorf("有 dead 记录 Health = %q, want stuck", q.Health)
	}
}

func TestSummarizeQueueSumsAcrossTopics(t *testing.T) {
	q := summarizeQueue(store.QueueStats{
		OutboxPending: []store.QueueDepth{
			{Topic: "signals", Count: 3},
			{Topic: "incidents", Count: 4},
		},
		DeadLetters: []store.QueueDepth{{Topic: "signals", Count: 2}},
	})
	if q.OutboxPending != 7 {
		t.Errorf("OutboxPending = %d, want 7(跨 topic 求和)", q.OutboxPending)
	}
	if q.DeadLetters != 2 {
		t.Errorf("DeadLetters = %d, want 2", q.DeadLetters)
	}
}

// ---- 趋势桶 ----

func trendPrincipal() (auth.Principal, auth.AgentServiceScope) {
	return auth.Principal{Roles: []string{"sre"}, Clusters: []string{"*"}, Namespaces: []string{"*"}},
		auth.AgentServiceScope{Clusters: []string{"*"}}
}

func TestBuildTrendBucketsCoverWindow(t *testing.T) {
	p, scope := trendPrincipal()
	now := time.Now()
	start := now.Add(-24 * time.Hour)
	got := buildTrend(nil, nil, p, scope, start, now, 24)
	if len(got) != 24 {
		t.Fatalf("桶数 = %d, want 24", len(got))
	}
	// 桶起始时刻必须递增且首桶等于窗口起点 —— x 轴标签依赖它。
	for i := 1; i < len(got); i++ {
		prev, _ := time.Parse(time.RFC3339, got[i-1].Ts)
		cur, _ := time.Parse(time.RFC3339, got[i].Ts)
		if !cur.After(prev) {
			t.Fatalf("桶 %d 的时刻未递增: %s -> %s", i, got[i-1].Ts, got[i].Ts)
		}
	}
}

func TestBuildTrendCountsNewAndResolved(t *testing.T) {
	p, scope := trendPrincipal()
	now := time.Now()
	start := now.Add(-24 * time.Hour)
	resolved := now.Add(-30 * time.Minute)
	rows := []store.IncidentStatRow{
		{ClusterID: "c1", FirstSeen: now.Add(-3 * time.Hour), LastSeen: now},
		{ClusterID: "c1", FirstSeen: now.Add(-2 * time.Hour), ResolvedAt: &resolved, LastSeen: now},
	}
	invs := []store.InvestigationStatRow{{ClusterID: "c1", StartedAt: now.Add(-time.Hour)}}

	got := buildTrend(rows, invs, p, scope, start, now, 24)
	var newSum, resSum, invSum int
	for _, b := range got {
		newSum += b.New
		resSum += b.Resolved
		invSum += b.Investigations
	}
	if newSum != 2 {
		t.Errorf("new 总数 = %d, want 2", newSum)
	}
	if resSum != 1 {
		t.Errorf("resolved 总数 = %d, want 1", resSum)
	}
	if invSum != 1 {
		t.Errorf("investigations 总数 = %d, want 1", invSum)
	}
}

func TestBuildTrendKeepsJustNowInLastBucket(t *testing.T) {
	// 刚发生的事件最该被看见。now 落在 windowStart+24*step 的边界上,
	// 整数除法会算出索引 24(越界),必须归入末桶而不是丢弃。
	p, scope := trendPrincipal()
	now := time.Now()
	start := now.Add(-24 * time.Hour)
	rows := []store.IncidentStatRow{{ClusterID: "c1", FirstSeen: now, LastSeen: now}}

	got := buildTrend(rows, nil, p, scope, start, now, 24)
	if got[len(got)-1].New != 1 {
		var sum int
		for _, b := range got {
			sum += b.New
		}
		t.Errorf("末桶 new = %d(总计 %d), want 1 —— 刚发生的 incident 被丢弃了",
			got[len(got)-1].New, sum)
	}
}

func TestBuildTrendDropsOutOfScope(t *testing.T) {
	// ABAC 必须在趋势里同样生效:否则曲线的量级会超过用户在列表里能看到的数量,
	// 两个视图对不上。
	bob := auth.Principal{Roles: []string{"oncall"}, Clusters: []string{"c1"},
		Namespaces: []string{"payment"}}
	scope := auth.AgentServiceScope{Clusters: []string{"*"}}
	now := time.Now()
	start := now.Add(-24 * time.Hour)
	rows := []store.IncidentStatRow{
		{ClusterID: "c1", Namespace: "payment", FirstSeen: now.Add(-time.Hour), LastSeen: now},
		{ClusterID: "c1", Namespace: "inventory", FirstSeen: now.Add(-time.Hour), LastSeen: now},
		{ClusterID: "c2", Namespace: "payment", FirstSeen: now.Add(-time.Hour), LastSeen: now},
	}
	got := buildTrend(rows, nil, bob, scope, start, now, 24)
	var sum int
	for _, b := range got {
		sum += b.New
	}
	if sum != 1 {
		t.Errorf("new 总数 = %d, want 1(越权行未被过滤)", sum)
	}
}

func TestTerminalPhasesMatchesIsTerminal(t *testing.T) {
	// overview 的 terminalPhases 与 SSE 的 isTerminal 必须一致。
	// 两处漂移会让"活跃调查数"与"SSE 是否还在推流"互相矛盾:
	// 面板说有 1 个在跑,详情页却已经收到 done 事件。
	all := []string{"queued", "triaging", "triage_published", "planning", "collecting",
		"synthesizing", "concluded", "needs_human", "waiting_feedback", "closed", "cancelled"}
	for _, ph := range all {
		if terminalPhases[ph] != isTerminal(ph) {
			t.Errorf("阶段 %q:terminalPhases=%v, isTerminal=%v —— 两处判定漂移了",
				ph, terminalPhases[ph], isTerminal(ph))
		}
	}
}
