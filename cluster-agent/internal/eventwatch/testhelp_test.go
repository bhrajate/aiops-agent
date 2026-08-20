package eventwatch

import "k8s.io/apimachinery/pkg/runtime"

// toRuntime 把预置事件转成 fake clientset 需要的 runtime.Object 切片。
func toRuntime(objs []any) []runtime.Object {
	out := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		if ro, ok := o.(runtime.Object); ok {
			out = append(out, ro)
		}
	}
	return out
}
