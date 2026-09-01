// ============================================================================
// internal/ingest/server.go
// ----------------------------------------------------------------------------
// gRPC 遥测摄入层：把 C++ Agent 上报的 Envelope 流翻译成 Store 操作。
//
// 这里是“协议边界”，核心职责：
//   1. 流的生命周期管理：Hello 必须在第一条，流结束时标记节点断线；
//   2. 输入校验：node_id 格式、sequence > 0、指标名非空、值非 NaN/Inf；
//   3. 容量限制：单批最多 2048 个指标、1024 个内核事件，超限统一计数丢弃；
//   4. 幂等：把“重复批次”转换成 ACK 回执而不是报错，让 Agent 能安全重发。
//
// 面试要点：错误分类很讲究——协议错误（invalid_argument）、状态错误
// （failed_precondition）、重复批次（不算错误，只是 ACK 里说明 dropped）。
// ============================================================================

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
	maxSamplesPerBatch = 2048 // 单批最大指标条数（超出即丢弃，防止放大攻击/异常 Agent）
	maxEventsPerBatch  = 1024 // 单批最大内核事件条数（与 Agent 端 1024 上限对齐）
)

// nodeIDPattern：node_id 白名单校验。
// 以字母数字开头，后续允许字母数字以及 . _ -，最长 128 字符。
var nodeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// Server 实现 proto 生成的 TelemetryServiceServer 接口。
// 通过组合 UnimplementedTelemetryServiceServer 保证未来 proto 加方法时
// 仍能编译通过（缺省实现返回 Unimplemented 错误）。
type Server struct {
	sentinelv1.UnimplementedTelemetryServiceServer
	store         *store.Memory // 指向全局唯一的内存 Store
	configVersion uint64        // 配置版本号，随 ACK 下发给 Agent（当前固定 1）
}

func NewServer(memory *store.Memory) *Server {
	return &Server{store: memory, configVersion: 1}
}

// Stream 是 gRPC 双向流处理函数。grpc-go 会为每个流分配一个 goroutine。
//
// 协议约定：
//   - 第一条消息必须是 Hello（nodeID 在该流内被记住）；
//   - 之后的 Batch / Heartbeat 都关联到该 nodeID；
//   - 流结束（EOF 或错误）时 defer 中调用 Disconnect，把节点标记为离线。
//
// 幂等语义：重复批次（sequence <= last_sequence）不会报错，
// 而是返回带 dropped 计数的 ACK，Agent 据此认为“这个序号已被接受”。
func (s *Server) Stream(stream grpc.BidiStreamingServer[sentinelv1.TelemetryEnvelope, sentinelv1.CollectorAck]) error {
	var nodeID string
	defer func() {
		// 无论正常结束还是异常断开，只要注册过 nodeID 就要标记离线，
		// 否则节点会一直停留在 Connected=true 状态影响路由权重。
		if nodeID != "" {
			s.store.Disconnect(nodeID)
		}
	}()

	for {
		envelope, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			// 客户端正常关闭写方向（WritesDone），服务端优雅结束流。
			return nil
		}
		if err != nil {
			return err
		}

		// 利用 proto oneof 的 Go 表示做类型分派
		switch payload := envelope.Payload.(type) {

		case *sentinelv1.TelemetryEnvelope_Hello:
			// Hello 只能出现一次
			if nodeID != "" {
				return status.Error(codes.AlreadyExists, "agent hello was already received")
			}
			hello := payload.Hello
			if hello == nil || !nodeIDPattern.MatchString(hello.NodeId) {
				return status.Error(codes.InvalidArgument, "node_id must match [a-zA-Z0-9][a-zA-Z0-9._-]{0,127}")
			}
			nodeID = hello.NodeId
			// Connect 会处理 Boot ID 变化（新启动周期则重置评分状态机），
			// 并返回当前快照（内含 last_sequence）用于幂等续传。
			snapshot := s.store.Connect(store.Hello{
				NodeID:       hello.NodeId,
				Hostname:     hello.Hostname,
				IPAddress:    hello.IpAddress,
				AgentVersion: hello.AgentVersion,
				BootID:       hello.BootId,
			})
			// 回执里带回已接受的最大序列，Agent 据此从 next = last+1 续传，
			// 避免重复发送已处理的批次（同 Boot ID 场景）。
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
			// 清洗指标：过滤空名/NaN/Inf，超限计数；返回去重后的 map、接受数与丢弃数。
			metrics, accepted, dropped := sanitizeMetrics(batch.Metrics)
			// 内核事件同样有上限
			eventCount := len(batch.KernelEvents)
			if eventCount > maxEventsPerBatch {
				dropped += eventCount - maxEventsPerBatch
				eventCount = maxEventsPerBatch
			}
			// 采样时间：非法（<=0）时回退到服务端当前时间，保证快照有可用时间戳。
			observedAt := time.Unix(0, batch.ObservedAtUnixNano)
			if batch.ObservedAtUnixNano <= 0 {
				observedAt = time.Now()
			}
			// 关键路径：写锁内合并指标 -> 评分状态机 -> 刷新路由快照。
			snapshot, applyErr := s.store.ApplyBatch(nodeID, batch.Sequence, observedAt, metrics, eventCount)
			ack := &sentinelv1.CollectorAck{
				AcceptedSequence: snapshot.LastSequence,
				ConfigVersion:    s.configVersion,
				AcceptedSamples:  uint32(accepted),
				DroppedSamples:   uint32(dropped),
				Message:          "batch accepted",
			}
			if errors.Is(applyErr, store.ErrSequenceNotNewer) {
				// 重复/乱序批次：不重算，但 ACK 里如实说明——
				// 把本批指标计入 dropped，并告诉 Agent 当前已接受序列。
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
			// 心跳只刷新 LastSeen，不触发评分与路由重算。
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

// sanitizeMetrics 清洗并去重指标样本。
// 返回：干净的 metrics map（key=name{labels}）、接受数、丢弃数。
func sanitizeMetrics(samples []*sentinelv1.MetricSample) (map[string]domain.Metric, int, int) {
	metrics := make(map[string]domain.Metric)
	accepted := 0
	dropped := 0
	for index, sample := range samples {
		if index >= maxSamplesPerBatch {
			dropped++ // 超过单批上限：直接丢弃并计数
			continue
		}
		if sample == nil || strings.TrimSpace(sample.Name) == "" || math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			dropped++ // 非法样本：空名或非有限数值
			continue
		}
		// 相同 name+labels 的样本在 map 里合并（后者覆盖前者）
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

// metricKey 把 name 与 labels 编码成稳定 key。
// labels 先按 key 排序，保证 map 迭代顺序不影响 key 的确定性。
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

// copyLabels 深拷贝 label map，避免与 proto 对象共享底层数组造成数据竞争。
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
