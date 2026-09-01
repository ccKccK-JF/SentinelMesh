// ============================================================================
// cmd/fake-gateway/main.go
// ----------------------------------------------------------------------------
// 假网关：模拟真实网关/负载均衡器如何消费路由快照。
//
// 演示三个能力：
//   1. 拉取版本化路由快照（/v1/routing）；
//   2. 用平滑加权轮询（SWRR）把 N 个请求分给节点；
//   3. 输出分配统计，验证权重是否按比例生效。
//
// 用法：先启动控制面和 sim-agent，再运行本程序。
// 配合 ETag/304：生产网关应缓存快照、携带 If-None-Match 轮询。
// ============================================================================

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/ccKccK-JF/SentinelMesh/internal/routing"
)

func main() {
	endpoint := flag.String("routing-url", "http://127.0.0.1:8080/v1/routing", "routing snapshot URL")
	requests := flag.Int("requests", 10_000, "number of synthetic requests to allocate")
	timeout := flag.Duration("timeout", 3*time.Second, "routing request timeout")
	flag.Parse()
	if *requests <= 0 {
		fatalf("requests must be positive")
	}

	// 拉取路由快照
	client := &http.Client{Timeout: *timeout}
	response, err := client.Get(*endpoint)
	if err != nil {
		fatalf("fetch routing snapshot: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fatalf("fetch routing snapshot: HTTP %s", response.Status)
	}
	var snapshot routing.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		fatalf("decode routing snapshot: %v", err)
	}

	// 用 SWRR 分配请求
	selector := routing.NewSelector(snapshot)
	allocations := make(map[string]int)
	for range *requests {
		nodeID, ok := selector.Next()
		if !ok {
			fatalf("routing version %d has no eligible nodes", snapshot.Version)
		}
		allocations[nodeID]++
	}
	result := struct {
		Version     uint64         `json:"version"`
		Requests    int            `json:"requests"`
		Allocations map[string]int `json:"allocations"`
	}{snapshot.Version, *requests, allocations}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fatalf("encode result: %v", err)
	}
}

func fatalf(format string, arguments ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "fake-gateway: "+format+"\n", arguments...)
	os.Exit(1)
}
