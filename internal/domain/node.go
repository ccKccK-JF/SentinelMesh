package domain

import "time"

type HealthStatus string

const (
	HealthUnknown   HealthStatus = "unknown"
	HealthHealthy   HealthStatus = "healthy"
	HealthDegraded  HealthStatus = "degraded"
	HealthUnhealthy HealthStatus = "unhealthy"
)

type Metric struct {
	Value  float64           `json:"value"`
	Unit   string            `json:"unit,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

type NodeSnapshot struct {
	NodeID            string            `json:"node_id"`
	Hostname          string            `json:"hostname"`
	IPAddress         string            `json:"ip_address,omitempty"`
	AgentVersion      string            `json:"agent_version,omitempty"`
	BootID            string            `json:"boot_id,omitempty"`
	Connected         bool              `json:"connected"`
	LastSeen          time.Time         `json:"last_seen"`
	LastSequence      uint64            `json:"last_sequence"`
	ObservedAt        time.Time         `json:"observed_at,omitempty"`
	HealthScore       float64           `json:"health_score"`
	HealthStatus      HealthStatus      `json:"health_status"`
	HealthReason      string            `json:"health_reason,omitempty"`
	HealthChangedAt   time.Time         `json:"health_changed_at,omitzero"`
	RecoveryNotBefore time.Time         `json:"recovery_not_before,omitzero"`
	RecoveryStartedAt time.Time         `json:"recovery_started_at,omitzero"`
	Metrics           map[string]Metric `json:"metrics"`
	KernelEventCount  uint64            `json:"kernel_event_count"`
}
