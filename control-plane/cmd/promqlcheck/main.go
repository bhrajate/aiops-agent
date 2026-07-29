// promqlcheck 用真实 PromQL 解析器校验渲染后的告警表达式。
//
// 放在控制面模块内而不是独立小工具:它需要 prometheus/promql/parser,
// 而该依赖本模块已经有(obsquery 的 label 注入用它)。独立模块要再拉一份
// prometheus,为一个几十行的校验器不值得。镜像只构建 ./cmd/control-plane,
// 所以它不会被打进产物。
//
// 由 scripts/check-alert-rules.sh 调用,不单独使用。
//
// 目视检查抓不到的两类问题:
//  1. 语法错误 —— Prometheus 加载规则时会整组拒绝;
//  2. 引用不存在的 series —— 不报错、规则永不触发,看起来有覆盖实则没有。
//
// 第 2 类靠把表达式里的 metric 名与真实 /metrics 输出对账。
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/prometheus/prometheus/promql/parser"
)

func main() {
	exprs := strings.Split(strings.TrimRight(readAll(os.Args[1]), "\n"), "\x1e")
	known := map[string]bool{}
	for _, n := range strings.Fields(readAll(os.Args[2])) {
		known[n] = true
	}
	// 非本项目导出、由 kube-state-metrics 等提供的 series,单独放行。
	external := map[string]bool{"kube_pod_container_status_ready": true}

	bad := 0
	for _, e := range exprs {
		parts := strings.SplitN(e, "\x1f", 2)
		if len(parts) != 2 {
			continue
		}
		name, expr := parts[0], parts[1]
		ast, err := parser.NewParser(parser.Options{}).ParseExpr(expr)
		if err != nil {
			fmt.Printf("SYNTAX  %s: %v\n", name, err)
			bad++
			continue
		}
		var missing []string
		parser.Inspect(ast, func(n parser.Node, _ []parser.Node) error {
			vs, ok := n.(*parser.VectorSelector)
			if !ok {
				return nil
			}
			m := vs.Name
			if m == "" {
				for _, lm := range vs.LabelMatchers {
					if lm.Name == "__name__" {
						m = lm.Value
					}
				}
			}
			if m == "" || known[m] || external[m] {
				return nil
			}
			missing = append(missing, m)
			return nil
		})
		if len(missing) > 0 {
			sort.Strings(missing)
			fmt.Printf("UNKNOWN %s: 引用了未导出的 series %v(规则会永不触发)\n", name, missing)
			bad++
			continue
		}
		fmt.Printf("OK      %s\n", name)
	}
	if bad > 0 {
		fmt.Printf("\n%d 条表达式有问题\n", bad)
		os.Exit(1)
	}
	fmt.Printf("\n全部 %d 条表达式解析通过且 metric 名均存在\n", len(exprs))
}

func readAll(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return string(b)
}
