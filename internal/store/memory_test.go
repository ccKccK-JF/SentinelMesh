package store

import (
	"errors"
	"testing"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
	"github.com/ccKccK-JF/SentinelMesh/internal/scoring"
)

func TestMemoryRejectsDuplicateSequence(t *testing.T) {
	memory := NewMemory(scoring.New(scoring.DefaultConfig()))
	memory.Connect(Hello{NodeID: "game-1", Hostname: "game-1", BootID: "boot-a"})
	metrics := map[string]domain.Metric{scoring.CPUUtilization: {Value: 20}}
	if _, err := memory.ApplyBatch("game-1", 1, time.Now(), metrics, 0); err != nil {
		t.Fatalf("first batch failed: %v", err)
	}
	if _, err := memory.ApplyBatch("game-1", 1, time.Now(), metrics, 0); !errors.Is(err, ErrSequenceNotNewer) {
		t.Fatalf("expected ErrSequenceNotNewer, got %v", err)
	}
}

func TestReconnectWithNewBootResetsSequence(t *testing.T) {
	memory := NewMemory(scoring.New(scoring.DefaultConfig()))
	memory.Connect(Hello{NodeID: "game-1", BootID: "boot-a"})
	_, _ = memory.ApplyBatch("game-1", 8, time.Now(), map[string]domain.Metric{}, 0)
	memory.Connect(Hello{NodeID: "game-1", BootID: "boot-b"})
	snapshot, err := memory.ApplyBatch("game-1", 1, time.Now(), map[string]domain.Metric{}, 0)
	if err != nil {
		t.Fatalf("new boot should reset sequence: %v", err)
	}
	if snapshot.LastSequence != 1 {
		t.Fatalf("expected sequence 1, got %d", snapshot.LastSequence)
	}
}

func TestNewBootResetsHealthState(t *testing.T) {
	config := scoring.DefaultConfig()
	config.RecoveryCooldown = time.Minute
	memory := NewMemory(scoring.New(config))
	now := time.Unix(400, 0)
	memory.now = func() time.Time { return now }
	memory.Connect(Hello{NodeID: "game-1", BootID: "boot-a"})
	unhealthy, err := memory.ApplyBatch("game-1", 1, now, map[string]domain.Metric{
		scoring.CPUUtilization: {Value: 100},
	}, 0)
	if err != nil || unhealthy.HealthStatus != domain.HealthUnhealthy {
		t.Fatalf("expected hard-limit unhealthy state: snapshot=%+v err=%v", unhealthy, err)
	}

	now = now.Add(time.Second)
	memory.Connect(Hello{NodeID: "game-1", BootID: "boot-b"})
	healthy, err := memory.ApplyBatch("game-1", 1, now, map[string]domain.Metric{
		scoring.CPUUtilization: {Value: 20},
	}, 0)
	if err != nil || healthy.HealthStatus != domain.HealthHealthy {
		t.Fatalf("new boot must reset cooldown and EWMA: snapshot=%+v err=%v", healthy, err)
	}
	if !healthy.RecoveryNotBefore.IsZero() || !healthy.RecoveryStartedAt.IsZero() {
		t.Fatalf("new boot retained recovery state: %+v", healthy)
	}
}

func TestRoutingVersionChangesOnlyWithAssignments(t *testing.T) {
	config := scoring.DefaultConfig()
	config.EWMAAlpha = 1
	memory := NewMemory(scoring.New(config))
	now := time.Unix(500, 0)
	memory.now = func() time.Time { return now }
	memory.Connect(Hello{NodeID: "game-1", BootID: "boot-a"})
	if version := memory.Routing().Version; version != 1 {
		t.Fatalf("connect should create routing version 1, got %d", version)
	}

	metrics := map[string]domain.Metric{scoring.CPUUtilization: {Value: 20}}
	if _, err := memory.ApplyBatch("game-1", 1, now, metrics, 0); err != nil {
		t.Fatalf("apply first batch: %v", err)
	}
	first := memory.Routing()
	if first.Version != 2 || len(first.Nodes) != 1 || first.Nodes[0].Weight != 10_000 || !first.Nodes[0].Eligible {
		t.Fatalf("unexpected first eligible routing snapshot: %+v", first)
	}

	now = now.Add(time.Second)
	if _, err := memory.ApplyBatch("game-1", 2, now, metrics, 0); err != nil {
		t.Fatalf("apply identical batch: %v", err)
	}
	if version := memory.Routing().Version; version != first.Version {
		t.Fatalf("unchanged assignment incremented version from %d to %d", first.Version, version)
	}

	memory.Connect(Hello{NodeID: "game-2", BootID: "boot-b"})
	if version := memory.Routing().Version; version != 3 {
		t.Fatalf("second node should increment version, got %d", version)
	}
	memory.Disconnect("game-1")
	latest := memory.Routing()
	if latest.Version != 4 || latest.Nodes[0].Eligible || latest.Nodes[0].Weight != 0 {
		t.Fatalf("disconnect did not remove game-1: %+v", latest)
	}
}
