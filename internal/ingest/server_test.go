package ingest

import (
	"context"
	"net"
	"testing"
	"time"

	sentinelv1 "github.com/ccKccK-JF/SentinelMesh/gen/go/sentinel/v1"
	"github.com/ccKccK-JF/SentinelMesh/internal/scoring"
	"github.com/ccKccK-JF/SentinelMesh/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestTelemetryStreamAcceptsBatch(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	memory := store.NewMemory(scoring.New(scoring.DefaultConfig()))
	grpcServer := grpc.NewServer()
	sentinelv1.RegisterTelemetryServiceServer(grpcServer, NewServer(memory))
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer connection.Close()

	stream, err := sentinelv1.NewTelemetryServiceClient(connection).Stream(ctx)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if err := stream.Send(&sentinelv1.TelemetryEnvelope{Payload: &sentinelv1.TelemetryEnvelope_Hello{
		Hello: &sentinelv1.AgentHello{NodeId: "game-1", Hostname: "game-1", BootId: "boot-1"},
	}}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	helloAck, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive hello ack: %v", err)
	}
	if helloAck.AcceptedSequence != 0 {
		t.Fatalf("new agent should start at sequence zero, got %d", helloAck.AcceptedSequence)
	}

	if err := stream.Send(&sentinelv1.TelemetryEnvelope{Payload: &sentinelv1.TelemetryEnvelope_Batch{
		Batch: &sentinelv1.MetricBatch{
			Sequence:           1,
			ObservedAtUnixNano: time.Now().UnixNano(),
			Metrics: []*sentinelv1.MetricSample{
				{Name: scoring.CPUUtilization, Value: 30, Unit: "percent"},
				{Name: scoring.MemoryUtilization, Value: 40, Unit: "percent"},
			},
		},
	}}); err != nil {
		t.Fatalf("send batch: %v", err)
	}
	ack, err := stream.Recv()
	if err != nil {
		t.Fatalf("receive batch ack: %v", err)
	}
	if ack.AcceptedSequence != 1 || ack.AcceptedSamples != 2 {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	snapshot, ok := memory.Get("game-1")
	if !ok || snapshot.LastSequence != 1 {
		t.Fatalf("batch was not stored: %+v", snapshot)
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close first stream: %v", err)
	}
	_, _ = stream.Recv()

	reconnected, err := sentinelv1.NewTelemetryServiceClient(connection).Stream(ctx)
	if err != nil {
		t.Fatalf("reconnect stream: %v", err)
	}
	if err := reconnected.Send(&sentinelv1.TelemetryEnvelope{Payload: &sentinelv1.TelemetryEnvelope_Hello{
		Hello: &sentinelv1.AgentHello{NodeId: "game-1", Hostname: "game-1", BootId: "boot-1"},
	}}); err != nil {
		t.Fatalf("send reconnect hello: %v", err)
	}
	reconnectAck, err := reconnected.Recv()
	if err != nil {
		t.Fatalf("receive reconnect ack: %v", err)
	}
	if reconnectAck.AcceptedSequence != 1 {
		t.Fatalf("expected reconnect sequence 1, got %d", reconnectAck.AcceptedSequence)
	}
	_ = reconnected.CloseSend()
}
