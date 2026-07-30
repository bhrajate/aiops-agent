package datasource

import (
	"errors"
	"os"
	"testing"
)

// 这组用例守住一条不变量:**生产模式下绝不可能解析出 mock 数据源。**
//
// 为什么值得专门测:mock 的默认值 + 部署清单从没设过 AIOPS_DATASOURCE,
// 二者叠加的结果是生产环境静默使用假 K8s 数据。而这个错误不会在任何地方报错 ——
// 假数据自洽、工具全部返回 200、Evidence 正常入库、诊断结论"有据可查",
// 只有结论内容是编造的。所以唯一能挡住它的地方就是启动。

// setDatasource 设置 AIOPS_DATASOURCE;value 为 unset 哨兵时删除该变量,
// 用来复现"部署清单压根没配这一项"这个真实场景(而非配了空值)。
const unset = "\x00unset"

func setEnvOrUnset(t *testing.T, key, value string) {
	t.Helper()
	// 先 Setenv 注册 Cleanup(它会在用例结束时恢复原值),再按需删除。
	t.Setenv(key, "")
	if value == unset {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		return
	}
	t.Setenv(key, value)
}

func TestFromEnv_ProductionRejectsMock(t *testing.T) {
	cases := []struct {
		name       string
		env        string
		datasource string
		wantMode   string
		wantErr    bool
	}{
		// 缺陷本体:生产 + 漏配 → 此前静默 mock,现在必须拒绝。
		{"生产漏配 datasource", "production", unset, "mock", true},
		{"生产空值 datasource", "production", "", "mock", true},
		{"生产显式 mock", "production", "mock", "mock", true},
		{"生产简写 prod + 漏配", "prod", unset, "mock", true},
		{"生产大小写混写", "Production", "MOCK", "mock", true},
		{"生产带空格", "  prod  ", "  mock  ", "mock", true},
		// 生产 + live 是唯一放行的组合。
		{"生产 live", "production", "live", "live", false},
		{"生产 live 大小写与空格", "PROD", "  Live  ", "live", false},
		// 非生产:mock 是合法且默认的选择(零基础设施端到端演示依赖它)。
		{"开发漏配", "development", unset, "mock", false},
		{"开发显式 mock", "development", "mock", "mock", false},
		{"开发 live", "development", "live", "live", false},
		{"环境变量本身漏配", unset, unset, "mock", false},
		// "production" 的子串/近似值不算生产,避免把 staging 误判成生产而拒启动。
		{"staging 不算生产", "staging", unset, "mock", false},
		{"preprod 不算生产", "preprod", unset, "mock", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setEnvOrUnset(t, "AIOPS_ENV", tc.env)
			setEnvOrUnset(t, "AIOPS_DATASOURCE", tc.datasource)

			ds, mode, err := FromEnv()

			if mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", mode, tc.wantMode)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("期望拒绝启动,却放行了(生产将使用虚构 K8s 证据)")
				}
				if !errors.Is(err, ErrMockInProduction) {
					t.Errorf("err = %v, want ErrMockInProduction", err)
				}
				// 拒绝时不能返回可用数据源:否则调用方忽略 err 仍会跑起来。
				if ds != nil {
					t.Error("拒绝启动时仍返回了数据源,调用方漏判 err 就会带假数据运行")
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if ds == nil {
				t.Fatal("放行时必须返回可用数据源")
			}
		})
	}
}

// 放行 live 时不应因为 kubeconfig 缺失而报错 —— live 的降级是**按工具粒度**的
// (返回 unavailable),不是启动失败。CI 容器里没有 in-cluster 配置,
// 这条用例因此也守住了"live 在无 K8s 环境下仍能启动"。
func TestFromEnv_LiveStartsWithoutKubeconfig(t *testing.T) {
	setEnvOrUnset(t, "AIOPS_ENV", "production")
	setEnvOrUnset(t, "AIOPS_DATASOURCE", "live")
	setEnvOrUnset(t, "AIOPS_KUBECONFIG", unset)

	ds, mode, err := FromEnv()
	if err != nil {
		t.Fatalf("live 不应因缺 kubeconfig 而拒绝启动: %v", err)
	}
	if mode != "live" || ds == nil {
		t.Fatalf("mode = %q, ds == nil: %v", mode, ds == nil)
	}
}
