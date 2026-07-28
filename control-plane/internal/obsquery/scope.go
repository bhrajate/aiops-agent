package obsquery

// live_scope.go:基于 HTTP 的数据源所使用的 scope 强制辅助函数。
//
// Cluster Agent 把每次调用都约束在单一命名空间内(见 datasource.go)。对
// Prometheus / Loki 而言,这一约束在此处落实:把目标 namespace label 强制注入
// 每个 stream/metric 选择器;调用方传入的表达式若引用了**其他** namespace 一律
// 拒绝;资源名与命名空间名按 DNS-1123 字符白名单校验,使其无法突破查询语法。

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

// dns1123Subdomain 匹配 Kubernetes DNS-1123 子域名(命名空间与资源名)。它只允许
// [a-z0-9.-] 且首尾为字母数字,这样从构造上就不含任何 PromQL/LogQL 语法字符
// ({}"=~,\ 等),因此注入的 `namespace="<ns>"` 匹配器无法被转义突破。
var dns1123Subdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// validateDNS1123 校验由 scope 推导出的名字是否为安全的 DNS-1123 子域名。
// 允许为空(是否必须有名字由调用方决定),这样未限定资源的场景仍可工作。
func validateDNS1123(kind, name string) error {
	if name == "" {
		return nil
	}
	if len(name) > 253 || !dns1123Subdomain.MatchString(name) {
		return fmt.Errorf("%s %q 不是合法的 DNS-1123 名称", kind, name)
	}
	return nil
}

// ScopeLabel 一个必须被强制的 label 约束(如 namespace="payment"、cluster="prod-cn-1")。
type ScopeLabel struct {
	Name  string
	Value string
}

// scopePromQL 在 AST 层面对 PromQL 表达式强制 label 隔离(可抵御
// `up{namespace="x"} or up` 这类裸选择器绕过)。
//
// 对 required 中每个 label:每个 VectorSelector 若未携带则注入 `name="value"`;
// 若已携带但值不同或非精确匹配(!=/=~/!~),则拒绝整条查询。
//
// **共享观测后端必须同时约束 cluster 与 namespace**:多集群共用一套
// Prometheus/Loki/Tempo 时,只按 namespace 过滤会读到其他集群的同名 namespace。
func scopePromQL(expr string, required ...ScopeLabel) (string, error) {
	e, err := parser.NewParser(parser.Options{}).ParseExpr(expr)
	if err != nil {
		return "", fmt.Errorf("查询表达式非法 PromQL: %w", err)
	}
	var vErr error
	parser.Inspect(e, func(node parser.Node, _ []parser.Node) error {
		vs, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		for _, req := range required {
			if req.Name == "" || req.Value == "" {
				continue // 未配置该维度(如集群 label 名为空)则不强制
			}
			found := false
			for _, m := range vs.LabelMatchers {
				if m.Name != req.Name {
					continue
				}
				found = true
				if m.Type != labels.MatchEqual || m.Value != req.Value {
					vErr = fmt.Errorf("查询越出授权范围(仅允许 %s=%q)", req.Name, req.Value)
				}
				break
			}
			if !found {
				vs.LabelMatchers = append(vs.LabelMatchers,
					&labels.Matcher{Type: labels.MatchEqual, Name: req.Name, Value: req.Value})
			}
		}
		return nil
	})
	if vErr != nil {
		return "", vErr
	}
	return e.String(), nil
}

