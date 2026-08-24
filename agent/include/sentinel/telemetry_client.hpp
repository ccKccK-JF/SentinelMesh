#pragma once

#include <cstdint>
#include <memory>
#include <string>

#include <grpcpp/grpcpp.h>

#include "sentinel/metrics.hpp"
#include "sentinel/v1/telemetry.grpc.pb.h"

namespace sentinel {

struct AgentIdentity {
  std::string node_id;
  std::string hostname;
  std::string ip_address;
  std::string agent_version;
  std::string boot_id;
};

class TelemetryClient {
 public:
  explicit TelemetryClient(std::string address);
  ~TelemetryClient();

  TelemetryClient(const TelemetryClient&) = delete;
  TelemetryClient& operator=(const TelemetryClient&) = delete;

  bool Connect(const AgentIdentity& identity);
  bool SendSnapshot(std::uint64_t sequence, std::int64_t observed_at_unix_nano,
                    const Snapshot& snapshot);
  bool SendHeartbeat(std::uint64_t last_sequence,
                     std::int64_t sent_at_unix_nano);
  void Close();

  [[nodiscard]] std::uint64_t accepted_sequence() const noexcept {
    return accepted_sequence_;
  }
  [[nodiscard]] std::uint64_t config_version() const noexcept {
    return config_version_;
  }
  [[nodiscard]] const std::string& last_error() const noexcept {
    return last_error_;
  }

 private:
  bool Exchange(const sentinel::v1::TelemetryEnvelope& envelope,
                sentinel::v1::CollectorAck* acknowledgement);
  void Abort();

  std::shared_ptr<grpc::Channel> channel_;
  std::unique_ptr<sentinel::v1::TelemetryService::Stub> stub_;
  std::unique_ptr<grpc::ClientContext> context_;
  std::unique_ptr<grpc::ClientReaderWriter<sentinel::v1::TelemetryEnvelope,
                                           sentinel::v1::CollectorAck>> stream_;
  std::uint64_t accepted_sequence_{0};
  std::uint64_t config_version_{0};
  std::string last_error_;
};

}  // namespace sentinel
