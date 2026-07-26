// Package config 从环境变量(统一前缀 AIOPS_)加载控制面配置。
package config

import (
	"os"
	"strings"
)

type Config struct {
	// 网络
	PublicAddr   string // 公共 API,默认 :8080
	InternalAddr string // 内部 API,默认 :8090

	// 依赖
	DBDSN            string
	KafkaBrokers     []string
	TemporalHostPort string
	TemporalNS       string
	TemporalQueue    string

	// 组件互联
	ClusterAgentURL string
	InternalURL     string // 供 AI Worker 回写的内部 API base(下发给 workflow)
	ClusterID       string
	Tenant          string
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Load 读取环境变量并填充默认值。
func Load() Config {
	brokers := strings.Split(getenv("AIOPS_KAFKA_BROKERS", "localhost:19092"), ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
	}
	return Config{
		PublicAddr:       getenv("AIOPS_PUBLIC_ADDR", ":8088"),
		InternalAddr:     getenv("AIOPS_INTERNAL_ADDR", ":8090"),
		DBDSN:            getenv("AIOPS_DB_DSN", "postgres://aiops:aiops@localhost:5432/aiops?sslmode=disable"),
		KafkaBrokers:     brokers,
		TemporalHostPort: getenv("AIOPS_TEMPORAL_HOSTPORT", "localhost:7233"),
		TemporalNS:       getenv("AIOPS_TEMPORAL_NAMESPACE", "default"),
		TemporalQueue:    getenv("AIOPS_TEMPORAL_QUEUE", "investigation-ai"),
		ClusterAgentURL:  getenv("AIOPS_CLUSTER_AGENT_URL", "http://localhost:9100"),
		InternalURL:      getenv("AIOPS_CONTROL_INTERNAL_URL", "http://localhost:8090"),
		ClusterID:        getenv("AIOPS_CLUSTER_ID", "prod-cn-1"),
		Tenant:           getenv("AIOPS_TENANT", "default"),
	}
}
