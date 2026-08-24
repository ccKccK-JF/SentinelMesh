package scoring

import (
	"testing"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
)

func TestComputeHealthyNode(t *testing.T) {
	scorer := New(DefaultConfig())
	result := scorer.Compute(map[string]domain.Metric{
		CPUUtilization:    {Value: 20},
		MemoryUtilization: {Value: 30},
		LoadNormalized:    {Value: 0.2},
		DiskUtilization:   {Value: 10},
	})

	if result.Status != domain.HealthHealthy {
		t.Fatalf("expected healthy, got %s (score %.2f)", result.Status, result.Score)
	}
	if result.Score < 70 || result.Score > 100 {
		t.Fatalf("unexpected score %.2f", result.Score)
	}
}

func TestComputeAppliesHardLimit(t *testing.T) {
	scorer := New(DefaultConfig())
	result := scorer.Compute(map[string]domain.Metric{
		CPUUtilization:    {Value: 10},
		MemoryUtilization: {Value: 98.5},
	})

	if result.Status != domain.HealthUnhealthy {
		t.Fatalf("expected unhealthy, got %s", result.Status)
	}
	if result.Reason != "memory hard limit exceeded" {
		t.Fatalf("unexpected reason %q", result.Reason)
	}
}

func TestComputeUnknownWithoutSupportedMetrics(t *testing.T) {
	result := New(DefaultConfig()).Compute(map[string]domain.Metric{
		"custom.metric": {Value: 1},
	})
	if result.Status != domain.HealthUnknown {
		t.Fatalf("expected unknown, got %s", result.Status)
	}
}
