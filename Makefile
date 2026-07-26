# 顶层便捷入口。各子模块有自己的 Makefile。
.PHONY: infra-up infra-down build test cp-run agent-run worker-run web-run demo

infra-up:
	$(MAKE) -C deploy up

infra-down:
	$(MAKE) -C deploy down

# 构建 Go 组件 + 前端类型检查
build:
	$(MAKE) -C control-plane build
	$(MAKE) -C cluster-agent build

# 运行所有单元测试
test:
	$(MAKE) -C control-plane test
	$(MAKE) -C cluster-agent test
	$(MAKE) -C ai-worker test

cp-run:
	$(MAKE) -C control-plane run

agent-run:
	$(MAKE) -C cluster-agent run

worker-run:
	$(MAKE) -C ai-worker run

web-run:
	cd frontend && npm run dev

# 注入一条演示 Signal(需控制面已运行)
demo:
	curl -s localhost:8088/v1/signals -H 'Content-Type: application/json' -d '{"alerts":[{"status":"firing","labels":{"alertname":"HighErrorRate","severity":"critical","namespace":"payment","deployment":"checkout","cluster":"prod-cn-1","rule_id":"r-101"},"startsAt":"2026-07-26T10:00:00Z"}]}'
	@echo ""
