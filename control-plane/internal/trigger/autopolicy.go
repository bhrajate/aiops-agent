package trigger

// 自动触发策略(F7)。
//
// 原问题:`EvaluateAuto` 的四个分支**全部返回 true**,只是产出不同的 reason 字符串。
// 它是一个伪装成策略的常量 —— 名字、注释和 Decision.Trigger 字段都暗示"会拦一些",
// 实际一条也不拦。后果:每个 incident 都消耗一次 triage 模型调用,含 P4 单信号
// (磁盘用量到 80% 这类)。按每集群每天数千告警估算,这是持续的固定成本,
// 而其中相当一部分永远不会有人看诊断结论。
//
// 为什么现在能收紧:调查内部还有第二道闸门
// (`ai-worker/aiops_worker/policy.py:evaluate_deep_rca_policy`),它决定是否从快速
// 分诊进入深度 RCA。所以外层这道闸门的职责不是"决定挖多深",而是
// **"这个 incident 值不值得花一次模型调用"**。两道闸门问的是不同问题,
// 外层一律放行等于放弃了成本控制的第一道防线。
//
// 设计取舍:
//
//  1. **不跳过的一律说清理由,跳过的也一律留痕。** 跳过不等于忽略:incident 仍然
//     入库、仍在前端可见、仍可人工发起调查(手动路径本就不过这道闸门)。
//     跳过会写审计 + 指标,否则"为什么这个故障没有诊断"将无从回答 ——
//     静默丢弃比不拦更糟。
//
//  2. **默认阈值偏保守(只拦最低价值的一类)。** 首版只拦 P4 且单信号且无变更关联。
//     宁可多花一点模型调用,也不要让值班人员发现"系统认为这个不值得看"。
//     阈值全部可配,`AIOPS_AUTO_TRIGGER_ALL=true` 可完整回到旧行为。
//
//  3. **跳过的判断只用已知事实,不做预测。** 严重度、信号数、变更关联都是 incident
//     上的确定字段。不引入"历史上这类告警没人看"这类统计推断 —— 那需要反馈闭环
//     数据支撑,而这个系统的反馈数据目前还没有回流到策略(见 OPTIMIZATION-LOG
//     的结构性空白)。

import (
	"fmt"
	"strings"

	"github.com/aiops/control-plane/internal/model"
)

// AutoPolicyConfig 自动触发阈值。零值不可用,请用 DefaultAutoPolicy()。
type AutoPolicyConfig struct {
	// TriggerAll 为 true 时一律触发(旧行为)。用于回退或对照。
	TriggerAll bool
	// AlwaysSeverities 无条件触发的严重度集合。
	AlwaysSeverities map[string]bool
	// BurstSignalCount 信号数达到此值即视为突发并触发;<=0 关闭该判据。
	BurstSignalCount int
	// TriggerOnChangeCorrelation 故障与近期变更相关时触发。
	// 默认开:变更关联是最容易被自动定位的一类根因,放过它损失最大。
	TriggerOnChangeCorrelation bool
	// SkipSeverities 在不满足上述任一条件时可被跳过的严重度集合。
	// 不在此集合中的严重度即使不满足其他条件也会触发(保守兜底)。
	SkipSeverities map[string]bool
}

// DefaultAutoPolicy 返回默认阈值:
// P1/P2 与变更关联必触发;信号数 >=3 视为突发;仅 P4 且单信号且无变更关联可跳过。
//
// P3 刻意**不**列入可跳过:它是最常见的级别,拦它会显著改变值班人员的预期,
// 且 P3 里混着不少真问题。需要更省时由部署方显式把 P3 加入跳过集合。
func DefaultAutoPolicy() AutoPolicyConfig {
	return AutoPolicyConfig{
		AlwaysSeverities:           map[string]bool{"P1": true, "P2": true},
		BurstSignalCount:           3,
		TriggerOnChangeCorrelation: true,
		SkipSeverities:             map[string]bool{"P4": true},
	}
}

// Decision 增加 Skipped 语义所需的说明。Reason 在两种情形下都非空。
//
// 注:Decision 定义在 policy.go,这里只扩展它的使用约定 ——
// Trigger=false 时 Reason 是**跳过原因**,会被写入审计与指标。

// EvaluateAutoWithConfig 按配置判断是否自动发起调查。
//
// 判据顺序即优先级,先命中先返回,便于审计里看到"因为什么触发"而不是
// "满足了哪些条件的集合"。
func EvaluateAutoWithConfig(inc model.Incident, cfg AutoPolicyConfig) Decision {
	if cfg.TriggerAll {
		return Decision{true, "auto_trigger_all"}
	}

	sev := strings.ToUpper(strings.TrimSpace(inc.Severity))

	// 1) 高严重度:无条件。
	if cfg.AlwaysSeverities[sev] {
		return Decision{true, "severity_" + strings.ToLower(sev)}
	}

	// 2) 变更关联:最容易被自动定位的一类根因,放过它损失最大。
	if cfg.TriggerOnChangeCorrelation &&
		(inc.FaultCategory == "release_regression" || len(inc.ChangeRefs) > 0) {
		return Decision{true, "recent_change_correlation"}
	}

	// 3) 信号突发:异常持续或影响面扩大的近似。
	//    注意 SignalCount 的可信度依赖 F5(重投递去重)——在那之前一条告警重投
	//    三次就会命中这里,把重复投递误判为突发。
	if cfg.BurstSignalCount > 0 && inc.SignalCount >= cfg.BurstSignalCount {
		return Decision{true, "signal_burst"}
	}

	// 4) 影响面已经扩大:即使级别低也值得看。
	//    blast_radius 由两层聚合模型产出,services 已按工作负载归约(F3)。
	if blastExpanded(inc.BlastRadius) {
		return Decision{true, "blast_radius_expanded"}
	}

	// 5) 可跳过集合内且以上均未命中 → 跳过,但留痕。
	if cfg.SkipSeverities[sev] {
		return Decision{false, fmt.Sprintf("low_value_%s_single_signal", strings.ToLower(sev))}
	}

	// 6) 兜底触发。未知/未配置的严重度一律触发 —— 拿不准时宁可多花一次调用,
	//    也不要静默跳过一个可能重要的故障。
	return Decision{true, "default_triage"}
}

// blastExpanded 判断影响面是否已跨多个服务或命名空间。
//
// 值来自 JSONB,经 JSON 解码后数字是 float64;同时容忍 int(内部直接构造的场景)。
func blastExpanded(blast map[string]any) bool {
	for _, k := range []string{"services", "namespaces"} {
		if n, ok := numFromAny(blast[k]); ok && n > 1 {
			return true
		}
	}
	return false
}

func numFromAny(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}
