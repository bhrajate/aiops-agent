package trigger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/aiops/control-plane/internal/temporalx"
)

// fakeStarter 记录启动调用,可配置失败。
type fakeStarter struct {
	started []string
	failN   int // 前 failN 次调用失败
	calls   int
}

func (f *fakeStarter) Start(_ context.Context, wfID string, _ temporalx.StartArgs) (string, error) {
	f.calls++
	if f.calls <= f.failN {
		return "", fmt.Errorf("temporal unavailable")
	}
	f.started = append(f.started, wfID)
	return "run-" + wfID, nil
}

func (f *fakeStarter) Signal(_ context.Context, _, _ string, _ any) error { return nil }

func TestWorkflowIDStableAndDerivable(t *testing.T) {
	// 孤儿补偿依赖 workflow ID 可从 (incident, version) 稳定推导 → 重启动幂等
	a := workflowID("inc-1", 3)
	b := workflowID("inc-1", 3)
	if a != b {
		t.Fatal("同 incident+version 应推导出相同 workflow ID")
	}
	if a != "investigation/inc-1/3" {
		t.Errorf("workflow ID 格式变化: %q", a)
	}
	if workflowID("inc-1", 4) == a {
		t.Error("不同版本应有不同 workflow ID")
	}
}

func TestReconcilerRetryIsIdempotentByWorkflowID(t *testing.T) {
	// 同一孤儿重复补偿会用相同 workflow ID 启动;Temporal 侧靠 ID 去重,
	// 因此 Reconciler 可以安全重试(此处验证 ID 推导一致)。
	fs := &fakeStarter{}
	for i := 0; i < 3; i++ {
		id := workflowID("inc-x", 1)
		if _, err := fs.Start(context.Background(), id, temporalx.StartArgs{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, got := range fs.started {
		if got != "investigation/inc-x/1" {
			t.Errorf("重试应使用同一 workflow ID, got %q", got)
		}
	}
}

func TestNewReconcilerDefaults(t *testing.T) {
	r := NewReconciler(nil, &fakeStarter{}, "http://internal", 0, testLogger())
	if r.graceSec <= 0 {
		t.Error("graceSec 非法值应回落到默认")
	}
	if r.maxAge <= 0 {
		t.Error("maxAge 应有默认")
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
