package incident

import "testing"

type recordingMetrics struct {
	calls []struct{ severity, category string }
}

func (r *recordingMetrics) IncIncident(severity, category string) {
	r.calls = append(r.calls, struct{ severity, category string }{severity, category})
}

// TestManagerAcceptsMetrics 锁住 Manager 持有指标接口这一事实。
//
// 背景:IncIncident 此前**定义了却从未被任何代码调用**,于是
// aiops_incidents_created_total 这个 series 根本不存在。任何引用它的告警规则
// 都会永不触发 —— Prometheus 对不存在的 series 不报错,看起来有覆盖实则没有。
// 这个缺陷是 scripts/check-alert-rules.sh 拿规则表达式对着**真实 /metrics 输出**
// 对账时抓到的;用代码里的常量名对账等于自己和自己核对,抓不到。
//
// 这里只能验证装配(实际 +1 需要数据库,由 check-alert-rules.sh 端到端覆盖:
// 它断言该 series 出现在真实 /metrics 上)。
func TestManagerAcceptsMetrics(t *testing.T) {
	rec := &recordingMetrics{}
	m := New(nil, 900, rec, nil)
	if m.metrics == nil {
		t.Fatal("Manager 未持有指标接口:IncIncident 会重新变成死代码")
	}
	// 接口可用性:调一次确认签名与记录行为对得上。
	m.metrics.IncIncident("P1", "release_regression")
	if len(rec.calls) != 1 || rec.calls[0].severity != "P1" ||
		rec.calls[0].category != "release_regression" {
		t.Errorf("指标调用未按 (severity, category) 传递: %+v", rec.calls)
	}
}

// TestManagerNilMetricsSafe 指标为 nil 时不应 panic(降级路径)。
func TestManagerNilMetricsSafe(t *testing.T) {
	m := New(nil, 900, nil, nil)
	if m.metrics != nil {
		t.Error("传 nil 时 metrics 应为 nil")
	}
	// HandleSignal 里的调用点有 m.metrics != nil 守卫,此处只确认构造不 panic。
}
