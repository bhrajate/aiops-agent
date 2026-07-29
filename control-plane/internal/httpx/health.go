package httpx

// 健康与就绪探针——**两个端点、两种语义**,不能共用一个。
//
// 此前 /healthz 在 store 降级时只改响应体、状态码恒 200,而 readiness 与 liveness
// 都指向它。后果:数据库断连的副本**不会被摘出 Service endpoints**,继续接流量
// 然后每个请求 500。探针"通过"了,但服务在对外报错。
//
// 拆开的理由是两者要回答不同的问题:
//
//	/readyz  "现在能处理请求吗?" 依赖不可用 → 503 → kubelet 把 Pod 从
//	         Service endpoints 摘掉,流量转到健康副本。依赖恢复后自动放回。
//	/healthz "进程还活着吗?" 恒 200(只要 HTTP 能应答)→ liveness 用它。
//
// 关键在于 **liveness 绝不能查数据库**。数据库挂了重启进程修不了数据库,只会:
//   - 所有副本同时进入 CrashLoopBackOff,数据库恢复后还要等退避;
//   - 丢掉进程内状态(限流令牌桶、正在处理的请求);
//   - 让 Pod 日志被重启冲掉,恰好在最需要它的时候。
//
// 所以 liveness 只该检测"进程卡死/死锁"这类重启确实能修的问题。

import (
	"context"
	"net/http"
	"time"
)

// HealthChecker 是一个具名依赖的健康检查。
type HealthChecker struct {
	Name  string
	Check func(context.Context) error
	// Critical 为 true 表示该依赖不可用时本副本无法提供服务(应摘流量)。
	// 为 false 表示可降级运行:仍然 ready,但在响应里标记出来。
	//
	// 这个区分是必要的:Temporal / 对象存储不可用时,控制面仍能接收信号、
	// 聚合 incident、提供查询——把这类副本摘掉只会让可用性更差。
	Critical bool
}

// ReadyzHandler 返回 /readyz 处理器。
//
// 任一 Critical 依赖失败 → 503;非 Critical 失败 → 200 但标注 degraded。
// 响应体列出每个依赖的状态,便于 kubectl describe / 人工排查时直接看到是谁挂了。
func ReadyzHandler(checkers ...HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 探针必须有自己的超时:依赖 hang 住时若无超时,探针请求会一直挂着,
		// kubelet 侧超时后当失败处理,但服务端连接堆积。
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		deps := make(map[string]any, len(checkers))
		ready := true
		degraded := false
		for _, c := range checkers {
			if c.Check == nil {
				continue
			}
			if err := c.Check(ctx); err != nil {
				deps[c.Name] = map[string]any{"status": "down", "error": err.Error()}
				if c.Critical {
					ready = false
				} else {
					degraded = true
				}
				continue
			}
			deps[c.Name] = map[string]any{"status": "up"}
		}

		status, code := "ready", http.StatusOK
		switch {
		case !ready:
			status, code = "not_ready", http.StatusServiceUnavailable
		case degraded:
			status = "degraded"
		}
		JSON(w, code, map[string]any{"status": status, "dependencies": deps})
	}
}

// HealthzHandler 返回 /healthz 处理器:恒 200,供 liveness 使用。
// 刻意不做任何依赖检查,理由见文件头。
func HealthzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
