package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ccKccK-JF/SentinelMesh/internal/store"
)

type Server struct {
	store *store.Memory
}

func New(memory *store.Memory) *Server {
	return &Server{store: memory}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/nodes", s.listNodes)
	mux.HandleFunc("GET /v1/nodes/{nodeID}", s.getNode)
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) listNodes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"nodes": s.store.List()})
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.PathValue("nodeID"))
	node, ok := s.store.Get(nodeID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not found"})
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
