// Package temporalx 封装 Temporal 客户端:启动/信号/取消 Investigation Workflow。
// 控制面只负责可靠地"启动"工作流;推理逻辑在 Python Worker 中执行。
package temporalx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/client"
)

type Client struct {
	c          client.Client
	namespace  string
	taskQueue  string
	runTimeout time.Duration
}

// WorkflowTypeName 跨语言字符串,必须与 Python Worker 注册的名字一致。
const WorkflowTypeName = "InvestigationWorkflow"

// WorkerFeedbackTimeout 是 Python Worker 侧 _FEEDBACK_TIMEOUT 的镜像值
// (ai-worker/aiops_worker/workflow.py)。此处只用于校验 run timeout 的下限,
// 不参与任何调度决策。改动那一侧时必须同步改这里 —— MinRunTimeout 的断言会失败,
// 从而把这处跨语言耦合暴露在测试里,而不是在生产上静默截断人工反馈等待。
const WorkerFeedbackTimeout = 48 * time.Hour

// MinRunTimeout 是允许的最小 run timeout。
//
// 为什么需要下限:run timeout 到点是**硬终止** —— 服务端直接结束 run,工作流
// 代码不会执行 CLOSED 迁移、也不会 flush 用量。若它小于 Worker 的人工反馈等待
// (48h),一条**正在正常等待人工**的调查会被掐掉,库里永久停在
// waiting_feedback,用量永不落账。那比完全不设 run timeout 更糟:当前设计已有
// 三重终止保证(预算、max_rounds、反馈超时),run timeout 只是第四道兜底,
// 用来兜住「连反馈超时都没生效」的异常 run。
//
// 留 12h 余量:反馈超时**之后**工作流还要走 CLOSED 迁移与用量落账,这些都是
// 带重试的 activity。
const MinRunTimeout = WorkerFeedbackTimeout + 12*time.Hour

// DefaultRunTimeout 默认值。取 7 天:足够宽松到不会误伤任何正常调查,
// 又能让异常挂死的 run 最终被服务端回收(否则会长期占用 visibility 记录)。
const DefaultRunTimeout = 7 * 24 * time.Hour

// ErrConfig 标记「配置非法」这一类错误,与「依赖暂时不可达」区分开。
//
// 为什么需要这个区分:Temporal 是**可降级**依赖 —— 连不上时控制面继续跑
// (调查照常落库,只是工作流不启动)。但配置非法不该走降级:那样运维只会看到
// 一条 warn,而工作流永远不会启动,且日志里看不出是配错了。
// 调用方用 errors.Is(err, ErrConfig) 判定是否 fail-fast。
var ErrConfig = errors.New("temporalx: invalid configuration")

// ErrRunTimeoutTooShort run timeout 小于 MinRunTimeout 时返回。
type ErrRunTimeoutTooShort struct {
	Got, Min time.Duration
}

func (e ErrRunTimeoutTooShort) Error() string {
	return fmt.Sprintf(
		"workflow run timeout %s is shorter than the minimum %s "+
			"(worker waits %s for human feedback; a shorter run timeout would hard-terminate "+
			"investigations that are legitimately waiting, leaving them stuck at waiting_feedback)",
		e.Got, e.Min, WorkerFeedbackTimeout)
}

// Unwrap 让 errors.Is(err, ErrConfig) 成立。
func (e ErrRunTimeoutTooShort) Unwrap() error { return ErrConfig }

func Dial(hostPort, namespace, taskQueue string, runTimeout time.Duration) (*Client, error) {
	if runTimeout <= 0 {
		runTimeout = DefaultRunTimeout
	}
	if runTimeout < MinRunTimeout {
		return nil, ErrRunTimeoutTooShort{Got: runTimeout, Min: MinRunTimeout}
	}
	c, err := client.Dial(client.Options{HostPort: hostPort, Namespace: namespace})
	if err != nil {
		return nil, fmt.Errorf("dial temporal: %w", err)
	}
	return &Client{c: c, namespace: namespace, taskQueue: taskQueue, runTimeout: runTimeout}, nil
}

func (t *Client) Close() { t.c.Close() }

// StartArgs Workflow 启动参数(单个 JSON 对象,见 docs/INTEGRATION.md)。
type StartArgs struct {
	InvestigationID    string         `json:"investigation_id"`
	IncidentID         string         `json:"incident_id"`
	IncidentVersion    int            `json:"incident_version"`
	TenantID           string         `json:"tenant_id"`
	ClusterID          string         `json:"cluster_id"`
	Budget             map[string]any `json:"budget"`
	ControlInternalURL string         `json:"control_internal_url"`
}

// Start 以 investigation/{incident}/{version} 为 workflow id 启动(幂等:重复启动同 id 返回已存在的 run)。
func (t *Client) Start(ctx context.Context, workflowID string, args StartArgs) (runID string, err error) {
	opts := client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: t.taskQueue,
		// 0 = UNSPECIFIED,服务端按 AllowDuplicate 处理:上一个 run 结束后同 id 可再启动,
		// 但仍在运行时返回 already-started —— 上层的启动幂等正是依赖这个行为。
		WorkflowIDReusePolicy: 0,
		// 最外层的硬兜底。正常终止全部由 Worker 侧完成(预算 / max_rounds /
		// 人工反馈超时),到这里说明 run 已经异常挂死。下限由 Dial 校验,
		// 见 MinRunTimeout。
		WorkflowRunTimeout: t.runTimeout,
	}
	run, err := t.c.ExecuteWorkflow(ctx, opts, WorkflowTypeName, args)
	if err != nil {
		return "", err
	}
	return run.GetRunID(), nil
}

// Signal 向工作流发送信号(IncidentUpdated / IncidentResolved / HumanFeedback / Cancel)。
func (t *Client) Signal(ctx context.Context, workflowID, signalName string, payload any) error {
	return t.c.SignalWorkflow(ctx, workflowID, "", signalName, payload)
}

// Cancel 取消工作流。
func (t *Client) Cancel(ctx context.Context, workflowID string) error {
	return t.c.CancelWorkflow(ctx, workflowID, "")
}

// Healthy 简单探活。
func (t *Client) Healthy(ctx context.Context) error {
	_, err := t.c.CheckHealth(ctx, &client.CheckHealthRequest{})
	return err
}