// injectNamespaceMatchers 重写 PromQL/LogQL 表达式,使每个选择器块({ ... })都带上
// 值等于 ns 的精确 namespace 匹配器。
//
//   - 不含 namespace 匹配器的块,前置插入 `namespace="<ns>"`。
//   - namespace 匹配器已等于 ns 的块保持原样。
//   - 引用了**其他** namespace,或使用非精确 namespace 操作符(!=、=~、!~)的块,
//     一律拒绝(跨命名空间守卫)。
//   - 完全不含选择器块的表达式也拒绝:它无法被限定范围,与其放它做全集群查询,
//     不如直接拒掉。
//
// 扫描过程感知引号,因此 label 值中含 { } 或 , 也能正确处理。
func injectNamespaceMatchers(expr string, required ...ScopeLabel) (string, error) {
	var out strings.Builder
	blocks := 0
	i := 0
	n := len(expr)
	for i < n {
		c := expr[i]
		if c != '{' {
			out.WriteByte(c)
			i++
			continue
		}
		// 找到一个选择器块:感知引号地定位其右花括号。
		j := i + 1
		inQuote := false
		for j < n {
			ch := expr[j]
			if ch == '\\' && inQuote {
				j += 2
				continue
			}
			if ch == '"' {
				inQuote = !inQuote
			} else if ch == '}' && !inQuote {
				break
			}
			j++
		}
		if j >= n {
			return "", fmt.Errorf("查询表达式括号不匹配")
		}
		inner := expr[i+1 : j]
		rewritten, err := scopeSelectorInner(inner, required...)
		if err != nil {
			return "", err
		}
		out.WriteByte('{')
		out.WriteString(rewritten)
		out.WriteByte('}')
		blocks++
		i = j + 1
	}
	if blocks == 0 {
		return "", fmt.Errorf("查询表达式缺少 { } 选择器,无法按 namespace 限定")
	}
	return out.String(), nil
}

// scopeSelectorInner 处理单个 { ... } 块的内容,强制其中每个必需的
// label(如 namespace + cluster)。
func scopeSelectorInner(inner string, required ...ScopeLabel) (string, error) {
	matchers := splitMatchers(inner)
	var toPrepend []string
	for _, req := range required {
		if req.Name == "" || req.Value == "" {
			continue // 未配置该维度则不强制
		}
		found := false
		for _, m := range matchers {
			name, op := matcherLabel(m)
			if name != req.Name {
				continue
			}
			found = true
			if op != "=" || matcherValue(m) != req.Value {
				return "", fmt.Errorf("查询越出授权范围(仅允许 %s=%q)", req.Name, req.Value)
			}
			break
		}
		if !found {
			toPrepend = append(toPrepend, fmt.Sprintf("%s=%q", req.Name, req.Value))
		}
	}
	if len(toPrepend) == 0 {
		return inner, nil
	}
	prefix := strings.Join(toPrepend, ",")
	if strings.TrimSpace(inner) == "" {
		return prefix, nil
	}
	return prefix + "," + inner, nil
}

// splitMatchers 按顶层逗号切分选择器主体(忽略带引号 label 值内部的逗号)。
func splitMatchers(inner string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c == '\\' && inQuote {
			cur.WriteByte(c)
			if i+1 < len(inner) {
				cur.WriteByte(inner[i+1])
				i++
			}
			continue
		}
		if c == '"' {
			inQuote = !inQuote
		}
		if c == ',' && !inQuote {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if strings.TrimSpace(cur.String()) != "" {
		parts = append(parts, cur.String())
	}
	return parts
}

// matcherLabel 返回单个匹配器的 label 名与操作符,例如 `namespace="foo"`
// 或 `code=~"5.."`。
func matcherLabel(m string) (name, op string) {
	m = strings.TrimSpace(m)
	for _, o := range []string{"=~", "!~", "!=", "="} {
		if idx := strings.Index(m, o); idx > 0 {
			return strings.TrimSpace(m[:idx]), o
		}
	}
	return strings.TrimSpace(m), ""
}

// matcherValue 返回匹配器去引号后的值(尽力而为)。
func matcherValue(m string) string {
	if idx := strings.Index(m, `"`); idx >= 0 {
		rest := m[idx+1:]
		if end := strings.Index(rest, `"`); end >= 0 {
			return rest[:end]
		}
	}
	return ""
}
