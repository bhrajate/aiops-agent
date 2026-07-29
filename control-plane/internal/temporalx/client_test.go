package temporalx

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiops/control-plane/internal/config"
)

// run timeout 的下限与 Worker 侧的人工反馈等待是一处**跨语言耦合**:
// ai-worker/aiops_worker/workflow.py 的 _FEEDBACK_TIMEOUT 改了而这里没跟着改,
// 会让正在正常等待人工的调查被硬终止,永久停在 waiting_feedback。
// 这类耦合没有编译器守护,所以用断言把它钉住。
func TestMinRunTimeoutExceedsWorkerFeedbackWait(t *testing.T) {
	if MinRunTimeout <= WorkerFeedbackTimeout {
		t.Fatalf("MinRunTimeout(%s) 必须严格大于 WorkerFeedbackTimeout(%s):"+
			"否则正在等待人工的调查会被 run timeout 硬终止",
			MinRunTimeout, WorkerFeedbackTimeout)
	}
	// 反馈超时之后工作流还要走 CLOSED 迁移 + 用量落账(都是带重试的 activity),
	// 余量太小同样会截断收尾。
	if margin := MinRunTimeout - WorkerFeedbackTimeout; margin < time.Hour {
		t.Fatalf("余量 %s 过小:反馈超时后仍需执行 CLOSED 迁移与用量落账", margin)
	}
}

// config 侧做数值比较、temporalx 侧做 Duration 比较,两处必须等价。
// 任何一侧单独改动都会让另一侧失去保护。
func TestConfigMinMatchesTemporalxMin(t *testing.T) {
	if got := time.Duration(config.MinTemporalRunTimeoutSec) * time.Second; got != MinRunTimeout {
		t.Fatalf("config.MinTemporalRunTimeoutSec=%d(=%s)与 temporalx.MinRunTimeout=%s 不一致",
			config.MinTemporalRunTimeoutSec, got, MinRunTimeout)
	}
}

func TestDefaultRunTimeoutIsAcceptable(t *testing.T) {
	if DefaultRunTimeout < MinRunTimeout {
		t.Fatalf("默认值 %s 低于下限 %s —— 不配该变量就会启动失败", DefaultRunTimeout, MinRunTimeout)
	}
}

func TestDialRejectsTooShortRunTimeout(t *testing.T) {
	// 刻意用一个「看起来合理」的值:24h 对一次调查绰绰有余,但小于 48h 反馈等待。
	// 这正是最容易被误配的量级。
	_, err := Dial("127.0.0.1:1", "default", "q", 24*time.Hour)
	if err == nil {
		t.Fatal("期望拒绝 24h run timeout")
	}
	var want ErrRunTimeoutTooShort
	if !errors.As(err, &want) {
		t.Fatalf("期望 ErrRunTimeoutTooShort,得到 %T: %v", err, err)
	}
	if want.Got != 24*time.Hour || want.Min != MinRunTimeout {
		t.Fatalf("错误内容不对: got=%s min=%s", want.Got, want.Min)
	}
	// 错误信息必须说清后果,否则运维只会看到一个数字而不知道该调大还是调小。
	if msg := err.Error(); !strings.Contains(msg, "waiting_feedback") {
		t.Fatalf("错误信息应说明后果(卡在 waiting_feedback),实际: %s", msg)
	}
}

func TestDialRejectsBeforeNetworkDial(t *testing.T) {
	// 校验必须发生在拨号**之前**:否则在 Temporal 可达的环境里,
	// 非法配置会先返回连接错误,被 main 当成「可降级」而只打一条 warn。
	// 127.0.0.1:1 必然连不上,所以若返回的是 ErrRunTimeoutTooShort,
	// 就证明校验在拨号前完成。
	_, err := Dial("127.0.0.1:1", "default", "q", time.Hour)
	var want ErrRunTimeoutTooShort
	if !errors.As(err, &want) {
		t.Fatalf("期望在拨号前就返回 ErrRunTimeoutTooShort,得到 %T: %v", err, err)
	}
}

func TestDialTreatsNonPositiveAsDefault(t *testing.T) {
	// 0 表示「未配置」,应套用默认值而不是被判为过小。
	// 此处必然因连不上 Temporal 而失败,但错误**不应**是 ErrRunTimeoutTooShort。
	for _, v := range []time.Duration{0, -1} {
		_, err := Dial("127.0.0.1:1", "default", "q", v)
		var tooShort ErrRunTimeoutTooShort
		if errors.As(err, &tooShort) {
			t.Fatalf("runTimeout=%s 应回落到默认值,而不是被判过小", v)
		}
	}
}

// 配置类错误必须能被 errors.Is(err, ErrConfig) 认出来。
// main.go 依赖这一点来决定 fail-fast 还是降级 —— 不成立就会静默降级。
func TestRunTimeoutErrorIsConfigClass(t *testing.T) {
	_, err := Dial("127.0.0.1:1", "default", "q", 24*time.Hour)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("期望 errors.Is(err, ErrConfig) 成立,否则 main 会把配置错误当可降级依赖: %v", err)
	}
}

// 反向:连接失败**不能**被误判为配置错误,否则 Temporal 短暂不可用会让控制面
// 拒绝启动,失去可降级设计的意义。
func TestDialErrorIsNotConfigClass(t *testing.T) {
	// 合法 run timeout + 不可达地址 -> 必须是连接错误。
	_, err := Dial("127.0.0.1:1", "default", "q", DefaultRunTimeout)
	if err == nil {
		t.Skip("127.0.0.1:1 意外可连,跳过")
	}
	if errors.Is(err, ErrConfig) {
		t.Fatalf("连接失败被误判为配置错误,会让 Temporal 抖动时拒绝启动: %v", err)
	}
}
