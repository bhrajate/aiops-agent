// Package temporalx 封装 Temporal 客户端:启动/信号/取消 Investigation Workflow。
// 控制面只负责可靠地"启动"工作流;推理逻辑在 Python Worker 中执行。
package temporalx

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/client"
)

type Client struct {
	c         client.Client
	namespace string
	taskQueue string
}

// WorkflowTypeName 跨语言字符串,必须与 Python Worker 注册的名字一致。
const WorkflowTypeName = "InvestigationWorkflow"

func Dial(hostPort, namespace, taskQueue string) (*Client, error) {
	c, err := client.Dial(client.Options{HostPort: hostPort, Namespace: namespace})
	if err != nil {
		return nil, fmt.Errorf("dial temporal: %w", err)
	}
	return &Client{c: c, namespace: namespace, taskQueue: taskQueue}, nil
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
		ID:                    workflowID,
		TaskQueue:             t.taskQueue,
		WorkflowIDReusePolicy: 0, // AllowDuplicate 默认策略下,同 id 若在运行会返回 already-started 错误
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
