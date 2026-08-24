package ingest

import (
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	sentinelv1 "github.com/ccKccK-JF/SentinelMesh/gen/go/sentinel/v1"
	"github.com/ccKccK-JF/SentinelMesh/internal/domain"
	"github.com/ccKccK-JF/SentinelMesh/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxSamplesPerBatch = 2048
	maxEventsPerBatch  = 1024
)

var nodeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

type Server struct {
	sentinelv1.UnimplementedTelemetryServiceServer
	store         *store.Memory
	configVersion uint64
}

func NewServer(memory *store.Memory) *Server {
	return &Server{store: memory, configVersion: 1}
}

func (s *Server) Stream(stream grpc.BidiStreamingServer[sentinelv1.TelemetryEnvelope, sentinelv1.CollectorAck]) error {
	var nodeID string
	defer func() {
		if nodeID != "" {
			s.store.Disconnect(nodeID)
		}
	}()

	for {
		envelope, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		switch payload := envelope.Payload.(type) {
		case *sentinelv1.TelemetryEnvelope_Hello:
			if nodeID != "" {
				return status.Error(codes.AlreadyExists, "agent hello was already received")
			}
			hello := payload.Hello
			if hello == nil || !nodeIDPattern.MatchString(hello.NodeId) {
				return status.Error(codes.InvalidArgument, "node_id must match [a-zA-Z0-9][a-zA-Z0-9._-]{0,127}")
			}
			nodeID = hello.NodeId
			snapshot := s.store.Connect(store.Hello{
				NodeID:       hello.NodeId,
				Hostname:     hello.Hostname,
				IPAddress:    hello.IpAddress,
				AgentVersion: hello.AgentVersion,
				BootID:       hello.BootId,
			})
			if err := stream.Send(&sentinelv1.CollectorAck{
				AcceptedSequence: snapshot.LastSequence,
				ConfigVersion:    s.configVersion,
				Message:          "agent registered",
			}); err != nil {
				return err
			}

		case *sentinelv1.TelemetryEnvelope_Batch:
			if nodeID == "" {
				return status.Error(codes.FailedPrecondition, "agent hello must be the first message")
			}
			batch := payload.Batch
			if batch == nil || batch.Sequence == 0 {
				return status.Error(codes.InvalidArgument, "batch sequence must be greater than zero")
			}
			metrics, accepted, dropped := sanitizeMetrics(batch.Metrics)
			eventCount := len(batch.KernelEvents)
			if eventCount > maxEventsPerBatch {
				dropped += eventCount - maxEventsPerBatch
				eventCount = maxEventsPerBatch
			}
			observedAt := time.Unix(0, batch.ObservedAtUnixNano)
			if batch.ObservedAtUnixNano <= 0 {
				observedAt = time.Now()
			}
			snapshot, applyErr := s.store.ApplyBatch(nodeID, batch.Sequence, observedAt, metrics, eventCount)
			ack := &sentinelv1.CollectorAck{
				AcceptedSequence: snapshot.LastSequence,
				ConfigVersion:    s.configVersion,
				AcceptedSamples:  uint32(accepted),
				DroppedSamples:   uint32(dropped),
				Message:          "batch accepted",
			}
			if errors.Is(applyErr, store.ErrSequenceNotNewer) {
				ack.AcceptedSamples = 0
				ack.DroppedSamples += uint32(accepted)
				ack.Message = store.ErrSequenceNotNewer.Error()
			} else if applyErr != nil {
				return status.Errorf(codes.Internal, "apply batch: %v", applyErr)
			}
			if err := stream.Send(ack); err != nil {
				return err
			}

		case *sentinelv1.TelemetryEnvelope_Heartbeat:
			if nodeID == "" {
				return status.Error(codes.FailedPrecondition, "agent hello must be the first message")
			}
			snapshot, err := s.store.Touch(nodeID)
			if err != nil {
				return status.Errorf(codes.Internal, "touch node: %v", err)
			}
			if err := stream.Send(&sentinelv1.CollectorAck{
				AcceptedSequence: snapshot.LastSequence,
				ConfigVersion:    s.configVersion,
				Message:          "heartbeat accepted",
			}); err != nil {
				return err
			}

		default:
			return status.Error(codes.InvalidArgument, "telemetry envelope has no payload")
		}
	}
}

func sanitizeMetrics(samples []*sentinelv1.MetricSample) (map[string]domain.Metric, int, int) {
	metrics := make(map[string]domain.Metric)
	accepted := 0
	dropped := 0
	for index, sample := range samples {
		if index >= maxSamplesPerBatch {
			dropped++
			continue
		}
		if sample == nil || strings.TrimSpace(sample.Name) == "" || math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			dropped++
			continue
		}
		name := metricKey(strings.TrimSpace(sample.Name), sample.Labels)
		metrics[name] = domain.Metric{
			Value:  sample.Value,
			Unit:   strings.TrimSpace(sample.Unit),
			Labels: copyLabels(sample.Labels),
		}
		accepted++
	}
	return metrics, accepted, dropped
}

func metricKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, labels[key]))
	}
	return name + "{" + strings.Join(parts, ",") + "}"
}

func copyLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		result[key] = value
	}
	return result
}
