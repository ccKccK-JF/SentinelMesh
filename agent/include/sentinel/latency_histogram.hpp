#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <optional>

namespace sentinel {

constexpr std::size_t kLatencyHistogramSlots = 32;
using LatencyHistogram = std::array<std::uint64_t, kLatencyHistogramSlots>;

struct LatencySummary {
  std::uint64_t events{};
  double p95_microseconds{};
  double p99_microseconds{};
};

std::optional<LatencySummary> SummarizeLatencyHistogram(
    const LatencyHistogram& current, const LatencyHistogram& previous);

}  // namespace sentinel
