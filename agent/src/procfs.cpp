// ============================================================================
// agent/src/procfs.cpp
// ----------------------------------------------------------------------------
// procfs 采集器：从 /proc 虚拟文件系统读取 Linux 内核暴露的累计计数器。
//
// 为什么用 procfs 而不是内核模块？
//   - CPU/内存/Load/网络这些“稳定、低频、累计”指标已被内核暴露为文本，
//     读取成本极低，无需内核模块（模块会增加崩溃面/版本适配/权限成本）。
//
// 核心技巧：差值法（Delta）。
//   /proc/stat 的 CPU 时间是系统启动以来的累计 jiffies；/proc/net/dev 的
//   字节数是累计计数。要得到“当前窗口的利用率/速率”，必须拿相邻两次
//   采样的差值除以时间间隔，而不是直接读一个百分比。
//
// 面试要点：内存利用率用 MemAvailable（可回收页缓存语义），
// 而不是 MemFree（后者会高估内存压力）。
// ============================================================================

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

// 差值保护：当前值小于上次值（计数器回绕）时返回 0，避免负数。
std::uint64_t Delta(std::uint64_t current, std::uint64_t previous) {
  return current >= previous ? current - previous : 0;
}

// 去除首尾空白（用于解析 /proc/net/dev 的接口名）。
std::string Trim(std::string value) {
  const auto first = value.find_first_not_of(" \t\r\n");
  if (first == std::string::npos) return {};
  const auto last = value.find_last_not_of(" \t\r\n");
  return value.substr(first, last - first + 1);
}

// JSON 字符串转义（自实现，避免引入 JSON 库）。
std::string JsonEscape(std::string_view value) {
  std::ostringstream escaped;
  for (const char character : value) {
    switch (character) {
      case '"':  escaped << "\\\""; break;
      case '\\': escaped << "\\\\"; break;
      case '\b': escaped << "\\b"; break;
      case '\f': escaped << "\\f"; break;
      case '\n': escaped << "\\n"; break;
      case '\r': escaped << "\\r"; break;
      case '\t': escaped << "\\t"; break;
      default:
        if (static_cast<unsigned char>(character) < 0x20) {
          escaped << "\\u" << std::hex << std::setw(4) << std::setfill('0')
                  << static_cast<int>(static_cast<unsigned char>(character))
                  << std::dec;
        } else {
          escaped << character;
        }
    }
  }
  return escaped.str();
}

}  // namespace

// 解析 /proc/stat 的 "cpu " 行（聚合所有核的时间片）。
// 格式：cpu  user nice system idle iowait irq softirq steal ...
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

// 解析 /proc/meminfo，取 MemTotal 与 MemAvailable。
// 老内核可能没有 MemAvailable，此时回退到 MemFree+Buffers+Cached 近似。
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
    // 老内核回退：可用 ≈ free + buffers + cached（页缓存可回收）
    result.available_kib = free_kib + buffers_kib + cached_kib;
  }
  return result;
}

// 解析 /proc/loadavg，只取 1 分钟负载。
// load 是“就绪任务数”，除以 CPU 数得到归一化负载（<=1 说明基本不排队）。
double ParseLoadAverage(std::string_view content) {
  std::istringstream input{std::string(content)};
  double load_one = 0;
  if (!(input >> load_one)) {
    throw std::runtime_error("invalid /proc/loadavg");
  }
  return load_one;
}

