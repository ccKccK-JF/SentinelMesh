// ============================================================================
// internal/scoring/scorer.go
// ----------------------------------------------------------------------------
// 健康评分状态机：控制面的“判断逻辑”核心。
//
// 四条原则（面试重点）：
//   1. EWMA 平滑：普通波动用 EWMA(alpha=0.35) 平滑，抑制瞬时尖峰造成的抖动；
//   2. 硬门槛：CPU/内存/磁盘命中极值（如 CPU>=99.5%）时读原始值、立即 unhealthy，
//      不被历史低值“稀释”，保证过载节点秒摘；
//   3. 滞回（Hysteresis）：恶化需连续 2 个同向样本、改善需连续 3 个才迁移，
//      防止状态在边界来回震荡；
//   4. 恢复冷却：unhealthy 之后必须先等 30 秒冷却，再累计 3 个改善样本，
//      避免“故障刚消失就立刻回流”的流量抖动。
//
// 状态存储按 node_id 隔离，每个节点有自己的 EWMA 与候选计数器。
// ============================================================================

package scoring

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
)

// 参与打分的指标名（作为 domain.Metric 的 key）。
// 其余指标（如 P95 延迟、网络速率）只存储展示，不进入健康分。
const (
	CPUUtilization    = "cpu.utilization.percent"
	MemoryUtilization = "memory.utilization.percent"
	LoadNormalized    = "system.load.normalized"
	DiskUtilization   = "disk.io.utilization.percent"
)

// Config 评分器配置（全部可调，默认见 DefaultConfig）。
type Config struct {
	CPUWeight              float64       // CPU 利用率权重
	MemoryWeight           float64       // 内存利用率权重
	LoadWeight             float64       // 归一化负载权重
	DiskWeight             float64       // 磁盘利用率权重
	CPUHardLimitPercent    float64       // CPU 硬门槛（百分比）
	MemoryHardLimitPercent float64       // 内存硬门槛
	DiskHardLimitPercent   float64       // 磁盘硬门槛
	HealthyScore           float64       // 健康分阈值：>= 该值 -> healthy
	DegradedScore          float64       // 降级阈值：>= 该值 -> degraded
	EWMAAlpha              float64       // EWMA 平滑系数
	FailureConsecutive     int           // 恶化迁移所需的连续样本数
	RecoveryConsecutive    int           // 改善迁移所需的连续样本数
	RecoveryCooldown       time.Duration // unhealthy 后的恢复冷却时长
}

func DefaultConfig() Config {
	return Config{
		CPUWeight:              0.35, // 四项权重合计 1.0，按重要度分配
		MemoryWeight:           0.30,
		LoadWeight:             0.20,
		DiskWeight:             0.15,
		CPUHardLimitPercent:    99.5,
		MemoryHardLimitPercent: 98,
		DiskHardLimitPercent:   99.5,
		HealthyScore:           70,  // 0~100 分
		DegradedScore:          40,
		EWMAAlpha:              0.35, // 越接近 1 越灵敏，越接近 0 越平滑
		FailureConsecutive:     2,    // 恶化从严：2 次即可摘除
		RecoveryConsecutive:    3,    // 恢复从宽：3 次才敢恢复
		RecoveryCooldown:       30 * time.Second,
	}
}

// Result 一次评估的输出：分数、状态、原因，以及冷却/恢复相关的时间戳。
type Result struct {
	Score             float64
	Status            domain.HealthStatus
	Reason            string
	ChangedAt         time.Time
	RecoveryNotBefore time.Time // 冷却截止时间：在此之前不允许恢复
	RecoveryStartedAt time.Time // 恢复增权起点：路由层用它计算 ramp
}

// Scorer 评分器：config + 每节点状态存储。
type Scorer struct {
	config Config
	state  *stateStore
}

// stateStore 是所有节点状态的容器（并发安全）。
type stateStore struct {
	mu    sync.Mutex
	nodes map[string]*nodeState
}

// nodeState 单个节点的状态机内部状态。
// candidate/candidateCount 实现滞回：记录“当前想迁移到的目标状态”及连续次数。
type nodeState struct {
	ewma              map[string]float64 // 每项指标的 EWMA 值
	status            domain.HealthStatus // 当前已生效状态
	candidate         domain.HealthStatus // 滞回候选状态
	candidateCount    int                 // 候选状态连续出现的次数
	changedAt         time.Time           // 最近一次状态迁移时间
	recoveryNotBefore time.Time           // 冷却截止
	recoveryStartedAt time.Time           // 恢复开始时间
}

func New(config Config) Scorer {
	defaults := DefaultConfig()
	// 配置兜底：非法值回退到默认，避免生产配置错误导致不可预期行为
	if config.EWMAAlpha <= 0 || config.EWMAAlpha > 1 {
		config.EWMAAlpha = defaults.EWMAAlpha
	}
	if config.FailureConsecutive <= 0 {
		config.FailureConsecutive = defaults.FailureConsecutive
	}
	if config.RecoveryConsecutive <= 0 {
		config.RecoveryConsecutive = defaults.RecoveryConsecutive
	}
	if config.RecoveryCooldown < 0 {
		config.RecoveryCooldown = 0
	}
	return Scorer{
		config: config,
		state:  &stateStore{nodes: make(map[string]*nodeState)},
	}
}

