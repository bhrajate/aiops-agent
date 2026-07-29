package slo

// SLI 定义的配置解析。
//
// 为什么用 JSON 配置而不是硬编码:每个部署的指标名都不一样
// (http_requests_total / istio_requests_total / 自定义 recording rule),
// 硬编码等于只能在一种环境里工作。而 SLI 定义又必须由部署方给出 ——
// 只有他们知道"什么算错误请求"。

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ExampleSLIsJSON 是给部署方参考的配置样例。
//
// 表达式必须是**比率**(0..1)且含 $WINDOW 占位符。这里用最常见的
// http_requests_total 形式;Istio / Envoy / 自定义 recording rule 的写法不同,
// 需按实际指标改。
const ExampleSLIsJSON = `[
  {
    "name": "checkout-availability",
    "namespace": "payment",
    "service": "checkout",
    "objective": 0.999,
    "error_ratio_expr": "sum(rate(http_requests_total{namespace=\"payment\",service=\"checkout\",code=~\"5..\"}[$WINDOW])) / sum(rate(http_requests_total{namespace=\"payment\",service=\"checkout\"}[$WINDOW]))"
  }
]`

// ParseSLIs 解析 SLI 定义(JSON 数组)。
//
// 任一条非法即整体失败,不做"跳过坏的那条"——部分生效会让运维以为都在监视,
// 而实际有一个 SLO 静默没在看。这类"以为有覆盖实则没有"是本项目反复吃过的亏。
func ParseSLIs(raw string) ([]SLI, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []SLI
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("解析 SLI 定义失败(应为 JSON 数组): %w", err)
	}
	seen := map[string]bool{}
	for i, s := range out {
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("第 %d 条 SLI 非法: %w", i+1, err)
		}
		if seen[s.Name] {
			// 重名会让 episodeStart 的键冲突,两个 SLO 互相清掉对方的片段状态,
			// 表现为"燃烧持续但不断产出新 signal"。
			return nil, fmt.Errorf("SLI 名称重复: %q", s.Name)
		}
		seen[s.Name] = true
	}
	return out, nil
}

// LoadSLIs 从环境变量或文件加载。
//
// 支持文件是因为 SLI 表达式很长(含完整 PromQL),塞进环境变量既难读也容易被
// shell 转义弄坏;而 ConfigMap 挂载成文件是 K8s 里的常规做法。
func LoadSLIs(inline, path string) ([]SLI, error) {
	if p := strings.TrimSpace(path); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("读取 SLI 定义文件 %s: %w", p, err)
		}
		return ParseSLIs(string(b))
	}
	return ParseSLIs(inline)
}
