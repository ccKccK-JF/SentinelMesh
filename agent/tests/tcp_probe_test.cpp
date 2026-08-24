#include <cassert>
#include <iostream>

#include "sentinel/latency_histogram.hpp"

int main() {
  sentinel::LatencyHistogram previous{};
  sentinel::LatencyHistogram current{};
  current[14] = 80;
  current[15] = 20;

  const auto summary = sentinel::SummarizeLatencyHistogram(current, previous);
  assert(summary.has_value());
  assert(summary->events == 100);
  assert(summary->p95_microseconds == 32768.0);
  assert(summary->p99_microseconds == 32768.0);

  std::cout << "TCP RTT latency summary tests passed\n";
  return 0;
}
