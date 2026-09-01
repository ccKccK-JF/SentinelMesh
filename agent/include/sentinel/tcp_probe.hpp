// ============================================================================
// tcp_probe.hpp —— TCP 指标探针接口
// ----------------------------------------------------------------------------
// 封装 tcplat.bpf.o 的加载/attach/读取。
// 除了 per-CPU 直方图/计数器，还负责消费内核事件 Ring Buffer。
// ============================================================================

#pragma once

#include <array>
#include <cstdint>
#include <filesystem>
#include <memory>
#include <optional>
#include <string>

#include "sentinel/latency_histogram.hpp"
#include "sentinel/metrics.hpp"

namespace sentinel {

// 计数器数量（与内核侧 tcp_counters map 对齐：重传/收RST/发RST）
constexpr std::size_t kTcpCounterCount = 3;

// 一个窗口的 TCP 汇总
struct TcpMetricsWindow {
  std::optional<LatencySummary> rtt;  // RTT 直方图窗口增量（无样本则 nullopt）
  std::uint64_t retransmissions{};    // 窗口内重传次数
  std::uint64_t receive_resets{};     // 窗口内收到 RST 次数
  std::uint64_t send_resets{};        // 窗口内发出 RST 次数
  std::uint64_t ring_buffer_dropped{}; // 内核+用户态合计丢弃事件数
  std::vector<KernelEvent> events;    // 本窗口事件详情（最多 1024 条）
};

class TcpMetricsProbe {
 public:
  TcpMetricsProbe();
  ~TcpMetricsProbe();

  TcpMetricsProbe(const TcpMetricsProbe&) = delete;
  TcpMetricsProbe& operator=(const TcpMetricsProbe&) = delete;

  bool Open(const std::filesystem::path& bpf_object_path);
  std::optional<TcpMetricsWindow> Collect();

  [[nodiscard]] const std::string& last_error() const noexcept {
    return last_error_;
  }

 private:
  struct Impl;
  std::unique_ptr<Impl> impl_;
  LatencyHistogram previous_rtt_{};    // 上一窗口 RTT 累计直方图
  std::array<std::uint64_t, kTcpCounterCount> previous_counters_{}; // 上一窗口计数器
  std::uint64_t previous_ring_buffer_dropped_{}; // 上一窗口内核丢弃数
  std::string last_error_;
};

}  // namespace sentinel
