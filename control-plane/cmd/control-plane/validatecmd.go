package main

// validate-config 子命令:只做启动配置校验,不连接任何基础设施。
//
// 为什么需要它独立存在:常驻进程的 Validate() 之后紧跟着连库/连 Temporal,
// 所以"配置对不对"这个问题没法在不具备基础设施的地方回答 —— 而最需要回答它的
// 时机恰恰是 CI 和上线前的 dry-run,那里通常没有生产库。
//
// 用法(退出码即结论,便于 CI 断言):
//
//	control-plane validate-config     # 0 = 通过;1 = 配置有问题(逐条打印)
//
// 典型用法是把渲染好的清单里的环境变量喂给它:
//
//	env $(helm template ... | yq '...') control-plane validate-config
//
// 见 scripts/check-prod-guards.sh。

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/aiops/control-plane/internal/config"
)

// runValidateConfigCmd 处理 `control-plane validate-config`。自行决定退出码,不返回。
func runValidateConfigCmd(log *slog.Logger) {
	cfg := config.Load()

	err := cfg.Validate()
	if err == nil {
		log.Info("configuration valid",
			"env", cfg.Env,
			"production", cfg.IsProduction(),
			"auth_mode", cfg.AuthMode,
			"obs_datasource", obsDatasourceLabel(cfg),
		)
		// 非生产模式下 Validate 只跑与环境无关的少数几条,通过并不代表这份配置
		// 能用于生产。说清楚这件事,避免把 dev 下的 PASS 当成生产就绪的凭据。
		if !cfg.IsProduction() {
			log.Warn("AIOPS_ENV 非 production:严格校验未执行" +
				"(auth 强度、internal token、webhook secret、观测后端、集群隔离等均未检查)")
		}
		return
	}

	var ve *config.ValidationError
	if errors.As(err, &ve) {
		fmt.Fprintf(os.Stderr, "配置校验失败(%d 项):\n", len(ve.Problems))
		for i, p := range ve.Problems {
			fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, p)
		}
	} else {
		fmt.Fprintf(os.Stderr, "配置校验失败: %v\n", err)
	}
	os.Exit(1)
}

// obsDatasourceLabel 报告观测数据源会解析成什么,供人工核对。
// mock 在生产下已被 Validate 拒绝,这里只是把结论显式打出来 ——
// "回退到 mock"是静默的,而静默正是它危险的原因。
func obsDatasourceLabel(cfg config.Config) string {
	if os.Getenv("AIOPS_OBS_DATASOURCE") != "" {
		return os.Getenv("AIOPS_OBS_DATASOURCE")
	}
	if cfg.PrometheusURL == "" && cfg.LokiURL == "" && cfg.TempoURL == "" {
		return "mock (未配置任何后端,将回退)"
	}
	return "live"
}
