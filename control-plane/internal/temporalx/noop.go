package temporalx

import (
	"context"
	"errors"
)

// Noop 在 Temporal 不可用时作为降级实现:
// 调查记录仍会持久化(业务库为事实源),但工作流不会启动。
// 满足文档"模型/工具/数据源失败时允许降级"的隔离要求。
type Noop struct{}

var errUnavailable = errors.New("temporal unavailable (running in degraded mode)")

func (Noop) Start(_ context.Context, _ string, _ StartArgs) (string, error) {
	return "", errUnavailable
}

func (Noop) Signal(_ context.Context, _, _ string, _ any) error { return errUnavailable }

func (Noop) Cancel(_ context.Context, _ string) error { return errUnavailable }