// Evaluate 是状态机的核心入口：EWMA 平滑 + 硬门槛 + 滞回 + 冷却。
func (s Scorer) Evaluate(nodeID string, metrics map[string]domain.Metric, now time.Time) Result {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()

	// 懒初始化节点状态
	state, ok := s.state.nodes[nodeID]
	if !ok {
		state = &nodeState{ewma: make(map[string]float64), status: domain.HealthUnknown}
		s.state.nodes[nodeID] = state
	}

	// 第一步：EWMA 平滑（只对打分指标做平滑）
	smoothed := s.smooth(state, metrics)
	raw := s.Compute(smoothed) // 用平滑后的值计算健康分与候选状态

	// 第二步：硬门槛检查。硬门槛必须读“原始值”而不是平滑值——
	// 100% CPU 被 0.35 的 alpha 平滑后可能只显示 60%，延迟摘除。
	if hard, exceeded := s.hardLimit(metrics); exceeded {
		raw = hard
		if state.status != domain.HealthUnhealthy {
			state.status = domain.HealthUnhealthy
			state.changedAt = now
		}
		state.candidate = domain.HealthUnknown
		state.candidateCount = 0
		// 硬门槛触发后进入冷却：至少等 RecoveryCooldown 才能恢复
		state.recoveryNotBefore = now.Add(s.config.RecoveryCooldown)
		state.recoveryStartedAt = time.Time{}
		return s.result(raw, state)
	}

	// 首次评估：直接采用原始候选状态（无历史，无从滞回）
	if state.status == domain.HealthUnknown {
		state.status = raw.Status
		state.changedAt = now
		return s.result(raw, state)
	}
	// 指标缺失导致无法打分：保持原状态，不因为“缺数据”而误判
	if raw.Status == domain.HealthUnknown {
		raw.Status = state.status
		raw.Reason = "hysteresis holding " + string(state.status) + "; scoring metrics unavailable"
		return s.result(raw, state)
	}
	// 状态没变：清零候选计数器（避免旧候选残留干扰后续判断）
	if raw.Status == state.status {
		state.candidate = domain.HealthUnknown
		state.candidateCount = 0
		return s.result(raw, state)
	}

	// 状态变化意图出现。先判断方向：healthRank 数字越大越健康。
	improving := healthRank(raw.Status) > healthRank(state.status)
	// 冷却检查：unhealthy 节点试图恢复，但还没到冷却截止时间 -> 拒绝恢复
	if improving && state.status == domain.HealthUnhealthy && now.Before(state.recoveryNotBefore) {
		state.candidate = domain.HealthUnknown
		state.candidateCount = 0
		raw.Status = state.status
		raw.Reason = "recovery cooldown until " + state.recoveryNotBefore.UTC().Format(time.RFC3339)
		return s.result(raw, state)
	}

	// 滞回计数：恶化 2 次 / 改善 3 次才真正迁移
	required := s.config.FailureConsecutive
	if improving {
		required = s.config.RecoveryConsecutive
	}
	if state.candidate != raw.Status {
		state.candidate = raw.Status
		state.candidateCount = 1
	} else {
		state.candidateCount++
	}
	if state.candidateCount >= required {
		// 达到阈值，正式迁移状态
		previousStatus := state.status
		state.status = raw.Status
		state.changedAt = now
		state.candidate = domain.HealthUnknown
		state.candidateCount = 0
		if state.status == domain.HealthUnhealthy {
			// 变坏：进入冷却
			state.recoveryNotBefore = now.Add(s.config.RecoveryCooldown)
			state.recoveryStartedAt = time.Time{}
		} else {
			// 恢复（healthy/degraded）：清冷却，记录恢复起点供路由 ramp 使用
			state.recoveryNotBefore = time.Time{}
			if previousStatus == domain.HealthUnhealthy {
				state.recoveryStartedAt = now
			}
		}
		return s.result(raw, state)
	}

	// 未达阈值：保持原状态，返回候选进度信息（便于排查与展示）
	candidate := raw.Status
	count := state.candidateCount
	raw.Status = state.status
	raw.Reason = fmt.Sprintf("hysteresis holding %s; candidate %s %d/%d",
		state.status, candidate, count, required)
	return s.result(raw, state)
}

// Reset 清空节点状态：Boot ID 变化（系统重启）时调用。
func (s Scorer) Reset(nodeID string) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	delete(s.state.nodes, nodeID)
}

