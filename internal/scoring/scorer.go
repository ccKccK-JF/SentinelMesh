package scoring

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
)

const (
	CPUUtilization    = "cpu.utilization.percent"
	MemoryUtilization = "memory.utilization.percent"
	LoadNormalized    = "system.load.normalized"
	DiskUtilization   = "disk.io.utilization.percent"
)

type Config struct {
	CPUWeight              float64
	MemoryWeight           float64
	LoadWeight             float64
	DiskWeight             float64
	CPUHardLimitPercent    float64
	MemoryHardLimitPercent float64
	DiskHardLimitPercent   float64
	HealthyScore           float64
	DegradedScore          float64
	EWMAAlpha              float64
	FailureConsecutive     int
	RecoveryConsecutive    int
	RecoveryCooldown       time.Duration
}

func DefaultConfig() Config {
	return Config{
		CPUWeight:              0.35,
		MemoryWeight:           0.30,
		LoadWeight:             0.20,
		DiskWeight:             0.15,
		CPUHardLimitPercent:    99.5,
		MemoryHardLimitPercent: 98,
		DiskHardLimitPercent:   99.5,
		HealthyScore:           70,
		DegradedScore:          40,
		EWMAAlpha:              0.35,
		FailureConsecutive:     2,
		RecoveryConsecutive:    3,
		RecoveryCooldown:       30 * time.Second,
	}
}

type Result struct {
	Score             float64
	Status            domain.HealthStatus
	Reason            string
	ChangedAt         time.Time
	RecoveryNotBefore time.Time
}

type Scorer struct {
	config Config
	state  *stateStore
}

type stateStore struct {
	mu    sync.Mutex
	nodes map[string]*nodeState
}

type nodeState struct {
	ewma              map[string]float64
	status            domain.HealthStatus
	candidate         domain.HealthStatus
	candidateCount    int
	changedAt         time.Time
	recoveryNotBefore time.Time
}

func New(config Config) Scorer {
	defaults := DefaultConfig()
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

// Evaluate applies per-node EWMA smoothing and transition hysteresis. Raw hard
// limits bypass smoothing so an overloaded node is marked unhealthy immediately.
func (s Scorer) Evaluate(nodeID string, metrics map[string]domain.Metric, now time.Time) Result {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()

	state, ok := s.state.nodes[nodeID]
	if !ok {
		state = &nodeState{ewma: make(map[string]float64), status: domain.HealthUnknown}
		s.state.nodes[nodeID] = state
	}
	smoothed := s.smooth(state, metrics)
	raw := s.Compute(smoothed)
	if hard, exceeded := s.hardLimit(metrics); exceeded {
		raw = hard
		if state.status != domain.HealthUnhealthy {
			state.status = domain.HealthUnhealthy
			state.changedAt = now
		}
		state.candidate = domain.HealthUnknown
		state.candidateCount = 0
		state.recoveryNotBefore = now.Add(s.config.RecoveryCooldown)
		return s.result(raw, state)
	}

	if state.status == domain.HealthUnknown {
		state.status = raw.Status
		state.changedAt = now
		return s.result(raw, state)
	}
	if raw.Status == domain.HealthUnknown {
		raw.Status = state.status
		raw.Reason = "hysteresis holding " + string(state.status) + "; scoring metrics unavailable"
		return s.result(raw, state)
	}
	if raw.Status == state.status {
		state.candidate = domain.HealthUnknown
		state.candidateCount = 0
		return s.result(raw, state)
	}

	improving := healthRank(raw.Status) > healthRank(state.status)
	if improving && state.status == domain.HealthUnhealthy && now.Before(state.recoveryNotBefore) {
		state.candidate = domain.HealthUnknown
		state.candidateCount = 0
		raw.Status = state.status
		raw.Reason = "recovery cooldown until " + state.recoveryNotBefore.UTC().Format(time.RFC3339)
		return s.result(raw, state)
	}

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
		state.status = raw.Status
		state.changedAt = now
		state.candidate = domain.HealthUnknown
		state.candidateCount = 0
		if state.status == domain.HealthUnhealthy {
			state.recoveryNotBefore = now.Add(s.config.RecoveryCooldown)
		} else {
			state.recoveryNotBefore = time.Time{}
		}
		return s.result(raw, state)
	}

	candidate := raw.Status
	count := state.candidateCount
	raw.Status = state.status
	raw.Reason = fmt.Sprintf("hysteresis holding %s; candidate %s %d/%d",
		state.status, candidate, count, required)
	return s.result(raw, state)
}

func (s Scorer) Reset(nodeID string) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	delete(s.state.nodes, nodeID)
}

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

func (s Scorer) result(raw Result, state *nodeState) Result {
	raw.Status = state.status
	raw.ChangedAt = state.changedAt
	raw.RecoveryNotBefore = state.recoveryNotBefore
	return raw
}

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

func isScoringMetric(name string) bool {
	return name == CPUUtilization || name == MemoryUtilization ||
		name == LoadNormalized || name == DiskUtilization
}

func (s Scorer) Compute(metrics map[string]domain.Metric) Result {
	if len(metrics) == 0 {
		return Result{Status: domain.HealthUnknown, Reason: "no metrics received"}
	}

	if hard, exceeded := s.hardLimit(metrics); exceeded {
		return hard
	}

	weightedScore := 0.0
	weightSum := 0.0
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

	score := math.Round(weightedScore/weightSum*10000) / 100
	result := Result{Score: score, Status: domain.HealthUnhealthy}
	switch {
	case score >= s.config.HealthyScore:
		result.Status = domain.HealthHealthy
	case score >= s.config.DegradedScore:
		result.Status = domain.HealthDegraded
	}
	return result
}

func value(metrics map[string]domain.Metric, name string) float64 {
	metric, ok := metrics[name]
	if !ok {
		return math.Inf(-1)
	}
	return metric.Value
}

func percentAvailable(value float64) float64 {
	return 1 - value/100
}

func clamp(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
