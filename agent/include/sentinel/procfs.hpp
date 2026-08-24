#pragma once

#include <chrono>
#include <filesystem>
#include <optional>
#include <string_view>
#include <unordered_map>
#include <vector>

#include "sentinel/metrics.hpp"

namespace sentinel {

CpuTimes ParseCpuStat(std::string_view content);
MemoryInfo ParseMemInfo(std::string_view content);
double ParseLoadAverage(std::string_view content);
std::vector<NetCounters> ParseNetDev(std::string_view content);

class ProcfsCollector {
 public:
  explicit ProcfsCollector(std::filesystem::path proc_root = "/proc");

  Snapshot Collect();

 private:
  std::string Read(std::string_view relative_path) const;

  std::filesystem::path proc_root_;
  std::optional<CpuTimes> previous_cpu_;
  std::unordered_map<std::string, NetCounters> previous_network_;
  std::optional<std::chrono::steady_clock::time_point> previous_time_;
  unsigned int cpu_count_{1};
};

std::string ToJson(const Snapshot& snapshot);

}  // namespace sentinel
