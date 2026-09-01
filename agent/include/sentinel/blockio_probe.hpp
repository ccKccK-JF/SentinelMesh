// ============================================================================
// blockio_probe.hpp —— 块 I/O 延迟探针接口
// ----------------------------------------------------------------------------
// 封装 blocklat.bpf.o 的加载/attach/读取。
// 与 runqlat 探针的区别：直方图有 64 个桶（0..31 读、32..63 写），
// 用户态分别汇总读写两个窗口增量。
// ============================================================================

#pragma once

#include <filesystem>
#include <memory>
#include <optional>
#include <string>

#include "sentinel/latency_histogram.hpp"

namespace sentinel {

// 读写各自一份窗口汇总
struct BlockIoLatencySummary {
  std::optional<LatencySummary> read;  // 读延迟汇总（窗口内无读则为 nullopt）
  std::optional<LatencySummary> write; // 写延迟汇总
};

class BlockIoLatencyProbe {
 public:
  BlockIoLatencyProbe();
  ~BlockIoLatencyProbe();

  BlockIoLatencyProbe(const BlockIoLatencyProbe&) = delete;
  BlockIoLatencyProbe& operator=(const BlockIoLatencyProbe&) = delete;

  bool Open(const std::filesystem::path& bpf_object_path);
  std::optional<BlockIoLatencySummary> Collect();

  [[nodiscard]] const std::string& last_error() const noexcept {
    return last_error_;
  }

 private:
  struct Impl;
  std::unique_ptr<Impl> impl_;
  LatencyHistogram previous_read_{};  // 上一窗口读累计直方图
  LatencyHistogram previous_write_{}; // 上一窗口写累计直方图
  std::string last_error_;
};

}  // namespace sentinel
