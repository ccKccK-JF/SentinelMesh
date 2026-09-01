// ============================================================================
// telemetry_client.hpp —— gRPC 遥测客户端接口
// ----------------------------------------------------------------------------
// 负责与 Go 控制面建立双向流、执行 Hello 注册、发送批次并解析 ACK。
// 幂等语义体现在 Connect() 返回 accepted_sequence，调用方据此续传。
// ============================================================================

#pragma once

#include <cstdint>
#include <memory>
#include <string>

#include <grpcpp/grpcpp.h>

#include "sentinel/metrics.hpp"
#include "sentinel/v1/telemetry.grpc.pb.h"

namespace sentinel {

// Agent 身份：控制面用它识别“哪台机器的哪次启动周期”。
struct AgentIdentity {
  std::string node_id;       // 节点 ID
  std::string hostname;      // 主机名
  std::string ip_address;    // IP（当前为空）
  std::string agent_version; // Agent 版本
  std::string boot_id;       // /sys/kernel/random/boot_id（启动周期标识）
};

class TelemetryClient {
 public:
  explicit TelemetryClient(std::string address);
  ~TelemetryClient();

  // 禁止拷贝：内部持有 gRPC 流句柄，不可复制
  TelemetryClient(const TelemetryClient&) = delete;
  TelemetryClient& operator=(const TelemetryClient&) = delete;

  // 连接并发送 Hello；成功后 accepted_sequence() 为服务端已接受的最大序列
  bool Connect(const AgentIdentity& identity);
  // 发送一个批次，等待 ACK；只有 accepted_sequence >= sequence 才算成功
  bool SendSnapshot(std::uint64_t sequence, std::int64_t observed_at_unix_nano,
                    const Snapshot& snapshot);
  // 发送心跳（当前 main 流程未使用）
  bool SendHeartbeat(std::uint64_t last_sequence,
                     std::int64_t sent_at_unix_nano);
  // 正常关闭流
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
  // 一次“写 Envelope + 读 ACK”的请求-响应配对
  bool Exchange(const sentinel::v1::TelemetryEnvelope& envelope,
                sentinel::v1::CollectorAck* acknowledgement);
  // 强制取消并回收流资源
  void Abort();

  std::shared_ptr<grpc::Channel> channel_;
  std::unique_ptr<sentinel::v1::TelemetryService::Stub> stub_;
  std::unique_ptr<grpc::ClientContext> context_;
  std::unique_ptr<grpc::ClientReaderWriter<sentinel::v1::TelemetryEnvelope,
                                           sentinel::v1::CollectorAck>> stream_;
  std::uint64_t accepted_sequence_{0}; // 服务端已接受的最大序列
  std::uint64_t config_version_{0};    // 服务端下发的配置版本
  std::string last_error_;
};

}  // namespace sentinel
