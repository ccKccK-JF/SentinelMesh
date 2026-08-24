#include <cassert>
#include <cmath>
#include <iostream>

#include "sentinel/latency_histogram.hpp"

int main() {
  sentinel::LatencyHistogram previous{};
  sentinel::LatencyHistogram current{};
  current[6] = 95;
  current[10] = 5;

  const auto summary = sentinel::SummarizeLatencyHistogram(current, previous);
  assert(summary.has_value());
  assert(summary->events == 100);
  assert(std::abs(summary->p95_microseconds - 64.0) < 0.001);
  assert(std::abs(summary->p99_microseconds - 1024.0) < 0.001);

  std::cout << "block I/O latency summary tests passed\n";
  return 0;
}
