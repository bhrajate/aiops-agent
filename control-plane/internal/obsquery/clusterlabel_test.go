package obsquery

import (
	"strings"
	"testing"
)

// TestValidate_RejectsDotsForPromLoki 是本改动最核心的一条:
// 点号在 PromQL/LogQL 里是**语法错误**,不是"查不到数据"。此前共用一个
// AIOPS_CLUSTER_LABEL 时,为了让 Tempo 能用 k8s.cluster.name,就会把点号
// 带给 Prometheus/Loki,导致每次查询都语法错。现在启动时即拦下。
func TestValidate_RejectsDotsForPromLoki(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels ClusterLabels
		wantIn string
	}{
		{"prometheus 带点号", ClusterLabels{Prometheus: "k8s.cluster.name"}, "AIOPS_PROM_CLUSTER_LABEL"},
		{"loki 带点号", ClusterLabels{Loki: "k8s.cluster.name"}, "AIOPS_LOKI_CLUSTER_LABEL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.labels.Validate()
			if err == nil {
				t.Fatal("带点号的 label 应被拒绝")
			}
			msg := err.Error()
			// 错误信息必须指名**具体哪个环境变量**——运维看到的第一手信息就是这条。
			if !strings.Contains(msg, tc.wantIn) {
				t.Errorf("错误应指名环境变量 %s: %s", tc.wantIn, msg)
			}
			// 且必须给出可操作的改法(点号转下划线)。
			if !strings.Contains(msg, "k8s_cluster_name") {
				t.Errorf("错误应给出改法 k8s_cluster_name: %s", msg)
			}
		})
	}
}

// TestValidate_AcceptsTempoDotted 验证 Tempo 允许点分段(OTel 语义约定)。
func TestValidate_AcceptsTempoDotted(t *testing.T) {
	if err := (ClusterLabels{Tempo: "k8s.cluster.name"}).Validate(); err != nil {
		t.Errorf("Tempo 应允许点分段: %v", err)
	}
}

// TestValidate_ThreeBackendsSimultaneously 验证三后端各用自己惯例的组合合法。
// 这正是改动前不可能的配置。
func TestValidate_ThreeBackendsSimultaneously(t *testing.T) {
	cl := ClusterLabels{
		Prometheus: "cluster",
		Loki:       "k8s_cluster_name",
		Tempo:      "k8s.cluster.name",
	}
	if err := cl.Validate(); err != nil {
		t.Errorf("三后端各用自己的惯例应合法: %v", err)
	}
}

// TestValidate_EmptyIsLegal 空值表示不强制该后端(单集群专用后端的合法配置)。
func TestValidate_EmptyIsLegal(t *testing.T) {
	if err := (ClusterLabels{}).Validate(); err != nil {
		t.Errorf("全空应合法(不强制): %v", err)
	}
	if err := (ClusterLabels{Prometheus: "cluster"}).Validate(); err != nil {
		t.Errorf("部分配置应合法: %v", err)
	}
}

// TestValidate_RejectsMalformed 覆盖其他非法形态,含注入尝试——
// label 名会被拼进查询表达式,必须挡住语法字符。
func TestValidate_RejectsMalformed(t *testing.T) {
	bad := []ClusterLabels{
		{Prometheus: "1cluster"},         // 数字开头
		{Prometheus: "my-cluster"},       // 连字符
		{Prometheus: "my cluster"},       // 空格
		{Prometheus: `cluster"} or up{`}, // 注入尝试
		{Loki: "cluster,namespace"},      // 逗号
		{Tempo: "k8s..cluster"},          // 空段
		{Tempo: "k8s.cluster."},          // 尾点
		{Tempo: ".k8s.cluster"},          // 首点
		{Tempo: `k8s.cluster" x="y`},     // 注入尝试
	}
	for _, cl := range bad {
		if err := cl.Validate(); err == nil {
			t.Errorf("非法 label 应被拒绝: %+v", cl)
		}
	}
}

// TestResolve 验证空字段回落到全局值、已设字段不被覆盖。
func TestResolve(t *testing.T) {
	got := ClusterLabels{Tempo: "k8s.cluster.name"}.Resolve("cluster")
	if got.Prometheus != "cluster" || got.Loki != "cluster" {
		t.Errorf("空字段应回落到全局值: %+v", got)
	}
	if got.Tempo != "k8s.cluster.name" {
		t.Errorf("已设字段不应被全局值覆盖: %q", got.Tempo)
	}
}

// TestResolve_EmptyGlobalKeepsUnenforced 全局值为空时不补内置默认值:
// 显式留空即"不强制该后端",不该被默认值悄悄改成强制。
func TestResolve_EmptyGlobalKeepsUnenforced(t *testing.T) {
	got := ClusterLabels{Prometheus: "cluster"}.Resolve("")
	if got.Loki != "" || got.Tempo != "" {
		t.Errorf("全局为空时不应补默认值: %+v", got)
	}
}

// TestEnvVarFor 锁死变量名映射。
// 曾经用字符串拼接 "AIOPS_"+backend+"_CLUSTER_LABEL",对 prometheus 会生成
// 不存在的 AIOPS_PROMETHEUS_CLUSTER_LABEL,让运维去改一个没用的变量。
func TestEnvVarFor(t *testing.T) {
	want := map[string]string{
		BackendPrometheus: "AIOPS_PROM_CLUSTER_LABEL",
		BackendLoki:       "AIOPS_LOKI_CLUSTER_LABEL",
		BackendTempo:      "AIOPS_TEMPO_CLUSTER_LABEL",
	}
	var cl ClusterLabels
	for backend, wantVar := range want {
		if got := cl.EnvVarFor(backend); got != wantVar {
			t.Errorf("EnvVarFor(%s) = %q, want %q", backend, got, wantVar)
		}
	}
	if cl.EnvVarFor("unknown") != "" {
		t.Error("未知后端应返回空")
	}
	// 每个 Unenforced 的后端都必须有对应变量名,否则启动告警会指向不存在的变量。
	for _, b := range cl.Unenforced() {
		if cl.EnvVarFor(b) == "" {
			t.Errorf("后端 %s 无对应环境变量名", b)
		}
	}
}

func TestUnenforcedAndDescribe(t *testing.T) {
	if got := len((ClusterLabels{}).Unenforced()); got != 3 {
		t.Errorf("全空应有 3 个未强制后端, got %d", got)
	}
	full := ClusterLabels{Prometheus: "a", Loki: "b", Tempo: "c"}
	if got := len(full.Unenforced()); got != 0 {
		t.Errorf("全配置应无未强制后端, got %d", got)
	}
	d := (ClusterLabels{Prometheus: "cluster"}).Describe()
	if !strings.Contains(d, "prometheus=cluster") || !strings.Contains(d, "<unenforced>") {
		t.Errorf("Describe 应含各后端与未强制标记: %s", d)
	}
}
