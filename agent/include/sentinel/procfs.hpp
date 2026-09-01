// ============================================================================
// procfs.hpp —— procfs 采集器接口
// ----------------------------------------------------------------------------
// ProcfsCollector 负责从 /proc 读取 CPU/内存/Load/网络指标。
// 它是有状态的：必须保留上一次采样的累计值和时间，才能用差值法
// 计算 CPU 利用率与网络速率。
// ============================================================================

#pragma once

#include <chrono>
#include <filesystem>
#include <optional>
#include <string_view>
#include <unordered_map>
#include <vector>

#include "sentinel/metrics.hpp"

namespace sentinel {

// 各 proc 文件解析函数（独立暴露，便于单测时传字符串夹具）
CpuTimes ParseCpuStat(std::string_view content);
MemoryInfo ParseMemInfo(std::string_view content);
double ParseLoadAverage(std::string_view content);
std::vector<NetCounters> ParseNetDev(std::string_view content);

class ProcfsCollector {
 public:
  // proc_root 默认 /proc；测试可指向夹具目录（如 tests/fixtures/proc）
  explicit ProcfsCollector(std::filesystem::path proc_root = "/proc");

  // 执行一次采集。内部会更新 previous_* 状态，供下次差值使用。
  Snapshot Collect();

 private:
  // 读取 proc_root 下相对路径文件的完整内容
  std::string Read(std::string_view relative_path) const;

  std::filesystem::path proc_root_;
  std::optional<CpuTimes> previous_cpu_;                          // 上次 CPU 时间片
  std::unordered_map<std::string, NetCounters> previous_network_; // 上次各网卡计数
  std::optional<std::chrono::steady_clock::time_point> previous_time_; // 上次采样时刻
  unsigned int cpu_count_{1}; // CPU 核数（用于 load 归一化）
};

// 把 Snapshot 序列化为 JSON（--stdout 调试模式用，自实现避免 JSON 依赖）
std::string ToJson(const Snapshot& snapshot);

}  // namespace sentinel
