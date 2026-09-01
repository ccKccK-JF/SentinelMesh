// ============================================================================
// latency_histogram.hpp —— 对数延迟直方图
// ----------------------------------------------------------------------------
// 32 个 2^n 幂次桶覆盖 1μs ~ 数十秒的延迟范围，固定内存、热路径开销低。
// 用户态负责把两个相邻窗口的累计直方图相减，得到窗口增量并估算 P95/P99。
// ============================================================================

#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <optional>

namespace sentinel {

// 直方图桶数（与 eBPF 侧数组大小一致）
constexpr std::size_t kLatencyHistogramSlots = 32;
// 一个直方图：每个桶的事件计数（累计值，非增量）
using LatencyHistogram = std::array<std::uint64_t, kLatencyHistogramSlots>;

// 窗口汇总结果：事件总数 + P95/P99（微秒，桶上界近似值）
struct LatencySummary {
  std::uint64_t events{};          // 窗口内事件总数
  double p95_microseconds{};       // P95（桶上界近似）
  double p99_microseconds{};       // P99（桶上界近似）
};

// 汇总两个相邻窗口：current - previous = 本窗口增量直方图，再算分位数。
// 返回 nullopt 表示本窗口无事件。
std::optional<LatencySummary> SummarizeLatencyHistogram(
    const LatencyHistogram& current, const LatencyHistogram& previous);

}  // namespace sentinel
