#include "sentinel/latency_histogram.hpp"

#include <cmath>

namespace sentinel {
namespace {

double SlotUpperBoundMicroseconds(std::size_t slot) {
  if (slot == 0) return 1.0;
  return static_cast<double>(std::uint64_t{1} << slot);
}

double Quantile(const LatencyHistogram& delta, std::uint64_t total,
                double quantile) {
  const auto target = static_cast<std::uint64_t>(
      std::ceil(static_cast<long double>(total) * quantile));
  std::uint64_t cumulative = 0;
  for (std::size_t slot = 0; slot < delta.size(); ++slot) {
    cumulative += delta[slot];
    if (cumulative >= target) return SlotUpperBoundMicroseconds(slot);
  }
  return SlotUpperBoundMicroseconds(delta.size() - 1);
}

}  // namespace

std::optional<LatencySummary> SummarizeLatencyHistogram(
    const LatencyHistogram& current, const LatencyHistogram& previous) {
  LatencyHistogram delta{};
  std::uint64_t total = 0;
  for (std::size_t slot = 0; slot < current.size(); ++slot) {
    delta[slot] = current[slot] >= previous[slot]
                      ? current[slot] - previous[slot]
                      : current[slot];
    total += delta[slot];
  }
  if (total == 0) return std::nullopt;

  return LatencySummary{
      .events = total,
      .p95_microseconds = Quantile(delta, total, 0.95),
      .p99_microseconds = Quantile(delta, total, 0.99),
  };
}

}  // namespace sentinel