// 解析 /proc/net/dev：每行一个网卡的累计收发字节/丢包计数。
// 注意首两行是头部说明（Inter-| Receive ...），通过找 ':' 跳过无效行。
std::vector<NetCounters> ParseNetDev(std::string_view content) {
  std::istringstream input{std::string(content)};
  std::string line;
  std::vector<NetCounters> result;
  while (std::getline(input, line)) {
    const auto colon = line.find(':');
    if (colon == std::string::npos) continue; // 头部行，跳过

    NetCounters counters;
    counters.interface_name = Trim(line.substr(0, colon)); // ':' 左边是接口名
    // ':' 右边是按空格分隔的 16 个计数
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

// 构造采集器：proc_root 允许指向非 /proc 的目录（测试用夹具）。
// cpu_count_ 用于把 loadavg 归一化。
ProcfsCollector::ProcfsCollector(std::filesystem::path proc_root)
    : proc_root_(std::move(proc_root)),
      cpu_count_(std::max(1u, std::thread::hardware_concurrency())) {}

// Collect 执行一次完整采集。
// 用 steady_clock（单调时钟）记录采样时间；CPU 与网络速率依赖
// 与 previous 样本的差值，首次调用时拿不到速率（置 0）。
Snapshot ProcfsCollector::Collect() {
  const auto now = std::chrono::steady_clock::now();
  const CpuTimes cpu = ParseCpuStat(Read("stat"));
  const MemoryInfo memory = ParseMemInfo(Read("meminfo"));
  const double load_one = ParseLoadAverage(Read("loadavg"));
  const auto network = ParseNetDev(Read("net/dev"));

  Snapshot snapshot;
  // 内存利用率 = 1 - 可用/总量，MemAvailable 包含可回收页缓存
  snapshot.memory_utilization_percent =
      (1.0 - static_cast<double>(memory.available_kib) /
                 static_cast<double>(memory.total_kib)) *
      100.0;
  // 归一化负载 = load1 / CPU 数
  snapshot.load_normalized = load_one / static_cast<double>(cpu_count_);

  // CPU 利用率 = (delta_total - delta_idle) / delta_total * 100
  // idle 包含 idle + iowait（iowait 期间 CPU 其实也闲着）
  if (previous_cpu_) {
    const auto total_delta = Delta(cpu.Total(), previous_cpu_->Total());
    const auto idle_delta = Delta(cpu.IdleAll(), previous_cpu_->IdleAll());
    if (total_delta > 0) {
      snapshot.cpu_utilization_percent =
          static_cast<double>(total_delta - std::min(total_delta, idle_delta)) /
          static_cast<double>(total_delta) * 100.0;
    }
  }

  // 网络速率：字节差值 / 采样时间间隔（秒）
  double elapsed_seconds = 0;
  if (previous_time_) {
    elapsed_seconds = std::chrono::duration<double>(now - *previous_time_).count();
  }
  for (const auto& current : network) {
    NetRate rate;
    rate.interface_name = current.interface_name;
    rate.receive_drops = current.receive_drops; // 丢包是累计计数，直接透传
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
    previous_network_[current.interface_name] = current; // 存快照供下次差值
  }

  previous_cpu_ = cpu;
  previous_time_ = now;
  return snapshot;
}

// Read 读取 proc_root 下的一个相对文件，返回完整文本。
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

// ToJson 把 Snapshot 渲染成 JSON（自实现，字段顺序固定，便于 diff）。
// optional 字段为空时省略，避免输出无意义的值。
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
  if (snapshot.kernel_ring_buffer_dropped) {
    output << ",\"kernel.ring_buffer.dropped\":"
           << *snapshot.kernel_ring_buffer_dropped;
  }
  output << "},\"network\":[";
  for (std::size_t i = 0; i < snapshot.network.size(); ++i) {
    if (i != 0) output << ',';
    const auto& net = snapshot.network[i];
    output << "{\"interface\":\"" << JsonEscape(net.interface_name)
           << "\",";
    output << "\"receive_bytes_per_second\":" << net.receive_bytes_per_second << ',';
    output << "\"transmit_bytes_per_second\":" << net.transmit_bytes_per_second << ',';
    output << "\"receive_drops\":" << net.receive_drops << ',';
    output << "\"transmit_drops\":" << net.transmit_drops << '}';
  }
  output << "],\"kernel_events\":[";
  for (std::size_t i = 0; i < snapshot.kernel_events.size(); ++i) {
    if (i != 0) output << ',';
    const auto& event = snapshot.kernel_events[i];
    output << "{\"type\":\"" << JsonEscape(event.type) << "\",";
    output << "\"observed_at_unix_nano\":"
           << event.observed_at_unix_nano << ',';
    output << "\"process_id\":" << event.process_id << ',';
    output << "\"process_name\":\"" << JsonEscape(event.process_name)
           << "\",";
    output << "\"latency_ns\":" << event.latency_ns << '}';
  }
  output << "]}";
  return output.str();
}

}  // namespace sentinel
