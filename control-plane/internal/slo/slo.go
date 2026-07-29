// Package slo 实现主动异常检测:SLO 错误预算燃尽率监视。
//
// 此前系统完全是**被动**的 —— 只在告警流入时才有反应。没有告警规则覆盖的退化
// (错误率从 0.05% 缓慢爬到 0.4%,不触发任何静态阈值)就完全看不见,
// 直到它变成用户投诉。
//
// # 为什么选燃尽率而不是统计异常检测
//
// 统计异常检测(3σ / 时序分解 / 孤立森林)听起来更"智能",但对这个系统是错的选择:
//
//   - **不可解释。** 它能说"这个指标偏离基线 4.2σ",但说不出"所以呢"。
//     而本系统的产出是给人看的诊断结论,一个无法解释的触发理由会污染整条推理链。
//   - **误报率高且难调。** 季节性、发布窗口、流量自然波动都会触发。误报会训练
//     值班人员忽略告警 —— 那比没有检测更糟。
//   - **与用户影响脱钩。** 偏离基线不等于用户受损;而 SLO 燃尽率直接度量
//     "错误预算消耗得多快",天然按用户影响排序。
//
// 燃尽率的三个优点正好相反:可解释(消耗了 2% 的月度预算)、阈值有业界共识
// (SRE workbook 表 5-8)、直接对应用户影响。
//
// # 多窗口
//
// 单窗口燃尽率有个已知问题:错误停止后长窗口的均值要过整个窗口才降下来,
// 告警会持续 fire 一小时。SRE workbook 的做法是加一个**短窗口**(长窗的 1/12)
// 作为"仍在燃烧"的确认 —— 两个窗口都超阈值才触发,错误一停短窗立刻降下来。
//
// 参数取自 workbook 表 5-8(99.9% SLO):
//
//	严重度   长窗    短窗     燃尽率   预算消耗
//	page    1h     5m      14.4    2%
//	page    6h     30m     6       5%
//	ticket  3d     6h      1       10%
//
// # 输出方式
//
// 检测到燃尽后**合成一条 signal 走既有入口**,而不是直接建 incident。
// 这样它自动获得两层聚合、触发策略、幂等去重、审计 —— 一条新路径会把这些全绕过。
package slo

import (
	"fmt"
	"strings"
	"time"
)

// Tier 一个燃尽率档位。
type Tier struct {
	Name        string        // 档位名(进 signal 标签,便于人工识别)
	BurnRate    float64       // 燃尽率倍数
	LongWindow  time.Duration // 长窗口
	ShortWindow time.Duration // 短窗口(约为长窗的 1/12)
	Severity    string        // 映射到本系统的严重度
}

// DefaultTiers 返回 SRE workbook 表 5-8 推荐的档位。
//
// 严重度映射:14.4×(2% 预算/1 小时)是明确的"立刻处理" → P1;
// 6×(5%/6 小时)仍是 page 但缓一些 → P2;1×(10%/3 天)是 ticket → P3。
// 刻意不产出 P4:燃尽率能触发说明用户已经在受损,那不该是"无关紧要"。
func DefaultTiers() []Tier {
	return []Tier{
		{Name: "fast", BurnRate: 14.4, LongWindow: time.Hour, ShortWindow: 5 * time.Minute, Severity: "critical"},
		{Name: "medium", BurnRate: 6, LongWindow: 6 * time.Hour, ShortWindow: 30 * time.Minute, Severity: "error"},
		{Name: "slow", BurnRate: 1, LongWindow: 72 * time.Hour, ShortWindow: 6 * time.Hour, Severity: "warning"},
	}
}

// SLI 一个被监视的服务等级指标。
//
// ErrorRatioExpr 必须是一个**比率**表达式(0..1),且**必须**含 $WINDOW 占位符 ——
// 监视器会把它替换成各档位的窗口长度。用占位符而非拼接字符串:表达式里可能有多处
// 需要同一窗口(分子分母各一处),手工拼接容易只改一处而静默算错。
type SLI struct {
	Name           string  `json:"name"`
	Namespace      string  `json:"namespace"`
	Service        string  `json:"service"`
	Objective      float64 `json:"objective"` // 目标可用性,如 0.999
	ErrorRatioExpr string  `json:"error_ratio_expr"`
}

// Validate 校验 SLI 定义。配置错误必须在启动时暴露 ——
// 运行期才发现意味着这段时间里 SLO 监视静默无效。
func (s SLI) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("SLI 缺少 name")
	}
	if s.Objective <= 0 || s.Objective >= 1 {
		return fmt.Errorf("SLI %q 的 objective 必须在 (0,1) 区间,得到 %v", s.Name, s.Objective)
	}
	expr := strings.TrimSpace(s.ErrorRatioExpr)
	if expr == "" {
		return fmt.Errorf("SLI %q 缺少 error_ratio_expr", s.Name)
	}
	if !strings.Contains(expr, WindowPlaceholder) {
		return fmt.Errorf("SLI %q 的 error_ratio_expr 必须含 %s 占位符(多窗口燃尽率需要它)",
			s.Name, WindowPlaceholder)
	}
	return nil
}

// WindowPlaceholder 在 SLI 表达式里代表窗口长度。
const WindowPlaceholder = "$WINDOW"

// ErrorBudget 返回该 SLI 的错误预算(1 - objective)。
func (s SLI) ErrorBudget() float64 { return 1 - s.Objective }

// exprFor 把占位符替换为具体窗口。
func (s SLI) exprFor(w time.Duration) string {
	return strings.ReplaceAll(s.ErrorRatioExpr, WindowPlaceholder, promDuration(w))
}

// promDuration 把 Duration 格式化为 PromQL 时长字面量。
//
// 不用 Duration.String():它对 72h 会输出 "72h0m0s",PromQL 解析不了。
// 也不简单地用小时数:5m 与 30m 必须保持分钟单位。
func promDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}

// Breach 一次燃尽率越限。
type Breach struct {
	SLI       SLI
	Tier      Tier
	LongRate  float64 // 长窗口实测错误率
	ShortRate float64 // 短窗口实测错误率
	Threshold float64 // 触发阈值 = BurnRate × ErrorBudget
}

// BudgetConsumedPct 返回该档位触发时已消耗的预算比例(用于人类可读的说明)。
//
// 推导:燃尽率 R 表示以 R 倍于"刚好耗尽预算"的速度消耗。在长窗口 W 内、
// SLO 周期为 P 时,消耗比例 = R × W / P。取 P=30 天(月度预算,业界惯例)。
func (b Breach) BudgetConsumedPct() float64 {
	const periodHours = 30 * 24
	return b.Tier.BurnRate * b.Tier.LongWindow.Hours() / periodHours * 100
}

// Describe 生成人类可读的越限说明,进 signal 的标签与标题。
//
// 说明里带上"消耗了多少预算"而不只是倍数:后者对不熟悉 SLO 的值班人员没有意义,
// 而"1 小时消耗了 2% 的月度预算"是可以直接判断严重性的。
func (b Breach) Describe() string {
	return fmt.Sprintf(
		"%s 错误预算燃尽率 %.1f×(长窗 %s 实测 %.3f%%,短窗 %s 实测 %.3f%%,阈值 %.3f%%);"+
			"按此速度约消耗月度预算的 %.1f%%",
		b.SLI.Name, b.Tier.BurnRate,
		promDuration(b.Tier.LongWindow), b.LongRate*100,
		promDuration(b.Tier.ShortWindow), b.ShortRate*100,
		b.Threshold*100, b.BudgetConsumedPct())
}
