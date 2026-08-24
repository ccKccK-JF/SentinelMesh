package scoring

import (
	"strings"
	"testing"
	"time"

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

func TestEvaluateAppliesEWMAAndFailureHysteresis(t *testing.T) {
	config := DefaultConfig()
	config.EWMAAlpha = 0.5
	scorer := New(config)
	now := time.Unix(100, 0)

	first := scorer.Evaluate("game-1", map[string]domain.Metric{
		CPUUtilization: {Value: 20},
	}, now)
	if first.Status != domain.HealthHealthy || first.Score != 80 {
		t.Fatalf("unexpected initial result: %+v", first)
	}

	second := scorer.Evaluate("game-1", map[string]domain.Metric{
		CPUUtilization: {Value: 80},
	}, now.Add(time.Second))
	if second.Score != 50 {
		t.Fatalf("expected EWMA score 50, got %.2f", second.Score)
	}
	if second.Status != domain.HealthHealthy ||
		!strings.Contains(second.Reason, "candidate degraded 1/2") {
		t.Fatalf("expected hysteresis to hold healthy: %+v", second)
	}
}

func TestEvaluateHardLimitCooldownAndRecovery(t *testing.T) {
	config := DefaultConfig()
	config.EWMAAlpha = 1
	config.FailureConsecutive = 2
	config.RecoveryConsecutive = 3
	config.RecoveryCooldown = 10 * time.Second
	scorer := New(config)
	start := time.Unix(200, 0)

	healthy := map[string]domain.Metric{CPUUtilization: {Value: 20}}
	degraded := map[string]domain.Metric{CPUUtilization: {Value: 50}}
	hard := map[string]domain.Metric{CPUUtilization: {Value: 100}}
	if result := scorer.Evaluate("game-1", healthy, start); result.Status != domain.HealthHealthy {
		t.Fatalf("expected initial healthy: %+v", result)
	}
	if result := scorer.Evaluate("game-1", degraded, start.Add(time.Second)); result.Status != domain.HealthHealthy {
		t.Fatalf("first degraded sample must be held: %+v", result)
	}
	if result := scorer.Evaluate("game-1", degraded, start.Add(2*time.Second)); result.Status != domain.HealthDegraded {
		t.Fatalf("second degraded sample must transition: %+v", result)
	}

	hardResult := scorer.Evaluate("game-1", hard, start.Add(3*time.Second))
	if hardResult.Status != domain.HealthUnhealthy || hardResult.Score != 5 {
		t.Fatalf("hard limit must bypass hysteresis: %+v", hardResult)
	}
	expectedRecovery := start.Add(13 * time.Second)
	if !hardResult.RecoveryNotBefore.Equal(expectedRecovery) {
		t.Fatalf("unexpected recovery deadline %s", hardResult.RecoveryNotBefore)
	}
	if result := scorer.Evaluate("game-1", healthy, start.Add(12*time.Second)); result.Status != domain.HealthUnhealthy || !strings.Contains(result.Reason, "recovery cooldown") {
		t.Fatalf("recovery before cooldown must be blocked: %+v", result)
	}
	for index := 0; index < 2; index++ {
		result := scorer.Evaluate("game-1", healthy, expectedRecovery.Add(time.Duration(index)*time.Second))
		if result.Status != domain.HealthUnhealthy {
			t.Fatalf("recovery sample %d must be held: %+v", index+1, result)
		}
	}
	recovered := scorer.Evaluate("game-1", healthy, expectedRecovery.Add(2*time.Second))
	if recovered.Status != domain.HealthHealthy || !recovered.RecoveryNotBefore.IsZero() {
		t.Fatalf("third stable sample must recover: %+v", recovered)
	}
}

func TestResetClearsPerNodeState(t *testing.T) {
	scorer := New(DefaultConfig())
	now := time.Unix(300, 0)
	_ = scorer.Evaluate("game-1", map[string]domain.Metric{
		CPUUtilization: {Value: 100},
	}, now)
	scorer.Reset("game-1")
	result := scorer.Evaluate("game-1", map[string]domain.Metric{
		CPUUtilization: {Value: 20},
	}, now.Add(time.Second))
	if result.Status != domain.HealthHealthy {
		t.Fatalf("reset state should accept initial healthy status: %+v", result)
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
