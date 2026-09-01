// ============================================================================
// internal/httpapi/server.go
// ----------------------------------------------------------------------------
// HTTP API 层：对外提供节点快照查询、路由权重查询、健康检查、Prometheus 指标。
//
// 路由表（Go 1.22+ 的 ServeMux 增强语法，支持路径参数与方法匹配）：
//   GET /healthz          存活探针
//   GET /metrics          Prometheus 文本指标
//   GET /v1/nodes         全部节点快照
//   GET /v1/nodes/{id}    单个节点快照
//   GET /v1/routing       版本化路由权重（支持 If-None-Match / ETag / 304）
//
// 面试要点：所有 handler 都只从 Store 读“深拷贝快照”，
// 不直接访问 Store 内部 map，因此与 gRPC 写路径天然无数据竞争。
// ============================================================================

package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/ccKccK-JF/SentinelMesh/internal/prometheus"
	"github.com/ccKccK-JF/SentinelMesh/internal/store"
)

// Server HTTP API 服务：持有 Store 引用。
type Server struct {
	store *store.Memory
}

func New(memory *store.Memory) *Server {
	return &Server{store: memory}
}

// Handler 组装 http.ServeMux。
// 注意 "GET /v1/nodes/{nodeID}" 这种带大括号的路径模式是 Go 1.22
// ServeMux 的路径参数语法，配合 r.PathValue("nodeID") 使用。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("GET /metrics", prometheus.New(s.store))
	mux.HandleFunc("GET /v1/nodes", s.listNodes)
	mux.HandleFunc("GET /v1/nodes/{nodeID}", s.getNode)
	mux.HandleFunc("GET /v1/routing", s.routing)
	return mux
}

// routing 返回版本化路由快照，并实现 HTTP 缓存协议：
//   - 用路由版本号做 ETag；
//   - 网关带 If-None-Match 且版本一致时返回 304（无响应体），
//     节省 JSON 序列化与传输成本。
func (s *Server) routing(w http.ResponseWriter, r *http.Request) {
	snapshot := s.store.Routing()
	etag := `"` + strconv.FormatUint(snapshot.Version, 10) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// health 存活探针：K8s/负载均衡器用它判断进程是否还活着。
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// listNodes 返回所有节点快照（Store.List 内部已排序）。
func (s *Server) listNodes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"nodes": s.store.List()})
}

// getNode 返回单个节点；不存在时 404。
func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.PathValue("nodeID"))
	node, ok := s.store.Get(nodeID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not found"})
		return
	}
	writeJSON(w, http.StatusOK, node)
}

// writeJSON 统一 JSON 写出：设置 Content-Type、写状态码、Encode。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
