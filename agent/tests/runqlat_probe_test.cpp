#include <cassert>
#include <cmath>
#include <iostream>

#include "sentinel/runqlat_probe.hpp"

int main() {
  sentinel::RunQueueLatencyHistogram previous{};
  sentinel::RunQueueLatencyHistogram current{};

  current[0] = 90;
  current[4] = 5;
  current[8] = 5;
  auto summary = sentinel::SummarizeRunQueueLatency(current, previous);
  assert(summary.has_value());
  assert(summary->events == 100);
  assert(std::abs(summary->p95_microseconds - 16.0) < 0.001);
  assert(std::abs(summary->p99_microseconds - 256.0) < 0.001);

  previous = current;
  current[1] += 3;
  summary = sentinel::SummarizeRunQueueLatency(current, previous);
  assert(summary.has_value());
  assert(summary->events == 3);
  assert(std::abs(summary->p95_microseconds - 2.0) < 0.001);

  previous = current;
  summary = sentinel::SummarizeRunQueueLatency(current, previous);
  assert(!summary.has_value());

  std::cout << "run queue latency summary tests passed\n";
  return 0;
}
