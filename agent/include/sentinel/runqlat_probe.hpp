#pragma once

#include <cstdint>
#include <filesystem>
#include <memory>
#include <optional>
#include <string>

#include "sentinel/latency_histogram.hpp"

namespace sentinel {

constexpr std::size_t kRunQueueLatencySlots = kLatencyHistogramSlots;
using RunQueueLatencyHistogram = LatencyHistogram;
using RunQueueLatencySummary = LatencySummary;

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
