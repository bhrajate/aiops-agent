package eventwatch

// 环境变量解析与启动前校验。

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrMockDatasource 表示在 mock 数据源下启用了 event watch。
//
// 这条比 datasource 的 ErrMockInProduction **更硬**:那条只在
// AIOPS_ENV=production 下拒绝,而这条在**任何**环境都拒绝。
//
// 理由是后果的性质不同。假 evidence 只污染一次调查的结论(值班人员看到一份
// "有据可查"但编造的根因);而假 signal 会**创建真 incident** —— 它走完整的
// 两层聚合、触发策略、可能拉起自动调查,并出现在值班界面上。
// 值班人员会去排查一个从不存在的故障,而库里所有痕迹都表明它真的发生过。
var ErrMockDatasource = errors.New(
	"event watch 需要 AIOPS_DATASOURCE=live:mock 会合成**假 signal**," +
		"而假 signal 会创建真 incident 并可能拉起自动调查 —— " +
		"值班人员会去排查一个不存在的故障")

// ErrMissingIngress 表示缺少控制面地址。
var ErrMissingIngress = errors.New(
	"event watch 需要 AIOPS_CONTROL_INGRESS_URL(控制面 /v1/signals 地址)")

// Enabled 报告是否启用 event watch。
//
// **默认关闭**是刻意的:它让 agent 从"纯被动"变成"会主动出站",
// 而这种形态变化不该靠升级悄悄生效 —— 出站目标、新增凭据、新的失败模式
// 都需要部署方明确知道。
func Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AIOPS_EVENT_WATCH_ENABLED")))
	return v == "1" || v == "true" || v == "yes"
}

// ConfigFromEnv 读取配置并校验。datasourceMode 由调用方传入(来自
// datasource.FromEnv 的返回),避免这里重复解析 AIOPS_DATASOURCE 而两处漂移。
func ConfigFromEnv(clusterID, datasourceMode string) (Config, error) {
	if !strings.EqualFold(datasourceMode, "live") {
		return Config{}, ErrMockDatasource
	}
	ingress := strings.TrimSpace(os.Getenv("AIOPS_CONTROL_INGRESS_URL"))
	if ingress == "" {
		return Config{}, ErrMissingIngress
	}
	// 允许只给基地址,自动补 /v1/signals —— 部署清单里写基地址更自然,
	// 而拼错路径的失败表现是 404,那时 agent 只看到非 2xx。
	if !strings.Contains(ingress, "/v1/signals") {
		ingress = strings.TrimRight(ingress, "/") + "/v1/signals"
	}

	cfg := Config{
		ClusterID:     clusterID,
		TenantID:      strings.TrimSpace(os.Getenv("AIOPS_TENANT")),
		IngressURL:    ingress,
		WebhookSecret: os.Getenv("AIOPS_WEBHOOK_SECRET"),
		Reasons:       splitList(os.Getenv("AIOPS_EVENT_WATCH_REASONS")),
		Namespaces:    splitList(os.Getenv("AIOPS_EVENT_WATCH_NAMESPACES")),
	}
	if r := strings.TrimSpace(os.Getenv("AIOPS_EVENT_WATCH_RATE_PER_SEC")); r != "" {
		v, err := strconv.ParseFloat(r, 64)
		if err != nil || v <= 0 {
			return Config{}, fmt.Errorf("AIOPS_EVENT_WATCH_RATE_PER_SEC=%q 不是正数", r)
		}
		cfg.RatePerSec = v
	}
	if p := strings.TrimSpace(os.Getenv("AIOPS_EVENT_WATCH_RESYNC_SEC")); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return Config{}, fmt.Errorf("AIOPS_EVENT_WATCH_RESYNC_SEC=%q 不是非负整数", p)
		}
		cfg.ResyncPeriod = time.Duration(v) * time.Second
	}
	return cfg, nil
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
