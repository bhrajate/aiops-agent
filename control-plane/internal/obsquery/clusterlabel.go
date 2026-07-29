package obsquery

// 集群维度 label 名——**按后端分别配置**。
//
// 为什么不能只用一个值:三个后端对"集群"的命名法互不兼容。
//
//	| 后端            | 惯例                 | 来源                                          |
//	|-----------------|----------------------|-----------------------------------------------|
//	| Prometheus/Mimir| cluster              | external_labels 注入;Grafana mixin 用         |
//	|                 |                      | per_cluster_label 暴露为可配                   |
//	| Loki (Alloy)    | cluster              | relabel 注入                                   |
//	| Loki (OTLP 原生)| k8s_cluster_name     | OTel resource attribute 提升为索引 label;      |
//	|                 |                      | LogQL label 名须符合 Prometheus 命名法,       |
//	|                 |                      | 故点号转下划线                                 |
//	| Tempo           | k8s.cluster.name     | OTel 语义约定,原生保留点号                     |
//
// 关键在于**点号在 PromQL/LogQL 里是语法错误**,不只是"查不到数据":
//   - 配 cluster       → Tempo 查询静默返回空;
//   - 配 k8s.cluster.name → Prometheus/Loki 每次查询都语法错。
//
// 所以单一配置项无法同时满足三者。业界做法就是按后端可配 + 各自默认值
// (Grafana 自家 mixin 亦然),这里照此实现。
//
// 校验按**两类配错的不同表现**分流:
//   - 语法非法(点号给了 Prometheus/Loki):启动时即可判定 → fail-fast,
//     指名具体环境变量与改法;
//   - 名字合法但不匹配(实际是 cluster_id 却配了 cluster):后端不报错、
//     只静默返回空结果,代码里判定不了 → 只能靠文档要求上线前核对
//     (见 docs/INTEGRATION.md)。

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// 后端标识符,用于按后端取 label 名。
const (
	BackendPrometheus = "prometheus"
	BackendLoki       = "loki"
	BackendTempo      = "tempo"
)

// 各后端默认集群 label 名(见上表)。
const (
	DefaultPromClusterLabel  = "cluster"
	DefaultLokiClusterLabel  = "cluster"
	DefaultTempoClusterLabel = "k8s.cluster.name"
)

