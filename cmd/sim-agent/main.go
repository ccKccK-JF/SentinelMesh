// ============================================================================
// cmd/sim-agent/main.go
// ----------------------------------------------------------------------------
// 模拟 Agent：不需要 Linux/eBPF，直接在开发机上扮演节点角色，
// 向控制面上报可控的指标批次。用于快速验证控制面链路。
//
// 用法：go run ./cmd/sim-agent --node-id game-1
// 可以指定固定指标值（--cpu 95）来制造“过载”场景，观察路由摘除。
// ============================================================================

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"time"

	sentinelv1 "github.com/ccKccK-JF/SentinelMesh/gen/go/sentinel/v1"
	"github.com/ccKccK-JF/SentinelMesh/internal/scoring"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	address := flag.String("address", "127.0.0.1:50051", "control-plane gRPC address")
	nodeID := flag.String("node-id", "game-1", "simulated node ID")
	count := flag.Int("count", 3, "number of metric batches")
	interval := flag.Duration("interval", time.Second, "interval between batches")
	// 固定指标值；为负则使用“基准+随机”的模拟值
	cpu := flag.Float64("cpu", -1, "fixed CPU utilization; negative uses random values")
	memory := flag.Float64("memory", -1, "fixed memory utilization; negative uses random values")
	load := flag.Float64("load", -1, "fixed normalized load; negative uses random values")
	disk := flag.Float64("disk", -1, "fixed disk utilization; negative uses random values")
	flag.Parse()

	// 总超时：给批次间隔留足余量
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*count+5)*(*interval)+5*time.Second)
	defer cancel()
	connection, err := grpc.NewClient(*address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer connection.Close()
	stream, err := sentinelv1.NewTelemetryServiceClient(connection).Stream(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// ---- Hello：模拟 boot_id 每次启动都不同（模拟 Agent 重启）----
	hostname, _ := os.Hostname()
	bootID := fmt.Sprintf("sim-%d", time.Now().UnixNano())
	if err := stream.Send(&sentinelv1.TelemetryEnvelope{Payload: &sentinelv1.TelemetryEnvelope_Hello{
		Hello: &sentinelv1.AgentHello{
			NodeId:       *nodeID,
			Hostname:     hostname,
			IpAddress:    "127.0.0.1",
			AgentVersion: "sim-agent/dev",
			BootId:       bootID,
		},
	}}); err != nil {
		log.Fatal(err)
	}
	receiveAck(stream)

	// ---- 按 sequence 递增发送批次 ----
	for sequence := 1; sequence <= *count; sequence++ {
		metrics := []*sentinelv1.MetricSample{
			// 使用与真实 Agent 一致的指标名（来自 scoring 常量）
			{Name: scoring.CPUUtilization, Value: sample(*cpu, 25, 20), Unit: "percent"},
			{Name: scoring.MemoryUtilization, Value: sample(*memory, 40, 10), Unit: "percent"},
			{Name: scoring.LoadNormalized, Value: sample(*load, 0.3, 0.2), Unit: "ratio"},
			{Name: scoring.DiskUtilization, Value: sample(*disk, 15, 10), Unit: "percent"},
		}
		if err := stream.Send(&sentinelv1.TelemetryEnvelope{Payload: &sentinelv1.TelemetryEnvelope_Batch{
			Batch: &sentinelv1.MetricBatch{
				Sequence:           uint64(sequence),
				ObservedAtUnixNano: time.Now().UnixNano(),
				Metrics:            metrics,
			},
		}}); err != nil {
			log.Fatal(err)
		}
		receiveAck(stream)
		if sequence < *count {
			time.Sleep(*interval)
		}
	}
	_ = stream.CloseSend()
}

// sample：指定了固定值则返回固定值，否则返回 base + rand[0, spread)。
func sample(fixed, base, spread float64) float64 {
	if fixed >= 0 {
		return fixed
	}
	return base + rand.Float64()*spread
}

// receiveAck 阻塞读取一条 ACK 并打印（模拟 Agent 同步等待确认）。
func receiveAck(stream grpc.BidiStreamingClient[sentinelv1.TelemetryEnvelope, sentinelv1.CollectorAck]) {
	ack, err := stream.Recv()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("ack sequence=%d accepted=%d dropped=%d message=%q",
		ack.AcceptedSequence, ack.AcceptedSamples, ack.DroppedSamples, ack.Message)
}
