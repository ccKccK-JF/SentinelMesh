package experiment

import (
	"math"
	"sort"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
	"github.com/ccKccK-JF/SentinelMesh/internal/routing"
)

type RoutingConfig struct {
	Requests         int
	FailureStart     int
	FailureEnd       int
	DetectionRequest int
	RecoveryRequest  int
	RebalanceEvery   int
	RequestInterval  time.Duration
}

func DefaultRoutingConfig() RoutingConfig {
	return RoutingConfig{
		Requests:         12_000,
		FailureStart:     2_000,
		FailureEnd:       7_000,
		DetectionRequest: 2_200,
		RecoveryRequest:  7_300,
		RebalanceEvery:   100,
		RequestInterval:  10 * time.Millisecond,
	}
}

type Result struct {
	Strategy              string         `json:"strategy"`
	Requests              int            `json:"requests"`
	Errors                int            `json:"errors"`
	ErrorRatePercent      float64        `json:"error_rate_percent"`
	P50Milliseconds       float64        `json:"p50_milliseconds"`
	P95Milliseconds       float64        `json:"p95_milliseconds"`
	P99Milliseconds       float64        `json:"p99_milliseconds"`
	Allocations           map[string]int `json:"allocations"`
	RemovalDelayRequests  *int           `json:"removal_delay_requests,omitempty"`
	RecoveryDelayRequests *int           `json:"recovery_delay_requests,omitempty"`
}

func RunRoutingComparison(config RoutingConfig) []Result {
	return []Result{
		runRoundRobin(config),
		runAdaptive(config),
	}
}

func runRoundRobin(config RoutingConfig) Result {
	return run(config, "round_robin", func(request int) string {
		if request%2 == 0 {
			return "game-a"
		}
		return "game-b"
	})
}

func runAdaptive(config RoutingConfig) Result {
	policy := routing.New(routing.DefaultConfig())
	startedAt := time.Unix(0, 0).UTC()
	var selector *routing.Selector
	selectNode := func(request int) string {
		if selector == nil || request%config.RebalanceEvery == 0 ||
			request == config.DetectionRequest || request == config.RecoveryRequest {
			now := startedAt.Add(time.Duration(request) * config.RequestInterval)
			nodeB := domain.NodeSnapshot{
				NodeID: "game-b", Connected: true,
				HealthStatus: domain.HealthHealthy, HealthScore: 85,
			}
			if request >= config.DetectionRequest && request < config.RecoveryRequest {
				nodeB.HealthStatus = domain.HealthUnhealthy
				nodeB.HealthScore = 5
			}
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

func run(config RoutingConfig, strategy string, selectNode func(int) string) Result {
	latencies := make([]float64, 0, config.Requests)
	allocations := map[string]int{}
	errors := 0
	failedNodeRequests := 0
	for request := range config.Requests {
		nodeID := selectNode(request)
		allocations[nodeID]++
		failed := nodeID == "game-b" && request >= config.FailureStart && request < config.FailureEnd
		latency := float64(8 + request%7)
		if nodeID == "game-b" {
			latency += 2
		}
		if failed {
			failedNodeRequests++
			latency = float64(180 + request%41)
			if failedNodeRequests%4 == 0 {
				errors++
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

func round(value float64) float64 {
	return math.Round(value*10_000) / 10_000
}
