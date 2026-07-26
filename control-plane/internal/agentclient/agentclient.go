// Package agentclient 调用集群内只读 Cluster Agent 的类型化工具。
package agentclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// MTLSConfig 客户端 mTLS 配置(SECURITY §3)。
type MTLSConfig struct {
	ClientCert string
	ClientKey  string
	CA         string
}

// NewMTLS 创建启用 mTLS 的客户端(向 cluster-agent 出示客户端证书并校验服务端)。
func NewMTLS(baseURL string, cfg MTLSConfig) (*Client, error) {
	cert, err := tls.LoadX509KeyPair(cfg.ClientCert, cfg.ClientKey)
	if err != nil {
		return nil, fmt.Errorf("load client cert: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.CA)
	if err != nil {
		return nil, fmt.Errorf("read ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse ca")
	}
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{cert},
					RootCAs:      pool,
					MinVersion:   tls.VersionTLS12,
				},
			},
		},
	}, nil
}

// Scope 工具调用范围(由 Gateway 注入)。
type Scope struct {
	ClusterID string         `json:"cluster_id"`
	Namespace string         `json:"namespace,omitempty"`
	Resource  map[string]any `json:"resource,omitempty"`
	TimeRange map[string]any `json:"time_range,omitempty"`
}

type toolRequest struct {
	Arguments map[string]any `json:"arguments"`
	Scope     Scope          `json:"scope"`
}

// ToolResult 工具返回。
type ToolResult struct {
	Source    string         `json:"source"`
	Summary   string         `json:"summary"`
	Raw       map[string]any `json:"raw"`
	Freshness string         `json:"freshness"`
}

// Invoke 调用一个工具。
func (c *Client) Invoke(ctx context.Context, tool string, args map[string]any, scope Scope) (ToolResult, error) {
	var res ToolResult
	body, _ := json.Marshal(toolRequest{Arguments: args, Scope: scope})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/tools/"+tool, bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 限制响应大小
	if resp.StatusCode != http.StatusOK {
		return res, fmt.Errorf("cluster-agent tool %s status %d: %s", tool, resp.StatusCode, string(data))
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return res, fmt.Errorf("decode tool result: %w", err)
	}
	return res, nil
}

// Health 检查 agent 健康。
func (c *Client) Health(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent unhealthy: %d", resp.StatusCode)
	}
	return nil
}
