package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ccKccK-JF/SentinelMesh/internal/routing"
	"github.com/ccKccK-JF/SentinelMesh/internal/scoring"
	"github.com/ccKccK-JF/SentinelMesh/internal/store"
)

func TestRoutingEndpointSupportsVersionETag(t *testing.T) {
	memory := store.NewMemory(scoring.New(scoring.DefaultConfig()))
	memory.Connect(store.Hello{NodeID: "game-1", BootID: "boot-1"})
	handler := New(memory).Handler()

	request := httptest.NewRequest(http.MethodGet, "/v1/routing", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"1"` {
		t.Fatalf("unexpected routing response: code=%d etag=%q", recorder.Code, recorder.Header().Get("ETag"))
	}
	var snapshot routing.Snapshot
	if err := json.NewDecoder(recorder.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode routing response: %v", err)
	}
	if snapshot.Version != 1 || len(snapshot.Nodes) != 1 {
		t.Fatalf("unexpected routing snapshot: %+v", snapshot)
	}

	conditional := httptest.NewRequest(http.MethodGet, "/v1/routing", nil)
	conditional.Header.Set("If-None-Match", `"1"`)
	conditionalRecorder := httptest.NewRecorder()
	handler.ServeHTTP(conditionalRecorder, conditional)
	if conditionalRecorder.Code != http.StatusNotModified || conditionalRecorder.Body.Len() != 0 {
		t.Fatalf("expected empty 304 response, got code=%d body=%q",
			conditionalRecorder.Code, conditionalRecorder.Body.String())
	}
}
