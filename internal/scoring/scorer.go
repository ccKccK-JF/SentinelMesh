package scoring

import (
	"math"

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
	}
}

type Result struct {
	Score  float64
	Status domain.HealthStatus
	Reason string
}

type Scorer struct {
	config Config
}

func New(config Config) Scorer {
	return Scorer{config: config}
}

func (s Scorer) Compute(metrics map[string]domain.Metric) Result {
	if len(metrics) == 0 {
		return Result{Status: domain.HealthUnknown, Reason: "no metrics received"}
	}

	if value(metrics, CPUUtilization) >= s.config.CPUHardLimitPercent {
		return Result{Score: 5, Status: domain.HealthUnhealthy, Reason: "cpu hard limit exceeded"}
	}
	if value(metrics, MemoryUtilization) >= s.config.MemoryHardLimitPercent {
		return Result{Score: 5, Status: domain.HealthUnhealthy, Reason: "memory hard limit exceeded"}
	}
	if value(metrics, DiskUtilization) >= s.config.DiskHardLimitPercent {
		return Result{Score: 5, Status: domain.HealthUnhealthy, Reason: "disk hard limit exceeded"}
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