// promLabelName 是 Prometheus/LogQL 的 label 名法:[a-zA-Z_][a-zA-Z0-9_]*。
// **不允许点号** —— 点号在 PromQL/LogQL 里会导致解析失败。
var promLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// otelAttrName 是 OTel 属性名法,允许点分段(k8s.cluster.name)。
// 每段须以字母或下划线开头,段内允许字母数字与下划线。
var otelAttrName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`)

// ClusterLabels 按后端保存集群维度 label 名。
// 某后端为空表示**该后端不做集群维度约束**(单集群专用后端的合法配置)。
type ClusterLabels struct {
	Prometheus string
	Loki       string
	Tempo      string
}

// clusterLabelsFromEnv 从环境变量解析,优先级:
//
//	后端专属变量 > AIOPS_CLUSTER_LABEL(全局,向后兼容)> 内置默认值
//
// AIOPS_CLUSTER_LABEL_DISABLED=true 显式关闭集群维度约束。要求**显式表态**而非
// 留空即关闭:留空静默不隔离会让 RCA 读到其他集群同名 namespace 的数据,
// 而这个错误在诊断结论里看不出来——证据齐全、逻辑自洽,只是来自错误的集群。
func clusterLabelsFromEnv() ClusterLabels {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("AIOPS_CLUSTER_LABEL_DISABLED")), "true") {
		return ClusterLabels{}
	}
	global := strings.TrimSpace(os.Getenv("AIOPS_CLUSTER_LABEL"))
	pick := func(envKey, def string) string {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			return v
		}
		if global != "" {
			return global
		}
		return def
	}
	return ClusterLabels{
		Prometheus: pick("AIOPS_PROM_CLUSTER_LABEL", DefaultPromClusterLabel),
		Loki:       pick("AIOPS_LOKI_CLUSTER_LABEL", DefaultLokiClusterLabel),
		Tempo:      pick("AIOPS_TEMPO_CLUSTER_LABEL", DefaultTempoClusterLabel),
	}
}

// Resolve 用全局回落值填充空字段,供直接构造 Config 的调用方(含测试)
// 获得与环境变量路径一致的回落语义。全局值为空时**不**补内置默认值——
// 显式留空即表示"不强制该后端"。
func (c ClusterLabels) Resolve(global string) ClusterLabels {
	global = strings.TrimSpace(global)
	if global == "" {
		return c
	}
	if c.Prometheus == "" {
		c.Prometheus = global
	}
	if c.Loki == "" {
		c.Loki = global
	}
	if c.Tempo == "" {
		c.Tempo = global
	}
	return c
}

// For 返回指定后端的集群 label 名;未知后端返回空(即不强制)。
func (c ClusterLabels) For(backend string) string {
	switch backend {
	case BackendPrometheus:
		return c.Prometheus
	case BackendLoki:
		return c.Loki
	case BackendTempo:
		return c.Tempo
	}
	return ""
}

// EnvVarFor 返回配置该后端 label 名的环境变量名。
// 用显式映射而非字符串拼接:Prometheus 的变量名是缩写 AIOPS_PROM_*,
// 拼接会得到不存在的 AIOPS_PROMETHEUS_*,让运维去改一个没用的变量。
func (c ClusterLabels) EnvVarFor(backend string) string {
	switch backend {
	case BackendPrometheus:
		return "AIOPS_PROM_CLUSTER_LABEL"
	case BackendLoki:
		return "AIOPS_LOKI_CLUSTER_LABEL"
	case BackendTempo:
		return "AIOPS_TEMPO_CLUSTER_LABEL"
	}
	return ""
}

// Validate 按各后端的 label 名法校验。空值合法(表示不强制该后端)。
//
// 只拦**语法非法**;名字合法但与后端实际不符无法在此判定(后端会静默返回空)。
func (c ClusterLabels) Validate() error {
	var problems []string
	check := func(backend, value string, re *regexp.Regexp, rule string) {
		if value == "" {
			return
		}
		if !re.MatchString(value) {
			problems = append(problems, fmt.Sprintf(
				"%s=%q 不是合法的%s(%s)%s",
				c.EnvVarFor(backend), value, rule, re.String(), hintFor(backend, value)))
		}
	}
	check(BackendPrometheus, c.Prometheus, promLabelName, "Prometheus label 名")
	check(BackendLoki, c.Loki, promLabelName, "LogQL label 名")
	check(BackendTempo, c.Tempo, otelAttrName, "OTel 属性名")
	if len(problems) > 0 {
		return fmt.Errorf("集群 label 配置非法:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// hintFor 针对最常见的配错给出具体改法。
func hintFor(backend, value string) string {
	if backend != BackendTempo && strings.Contains(value, ".") {
		return fmt.Sprintf(
			";点号在 PromQL/LogQL 里是语法错误,OTLP 原生 ingest 下应改用 %q",
			strings.ReplaceAll(value, ".", "_"))
	}
	return ""
}

// Unenforced 返回未配置集群维度的后端名,供启动时逐个告警。
func (c ClusterLabels) Unenforced() []string {
	var out []string
	for _, b := range []string{BackendPrometheus, BackendLoki, BackendTempo} {
		if c.For(b) == "" {
			out = append(out, b)
		}
	}
	return out
}

// Describe 生成启动日志用的紧凑描述。
func (c ClusterLabels) Describe() string {
	parts := make([]string, 0, 3)
	for _, b := range []string{BackendPrometheus, BackendLoki, BackendTempo} {
		v := c.For(b)
		if v == "" {
			v = "<unenforced>"
		}
		parts = append(parts, b+"="+v)
	}
	return strings.Join(parts, " ")
}
