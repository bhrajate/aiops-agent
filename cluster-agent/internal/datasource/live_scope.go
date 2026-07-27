package datasource

// live_scope.go: scope-enforcement helpers for the HTTP-backed data sources.
//
// The Cluster Agent constrains every call to a single namespace (see
// datasource.go). For Prometheus / Loki this is enforced here: the target
// namespace label is force-injected into every stream/metric selector, any
// caller-supplied expression that references a *different* namespace is
// rejected, and resource/namespace names are validated against a DNS-1123
// character whitelist so they cannot break out of the query syntax.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

// dns1123Subdomain matches a Kubernetes DNS-1123 subdomain (namespaces and
// resource names). It admits only [a-z0-9.-] with alnum ends, which by
// construction contains none of PromQL/LogQL's syntax characters ({}"=~,\ etc.),
// so an injected `namespace="<ns>"` matcher cannot be escaped.
var dns1123Subdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// validateDNS1123 checks that a scope-derived name is a safe DNS-1123 subdomain.
// Empty is allowed (the caller decides whether a name is required) so an
// unscoped resource still works.
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

// scopePromQL enforces label isolation on a PromQL expression at the AST level
// (robust against bare-selector bypass such as `up{namespace="x"} or up`).
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

// injectNamespaceMatchers rewrites a PromQL/LogQL expression so that every
// selector block ({ ... }) carries an exact namespace matcher equal to ns.
//
//   - A block without a namespace matcher gets `namespace="<ns>"` prepended.
//   - A block whose namespace matcher already equals ns is left as-is.
//   - A block that references a *different* namespace, or uses a non-exact
//     namespace operator (!=, =~, !~), is rejected (cross-namespace guard).
//   - An expression with no selector block at all is rejected: it cannot be
//     scoped, so we refuse rather than let it query cluster-wide.
//
// The scan is quote-aware so label values containing { } or , are handled.
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
		// Found a selector block: locate its closing brace, quote-aware.
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

// scopeSelectorInner processes the contents of a single { ... } block,
// enforcing every required label(如 namespace + cluster)。
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

// splitMatchers splits a selector body on top-level commas (ignoring commas
// inside quoted label values).
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

// matcherLabel returns the label name and operator of a single matcher such as
// `namespace="foo"` or `code=~"5.."`.
func matcherLabel(m string) (name, op string) {
	m = strings.TrimSpace(m)
	for _, o := range []string{"=~", "!~", "!=", "="} {
		if idx := strings.Index(m, o); idx > 0 {
			return strings.TrimSpace(m[:idx]), o
		}
	}
	return strings.TrimSpace(m), ""
}

// matcherValue returns the unquoted value of a matcher (best-effort).
func matcherValue(m string) string {
	if idx := strings.Index(m, `"`); idx >= 0 {
		rest := m[idx+1:]
		if end := strings.Index(rest, `"`); end >= 0 {
			return rest[:end]
		}
	}
	return ""
}
