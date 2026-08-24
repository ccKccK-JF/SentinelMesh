#pragma once

#include <cstdint>
#include <optional>
#include <string>
#include <vector>

namespace sentinel {

struct CpuTimes {
  std::uint64_t user{};
  std::uint64_t nice{};
  std::uint64_t system{};
  std::uint64_t idle{};
  std::uint64_t io_wait{};
  std::uint64_t irq{};
  std::uint64_t soft_irq{};
  std::uint64_t steal{};

  [[nodiscard]] std::uint64_t Total() const noexcept {
    return user + nice + system + idle + io_wait + irq + soft_irq + steal;
  }

  [[nodiscard]] std::uint64_t IdleAll() const noexcept { return idle + io_wait; }
};

struct MemoryInfo {
  std::uint64_t total_kib{};
  std::uint64_t available_kib{};
};

struct NetCounters {
  std::string interface_name;
  std::uint64_t receive_bytes{};
  std::uint64_t transmit_bytes{};
  std::uint64_t receive_drops{};
  std::uint64_t transmit_drops{};
};

struct NetRate {
  std::string interface_name;
  double receive_bytes_per_second{};
  double transmit_bytes_per_second{};
  std::uint64_t receive_drops{};
  std::uint64_t transmit_drops{};
};

struct Snapshot {
  double cpu_utilization_percent{};
  double memory_utilization_percent{};
  double load_normalized{};
  std::optional<double> scheduler_run_queue_p95_microseconds;
  std::optional<double> scheduler_run_queue_p99_microseconds;
  std::optional<std::uint64_t> scheduler_run_queue_events;
  std::optional<double> block_io_read_p95_microseconds;
  std::optional<double> block_io_read_p99_microseconds;
  std::optional<std::uint64_t> block_io_read_events;
  std::optional<double> block_io_write_p95_microseconds;
  std::optional<double> block_io_write_p99_microseconds;
  std::optional<std::uint64_t> block_io_write_events;
  std::optional<double> tcp_rtt_p95_microseconds;
  std::optional<double> tcp_rtt_p99_microseconds;
  std::optional<std::uint64_t> tcp_rtt_samples;
  std::optional<std::uint64_t> tcp_retransmissions;
  std::optional<std::uint64_t> tcp_receive_resets;
  std::optional<std::uint64_t> tcp_send_resets;
  std::vector<NetRate> network;
};

}  // namespace sentinel
