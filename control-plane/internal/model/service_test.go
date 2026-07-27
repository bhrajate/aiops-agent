package model

import "testing"

func TestServiceKeyReducesPodsToWorkload(t *testing.T) {
	cases := []struct {
		name string
		ref  ResourceRef
		want string
	}{
		// 同一 Deployment 的多个 Pod 必须归约到同一个 key —— 这是 F3 的核心。
		{"deployment pod", ResourceRef{Namespace: "shop", Kind: "Pod", Name: "order-api-7d9f8bc4f5-x2k9p"}, "shop/order-api"},
		{"same deployment other pod", ResourceRef{Namespace: "shop", Kind: "Pod", Name: "order-api-7d9f8bc4f5-qq41m"}, "shop/order-api"},
		{"same deployment new replicaset", ResourceRef{Namespace: "shop", Kind: "Pod", Name: "order-api-6c5b7a9d21-zz9kk"}, "shop/order-api"},

		{"daemonset pod", ResourceRef{Namespace: "kube-system", Kind: "Pod", Name: "node-exporter-x2k9p"}, "kube-system/node-exporter"},
		{"statefulset pod", ResourceRef{Namespace: "data", Kind: "Pod", Name: "pg-0"}, "data/pg"},
		{"statefulset pod high ordinal", ResourceRef{Namespace: "data", Kind: "Pod", Name: "pg-12"}, "data/pg"},

		// 非 Pod 已是服务级,原样保留。
		{"deployment", ResourceRef{Namespace: "shop", Kind: "Deployment", Name: "order-api"}, "shop/order-api"},
		{"service", ResourceRef{Namespace: "shop", Kind: "Service", Name: "order-api"}, "shop/order-api"},
		{"node", ResourceRef{Kind: "Node", Name: "ip-10-0-1-23"}, "/ip-10-0-1-23"},

		// 不该剥的:纯字母尾段是服务名的一部分,误剥会把不同服务合并(比不剥更危险)。
		{"hyphenated service name pod", ResourceRef{Namespace: "auth", Kind: "Pod", Name: "auth-service"}, "auth/auth-service"},
		{"bare pod", ResourceRef{Namespace: "dev", Kind: "Pod", Name: "debug-shell"}, "dev/debug-shell"},
		{"no hyphen", ResourceRef{Namespace: "dev", Kind: "Pod", Name: "redis"}, "dev/redis"},

		// 空名字退化到 namespace,避免多个"无名"资源被算成多个服务。
		{"empty name", ResourceRef{Namespace: "shop", Kind: "Pod"}, "shop/"},

		// Kind 大小写不敏感。
		{"lowercase kind", ResourceRef{Namespace: "shop", Kind: "pod", Name: "order-api-7d9f8bc4f5-x2k9p"}, "shop/order-api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ServiceKey(tc.ref); got != tc.want {
				t.Errorf("ServiceKey(%+v) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

// 回归护栏:单服务多 Pod 不得被算成多个服务,否则深度 RCA 闸门(services>1)
// 会把单服务故障误判为影响面扩大,拉起昂贵的多轮调查。
func TestServiceKeyCollapsesSingleServiceFanout(t *testing.T) {
	pods := []ResourceRef{
		{Namespace: "shop", Kind: "Pod", Name: "order-api-7d9f8bc4f5-x2k9p"},
		{Namespace: "shop", Kind: "Pod", Name: "order-api-7d9f8bc4f5-qq41m"},
		{Namespace: "shop", Kind: "Pod", Name: "order-api-7d9f8bc4f5-mn77t"},
	}
	svc := map[string]struct{}{}
	for _, p := range pods {
		svc[ServiceKey(p)] = struct{}{}
	}
	if len(svc) != 1 {
		t.Fatalf("同一 Deployment 的 3 个 Pod 归约出 %d 个服务,应为 1: %v", len(svc), svc)
	}
}

// 真正的跨服务扩散仍必须被识别为多服务。
func TestServiceKeyKeepsDistinctServicesDistinct(t *testing.T) {
	refs := []ResourceRef{
		{Namespace: "shop", Kind: "Pod", Name: "order-api-7d9f8bc4f5-x2k9p"},
		{Namespace: "shop", Kind: "Pod", Name: "payment-api-5a4b3c2d1e-yy8jj"},
		{Namespace: "auth", Kind: "Deployment", Name: "auth-service"},
	}
	svc := map[string]struct{}{}
	for _, r := range refs {
		svc[ServiceKey(r)] = struct{}{}
	}
	if len(svc) != 3 {
		t.Fatalf("3 个不同服务归约出 %d 个: %v", len(svc), svc)
	}
}
