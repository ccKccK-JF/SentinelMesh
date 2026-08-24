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
	cpu := flag.Float64("cpu", -1, "fixed CPU utilization; negative uses random values")
	memory := flag.Float64("memory", -1, "fixed memory utilization; negative uses random values")
	load := flag.Float64("load", -1, "fixed normalized load; negative uses random values")
	disk := flag.Float64("disk", -1, "fixed disk utilization; negative uses random values")
	flag.Parse()

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

	for sequence := 1; sequence <= *count; sequence++ {
		metrics := []*sentinelv1.MetricSample{
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

func sample(fixed, base, spread float64) float64 {
	if fixed >= 0 {
		return fixed
	}
	return base + rand.Float64()*spread
}

func receiveAck(stream grpc.BidiStreamingClient[sentinelv1.TelemetryEnvelope, sentinelv1.CollectorAck]) {
	ack, err := stream.Recv()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("ack sequence=%d accepted=%d dropped=%d message=%q",
		ack.AcceptedSequence, ack.AcceptedSamples, ack.DroppedSamples, ack.Message)
}
