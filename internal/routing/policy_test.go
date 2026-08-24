package routing

import (
	"testing"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
)

func TestPolicyExcludesUnavailableNodesAndNormalizesExactly(t *testing.T) {
	now := time.Unix(1000, 0)
	assignments := New(DefaultConfig()).Compute([]domain.NodeSnapshot{
		{NodeID: "healthy", Connected: true, HealthStatus: domain.HealthHealthy, HealthScore: 80},
		{NodeID: "degraded", Connected: true, HealthStatus: domain.HealthDegraded, HealthScore: 80},
		{NodeID: "unhealthy", Connected: true, HealthStatus: domain.HealthUnhealthy, HealthScore: 5},
		{NodeID: "offline", Connected: false, HealthStatus: domain.HealthHealthy, HealthScore: 90},
		{NodeID: "unknown", Connected: true, HealthScore: 90},
	}, now)

	byID := make(map[string]Assignment)
	total := uint32(0)
	for _, assignment := range assignments {
		byID[assignment.NodeID] = assignment
		total += assignment.Weight
	}
	if total != 10_000 {
		t.Fatalf("weights must total 10000, got %d", total)
	}
	if byID["healthy"].Weight != 6667 || byID["degraded"].Weight != 3333 {
		t.Fatalf("unexpected normalized weights: %+v", assignments)
	}
	if byID["unhealthy"].Eligible || byID["unhealthy"].Weight != 0 {
		t.Fatalf("unhealthy node remained eligible: %+v", byID["unhealthy"])
	}
	if byID["offline"].Reason != "agent_disconnected" {
		t.Fatalf("unexpected offline reason: %+v", byID["offline"])
	}
	if byID["unknown"].Eligible || byID["unknown"].Reason != "health_unknown" {
		t.Fatalf("zero-value status became eligible: %+v", byID["unknown"])
	}
}

func TestPolicyRampsRecoveredNode(t *testing.T) {
	config := DefaultConfig()
	config.RecoveryRamp = time.Minute
	policy := New(config)
	start := time.Unix(2000, 0)
	nodes := []domain.NodeSnapshot{
		{NodeID: "stable", Connected: true, HealthStatus: domain.HealthHealthy, HealthScore: 80},
		{NodeID: "recovered", Connected: true, HealthStatus: domain.HealthHealthy, HealthScore: 80, RecoveryStartedAt: start},
	}

	initial := policy.Compute(nodes, start)
	if initial[0].NodeID != "recovered" || initial[0].Weight != 909 || initial[1].Weight != 9091 {
		t.Fatalf("unexpected initial ramp weights: %+v", initial)
	}
	middle := policy.Compute(nodes, start.Add(30*time.Second))
	if middle[0].Weight != 3548 || middle[1].Weight != 6452 {
		t.Fatalf("unexpected half-ramp weights: %+v", middle)
	}
	complete := policy.Compute(nodes, start.Add(time.Minute))
	if complete[0].Weight != 5000 || complete[1].Weight != 5000 {
		t.Fatalf("unexpected completed ramp weights: %+v", complete)
	}
}

func TestSelectorMatchesWeightsAndExcludesIneligible(t *testing.T) {
	selector := NewSelector(Snapshot{Nodes: []Assignment{
		{NodeID: "a", Weight: 7500, Eligible: true},
		{NodeID: "b", Weight: 2500, Eligible: true},
		{NodeID: "c", Weight: 9000, Eligible: false},
	}})
	counts := map[string]int{}
	for range 10_000 {
		nodeID, ok := selector.Next()
		if !ok {
			t.Fatal("selector unexpectedly empty")
		}
		counts[nodeID]++
	}
	if counts["a"] != 7500 || counts["b"] != 2500 || counts["c"] != 0 {
		t.Fatalf("unexpected allocation counts: %+v", counts)
	}
}

func TestSelectorReturnsFalseWithoutEligibleNodes(t *testing.T) {
	if _, ok := NewSelector(Snapshot{}).Next(); ok {
		t.Fatal("empty selector returned a node")
	}
}
