// ============================================================================
// runqlat_probe.hpp —— 调度运行队列延迟探针接口
// ----------------------------------------------------------------------------
// 封装 runqlat.bpf.o 的加载/attach/读取。
// 内部用 Pimpl（Impl 结构体藏在 .cpp）隐藏 libbpf 的 C 类型，
// 让头文件不暴露第三方依赖。
// ============================================================================

#pragma once

#include <cstdint>
#include <filesystem>
#include <memory>
#include <optional>
#include <string>

#include "sentinel/latency_histogram.hpp"

namespace sentinel {

// 调度延迟直方图复用通用直方图类型（保持命名语义清晰）
constexpr std::size_t kRunQueueLatencySlots = kLatencyHistogramSlots;
using RunQueueLatencyHistogram = LatencyHistogram;
using RunQueueLatencySummary = LatencySummary;

std::optional<RunQueueLatencySummary> SummarizeRunQueueLatency(
    const RunQueueLatencyHistogram& current,
    const RunQueueLatencyHistogram& previous);

class RunQueueLatencyProbe {
 public:
  RunQueueLatencyProbe();
  ~RunQueueLatencyProbe();

  // 禁止拷贝
  RunQueueLatencyProbe(const RunQueueLatencyProbe&) = delete;
  RunQueueLatencyProbe& operator=(const RunQueueLatencyProbe&) = delete;

  // 打开并加载 BPF 对象，attach 到内核 tracepoint
  bool Open(const std::filesystem::path& bpf_object_path);
  // 读取直方图并与上一窗口做增量汇总；失败返回 nullopt
  std::optional<RunQueueLatencySummary> Collect();

  [[nodiscard]] const std::string& last_error() const noexcept {
    return last_error_;
  }

 private:
  struct Impl;                        // 前向声明，隐藏 libbpf 细节
  std::unique_ptr<Impl> impl_;        // Pimpl 指针
  RunQueueLatencyHistogram previous_{}; // 上一窗口累计直方图（做差值用）
  std::string last_error_;
};

}  // namespace sentinel
