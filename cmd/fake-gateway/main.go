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
