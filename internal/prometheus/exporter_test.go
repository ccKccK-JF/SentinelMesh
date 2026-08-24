package prometheus

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
	"github.com/ccKccK-JF/SentinelMesh/internal/scoring"
	"github.com/ccKccK-JF/SentinelMesh/internal/store"
)

func TestExporterRendersNodeAndAgentMetrics(t *testing.T) {
	memory := store.NewMemory(scoring.New(scoring.DefaultConfig()))
	memory.Connect(store.Hello{
		NodeID:   "game-1",
		Hostname: "game\"east\none",
		BootID:   "boot-1",
	})
	_, err := memory.ApplyBatch("game-1", 1, time.Unix(100, 0), map[string]domain.Metric{
		"cpu.utilization.percent": {Value: 42.5, Unit: "percent"},
		"network.receive.bytes_per_second{interface=eth0}": {
			Value: 128,
			Unit:  "bytes_per_second",
			Labels: map[string]string{
				"interface": "eth0",
			},
		},
	}, 2)
	if err != nil {
		t.Fatalf("apply batch: %v", err)
	}

	recorder := httptest.NewRecorder()
	New(memory).ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, expected := range []string{
		`sentinelmesh_cpu_utilization_percent{hostname="game\"east\none",node_id="game-1"} 42.5`,
		`sentinelmesh_network_receive_bytes_per_second{hostname="game\"east\none",interface="eth0",node_id="game-1"} 128`,
		`sentinelmesh_node_connected{hostname="game\"east\none",node_id="game-1"} 1`,
		`sentinelmesh_node_health_status{hostname="game\"east\none",node_id="game-1",status="degraded"} 1`,
		`sentinelmesh_node_kernel_events_total{hostname="game\"east\none",node_id="game-1"} 2`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in:\n%s", expected, body)
		}
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("unexpected content type %q", contentType)
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName("9.bad/name"); got != "_9_bad_name" {
		t.Fatalf("unexpected sanitized name %q", got)
	}
}
