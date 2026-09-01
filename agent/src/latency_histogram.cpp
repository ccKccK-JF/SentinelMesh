// ============================================================================
// agent/src/latency_histogram.cpp
// ----------------------------------------------------------------------------
// 对数直方图汇总：把“窗口内新增的事件延迟分布”压缩成 P95/P99。
//
// 原理：
//   eBPF 探针在内核里只做“查表 + 自增”，把每个延迟放入 32 个 2^n 幂次桶
//   （slot 0 表示 <=1μs，slot 1 表示 1~2μs，…，slot 31 表示 2^31 μs 以上）。
//   用户态拿到当前累计直方图后：
//     1. 与上一个窗口的直方图相减，得到本窗口的“增量直方图”（delta）；
//     2. 按桶累加，找到累计样本数到达分位目标（如 0.95*total）的第一个桶；
//     3. 返回该桶的“上界”作为分位数的近似值。
//
// 为什么用对数桶？固定内存覆盖极大延迟范围（1μs ~ 数秒），
// 热路径开销低；代价是分位数是桶上界（近似值），精度随桶宽变化。
// 这也是面试高频问题：P95/P99 是“近似值”，不是精确统计。
// ============================================================================

#include "sentinel/latency_histogram.hpp"

#include <cmath>

namespace sentinel {
namespace {

// 每个桶的上界（微秒）：slot 0 -> 1μs，slot n -> 2^n μs。
double SlotUpperBoundMicroseconds(std::size_t slot) {
  if (slot == 0) return 1.0;
  return static_cast<double>(std::uint64_t{1} << slot);
}

// 计算某个分位数：沿桶累加，累计数达到 target 的第一个桶上界即答案。
// target = ceil(total * quantile)，保证样本够多时不会返回 0。
double Quantile(const LatencyHistogram& delta, std::uint64_t total,
                double quantile) {
  const auto target = static_cast<std::uint64_t>(
      std::ceil(static_cast<long double>(total) * quantile));
  std::uint64_t cumulative = 0;
  for (std::size_t slot = 0; slot < delta.size(); ++slot) {
    cumulative += delta[slot];
    if (cumulative >= target) return SlotUpperBoundMicroseconds(slot);
  }
  // 理论上不会到这：delta 总和 == total >= target
  return SlotUpperBoundMicroseconds(delta.size() - 1);
}

}  // namespace

// SummarizeLatencyHistogram 汇总两个相邻窗口的累计直方图。
// 返回 nullopt 表示本窗口没有新事件（total == 0），调用方忽略即可。
std::optional<LatencySummary> SummarizeLatencyHistogram(
    const LatencyHistogram& current, const LatencyHistogram& previous) {
  LatencyHistogram delta{};
  std::uint64_t total = 0;
  for (std::size_t slot = 0; slot < current.size(); ++slot) {
    // 计数器可能被内核清空（如 map 重置），当前 < 上次时按当前值计
    delta[slot] = current[slot] >= previous[slot]
                      ? current[slot] - previous[slot]
                      : current[slot];
    total += delta[slot];
  }
  if (total == 0) return std::nullopt;

  return LatencySummary{
      .events = total, // 本窗口事件总数
      .p95_microseconds = Quantile(delta, total, 0.95),
      .p99_microseconds = Quantile(delta, total, 0.99),
  };
}

}  // namespace sentinel
