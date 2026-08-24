#include "sentinel/telemetry_client.hpp"

#include <chrono>
#include <utility>

namespace sentinel {
namespace {

void AddMetric(sentinel::v1::MetricBatch* batch, const std::string& name,
               double value, const std::string& unit) {
  auto* metric = batch->add_metrics();
  metric->set_name(name);
  metric->set_value(value);
  metric->set_unit(unit);
}

}  // namespace

TelemetryClient::TelemetryClient(std::string address)
    : channel_(grpc::CreateChannel(std::move(address),
                                   grpc::InsecureChannelCredentials())),
      stub_(sentinel::v1::TelemetryService::NewStub(channel_)) {}

TelemetryClient::~TelemetryClient() { Abort(); }

bool TelemetryClient::Connect(const AgentIdentity& identity) {
  Abort();
  last_error_.clear();
  accepted_sequence_ = 0;
  config_version_ = 0;

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

bool TelemetryClient::SendSnapshot(std::uint64_t sequence,
                                   std::int64_t observed_at_unix_nano,
                                   const Snapshot& snapshot) {
  sentinel::v1::TelemetryEnvelope envelope;
  auto* batch = envelope.mutable_batch();
  batch->set_sequence(sequence);
  batch->set_observed_at_unix_nano(observed_at_unix_nano);
  AddMetric(batch, "cpu.utilization.percent", snapshot.cpu_utilization_percent,
            "percent");
  AddMetric(batch, "memory.utilization.percent",
            snapshot.memory_utilization_percent, "percent");
  AddMetric(batch, "system.load.normalized", snapshot.load_normalized, "ratio");
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
  // A resent batch is successful when the server already accepted the same
  // sequence before the previous connection lost its ACK.
  if (acknowledgement.accepted_sequence() < sequence) {
    last_error_ = "collector did not acknowledge metric sequence " +
                  std::to_string(sequence) + ": " + acknowledgement.message();
    return false;
  }
  accepted_sequence_ = acknowledgement.accepted_sequence();
  return true;
}

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

void TelemetryClient::Abort() {
  if (context_) context_->TryCancel();
  if (stream_) {
    (void)stream_->Finish();
  }
  stream_.reset();
  context_.reset();
}

}  // namespace sentinel
