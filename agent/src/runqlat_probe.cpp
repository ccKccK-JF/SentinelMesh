// ============================================================================
// agent/src/runqlat_probe.cpp
// ----------------------------------------------------------------------------
// 调度运行队列延迟探针的用户态部分（libbpf）。
//
// 内核侧 runqlat.bpf.c 把延迟写入 per-CPU 对数直方图（latency_slots）。
// 这里的职责：
//   1. Open：用 libbpf 打开并加载 BPF 对象，attach 到内核 tracepoint；
//   2. Collect：遍历每个直方图桶、每个 CPU，把 per-CPU 值求和得到
//      当前累计直方图，再交给 SummarizeRunQueueLatency 与上一窗口做增量汇总。
//
// 面试要点：
//   - per-CPU Map 的意义：每个 CPU 独立计数，热路径上普通自增即可，
//     避免全局原子计数器造成的 cache line 竞争；代价是读取要跨 CPU 求和。
//   - 窗口增量：累计直方图无法直接读“当前延迟”，必须做相邻窗口差值。
// ============================================================================

#include "sentinel/runqlat_probe.hpp"

#include <algorithm>
#include <cerrno>
#include <cstring>
#include <vector>

#include <bpf/bpf.h>
#include <bpf/libbpf.h>

namespace sentinel {
namespace {

// libbpf 返回负 errno，转为可读字符串。
std::string LibbpfError(int error) {
  const int positive = error < 0 ? -error : error;
  return std::strerror(positive);
}

}  // namespace

// Pimpl 惯用法：隐藏 libbpf 类型，避免头文件引入 C 依赖。
struct RunQueueLatencyProbe::Impl {
  bpf_object* object{};         // 加载后的 BPF 对象（持有 map/program）
  std::vector<bpf_link*> links; // 每个 program 对应一个 attach 链接
  int histogram_fd{-1};         // latency_slots map 的文件描述符
  int possible_cpus{};          // 系统可能的 CPU 数（per-CPU map 的宽度）

  // RAII：析构时销毁 links 再关闭 object，保证资源不泄漏。
  ~Impl() {
    for (auto* link : links) {
      bpf_link__destroy(link);
    }
    if (object != nullptr) bpf_object__close(object);
  }
};

// 转发到通用直方图汇总实现（别名类型保持语义清晰）。
std::optional<RunQueueLatencySummary> SummarizeRunQueueLatency(
    const RunQueueLatencyHistogram& current,
    const RunQueueLatencyHistogram& previous) {
  return SummarizeLatencyHistogram(current, previous);
}

RunQueueLatencyProbe::RunQueueLatencyProbe() = default;
RunQueueLatencyProbe::~RunQueueLatencyProbe() = default;

// Open：加载 BPF 对象并 attach 所有程序。
// 注意：加载前先 reset 旧 impl 和 previous_ 直方图，
// 保证“重新打开”是干净状态。
bool RunQueueLatencyProbe::Open(
    const std::filesystem::path& bpf_object_path) {
  impl_.reset();
  previous_.fill(0);
  last_error_.clear();

  auto impl = std::make_unique<Impl>();
  // 打开 CO-RE BPF ELF 文件（含重定位信息）
  impl->object = bpf_object__open_file(bpf_object_path.c_str(), nullptr);
  const long open_error = libbpf_get_error(impl->object);
  if (open_error != 0) {
    impl->object = nullptr;
    last_error_ = "open BPF object: " + LibbpfError(static_cast<int>(open_error));
    return false;
  }

  // 加载进内核：解析 BTF 重定位、经过 verifier 校验
  const int load_error = bpf_object__load(impl->object);
  if (load_error != 0) {
    last_error_ = "load BPF object: " + LibbpfError(load_error);
    return false;
  }

  // attach 对象内所有 program（本对象只有一个 runqlat 程序）
  bpf_program* program = nullptr;
  bpf_object__for_each_program(program, impl->object) {
    bpf_link* link = bpf_program__attach(program);
    const long attach_error = libbpf_get_error(link);
    if (attach_error != 0) {
      last_error_ = "attach BPF program: " +
                    LibbpfError(static_cast<int>(attach_error));
      return false;
    }
    impl->links.push_back(link);
  }

  // 找到直方图 map 的 fd，供 Collect 读取
  impl->histogram_fd =
      bpf_object__find_map_fd_by_name(impl->object, "latency_slots");
  if (impl->histogram_fd < 0) {
    last_error_ = "latency_slots map was not found";
    return false;
  }
  impl->possible_cpus = libbpf_num_possible_cpus();
  if (impl->possible_cpus <= 0) {
    last_error_ = "cannot determine possible CPU count";
    return false;
  }

  impl_ = std::move(impl);
  return true;
}

// Collect：读取当前累计直方图并做窗口增量汇总。
// 遍历 32 个桶，每个桶读出 per-CPU 数组并求和到 current[slot]。
std::optional<RunQueueLatencySummary> RunQueueLatencyProbe::Collect() {
  if (!impl_) {
    last_error_ = "run queue latency probe is not open";
    return std::nullopt;
  }

  RunQueueLatencyHistogram current{};
  std::vector<std::uint64_t> per_cpu(
      static_cast<std::size_t>(impl_->possible_cpus));
  for (std::uint32_t slot = 0; slot < current.size(); ++slot) {
    std::fill(per_cpu.begin(), per_cpu.end(), 0);
    // 内核 PERCPU_ARRAY 的 value 就是 per-CPU 数组，一次性读出
    if (bpf_map_lookup_elem(impl_->histogram_fd, &slot, per_cpu.data()) != 0) {
      last_error_ = "read latency_slots map: " + std::string(std::strerror(errno));
      return std::nullopt;
    }
    for (const auto value : per_cpu) current[slot] += value; // 跨 CPU 求和
  }

  const auto summary = SummarizeRunQueueLatency(current, previous_);
  previous_ = current; // 记录本轮，供下一窗口差值
  return summary;
}

}  // namespace sentinel
