package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/ccKccK-JF/SentinelMesh/internal/experiment"
)

func main() {
	jsonOutput := flag.Bool("json", false, "emit JSON instead of Markdown")
	flag.Parse()
	results := experiment.RunRoutingComparison(experiment.DefaultRoutingConfig())
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(results); err != nil {
			panic(err)
		}
		return
	}

	fmt.Println("# Round Robin 与自适应路由确定性对照")
	fmt.Println()
	fmt.Println("场景：12,000个请求；`game-b`在请求2,000至6,999期间故障；控制面延迟200个请求摘除，并在故障结束300个请求后开始恢复渐进增权。")
	fmt.Println()
	fmt.Println("| 策略 | 请求数 | 错误数 | 错误率 | P50 | P95 | P99 | game-a分配 | game-b分配 |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, result := range results {
		fmt.Printf("| %s | %d | %d | %.4f%% | %.0f ms | %.0f ms | %.0f ms | %d | %d |\n",
			result.Strategy, result.Requests, result.Errors, result.ErrorRatePercent,
			result.P50Milliseconds, result.P95Milliseconds, result.P99Milliseconds,
			result.Allocations["game-a"], result.Allocations["game-b"])
	}
	fmt.Println()
	fmt.Println("> 这是确定性故障模型，用于回归路由算法，不替代真实服务器压测。真实stress-ng/fio/netem结果单独记录。")
}
