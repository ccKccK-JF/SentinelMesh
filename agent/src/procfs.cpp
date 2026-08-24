#include "sentinel/procfs.hpp"

#include <algorithm>
#include <fstream>
#include <iomanip>
#include <limits>
#include <sstream>
#include <stdexcept>
#include <string>
#include <thread>
#include <utility>

namespace sentinel {
namespace {

std::uint64_t Delta(std::uint64_t current, std::uint64_t previous) {
  return current >= previous ? current - previous : 0;
}

std::string Trim(std::string value) {
  const auto first = value.find_first_not_of(" \t\r\n");
  if (first == std::string::npos) return {};
  const auto last = value.find_last_not_of(" \t\r\n");
  return value.substr(first, last - first + 1);
}

}  // namespace

CpuTimes ParseCpuStat(std::string_view content) {
  std::istringstream input{std::string(content)};
  std::string cpu;
  CpuTimes result;
  if (!(input >> cpu >> result.user >> result.nice >> result.system >> result.idle >>
        result.io_wait >> result.irq >> result.soft_irq >> result.steal) || cpu != "cpu") {
    throw std::runtime_error("invalid /proc/stat cpu line");
  }
  return result;
}

MemoryInfo ParseMemInfo(std::string_view content) {
  std::istringstream input{std::string(content)};
  std::string line;
  MemoryInfo result;
  bool available_seen = false;
  std::uint64_t free_kib = 0;
  std::uint64_t buffers_kib = 0;
  std::uint64_t cached_kib = 0;
  while (std::getline(input, line)) {
    std::istringstream row(line);
    std::string key;
    std::uint64_t value = 0;
    row >> key >> value;
    if (key == "MemTotal:") result.total_kib = value;
    if (key == "MemAvailable:") {
      result.available_kib = value;
      available_seen = true;
    }
    if (key == "MemFree:") free_kib = value;
    if (key == "Buffers:") buffers_kib = value;
    if (key == "Cached:") cached_kib = value;
  }
  if (result.total_kib == 0) {
    throw std::runtime_error("MemTotal is missing from /proc/meminfo");
  }
  if (!available_seen) {
    result.available_kib = free_kib + buffers_kib + cached_kib;
  }
  return result;
}

double ParseLoadAverage(std::string_view content) {
  std::istringstream input{std::string(content)};
  double load_one = 0;
  if (!(input >> load_one)) {
    throw std::runtime_error("invalid /proc/loadavg");
  }
  return load_one;
}

std::vector<NetCounters> ParseNetDev(std::string_view content) {
  std::istringstream input{std::string(content)};
  std::string line;
  std::vector<NetCounters> result;
  while (std::getline(input, line)) {
    const auto colon = line.find(':');
    if (colon == std::string::npos) continue;

    NetCounters counters;
    counters.interface_name = Trim(line.substr(0, colon));
    std::istringstream values(line.substr(colon + 1));
    std::uint64_t receive_packets = 0;
    std::uint64_t receive_errors = 0;
    std::uint64_t receive_fifo = 0;
    std::uint64_t receive_frame = 0;
    std::uint64_t receive_compressed = 0;
    std::uint64_t receive_multicast = 0;
    std::uint64_t transmit_packets = 0;
    std::uint64_t transmit_errors = 0;
    std::uint64_t transmit_fifo = 0;
    std::uint64_t transmit_collisions = 0;
    std::uint64_t transmit_carrier = 0;
    std::uint64_t transmit_compressed = 0;
    if (values >> counters.receive_bytes >> receive_packets >> receive_errors >>
            counters.receive_drops >> receive_fifo >> receive_frame >>
            receive_compressed >> receive_multicast >> counters.transmit_bytes >>
            transmit_packets >> transmit_errors >> counters.transmit_drops >>
            transmit_fifo >> transmit_collisions >> transmit_carrier >>
            transmit_compressed) {
      result.push_back(std::move(counters));
    }
  }
  return result;
}

ProcfsCollector::ProcfsCollector(std::filesystem::path proc_root)
    : proc_root_(std::move(proc_root)),
      cpu_count_(std::max(1u, std::thread::hardware_concurrency())) {}

Snapshot ProcfsCollector::Collect() {
  const auto now = std::chrono::steady_clock::now();
  const CpuTimes cpu = ParseCpuStat(Read("stat"));
  const MemoryInfo memory = ParseMemInfo(Read("meminfo"));
  const double load_one = ParseLoadAverage(Read("loadavg"));
  const auto network = ParseNetDev(Read("net/dev"));

  Snapshot snapshot;
  snapshot.memory_utilization_percent =
      (1.0 - static_cast<double>(memory.available_kib) /
                 static_cast<double>(memory.total_kib)) *
      100.0;
  snapshot.load_normalized = load_one / static_cast<double>(cpu_count_);

  if (previous_cpu_) {
    const auto total_delta = Delta(cpu.Total(), previous_cpu_->Total());
    const auto idle_delta = Delta(cpu.IdleAll(), previous_cpu_->IdleAll());
    if (total_delta > 0) {
      snapshot.cpu_utilization_percent =
          static_cast<double>(total_delta - std::min(total_delta, idle_delta)) /
          static_cast<double>(total_delta) * 100.0;
    }
  }

  double elapsed_seconds = 0;
  if (previous_time_) {
    elapsed_seconds = std::chrono::duration<double>(now - *previous_time_).count();
  }
  for (const auto& current : network) {
    NetRate rate;
    rate.interface_name = current.interface_name;
    rate.receive_drops = current.receive_drops;
    rate.transmit_drops = current.transmit_drops;
    const auto found = previous_network_.find(current.interface_name);
    if (found != previous_network_.end() && elapsed_seconds > 0) {
      rate.receive_bytes_per_second =
          static_cast<double>(Delta(current.receive_bytes, found->second.receive_bytes)) /
          elapsed_seconds;
      rate.transmit_bytes_per_second =
          static_cast<double>(Delta(current.transmit_bytes, found->second.transmit_bytes)) /
          elapsed_seconds;
    }
    snapshot.network.push_back(std::move(rate));
    previous_network_[current.interface_name] = current;
  }

  previous_cpu_ = cpu;
  previous_time_ = now;
  return snapshot;
}

std::string ProcfsCollector::Read(std::string_view relative_path) const {
  const auto path = proc_root_ / std::filesystem::path(relative_path);
  std::ifstream input(path);
  if (!input) {
    throw std::runtime_error("cannot read " + path.string());
  }
  std::ostringstream content;
  content << input.rdbuf();
  return content.str();
}

std::string ToJson(const Snapshot& snapshot) {
  std::ostringstream output;
  output << std::fixed << std::setprecision(2);
  output << "{\"metrics\":{";
  output << "\"cpu.utilization.percent\":" << snapshot.cpu_utilization_percent << ',';
  output << "\"memory.utilization.percent\":"
         << snapshot.memory_utilization_percent << ',';
  output << "\"system.load.normalized\":" << snapshot.load_normalized;
  if (snapshot.scheduler_run_queue_p95_microseconds) {
    output << ",\"scheduler.run_queue.latency.p95.microseconds\":"
           << *snapshot.scheduler_run_queue_p95_microseconds;
  }
  if (snapshot.scheduler_run_queue_p99_microseconds) {
    output << ",\"scheduler.run_queue.latency.p99.microseconds\":"
           << *snapshot.scheduler_run_queue_p99_microseconds;
  }
  if (snapshot.scheduler_run_queue_events) {
    output << ",\"scheduler.run_queue.events\":"
           << *snapshot.scheduler_run_queue_events;
  }
  if (snapshot.block_io_read_p95_microseconds) {
    output << ",\"block.io.read.latency.p95.microseconds\":"
           << *snapshot.block_io_read_p95_microseconds;
  }
  if (snapshot.block_io_read_p99_microseconds) {
    output << ",\"block.io.read.latency.p99.microseconds\":"
           << *snapshot.block_io_read_p99_microseconds;
  }
  if (snapshot.block_io_read_events) {
    output << ",\"block.io.read.events\":" << *snapshot.block_io_read_events;
  }
  if (snapshot.block_io_write_p95_microseconds) {
    output << ",\"block.io.write.latency.p95.microseconds\":"
           << *snapshot.block_io_write_p95_microseconds;
  }
  if (snapshot.block_io_write_p99_microseconds) {
    output << ",\"block.io.write.latency.p99.microseconds\":"
           << *snapshot.block_io_write_p99_microseconds;
  }
  if (snapshot.block_io_write_events) {
    output << ",\"block.io.write.events\":"
           << *snapshot.block_io_write_events;
  }
  if (snapshot.tcp_rtt_p95_microseconds) {
    output << ",\"tcp.rtt.p95.microseconds\":"
           << *snapshot.tcp_rtt_p95_microseconds;
  }
  if (snapshot.tcp_rtt_p99_microseconds) {
    output << ",\"tcp.rtt.p99.microseconds\":"
           << *snapshot.tcp_rtt_p99_microseconds;
  }
  if (snapshot.tcp_rtt_samples) {
    output << ",\"tcp.rtt.samples\":" << *snapshot.tcp_rtt_samples;
  }
  if (snapshot.tcp_retransmissions) {
    output << ",\"tcp.retransmissions\":" << *snapshot.tcp_retransmissions;
  }
  if (snapshot.tcp_receive_resets) {
    output << ",\"tcp.receive_resets\":" << *snapshot.tcp_receive_resets;
  }
  if (snapshot.tcp_send_resets) {
    output << ",\"tcp.send_resets\":" << *snapshot.tcp_send_resets;
  }
  output << "},\"network\":[";
  for (std::size_t i = 0; i < snapshot.network.size(); ++i) {
    if (i != 0) output << ',';
    const auto& net = snapshot.network[i];
    output << "{\"interface\":\"" << net.interface_name << "\",";
    output << "\"receive_bytes_per_second\":" << net.receive_bytes_per_second << ',';
    output << "\"transmit_bytes_per_second\":" << net.transmit_bytes_per_second << ',';
    output << "\"receive_drops\":" << net.receive_drops << ',';
    output << "\"transmit_drops\":" << net.transmit_drops << '}';
  }
  output << "]}";
  return output.str();
}

}  // namespace sentinel
