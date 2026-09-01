// ============================================================================
// metrics.hpp —— Agent 内部的核心数据模型
// ----------------------------------------------------------------------------
// Snapshot 是“一次采集”的完整结果：procfs 基础指标 + eBPF 内核指标 +
// 内核异常事件 + 网络速率。它会被：
//   - 序列化为 JSON 输出（--stdout 模式）；
//   - 转换为 proto MetricBatch 上报控制面（gRPC 模式）。
//
// optional 语义：eBPF 探针未启用时对应字段为 nullopt，序列化/上报时省略。
// ============================================================================

#pragma once

#include <cstdint>
#include <map>
#include <optional>
#include <string>
#include <vector>

namespace sentinel {

// /proc/stat 中 "cpu " 行的各时间片（单位：USER_HZ 即 1/100 秒）。
struct CpuTimes {
  std::uint64_t user{};     // 用户态时间
  std::uint64_t nice{};     // 低优先级用户态时间
  std::uint64_t system{};   // 内核态时间
  std::uint64_t idle{};     // 空闲
  std::uint64_t io_wait{};  // 等待 I/O
  std::uint64_t irq{};      // 硬中断
  std::uint64_t soft_irq{}; // 软中断
  std::uint64_t steal{};    // 被虚拟机偷走的时间

  // 总时间 = 所有分片之和
  [[nodiscard]] std::uint64_t Total() const noexcept {
    return user + nice + system + idle + io_wait + irq + soft_irq + steal;
  }

  // “真正闲着”的时间 = idle + iowait（iowait 期间 CPU 未执行任务）
  [[nodiscard]] std::uint64_t IdleAll() const noexcept { return idle + io_wait; }
};

// /proc/meminfo 提取的字段（单位 KiB）。
struct MemoryInfo {
  std::uint64_t total_kib{};     // 物理内存总量
  std::uint64_t available_kib{}; // 可用内存（含可回收页缓存）
};

// /proc/net/dev 单个网卡的累计计数器（原始值）。
struct NetCounters {
  std::string interface_name;      // 接口名，如 eth0
  std::uint64_t receive_bytes{};   // 累计接收字节
  std::uint64_t transmit_bytes{};  // 累计发送字节
  std::uint64_t receive_drops{};   // 累计接收丢弃
  std::uint64_t transmit_drops{};  // 累计发送丢弃
};

// 网络速率（由 NetCounters 做窗口差值换算得到）。
struct NetRate {
  std::string interface_name;
  double receive_bytes_per_second{};  // 接收速率（字节/秒）
  double transmit_bytes_per_second{}; // 发送速率（字节/秒）
  std::uint64_t receive_drops{};      // 接收丢弃（累计值透传）
  std::uint64_t transmit_drops{};     // 发送丢弃（累计值透传）
};

// 内核异常事件（如 TCP 重传/RST），带进程信息用于根因定位。
struct KernelEvent {
  std::string type;                       // 事件类型
  std::int64_t observed_at_unix_nano{};   // Unix 纳秒时间戳
  std::uint32_t process_id{};             // PID
  std::string process_name;               // 进程名
  std::uint64_t latency_ns{};             // 相关延迟（无则 0）
  std::map<std::string, std::string> attributes; // 扩展属性
};

// 一次采集的完整快照。
// 基础四项（CPU/内存/Load）由 procfs 保证存在；
// 其余内核指标可选，取决于 eBPF 探针是否启用。
struct Snapshot {
  double cpu_utilization_percent{};         // CPU 利用率 %
  double memory_utilization_percent{};      // 内存利用率 %
  double load_normalized{};                 // 归一化负载（load1 / CPU数）
  // --- 调度运行队列延迟（runqlat 探针）---
  std::optional<double> scheduler_run_queue_p95_microseconds;
  std::optional<double> scheduler_run_queue_p99_microseconds;
  std::optional<std::uint64_t> scheduler_run_queue_events;
  // --- 块 I/O 延迟（blocklat 探针，读写分开）---
  std::optional<double> block_io_read_p95_microseconds;
  std::optional<double> block_io_read_p99_microseconds;
  std::optional<std::uint64_t> block_io_read_events;
  std::optional<double> block_io_write_p95_microseconds;
  std::optional<double> block_io_write_p99_microseconds;
  std::optional<std::uint64_t> block_io_write_events;
  // --- TCP 指标（tcplat 探针）---
  std::optional<double> tcp_rtt_p95_microseconds;
  std::optional<double> tcp_rtt_p99_microseconds;
  std::optional<std::uint64_t> tcp_rtt_samples;
  std::optional<std::uint64_t> tcp_retransmissions;
  std::optional<std::uint64_t> tcp_receive_resets;
  std::optional<std::uint64_t> tcp_send_resets;
  // 内核/用户态丢弃的 Ring Buffer 事件数（可靠性可观测）
  std::optional<std::uint64_t> kernel_ring_buffer_dropped;
  std::vector<KernelEvent> kernel_events;  // 本窗口的内核事件
  std::vector<NetRate> network;            // 各网卡速率
};

}  // namespace sentinel
