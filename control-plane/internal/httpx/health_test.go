package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func doProbe(t *testing.T, h http.HandlerFunc) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (body=%s)", err, rr.Body.String())
	}
	return rr.Code, body
}

// TestReadyz_CriticalDownReturns503 是本项修复的核心断言。
//
// 此前的 /healthz 在 DB 断连时返回 200(只改响应体),readiness 因此永远通过,
// 断连副本不会被摘出 Service endpoints —— 继续接流量然后每个请求 500。
// 状态码必须是 503,只改响应体没有任何作用:kubelet 只看状态码。
func TestReadyz_CriticalDownReturns503(t *testing.T) {
	h := ReadyzHandler(HealthChecker{
		Name:     "database",
		Check:    func(context.Context) error { return errors.New("connection refused") },
		Critical: true,
	})
	code, body := doProbe(t, h)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("关键依赖不可用必须返 503(kubelet 只看状态码),got %d", code)
	}
	if body["status"] != "not_ready" {
		t.Errorf("status 应为 not_ready, got %v", body["status"])
	}
	// 响应体要指名是谁挂了,否则 kubectl describe 只能看到"probe failed"。
	deps, _ := body["dependencies"].(map[string]any)
	db, _ := deps["database"].(map[string]any)
	if db["status"] != "down" {
		t.Errorf("应标明 database down: %+v", deps)
	}
	if db["error"] == "" || db["error"] == nil {
		t.Error("应带上具体错误,便于排查")
	}
}

// TestReadyz_AllUpReturns200 正常路径。
func TestReadyz_AllUpReturns200(t *testing.T) {
	h := ReadyzHandler(HealthChecker{
		Name: "database", Check: func(context.Context) error { return nil }, Critical: true,
	})
	code, body := doProbe(t, h)
	if code != http.StatusOK {
		t.Fatalf("依赖正常应返 200, got %d", code)
	}
	if body["status"] != "ready" {
		t.Errorf("status 应为 ready, got %v", body["status"])
	}
}

// TestReadyz_NonCriticalDownStaysReady 可降级依赖不该摘流量。
//
// Temporal / 对象存储不可用时,控制面仍能接收信号、聚合 incident、提供查询。
// 把这类副本摘掉只会让可用性更差 —— 所以它们标 degraded 但仍 ready。
func TestReadyz_NonCriticalDownStaysReady(t *testing.T) {
	h := ReadyzHandler(
		HealthChecker{Name: "database", Check: func(context.Context) error { return nil }, Critical: true},
		HealthChecker{Name: "temporal", Check: func(context.Context) error { return errors.New("dial timeout") }},
	)
	code, body := doProbe(t, h)
	if code != http.StatusOK {
		t.Fatalf("仅非关键依赖不可用时应仍 ready(摘流量会让可用性更差), got %d", code)
	}
	if body["status"] != "degraded" {
		t.Errorf("status 应为 degraded(既不隐瞒也不摘流量), got %v", body["status"])
	}
}

// TestReadyz_CriticalWinsOverDegraded 关键依赖挂了就是 not_ready,
// 不因为同时有非关键依赖正常而降级为 degraded。
func TestReadyz_CriticalWinsOverDegraded(t *testing.T) {
	h := ReadyzHandler(
		HealthChecker{Name: "database", Check: func(context.Context) error { return errors.New("down") }, Critical: true},
		HealthChecker{Name: "temporal", Check: func(context.Context) error { return errors.New("down") }},
	)
	code, body := doProbe(t, h)
	if code != http.StatusServiceUnavailable || body["status"] != "not_ready" {
		t.Errorf("关键依赖不可用应压过 degraded, got %d %v", code, body["status"])
	}
}

// TestReadyz_HasOwnTimeout 依赖 hang 住时探针必须自己超时返回,
// 否则请求一直挂着、服务端连接堆积,而 kubelet 侧已当失败处理。
func TestReadyz_HasOwnTimeout(t *testing.T) {
	h := ReadyzHandler(HealthChecker{
		Name: "database", Critical: true,
		Check: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err() // 探针的超时生效
			case <-time.After(30 * time.Second):
				return nil
			}
		},
	})
	done := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		h(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		done <- rr.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusServiceUnavailable {
			t.Errorf("依赖 hang 住应因超时判为 not_ready, got %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("探针没有自己的超时:依赖 hang 住会让探针请求一直挂着")
	}
}

// TestHealthz_AlwaysOK liveness 恒 200 且**不查任何依赖**。
//
// 这是刻意的:数据库挂了重启进程修不了数据库,只会让所有副本同时进入
// CrashLoopBackOff、丢掉进程内状态(限流令牌桶)、并把最需要的日志冲掉。
func TestHealthz_AlwaysOK(t *testing.T) {
	rr := httptest.NewRecorder()
	HealthzHandler()(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("liveness 应恒 200, got %d", rr.Code)
	}
}

// TestReadyz_NilCheckSkipped 未装配的依赖(如降级运行时的对象存储)不该让探针崩。
func TestReadyz_NilCheckSkipped(t *testing.T) {
	h := ReadyzHandler(
		HealthChecker{Name: "database", Check: func(context.Context) error { return nil }, Critical: true},
		HealthChecker{Name: "objstore", Check: nil},
	)
	code, body := doProbe(t, h)
	if code != http.StatusOK {
		t.Fatalf("nil check 应被跳过, got %d", code)
	}
	if deps, _ := body["dependencies"].(map[string]any); len(deps) != 1 {
		t.Errorf("nil check 不应出现在依赖列表: %+v", deps)
	}
}
