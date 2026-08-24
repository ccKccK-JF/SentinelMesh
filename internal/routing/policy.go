package routing

import (
	"math"
	"sort"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
)

type Config struct {
	TotalWeight       uint32
	DegradedFactor    float64
	MinRecoveryFactor float64
	RecoveryRamp      time.Duration
}

func DefaultConfig() Config {
	return Config{
		TotalWeight:       10_000,
		DegradedFactor:    0.5,
		MinRecoveryFactor: 0.1,
		RecoveryRamp:      time.Minute,
	}
}

type Assignment struct {
	NodeID       string              `json:"node_id"`
	Weight       uint32              `json:"weight"`
	Eligible     bool                `json:"eligible"`
	HealthScore  float64             `json:"health_score"`
	HealthStatus domain.HealthStatus `json:"health_status"`
	RampFactor   float64             `json:"ramp_factor"`
	Reason       string              `json:"reason"`
}

type Snapshot struct {
	Version     uint64       `json:"version"`
	GeneratedAt time.Time    `json:"generated_at"`
	Nodes       []Assignment `json:"nodes"`
}

type Policy struct {
	config Config
}

func New(config Config) Policy {
	defaults := DefaultConfig()
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

func (p Policy) Compute(nodes []domain.NodeSnapshot, now time.Time) []Assignment {
	sorted := append([]domain.NodeSnapshot(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].NodeID < sorted[j].NodeID })
	assignments := make([]Assignment, len(sorted))
	rawWeights := make([]float64, len(sorted))
	totalRaw := 0.0

	for index, node := range sorted {
		assignment := Assignment{
			NodeID:       node.NodeID,
			HealthScore:  node.HealthScore,
			HealthStatus: node.HealthStatus,
			RampFactor:   1,
		}
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

		if assignment.Eligible {
			raw := clamp(node.HealthScore, 1, 100)
			if node.HealthStatus == domain.HealthDegraded {
				raw *= p.config.DegradedFactor
			}
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

	if totalRaw == 0 {
		return assignments
	}
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
	sort.SliceStable(remainders, func(i, j int) bool {
		if remainders[i].value == remainders[j].value {
			return assignments[remainders[i].index].NodeID < assignments[remainders[j].index].NodeID
		}
		return remainders[i].value > remainders[j].value
	})
	for remaining, index := p.config.TotalWeight-assigned, 0; remaining > 0; remaining, index = remaining-1, index+1 {
		assignments[remainders[index%len(remainders)].index].Weight++
	}
	return assignments
}

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

func cloneAssignments(source []Assignment) []Assignment {
	return append([]Assignment(nil), source...)
}

func Clone(snapshot Snapshot) Snapshot {
	snapshot.Nodes = cloneAssignments(snapshot.Nodes)
	return snapshot
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
