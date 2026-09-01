// ============================================================================
// internal/experiment/routing.go
// ----------------------------------------------------------------------------
// 路由策略确定性对照实验。
//
// 为什么要“确定性”？
//   - 真实压测受环境噪声影响不可复现；
//   - 这里用固定故障模型（哪些请求命中故障节点、每个请求的延迟由公式
//     决定）模拟一次完整的“故障 -> 检测 -> 摘除 -> 恢复”周期，
//     让 Round Robin 与自适应策略在同一输入下直接对比。
//
// 模型假设：
//   - game-a 始终健康（P50~10ms）；
//   - game-b 在 [FailureStart, FailureEnd) 期间“故障”（每 4 个请求失败 1 个）；
//   - 自适应策略在 DetectionRequest 才发现并摘除 game-b，
//     在 RecoveryRequest 开始对 game-b 渐进增权。
//
// 限制（面试要主动说明）：这是算法回归实验，不等于真实线上收益。
// ============================================================================

package experiment

import (
	"math"
	"sort"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
	"github.com/ccKccK-JF/SentinelMesh/internal/routing"
)

// RoutingConfig 实验参数。
type RoutingConfig struct {
	Requests         int           // 总请求数
	FailureStart     int           // 故障开始（请求序号）
	FailureEnd       int           // 故障结束
	DetectionRequest int           // 自适应策略首次发现故障的请求序号
	RecoveryRequest  int           // 自适应策略开始恢复的请求序号
	RebalanceEvery   int           // 每多少请求重新计算一次路由（模拟控制面周期）
	RequestInterval  time.Duration // 请求间隔（用于推演“控制面眼中的时间”）
}

func DefaultRoutingConfig() RoutingConfig {
	return RoutingConfig{
		Requests:         12_000,
		FailureStart:     2_000,
		FailureEnd:       7_000,
		DetectionRequest: 2_200, // 故障开始 200 请求后才摘除（检测延迟）
		RecoveryRequest:  7_300, // 故障结束 300 请求后才开始恢复（冷却+滞回延迟）
		RebalanceEvery:   100,   // 控制面每 100 请求重算一次路由
		RequestInterval:  10 * time.Millisecond,
	}
}

// Result 一次策略跑完的统计结果。
type Result struct {
	Strategy              string         `json:"strategy"`
	Requests              int            `json:"requests"`
	Errors                int            `json:"errors"`
	ErrorRatePercent      float64        `json:"error_rate_percent"`
	P50Milliseconds       float64        `json:"p50_milliseconds"`
	P95Milliseconds       float64        `json:"p95_milliseconds"`
	P99Milliseconds       float64        `json:"p99_milliseconds"`
	Allocations           map[string]int `json:"allocations"`
	RemovalDelayRequests  *int           `json:"removal_delay_requests,omitempty"`  // 自适应：摘除延迟
	RecoveryDelayRequests *int           `json:"recovery_delay_requests,omitempty"` // 自适应：恢复延迟
}

// RunRoutingComparison 依次跑两种策略，返回对比结果。
func RunRoutingComparison(config RoutingConfig) []Result {
	return []Result{
		runRoundRobin(config),
		runAdaptive(config),
	}
}

// runRoundRobin：均匀轮询两个节点，不看健康状态（对照组）。
func runRoundRobin(config RoutingConfig) Result {
	return run(config, "round_robin", func(request int) string {
		if request%2 == 0 {
			return "game-a"
		}
		return "game-b"
	})
}

// runAdaptive：使用路由策略 + SWRR 选择节点。
// 关键：根据“请求序号”推演出控制面视角的时间，从而让策略
// 在正确的时机（DetectionRequest / RecoveryRequest）改变分配。
func runAdaptive(config RoutingConfig) Result {
	policy := routing.New(routing.DefaultConfig())
	startedAt := time.Unix(0, 0).UTC()
	var selector *routing.Selector
	selectNode := func(request int) string {
		// 周期性（或关键时刻）重算路由
		if selector == nil || request%config.RebalanceEvery == 0 ||
			request == config.DetectionRequest || request == config.RecoveryRequest {
			// 控制面“当前时间”由请求序号推导：request * interval
			now := startedAt.Add(time.Duration(request) * config.RequestInterval)
			nodeB := domain.NodeSnapshot{
				NodeID: "game-b", Connected: true,
				HealthStatus: domain.HealthHealthy, HealthScore: 85,
			}
			// 故障期：game-b 变 unhealthy（会被策略置零权重）
			if request >= config.DetectionRequest && request < config.RecoveryRequest {
				nodeB.HealthStatus = domain.HealthUnhealthy
				nodeB.HealthScore = 5
			}
			// 恢复期：game-b 带 RecoveryStartedAt，触发渐进增权
			if request >= config.RecoveryRequest {
				nodeB.RecoveryStartedAt = startedAt.Add(
					time.Duration(config.RecoveryRequest) * config.RequestInterval)
			}
			assignments := policy.Compute([]domain.NodeSnapshot{
				{NodeID: "game-a", Connected: true, HealthStatus: domain.HealthHealthy, HealthScore: 90},
				nodeB,
			}, now)
			selector = routing.NewSelector(routing.Snapshot{Nodes: assignments})
		}
		nodeID, _ := selector.Next()
		return nodeID
	}
	result := run(config, "adaptive", selectNode)
	removal := config.DetectionRequest - config.FailureStart
	recovery := config.RecoveryRequest - config.FailureEnd
	result.RemovalDelayRequests = &removal
	result.RecoveryDelayRequests = &recovery
	return result
}

// run 执行一次实验：按 selectNode 分配请求，模拟延迟与失败，统计结果。
// 延迟与失败都是确定性公式，保证可复现。
func run(config RoutingConfig, strategy string, selectNode func(int) string) Result {
	latencies := make([]float64, 0, config.Requests)
	allocations := map[string]int{}
	errors := 0
	failedNodeRequests := 0
	for request := range config.Requests {
		nodeID := selectNode(request)
		allocations[nodeID]++
		// 故障窗口内命中 game-b 的请求会失败（每第 4 个）
		failed := nodeID == "game-b" && request >= config.FailureStart && request < config.FailureEnd
		latency := float64(8 + request%7) // 基础延迟 8~14ms（确定性）
		if nodeID == "game-b" {
			latency += 2 // game-b 略慢
		}
		if failed {
			failedNodeRequests++
			latency = float64(180 + request%41) // 故障延迟 180~220ms
			if failedNodeRequests%4 == 0 {
				errors++ // 每 4 个故障请求失败 1 个
			}
		}
		latencies = append(latencies, latency)
	}
	sort.Float64s(latencies)
	return Result{
		Strategy:         strategy,
		Requests:         config.Requests,
		Errors:           errors,
		ErrorRatePercent: round(float64(errors) / float64(config.Requests) * 100),
		P50Milliseconds:  percentile(latencies, 0.50),
		P95Milliseconds:  percentile(latencies, 0.95),
		P99Milliseconds:  percentile(latencies, 0.99),
		Allocations:      allocations,
	}
}

// percentile：在已排序切片上取分位数。
func percentile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

// round：保留 4 位小数。
func round(value float64) float64 {
	return math.Round(value*10_000) / 10_000
}
