package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNodeSnapshotOmitsZeroTransitionTimes(t *testing.T) {
	encoded, err := json.Marshal(NodeSnapshot{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(encoded)
	if strings.Contains(text, "health_changed_at") || strings.Contains(text, "recovery_not_before") {
		t.Fatalf("zero transition times should be omitted: %s", text)
	}
}
