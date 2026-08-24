#pragma once

#include <filesystem>
#include <memory>
#include <optional>
#include <string>

#include "sentinel/latency_histogram.hpp"

namespace sentinel {

struct BlockIoLatencySummary {
  std::optional<LatencySummary> read;
  std::optional<LatencySummary> write;
};

class BlockIoLatencyProbe {
 public:
  BlockIoLatencyProbe();
  ~BlockIoLatencyProbe();

  BlockIoLatencyProbe(const BlockIoLatencyProbe&) = delete;
  BlockIoLatencyProbe& operator=(const BlockIoLatencyProbe&) = delete;

  bool Open(const std::filesystem::path& bpf_object_path);
  std::optional<BlockIoLatencySummary> Collect();

  [[nodiscard]] const std::string& last_error() const noexcept {
    return last_error_;
  }

 private:
  struct Impl;
  std::unique_ptr<Impl> impl_;
  LatencyHistogram previous_read_{};
  LatencyHistogram previous_write_{};
  std::string last_error_;
};

}  // namespace sentinel
