#pragma once

#include <array>
#include <cstdint>
#include <filesystem>
#include <memory>
#include <optional>
#include <string>

namespace sentinel {

constexpr std::size_t kRunQueueLatencySlots = 32;
using RunQueueLatencyHistogram =
    std::array<std::uint64_t, kRunQueueLatencySlots>;

struct RunQueueLatencySummary {
  std::uint64_t events{};
  double p95_microseconds{};
  double p99_microseconds{};
};

std::optional<RunQueueLatencySummary> SummarizeRunQueueLatency(
    const RunQueueLatencyHistogram& current,
    const RunQueueLatencyHistogram& previous);

class RunQueueLatencyProbe {
 public:
  RunQueueLatencyProbe();
  ~RunQueueLatencyProbe();

  RunQueueLatencyProbe(const RunQueueLatencyProbe&) = delete;
  RunQueueLatencyProbe& operator=(const RunQueueLatencyProbe&) = delete;

  bool Open(const std::filesystem::path& bpf_object_path);
  std::optional<RunQueueLatencySummary> Collect();

  [[nodiscard]] const std::string& last_error() const noexcept {
    return last_error_;
  }

 private:
  struct Impl;
  std::unique_ptr<Impl> impl_;
  RunQueueLatencyHistogram previous_{};
  std::string last_error_;
};

}  // namespace sentinel
