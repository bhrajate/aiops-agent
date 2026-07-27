package model

import "strings"

// ServiceKey 把一个资源引用归约到它所属的**服务**身份(namespace/服务名)。
//
// 为什么需要:blast_radius.services 是"影响了几个服务",它驱动深度 RCA 闸门
// (services>1 即判定影响面扩大)。若直接数资源,同一个 Deployment 下 3 个 Pod
// 各自告警就会被算成 3 个服务,把单服务故障误判为扩散,拉起昂贵的多轮 RCA。
//
// 归约规则(确定性):
//   - Pod:剥掉 K8s 生成的后缀,还原到其所属工作负载名;
//   - 其他 Kind(Deployment/StatefulSet/Service/Node…):本身即服务级,原样使用;
//   - 名字为空:退化为 namespace 维度,避免把多个"无名"资源算成多个服务。
//
// 局限(有意保留):这是**名字推导**,不查 K8s ownerReferences。同名不同 owner
// 的极端情况会被合并;若上游能提供 owner 标签(如 Alertmanager 的 `deployment`),
// Ingress 会直接给出服务级 ResourceRef,不走这里的启发式。
func ServiceKey(r ResourceRef) string {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return r.Namespace + "/"
	}
	if strings.EqualFold(strings.TrimSpace(r.Kind), "Pod") {
		name = workloadNameFromPod(name)
	}
	return r.Namespace + "/" + name
}

// workloadNameFromPod 从 Pod 名推导其工作负载名。
//
//	Deployment  order-api-7d9f8bc4f5-x2k9p -> order-api   (剥 rs-hash + 随机后缀)
//	DaemonSet   node-exporter-x2k9p        -> node-exporter
//	StatefulSet pg-0                       -> pg          (剥 ordinal)
//	裸 Pod      debug-shell                -> debug-shell  (无可剥后缀)
func workloadNameFromPod(name string) string {
	// StatefulSet:最后一段是纯数字序号。
	if base, last, ok := cutLastSegment(name); ok && isAllDigits(last) {
		return base
	}
	// Deployment/DaemonSet:最后一段是 5 字符随机串;Deployment 再多一段 rs-hash。
	base, last, ok := cutLastSegment(name)
	if !ok || !isGeneratedSuffix(last) {
		return name
	}
	if base2, last2, ok2 := cutLastSegment(base); ok2 && isGeneratedSuffix(last2) {
		return base2 // Deployment:<name>-<rs-hash>-<rand>
	}
	return base // DaemonSet:<name>-<rand>
}

func cutLastSegment(s string) (base, last string, ok bool) {
	i := strings.LastIndexByte(s, '-')
	if i <= 0 || i == len(s)-1 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isGeneratedSuffix 判断一段是否像 K8s 生成的随机后缀:
// 长度 5~10 的小写字母数字串,且**不是纯字母**(纯字母极可能是服务名的一部分,
// 如 "auth-service" 的 "service";误剥会把不同服务合并成一个,比不剥更危险)。
func isGeneratedSuffix(s string) bool {
	if len(s) < 5 || len(s) > 10 {
		return false
	}
	hasDigit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case c >= 'a' && c <= 'z':
		default:
			return false
		}
	}
	return hasDigit
}
