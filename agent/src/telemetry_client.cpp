// ============================================================================
// agent/src/telemetry_client.cpp
// ----------------------------------------------------------------------------
// gRPC 遥测客户端：Agent 与 Go 控制面之间的“信使”。
//
// 设计要点：
//   1. 双向流：一条长期流承载 Hello / Batch / Heartbeat，每次 Exchange
//      都是“写一个 Envelope + 读一个 ACK”的请求-响应配对；
//   2. ACK 语义：只有 ack.accepted_sequence >= 本批 sequence 才认为成功。
//      这是幂等协议的关键——如果服务端已处理但 ACK 丢失（连接断开），
//      重连后服务端会返回已接受的序列号，Agent 据此跳过已处理的批次；
//   3. 资源管理：RAII，析构自动 Abort（取消 gRPC 上下文并回收流）。
// ============================================================================

#include "sentinel/telemetry_client.hpp"

#include <chrono>
#include <utility>

namespace sentinel {
namespace {

// 往 MetricBatch 里追加一个指标（便捷封装）。
void AddMetric(sentinel::v1::MetricBatch* batch, const std::string& name,
               double value, const std::string& unit) {
  auto* metric = batch->add_metrics();
  metric->set_name(name);
  metric->set_value(value);
  metric->set_unit(unit);
}

}  // namespace

// 构造：创建 gRPC Channel 与 Stub。
// 注意：这里用的是 InsecureChannelCredentials（无 TLS），
// 仅适用于开发环境；生产必须替换为 mTLS。
TelemetryClient::TelemetryClient(std::string address)
    : channel_(grpc::CreateChannel(std::move(address),
                                   grpc::InsecureChannelCredentials())),
      stub_(sentinel::v1::TelemetryService::NewStub(channel_)) {}

TelemetryClient::~TelemetryClient() { Abort(); }

// Connect 建立双向流并完成 Hello 注册。
// 流程：取消旧流 -> 等待 channel ready（5s 超时）-> 创建流 ->
//       发送 AgentHello -> 读取注册 ACK -> 记录 accepted_sequence/config_version。
// 返回值：accepted_sequence 表示服务端已接受的最大序列，供调用方续传。
bool TelemetryClient::Connect(const AgentIdentity& identity) {
  Abort(); // 清掉可能残留的旧流
  last_error_.clear();
  accepted_sequence_ = 0;
  config_version_ = 0;

  // 同步等待连接就绪（最多 5 秒）
  if (!channel_->WaitForConnected(std::chrono::system_clock::now() +
                                  std::chrono::seconds(5))) {
    last_error_ = "control plane connection timed out";
    return false;
  }

  context_ = std::make_unique<grpc::ClientContext>();
  stream_ = stub_->Stream(context_.get());
  if (!stream_) {
    last_error_ = "failed to create telemetry stream";
    return false;
  }

  // 第一条消息必须是 Hello（携带 boot_id 标识启动周期）
  sentinel::v1::TelemetryEnvelope envelope;
  auto* hello = envelope.mutable_hello();
  hello->set_node_id(identity.node_id);
  hello->set_hostname(identity.hostname);
  hello->set_ip_address(identity.ip_address);
  hello->set_agent_version(identity.agent_version);
  hello->set_boot_id(identity.boot_id);

  sentinel::v1::CollectorAck acknowledgement;
  if (!Exchange(envelope, &acknowledgement)) {
    return false;
  }
  accepted_sequence_ = acknowledgement.accepted_sequence();
  config_version_ = acknowledgement.config_version();
  return true;
}

// SendSnapshot 把一份采集快照打包成 MetricBatch 并发送，等待 ACK。
// 只有 ack.accepted_sequence >= sequence 才算成功。
// 重发场景：若服务端此前已接受该 sequence（ACK 丢失后重连），
// 服务端会拒绝重算但仍返回 accepted_sequence >= sequence，因此成功。
bool TelemetryClient::SendSnapshot(std::uint64_t sequence,
                                   std::int64_t observed_at_unix_nano,
                                   const Snapshot& snapshot) {
  sentinel::v1::TelemetryEnvelope envelope;
  auto* batch = envelope.mutable_batch();
  batch->set_sequence(sequence);
  batch->set_observed_at_unix_nano(observed_at_unix_nano);

  // ---- 基础资源指标（始终存在）----
  AddMetric(batch, "cpu.utilization.percent", snapshot.cpu_utilization_percent,
            "percent");
  AddMetric(batch, "memory.utilization.percent",
            snapshot.memory_utilization_percent, "percent");
  AddMetric(batch, "system.load.normalized", snapshot.load_normalized, "ratio");

  // ---- 可选内核指标（只有开启对应探针才存在）----
  if (snapshot.scheduler_run_queue_p95_microseconds) {
    AddMetric(batch, "scheduler.run_queue.latency.p95.microseconds",
              *snapshot.scheduler_run_queue_p95_microseconds, "microseconds");
  }
  if (snapshot.scheduler_run_queue_p99_microseconds) {
    AddMetric(batch, "scheduler.run_queue.latency.p99.microseconds",
              *snapshot.scheduler_run_queue_p99_microseconds, "microseconds");
  }
  if (snapshot.scheduler_run_queue_events) {
    AddMetric(batch, "scheduler.run_queue.events",
              static_cast<double>(*snapshot.scheduler_run_queue_events), "count");
  }
  if (snapshot.block_io_read_p95_microseconds) {
    AddMetric(batch, "block.io.read.latency.p95.microseconds",
              *snapshot.block_io_read_p95_microseconds, "microseconds");
  }
  if (snapshot.block_io_read_p99_microseconds) {
    AddMetric(batch, "block.io.read.latency.p99.microseconds",
              *snapshot.block_io_read_p99_microseconds, "microseconds");
  }
  if (snapshot.block_io_read_events) {
    AddMetric(batch, "block.io.read.events",
              static_cast<double>(*snapshot.block_io_read_events), "count");
  }
  if (snapshot.block_io_write_p95_microseconds) {
    AddMetric(batch, "block.io.write.latency.p95.microseconds",
              *snapshot.block_io_write_p95_microseconds, "microseconds");
  }
  if (snapshot.block_io_write_p99_microseconds) {
    AddMetric(batch, "block.io.write.latency.p99.microseconds",
              *snapshot.block_io_write_p99_microseconds, "microseconds");
  }
  if (snapshot.block_io_write_events) {
    AddMetric(batch, "block.io.write.events",
              static_cast<double>(*snapshot.block_io_write_events), "count");
  }
  if (snapshot.tcp_rtt_p95_microseconds) {
    AddMetric(batch, "tcp.rtt.p95.microseconds",
              *snapshot.tcp_rtt_p95_microseconds, "microseconds");
  }
  if (snapshot.tcp_rtt_p99_microseconds) {
    AddMetric(batch, "tcp.rtt.p99.microseconds",
              *snapshot.tcp_rtt_p99_microseconds, "microseconds");
  }
  if (snapshot.tcp_rtt_samples) {
    AddMetric(batch, "tcp.rtt.samples",
              static_cast<double>(*snapshot.tcp_rtt_samples), "count");
  }
  if (snapshot.tcp_retransmissions) {
    AddMetric(batch, "tcp.retransmissions",
              static_cast<double>(*snapshot.tcp_retransmissions), "count");
  }
  if (snapshot.tcp_receive_resets) {
    AddMetric(batch, "tcp.receive_resets",
              static_cast<double>(*snapshot.tcp_receive_resets), "count");
  }
  if (snapshot.tcp_send_resets) {
    AddMetric(batch, "tcp.send_resets",
              static_cast<double>(*snapshot.tcp_send_resets), "count");
  }
  if (snapshot.kernel_ring_buffer_dropped) {
    AddMetric(batch, "kernel.ring_buffer.dropped",
              static_cast<double>(*snapshot.kernel_ring_buffer_dropped),
              "count");
  }

  // ---- 内核异常事件（每条转换为 KernelEvent message）----
  for (const auto& event : snapshot.kernel_events) {
    auto* kernel_event = batch->add_kernel_events();
    kernel_event->set_type(event.type);
    kernel_event->set_observed_at_unix_nano(event.observed_at_unix_nano);
    kernel_event->set_process_id(event.process_id);
    kernel_event->set_process_name(event.process_name);
    kernel_event->set_latency_ns(event.latency_ns);
    for (const auto& [key, value] : event.attributes) {
      (*kernel_event->mutable_attributes())[key] = value;
    }
  }

  // ---- 网络速率（每条网卡对应 4 个带 interface 标签的指标）----
  for (const auto& network : snapshot.network) {
    auto* receive = batch->add_metrics();
    receive->set_name("network.receive.bytes_per_second");
    receive->set_value(network.receive_bytes_per_second);
    receive->set_unit("bytes_per_second");
    (*receive->mutable_labels())["interface"] = network.interface_name;

    auto* transmit = batch->add_metrics();
    transmit->set_name("network.transmit.bytes_per_second");
    transmit->set_value(network.transmit_bytes_per_second);
    transmit->set_unit("bytes_per_second");
    (*transmit->mutable_labels())["interface"] = network.interface_name;

    auto* receive_drops = batch->add_metrics();
    receive_drops->set_name("network.receive.drops_total");
    receive_drops->set_value(static_cast<double>(network.receive_drops));
    receive_drops->set_unit("count");
    (*receive_drops->mutable_labels())["interface"] = network.interface_name;

    auto* transmit_drops = batch->add_metrics();
    transmit_drops->set_name("network.transmit.drops_total");
    transmit_drops->set_value(static_cast<double>(network.transmit_drops));
    transmit_drops->set_unit("count");
    (*transmit_drops->mutable_labels())["interface"] = network.interface_name;
  }

  sentinel::v1::CollectorAck acknowledgement;
  if (!Exchange(envelope, &acknowledgement)) {
    return false;
  }
  config_version_ = acknowledgement.config_version();
  // 幂等成功判定：accepted_sequence >= 本批 sequence
  // （重复批次被拒绝时，服务端返回的 accepted_sequence 仍 >= sequence）。
  if (acknowledgement.accepted_sequence() < sequence) {
    last_error_ = "collector did not acknowledge metric sequence " +
                  std::to_string(sequence) + ": " + acknowledgement.message();
    return false;
  }
  accepted_sequence_ = acknowledgement.accepted_sequence();
  return true;
}

// SendHeartbeat 发送心跳（当前 main 流程未使用，保留 API 完整性）。
bool TelemetryClient::SendHeartbeat(std::uint64_t last_sequence,
                                    std::int64_t sent_at_unix_nano) {
  sentinel::v1::TelemetryEnvelope envelope;
  auto* heartbeat = envelope.mutable_heartbeat();
  heartbeat->set_last_sequence(last_sequence);
  heartbeat->set_sent_at_unix_nano(sent_at_unix_nano);

  sentinel::v1::CollectorAck acknowledgement;
  if (!Exchange(envelope, &acknowledgement)) {
    return false;
  }
  accepted_sequence_ = acknowledgement.accepted_sequence();
  config_version_ = acknowledgement.config_version();
  return true;
}

// Exchange 执行一次写-读配对。
// 任何一步失败都会 Abort 整条流（连接已不可用），返回 false。
bool TelemetryClient::Exchange(
    const sentinel::v1::TelemetryEnvelope& envelope,
    sentinel::v1::CollectorAck* acknowledgement) {
  if (!stream_) {
    last_error_ = "telemetry stream is not connected";
    return false;
  }
  if (!stream_->Write(envelope)) {
    last_error_ = "failed to write telemetry message";
    Abort();
    return false;
  }
  if (!stream_->Read(acknowledgement)) {
    last_error_ = "failed to read collector acknowledgement";
    Abort();
    return false;
  }
  return true;
}

// Close 正常关闭流（写完成 + 等待服务端结束）。
void TelemetryClient::Close() {
  if (!stream_) return;
  stream_->WritesDone();
  const grpc::Status status = stream_->Finish();
  if (!status.ok()) {
    last_error_ = status.error_message();
  }
  stream_.reset();
  context_.reset();
}

// Abort 强制取消流并回收资源（断线重连前、析构时调用）。
void TelemetryClient::Abort() {
  if (context_) context_->TryCancel();
  if (stream_) {
    (void)stream_->Finish();
  }
  stream_.reset();
  context_.reset();
}

}  // namespace sentinel
