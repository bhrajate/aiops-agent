package api

import "testing"

// Kind 必须与 Name 的来源一致:否则 Pod 被标成 Deployment,
// model.ServiceKey 不会归约 Pod 名,单服务多 Pod 会虚高 blast_radius.services。
func TestResourceFromAlertLabels(t *testing.T) {
	cases := []struct {
		name       string
		labels     map[string]string
		wantKind   string
		wantName   string
		wantNSpace string
	}{
		{
			name:       "pod 标签必须标成 Pod",
			labels:     map[string]string{"namespace": "shop", "pod": "order-api-7d9f8bc4f5-x2k9p"},
			wantKind:   "Pod",
			wantName:   "order-api-7d9f8bc4f5-x2k9p",
			wantNSpace: "shop",
		},
		{
			name:     "服务级标签优先于 pod",
			labels:   map[string]string{"namespace": "shop", "deployment": "order-api", "pod": "order-api-7d9f8bc4f5-x2k9p"},
			wantKind: "Deployment",
			wantName: "order-api",
		},
		{
			name:     "statefulset",
			labels:   map[string]string{"statefulset": "pg"},
			wantKind: "StatefulSet",
			wantName: "pg",
		},
		{
			name:     "daemonset",
			labels:   map[string]string{"daemonset": "node-exporter"},
			wantKind: "DaemonSet",
			wantName: "node-exporter",
		},
		{
			name:     "显式 kind 覆盖推导",
			labels:   map[string]string{"pod": "pg-0", "kind": "StatefulSet"},
			wantKind: "StatefulSet",
			wantName: "pg-0",
		},
		{
			name:     "无资源标签保留历史缺省",
			labels:   map[string]string{"namespace": "shop"},
			wantKind: "Deployment",
			wantName: "",
		},
		{
			name:     "node",
			labels:   map[string]string{"node": "ip-10-0-1-23"},
			wantKind: "Node",
			wantName: "ip-10-0-1-23",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resourceFromAlertLabels(tc.labels)
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if tc.wantNSpace != "" && got.Namespace != tc.wantNSpace {
				t.Errorf("Namespace = %q, want %q", got.Namespace, tc.wantNSpace)
			}
		})
	}
}
