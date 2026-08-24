#pragma once

#include <array>
#include <cstdint>
#include <filesystem>
#include <memory>
#include <optional>
#include <string>

#include "sentinel/latency_histogram.hpp"

namespace sentinel {

constexpr std::size_t kTcpCounterCount = 3;

struct TcpMetricsWindow {
  std::optional<LatencySummary> rtt;
  std::uint64_t retransmissions{};
  std::uint64_t receive_resets{};
  std::uint64_t send_resets{};
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
  LatencyHistogram previous_rtt_{};
  std::array<std::uint64_t, kTcpCounterCount> previous_counters_{};
  std::string last_error_;
};

}  // namespace sentinel
