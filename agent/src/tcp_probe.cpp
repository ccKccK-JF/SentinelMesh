#include "sentinel/tcp_probe.hpp"

#include <algorithm>
#include <cerrno>
#include <cstring>
#include <vector>

#include <bpf/bpf.h>
#include <bpf/libbpf.h>

namespace sentinel {
namespace {

std::string LibbpfError(int error) {
  const int positive = error < 0 ? -error : error;
  return std::strerror(positive);
}

std::uint64_t CounterDelta(std::uint64_t current, std::uint64_t previous) {
  return current >= previous ? current - previous : current;
}

}  // namespace

struct TcpMetricsProbe::Impl {
  bpf_object* object{};
  std::vector<bpf_link*> links;
  int rtt_fd{-1};
  int counters_fd{-1};
  int possible_cpus{};

  ~Impl() {
    for (auto* link : links) bpf_link__destroy(link);
    if (object != nullptr) bpf_object__close(object);
  }
};

TcpMetricsProbe::TcpMetricsProbe() = default;
TcpMetricsProbe::~TcpMetricsProbe() = default;

bool TcpMetricsProbe::Open(const std::filesystem::path& bpf_object_path) {
  impl_.reset();
  previous_rtt_.fill(0);
  previous_counters_.fill(0);
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

  impl->rtt_fd = bpf_object__find_map_fd_by_name(impl->object, "tcp_rtt_slots");
  impl->counters_fd =
      bpf_object__find_map_fd_by_name(impl->object, "tcp_counters");
  if (impl->rtt_fd < 0 || impl->counters_fd < 0) {
    last_error_ = "TCP metrics maps were not found";
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

std::optional<TcpMetricsWindow> TcpMetricsProbe::Collect() {
  if (!impl_) {
    last_error_ = "TCP metrics probe is not open";
    return std::nullopt;
  }

  std::vector<std::uint64_t> per_cpu(
      static_cast<std::size_t>(impl_->possible_cpus));
  LatencyHistogram current_rtt{};
  for (std::uint32_t slot = 0; slot < current_rtt.size(); ++slot) {
    std::fill(per_cpu.begin(), per_cpu.end(), 0);
    if (bpf_map_lookup_elem(impl_->rtt_fd, &slot, per_cpu.data()) != 0) {
      last_error_ = "read tcp_rtt_slots map: " +
                    std::string(std::strerror(errno));
      return std::nullopt;
    }
    for (const auto value : per_cpu) current_rtt[slot] += value;
  }

  std::array<std::uint64_t, kTcpCounterCount> current_counters{};
  for (std::uint32_t key = 0; key < current_counters.size(); ++key) {
    std::fill(per_cpu.begin(), per_cpu.end(), 0);
    if (bpf_map_lookup_elem(impl_->counters_fd, &key, per_cpu.data()) != 0) {
      last_error_ = "read tcp_counters map: " +
                    std::string(std::strerror(errno));
      return std::nullopt;
    }
    for (const auto value : per_cpu) current_counters[key] += value;
  }

  TcpMetricsWindow window{
      .rtt = SummarizeLatencyHistogram(current_rtt, previous_rtt_),
      .retransmissions =
          CounterDelta(current_counters[0], previous_counters_[0]),
      .receive_resets =
          CounterDelta(current_counters[1], previous_counters_[1]),
      .send_resets = CounterDelta(current_counters[2], previous_counters_[2]),
  };
  previous_rtt_ = current_rtt;
  previous_counters_ = current_counters;
  return window;
}

}  // namespace sentinel
