// Package datasource defines the read-only data-source abstraction used by
// the Cluster Agent tools, plus the shared request/response types exchanged
// over the tool protocol.
//
// The Cluster Agent is READ-ONLY by contract: a DataSource must never mutate
// cluster state, execute commands, or open shells. Every method only queries.
package datasource

import "context"

// TimeRange is an inclusive query window. Both bounds are RFC3339 strings.
type TimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ResourceRef identifies the target resource. Sent by the Tool Gateway as an
// object (see docs/INTEGRATION.md); all fields optional.
type ResourceRef struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
	UID  string `json:"uid,omitempty"`
}

// Scope is injected by the Tool Gateway and constrains every tool call to a
// single cluster / namespace / resource / time window. Tools MUST honour it.
//
// Enforcement in the Live data source (see live.go / live_scope.go):
//   - Kubernetes tools query only the scoped namespace (Namespaced clients).
//   - Prometheus / Loki tools force-inject a `namespace="<ns>"` matcher into
//     every selector and reject caller expressions that reference a different
//     namespace; resource / namespace names are validated as DNS-1123 so they
//     cannot break out of the query syntax.
//   - The time window is clamped to a positive span no wider than 24h.
type Scope struct {
	ClusterID string      `json:"cluster_id"`
	Namespace string      `json:"namespace"`
	Resource  ResourceRef `json:"resource,omitempty"`
	TimeRange *TimeRange  `json:"time_range,omitempty"`
}

// ResourceName returns the target resource name (empty if unset).
func (s Scope) ResourceName() string { return s.Resource.Name }

// Result is the normalized tool output returned to the Tool Gateway.
//
//	source    origin system (kubernetes | prometheus | loki | tempo | ...)
//	summary   natural-language (Chinese) summary for the LLM
//	raw       structured evidence payload
//	freshness data staleness marker, e.g. "10s"
type Result struct {
	Source    string `json:"source"`
	Summary   string `json:"summary"`
	Raw       any    `json:"raw"`
	Freshness string `json:"freshness"`
}

// DataSource is the pluggable read-only backend. The first implementation is a
// deterministic Mock; future implementations may wrap client-go, Prometheus,
// Loki and Tempo. Each method maps to exactly one typed tool.
type DataSource interface {
	// GetWorkloadState reports Deployment / ReplicaSet / Pod health.
	GetWorkloadState(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	// GetKubernetesEvents returns recent Kubernetes events for the resource.
	GetKubernetesEvents(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	// QueryMetrics evaluates a PromQL-style expression over the time range.
	QueryMetrics(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	// SearchLogs returns matching log lines for the resource.
	SearchLogs(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	// GetTraces returns distributed traces / spans for the resource.
	GetTraces(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	// ListRecentChanges returns deploys, config and infra changes (first-class evidence).
	ListRecentChanges(ctx context.Context, scope Scope, args map[string]any) (Result, error)
	// InspectDependencies returns the service dependency edges around the resource.
	InspectDependencies(ctx context.Context, scope Scope, args map[string]any) (Result, error)
}
