// ============================================================================
// internal/routing/policy.go
// ----------------------------------------------------------------------------
// 路由权重策略：把节点健康状态翻译成“可以分多少流量”。
//
// 核心算法（面试重点）：
//   1. 资格判断：healthy/degraded 可承接流量；断连、unhealthy、unknown 权重为 0；
//   2. 原始权重：healthy 用健康分；degraded 额外乘 0.5（惩罚）；
//      恢复中的节点再乘 ramp factor（0.1 ~ 1.0 随 60 秒线性爬升）；
//   3. 最大余数归一化：浮点权重先向下取整，再把余数从大到小逐 1 补齐，
//      保证全部可用节点权重之和严格等于 TotalWeight(10,000)，
//      比逐项四舍五入更不容易产生总和漂移。
//
// 设计原则：路由计算必须确定（节点按 NodeID 排序），
// 相同输入永远产生相同输出，便于测试和版本化 diff。
// ============================================================================

package routing

import (
	"math"
	"sort"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
)

// Config 路由策略配置。
type Config struct {
	TotalWeight       uint32        // 权重总和（归一化目标值）
	DegradedFactor    float64       // degraded 节点的权重惩罚系数
	MinRecoveryFactor float64       // 恢复起步权重比例（默认 10%）
	RecoveryRamp      time.Duration // 恢复期权重爬升时长（默认 60 秒）
}

func DefaultConfig() Config {
	return Config{
		TotalWeight:       10_000,
		DegradedFactor:    0.5,
		MinRecoveryFactor: 0.1,
		RecoveryRamp:      time.Minute,
	}
}

// Assignment 单个节点的路由分配结果。
type Assignment struct {
	NodeID       string              `json:"node_id"`
	Weight       uint32              `json:"weight"`        // 归一化后的整数权重（总和=10000）
	Eligible     bool                `json:"eligible"`      // 是否可承接流量
	HealthScore  float64             `json:"health_score"`
	HealthStatus domain.HealthStatus `json:"health_status"`
	RampFactor   float64             `json:"ramp_factor"`   // 恢复期爬升因子（1 表示满权）
	Reason       string              `json:"reason"`        // 决策原因（排障用）
}

// Snapshot 版本化路由快照：网关通过 Version + ETag 做增量消费。
type Snapshot struct {
	Version     uint64       `json:"version"`      // 单调递增版本号（内容变化才 +1）
	GeneratedAt time.Time    `json:"generated_at"`
	Nodes       []Assignment `json:"nodes"`
}

// Policy 路由策略（无内部状态，可安全并发调用）。
type Policy struct {
	config Config
}

func New(config Config) Policy {
	defaults := DefaultConfig()
	// 配置兜底
	if config.TotalWeight == 0 {
		config.TotalWeight = defaults.TotalWeight
	}
	if config.DegradedFactor <= 0 || config.DegradedFactor > 1 {
		config.DegradedFactor = defaults.DegradedFactor
	}
	if config.MinRecoveryFactor <= 0 || config.MinRecoveryFactor > 1 {
		config.MinRecoveryFactor = defaults.MinRecoveryFactor
	}
	if config.RecoveryRamp < 0 {
		config.RecoveryRamp = 0
	}
	return Policy{config: config}
}