// smooth 对打分指标做 EWMA 平滑。
// EWMA: S(t) = alpha * X(t) + (1-alpha) * S(t-1)
// 首次出现某指标时没有历史值，直接用当前值。
func (s Scorer) smooth(state *nodeState, metrics map[string]domain.Metric) map[string]domain.Metric {
	result := make(map[string]domain.Metric, len(metrics))
	for name, metric := range metrics {
		copyMetric := metric
		if isScoringMetric(name) && !math.IsNaN(metric.Value) && !math.IsInf(metric.Value, 0) {
			if previous, ok := state.ewma[name]; ok {
				copyMetric.Value = s.config.EWMAAlpha*metric.Value +
					(1-s.config.EWMAAlpha)*previous
			}
			state.ewma[name] = copyMetric.Value
		}
		result[name] = copyMetric
	}
	return result
}

// result 把状态机的持久状态回填到 Result，统一出口。
func (s Scorer) result(raw Result, state *nodeState) Result {
	raw.Status = state.status
	raw.ChangedAt = state.changedAt
	raw.RecoveryNotBefore = state.recoveryNotBefore
	raw.RecoveryStartedAt = state.recoveryStartedAt
	return raw
}

// hardLimit 硬门槛检查：命中即返回“立即 unhealthy”的 Result。
// 返回的 bool 表示是否命中门槛。
func (s Scorer) hardLimit(metrics map[string]domain.Metric) (Result, bool) {
	if value(metrics, CPUUtilization) >= s.config.CPUHardLimitPercent {
		return Result{Score: 5, Status: domain.HealthUnhealthy, Reason: "cpu hard limit exceeded"}, true
	}
	if value(metrics, MemoryUtilization) >= s.config.MemoryHardLimitPercent {
		return Result{Score: 5, Status: domain.HealthUnhealthy, Reason: "memory hard limit exceeded"}, true
	}
	if value(metrics, DiskUtilization) >= s.config.DiskHardLimitPercent {
		return Result{Score: 5, Status: domain.HealthUnhealthy, Reason: "disk hard limit exceeded"}, true
	}
	return Result{}, false
}

// healthRank 给健康状态排序，用于判断“改善”还是“恶化”。
func healthRank(status domain.HealthStatus) int {
	switch status {
	case domain.HealthHealthy:
		return 3
	case domain.HealthDegraded:
		return 2
	case domain.HealthUnhealthy:
		return 1
	default:
		return 0
	}
}

// isScoringMetric 判断某个指标是否参与打分。
func isScoringMetric(name string) bool {
	return name == CPUUtilization || name == MemoryUtilization ||
		name == LoadNormalized || name == DiskUtilization
}

// Compute 依据（平滑后的）指标计算健康分与候选状态。
// 分数 = 加权可用度之和 / 实际参与权重之和，归一到 0~100。
// 可用度映射：CPU/内存/磁盘 = 1 - 利用率/100；load = 1 - load/1.5（1.5 是经验上限）。
func (s Scorer) Compute(metrics map[string]domain.Metric) Result {
	if len(metrics) == 0 {
		return Result{Status: domain.HealthUnknown, Reason: "no metrics received"}
	}

	if hard, exceeded := s.hardLimit(metrics); exceeded {
		return hard
	}

	weightedScore := 0.0
	weightSum := 0.0
	// add 辅助函数：缺指标或非法值时跳过，且不累计权重，
	// 保证“只有实际收到的指标”参与加权平均，避免部分数据被稀释。
	add := func(name string, weight float64, score func(float64) float64) {
		metric, ok := metrics[name]
		if !ok || math.IsNaN(metric.Value) || math.IsInf(metric.Value, 0) {
			return
		}
		weightedScore += clamp(score(metric.Value), 0, 1) * weight
		weightSum += weight
	}

	add(CPUUtilization, s.config.CPUWeight, percentAvailable)
	add(MemoryUtilization, s.config.MemoryWeight, percentAvailable)
	add(LoadNormalized, s.config.LoadWeight, func(v float64) float64 {
		return 1 - v/1.5
	})
	add(DiskUtilization, s.config.DiskWeight, percentAvailable)

	if weightSum == 0 {
		return Result{Status: domain.HealthUnknown, Reason: "no scoring metrics received"}
	}

	score := math.Round(weightedScore/weightSum*10000) / 100 // 保留两位小数
	result := Result{Score: score, Status: domain.HealthUnhealthy}
	switch {
	case score >= s.config.HealthyScore:
		result.Status = domain.HealthHealthy
	case score >= s.config.DegradedScore:
		result.Status = domain.HealthDegraded
	}
	return result
}

// value 取指标值；缺失时返回 -Inf，这样硬门槛比较必然不命中。
func value(metrics map[string]domain.Metric, name string) float64 {
	metric, ok := metrics[name]
	if !ok {
		return math.Inf(-1)
	}
	return metric.Value
}

// percentAvailable 利用率 -> 可用度：90% 利用率 -> 0.10 可用度。
func percentAvailable(value float64) float64 {
	return 1 - value/100
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
