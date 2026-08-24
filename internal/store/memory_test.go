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
