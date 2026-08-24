#include "sentinel/runqlat_probe.hpp"

#include <algorithm>
#include <cerrno>
#include <cmath>
#include <cstring>
#include <vector>

#include <bpf/bpf.h>
#include <bpf/libbpf.h>

namespace sentinel {
namespace {

double SlotUpperBoundMicroseconds(std::size_t slot) {
  if (slot == 0) return 1.0;
  return static_cast<double>(std::uint64_t{1} << slot);
}

double Quantile(const RunQueueLatencyHistogram& delta, std::uint64_t total,
                double quantile) {
  const auto target = static_cast<std::uint64_t>(
      std::ceil(static_cast<long double>(total) * quantile));
  std::uint64_t cumulative = 0;
  for (std::size_t slot = 0; slot < delta.size(); ++slot) {
    cumulative += delta[slot];
    if (cumulative >= target) return SlotUpperBoundMicroseconds(slot);
  }
  return SlotUpperBoundMicroseconds(delta.size() - 1);
}

std::string LibbpfError(int error) {
  const int positive = error < 0 ? -error : error;
  return std::strerror(positive);
}

}  // namespace

struct RunQueueLatencyProbe::Impl {
  bpf_object* object{};
  std::vector<bpf_link*> links;
  int histogram_fd{-1};
  int possible_cpus{};

  ~Impl() {
    for (auto* link : links) {
      bpf_link__destroy(link);
    }
    if (object != nullptr) bpf_object__close(object);
  }
};

std::optional<RunQueueLatencySummary> SummarizeRunQueueLatency(
    const RunQueueLatencyHistogram& current,
    const RunQueueLatencyHistogram& previous) {
  RunQueueLatencyHistogram delta{};
  std::uint64_t total = 0;
  for (std::size_t slot = 0; slot < current.size(); ++slot) {
    delta[slot] = current[slot] >= previous[slot]
                      ? current[slot] - previous[slot]
                      : current[slot];
    total += delta[slot];
  }
  if (total == 0) return std::nullopt;

  return RunQueueLatencySummary{
      .events = total,
      .p95_microseconds = Quantile(delta, total, 0.95),
      .p99_microseconds = Quantile(delta, total, 0.99),
  };
}

RunQueueLatencyProbe::RunQueueLatencyProbe() = default;
RunQueueLatencyProbe::~RunQueueLatencyProbe() = default;

bool RunQueueLatencyProbe::Open(
    const std::filesystem::path& bpf_object_path) {
  impl_.reset();
  previous_.fill(0);
  last_error_.clear();

  auto impl = std::make_unique<Impl>();
  impl->object = bpf_object__open_file(bpf_object_path.c_str(), nullptr);
  const long open_error = libbpf_get_error(impl->object);
  if (open_error != 0) {
    impl->object = nullptr;
    last_error_ = "open BPF object: " + LibbpfError(static_cast<int>(open_error));
    return false;
  }

  const int load_error = bpf_object__load(impl->object);
  if (load_error != 0) {
    last_error_ = "load BPF object: " + LibbpfError(load_error);
    return false;
  }

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
    if (bpf_map_lookup_elem(impl_->histogram_fd, &slot, per_cpu.data()) != 0) {
      last_error_ = "read latency_slots map: " + std::string(std::strerror(errno));
      return std::nullopt;
    }
    for (const auto value : per_cpu) current[slot] += value;
  }

  const auto summary = SummarizeRunQueueLatency(current, previous_);
  previous_ = current;
  return summary;
}

}  // namespace sentinel
