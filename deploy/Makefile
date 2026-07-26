.PHONY: up down restart logs ps clean wait psql topics

# 拉起全部基础设施
up:
	docker compose up -d
	@echo "等待服务健康检查..."
	@$(MAKE) wait
	@echo ""
	@echo "基础设施就绪:"
	@echo "  PostgreSQL(业务库)  localhost:5432  (aiops/aiops, db=aiops)"
	@echo "  Temporal            localhost:7233  UI: http://localhost:8233"
	@echo "  Redpanda(Kafka)     localhost:19092"
	@echo "  MinIO               http://localhost:9000  Console: http://localhost:9001 (minioadmin/minioadmin)"
	@echo "  Redis               localhost:6379"

down:
	docker compose down

# 停止并清空本地数据卷(危险:会丢失所有本地数据)
clean:
	docker compose down -v
	rm -rf ./.data

restart: down up

logs:
	docker compose logs -f --tail=100

ps:
	docker compose ps

# 阻塞直到关键服务健康
wait:
	@echo "waiting for postgres..."
	@until docker compose exec -T postgres pg_isready -U aiops -d aiops >/dev/null 2>&1; do sleep 2; done
	@echo "postgres ok"
	@echo "waiting for temporal..."
	@until docker compose exec -T temporal tctl --address temporal:7233 cluster health >/dev/null 2>&1; do sleep 2; done
	@echo "temporal ok"

# 快速进入业务库
psql:
	docker compose exec postgres psql -U aiops -d aiops

# 查看/重建 topics
topics:
	docker compose exec redpanda rpk topic list --brokers redpanda:29092
