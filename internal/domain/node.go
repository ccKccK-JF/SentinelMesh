// ============================================================================
// internal/domain/node.go
// ----------------------------------------------------------------------------
// 领域模型层：定义控制面内部使用的核心数据结构。
//
// 注意：这里的 NodeSnapshot 是 Go 控制面“自己的领域对象”，
// 不是 proto 生成的传输对象。proto 消息只在 ingest 边界出现，
// 进入 Store 后一律转换成 domain 类型，好处是：
//   - 业务代码不感知 wire 格式，后续换传输协议不污染核心逻辑；
//   - domain 结构带有 JSON 标签，直接用于 HTTP API 序列化。
// ============================================================================

package domain

import "time"

// HealthStatus 节点健康状态枚举。
// 状态机迁移规则（见 scoring 包）：unknown -> healthy/degraded/unhealthy，
// 普通恶化连续 2 次才迁移、改善连续 3 次才迁移（滞回），硬门槛立即 unhealthy。
type HealthStatus string

const (
	HealthUnknown   HealthStatus = "unknown"    // 尚未收到有效指标，或处于初始状态
	HealthHealthy   HealthStatus = "healthy"    // 健康，可正常承接流量
	HealthDegraded  HealthStatus = "degraded"   // 亚健康，权重打折（默认 ×0.5）
	HealthUnhealthy HealthStatus = "unhealthy"  // 异常，路由权重直接归零
)

// Metric 单条指标：值 + 单位 + 维度标签。
// Labels 是可选的，用于区分同名指标的不同维度（如按网卡区分的网络速率）。
type Metric struct {
	Value  float64           `json:"value"`
	Unit   string            `json:"unit,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// NodeSnapshot 节点快照：一次上报更新后控制面保存的完整节点状态。
// 兼具“资源指标 + 内核指标 + 健康状态 + 生命周期信息”四类字段。
//
// omitzero 是 Go 1.24 的新 json 标签能力：time.Time 为零值时省略字段，
// 避免把 "0001-01-01T00:00:00Z" 序列化给网关/前端。
type NodeSnapshot struct {
	NodeID            string            `json:"node_id"`               // 节点唯一 ID
	Hostname          string            `json:"hostname"`              // 主机名
	IPAddress         string            `json:"ip_address,omitempty"`  // IP（当前为空）
	AgentVersion      string            `json:"agent_version,omitempty"` // Agent 版本
	BootID            string            `json:"boot_id,omitempty"`     // 启动周期标识
	Connected         bool              `json:"connected"`             // 是否在线（有活跃流）
	LastSeen          time.Time         `json:"last_seen"`             // 最近一次活动时间
	LastSequence      uint64            `json:"last_sequence"`         // 已接受的最大批次序列（幂等基础）
	ObservedAt        time.Time         `json:"observed_at,omitempty"` // 最近一批的采样时刻
	HealthScore       float64           `json:"health_score"`          // 健康分 0~100
	HealthStatus      HealthStatus      `json:"health_status"`         // 健康状态
	HealthReason      string            `json:"health_reason,omitempty"` // 状态/评分原因说明
	HealthChangedAt   time.Time         `json:"health_changed_at,omitzero"` // 最近一次状态迁移时间
	RecoveryNotBefore time.Time         `json:"recovery_not_before,omitzero"` // 恢复冷却截止时间
	RecoveryStartedAt time.Time         `json:"recovery_started_at,omitzero"` // 恢复增权起点时间
	Metrics           map[string]Metric `json:"metrics"`               // 最新指标快照（key=name{labels}）
	KernelEventCount  uint64            `json:"kernel_event_count"`    // 累计接受的内核事件数
}
