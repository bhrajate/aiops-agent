package slo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseSLIs_AcceptsExample 文档里的样例必须真的能解析 ——
// 一个跑不通的样例比没有样例更糟。
func TestParseSLIs_AcceptsExample(t *testing.T) {
	slis, err := ParseSLIs(ExampleSLIsJSON)
	if err != nil {
		t.Fatalf("样例配置应可解析: %v", err)
	}
	if len(slis) != 1 || slis[0].Name != "checkout-availability" {
		t.Errorf("解析结果不符: %+v", slis)
	}
}

// TestParseSLIs_RequiresWindowPlaceholder 缺占位符必须报错。
//
// 这是最容易犯又最难发现的配置错误:没有 $WINDOW 时表达式里的窗口是写死的,
// 多窗口燃尽率退化成"同一个窗口比两次",两个条件恒同真同假 ——
// 短窗口过滤完全失效,而**表现上一切正常**(告警照常触发,只是不再有防抖)。
func TestParseSLIs_RequiresWindowPlaceholder(t *testing.T) {
	raw := `[{"name":"x","objective":0.999,
	         "error_ratio_expr":"sum(rate(http_requests_total[5m]))"}]`
	_, err := ParseSLIs(raw)
	if err == nil {
		t.Fatal("缺 $WINDOW 占位符必须报错:否则多窗口过滤静默失效")
	}
	if !strings.Contains(err.Error(), WindowPlaceholder) {
		t.Errorf("错误信息应指出缺少什么: %v", err)
	}
}

// TestParseSLIs_ValidatesObjective objective 必须在 (0,1)。
// 写成 99.9(百分数)是常见错误,会让错误预算变成负数、阈值失去意义。
func TestParseSLIs_ValidatesObjective(t *testing.T) {
	for _, bad := range []string{"0", "1", "99.9", "-0.5"} {
		raw := `[{"name":"x","objective":` + bad +
			`,"error_ratio_expr":"rate(x[$WINDOW])"}]`
		if _, err := ParseSLIs(raw); err == nil {
			t.Errorf("objective=%s 应被拒绝", bad)
		}
	}
}

// TestParseSLIs_RejectsDuplicateNames 重名会让 episodeStart 的键冲突,
// 两个 SLO 互相清掉对方的片段状态,表现为"燃烧持续但不断产出新 signal"。
func TestParseSLIs_RejectsDuplicateNames(t *testing.T) {
	raw := `[{"name":"same","objective":0.999,"error_ratio_expr":"rate(a[$WINDOW])"},
	         {"name":"same","objective":0.99,"error_ratio_expr":"rate(b[$WINDOW])"}]`
	if _, err := ParseSLIs(raw); err == nil {
		t.Error("重名应被拒绝")
	}
}

// TestParseSLIs_AllOrNothing 任一条非法即整体失败。
//
// 不做"跳过坏的那条":部分生效会让运维以为都在监视,而实际有一个 SLO 静默没在看。
// 这类"以为有覆盖实则没有"是本项目反复吃过的亏。
func TestParseSLIs_AllOrNothing(t *testing.T) {
	raw := `[{"name":"good","objective":0.999,"error_ratio_expr":"rate(a[$WINDOW])"},
	         {"name":"bad","objective":0.999,"error_ratio_expr":"no placeholder"}]`
	if _, err := ParseSLIs(raw); err == nil {
		t.Error("有一条非法就该整体失败,而不是跳过它")
	}
}

func TestParseSLIs_EmptyIsNotError(t *testing.T) {
	for _, in := range []string{"", "   ", "[]"} {
		slis, err := ParseSLIs(in)
		if err != nil {
			t.Errorf("空配置不该报错(%q): %v", in, err)
		}
		if len(slis) != 0 {
			t.Errorf("空配置应得到 0 条")
		}
	}
}

func TestLoadSLIs_FromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "slis.json")
	if err := os.WriteFile(p, []byte(ExampleSLIsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	slis, err := LoadSLIs("", p)
	if err != nil {
		t.Fatalf("从文件加载失败: %v", err)
	}
	if len(slis) != 1 {
		t.Errorf("应加载 1 条, got %d", len(slis))
	}
	// 文件优先于 inline
	slis2, err := LoadSLIs(`[{"name":"inline","objective":0.99,"error_ratio_expr":"rate(x[$WINDOW])"}]`, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(slis2) != 1 || slis2[0].Name != "checkout-availability" {
		t.Error("指定 path 时应优先用文件")
	}
	if _, err := LoadSLIs("", filepath.Join(dir, "missing.json")); err == nil {
		t.Error("文件不存在应报错,不该静默回落到 inline")
	}
}

// TestExprFor_ReplacesAllOccurrences 分子分母各有一处窗口,必须全部替换。
// 只替换第一处会让分子分母用不同窗口 —— 算出来的比率毫无意义,
// 而它仍然是个数字,不会报错。
func TestExprFor_ReplacesAllOccurrences(t *testing.T) {
	s := testSLI()
	expr := s.exprFor(time.Hour)
	if strings.Contains(expr, WindowPlaceholder) {
		t.Errorf("仍有未替换的占位符: %s", expr)
	}
	if n := strings.Count(expr, "[1h]"); n != 2 {
		t.Errorf("应替换全部 2 处窗口(分子分母),got %d 处: %s", n, expr)
	}
}

// TestPromDuration PromQL 时长字面量格式。
// Duration.String() 对 72h 输出 "72h0m0s",PromQL 解析不了。
func TestPromDuration(t *testing.T) {
	cases := map[time.Duration]string{
		time.Hour:        "1h",
		6 * time.Hour:    "6h",
		72 * time.Hour:   "72h",
		5 * time.Minute:  "5m",
		30 * time.Minute: "30m",
		90 * time.Second: "90s",
	}
	for d, want := range cases {
		if got := promDuration(d); got != want {
			t.Errorf("promDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// TestBudgetConsumedPct 预算消耗比例应与 SRE workbook 表 5-8 对得上:
// 14.4×/1h → 2%,6×/6h → 5%,1×/3d → 10%。
// 这个数字直接进告警说明,错了会误导严重性判断。
func TestBudgetConsumedPct(t *testing.T) {
	want := []float64{2, 5, 10}
	for i, tier := range DefaultTiers() {
		b := Breach{SLI: testSLI(), Tier: tier}
		got := b.BudgetConsumedPct()
		if got < want[i]-0.6 || got > want[i]+0.6 {
			t.Errorf("档位 %s 预算消耗 %.1f%%, want ≈%.0f%%(workbook 表 5-8)",
				tier.Name, got, want[i])
		}
	}
}

// TestBreachDescribe 说明必须含"消耗了多少预算"。
// 只给倍数对不熟悉 SLO 的值班人员没有意义,而"1 小时消耗 2% 月度预算"可直接判断。
func TestBreachDescribe(t *testing.T) {
	b := Breach{SLI: testSLI(), Tier: DefaultTiers()[0],
		LongRate: 0.05, ShortRate: 0.06, Threshold: 0.0144}
	d := b.Describe()
	for _, want := range []string{"checkout", "14.4", "1h", "5m", "预算"} {
		if !strings.Contains(d, want) {
			t.Errorf("说明缺少 %q: %s", want, d)
		}
	}
}