// Compute 根据节点快照集合计算权重分配。
// 输入顺序无关（内部先按 NodeID 排序），输出确定。
func (p Policy) Compute(nodes []domain.NodeSnapshot, now time.Time) []Assignment {
	sorted := append([]domain.NodeSnapshot(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].NodeID < sorted[j].NodeID })
	assignments := make([]Assignment, len(sorted))
	rawWeights := make([]float64, len(sorted)) // 归一化前的浮点原始权重
	totalRaw := 0.0

	for index, node := range sorted {
		assignment := Assignment{
			NodeID:       node.NodeID,
			HealthScore:  node.HealthScore,
			HealthStatus: node.HealthStatus,
			RampFactor:   1, // 默认满权；恢复期节点会被覆盖
		}
		// 第一步：资格判断
		switch {
		case !node.Connected:
			assignment.Reason = "agent_disconnected"
		case node.HealthStatus == domain.HealthUnhealthy:
			assignment.Reason = "health_unhealthy"
		case node.HealthStatus == domain.HealthHealthy || node.HealthStatus == domain.HealthDegraded:
			assignment.Eligible = true
			assignment.Reason = string(node.HealthStatus)
		default:
			assignment.Reason = "health_unknown"
		}

		// 第二步：计算原始权重
		if assignment.Eligible {
			raw := clamp(node.HealthScore, 1, 100) // 健康分钳到 [1,100]
			if node.HealthStatus == domain.HealthDegraded {
				raw *= p.config.DegradedFactor // degraded 惩罚：减半
			}
			// 恢复期节点按 ramp factor 渐进增权
			assignment.RampFactor = p.recoveryFactor(node.RecoveryStartedAt, now)
			if assignment.RampFactor < 1 {
				assignment.Reason = "recovery_ramp"
				raw *= assignment.RampFactor
			}
			rawWeights[index] = raw
			totalRaw += raw
		}
		assignments[index] = assignment
	}

	// 没有可用节点：所有权重保持 0（网关会显式失败）
	if totalRaw == 0 {
		return assignments
	}

	// 第三步：最大余数法归一化到 TotalWeight。
	// exact = raw / totalRaw * TotalWeight；向下取整后，把剩余权重按余数
	// 从大到小逐个 +1，保证最终总和严格等于 TotalWeight。
	type remainder struct {
		index int
		value float64
	}
	remainders := make([]remainder, 0, len(assignments))
	assigned := uint32(0)
	for index, raw := range rawWeights {
		if raw == 0 {
			continue
		}
		exact := raw / totalRaw * float64(p.config.TotalWeight)
		weight := uint32(math.Floor(exact))
		assignments[index].Weight = weight
		assigned += weight
		remainders = append(remainders, remainder{index: index, value: exact - float64(weight)})
	}
	// 余数相同（浮点相等）时按 NodeID 排序，保持确定性
	sort.SliceStable(remainders, func(i, j int) bool {
		if remainders[i].value == remainders[j].value {
			return assignments[remainders[i].index].NodeID < assignments[remainders[j].index].NodeID
		}
		return remainders[i].value > remainders[j].value
	})
	// 把 TotalWeight - assigned 的剩余配额逐 1 分给余数最大的节点
	for remaining, index := p.config.TotalWeight-assigned, 0; remaining > 0; remaining, index = remaining-1, index+1 {
		assignments[remainders[index%len(remainders)].index].Weight++
	}
	return assignments
}

// recoveryFactor 计算恢复期爬升因子。
// 恢复开始后：elapsed = now - startedAt。
//   - elapsed <= 0：起步 MinRecoveryFactor（10%）；
//   - elapsed >= RecoveryRamp（60 秒）：达到 1.0（满权）；
//   - 之间：线性插值 10% -> 100%。
func (p Policy) recoveryFactor(startedAt, now time.Time) float64 {
	if startedAt.IsZero() || p.config.RecoveryRamp == 0 {
		return 1
	}
	elapsed := now.Sub(startedAt)
	if elapsed >= p.config.RecoveryRamp {
		return 1
	}
	if elapsed <= 0 {
		return p.config.MinRecoveryFactor
	}
	progress := float64(elapsed) / float64(p.config.RecoveryRamp)
	return p.config.MinRecoveryFactor + (1-p.config.MinRecoveryFactor)*progress
}

// EqualAssignments 判断两次计算是否产生相同分配。
// 用于 Store 里“内容无变化则不递增版本号”的优化。
func EqualAssignments(left, right []Assignment) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// cloneAssignments 深拷贝 Assignment 切片。
func cloneAssignments(source []Assignment) []Assignment {
	return append([]Assignment(nil), source...)
}

// Clone 深拷贝 Snapshot，避免外部拿到共享切片。
func Clone(snapshot Snapshot) Snapshot {
	snapshot.Nodes = cloneAssignments(snapshot.Nodes)
	return snapshot
}

// clamp 把值限制到 [low, high]。
func clamp(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
