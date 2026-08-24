#include "sentinel/blockio_probe.hpp"

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

}  // namespace

struct BlockIoLatencyProbe::Impl {
  bpf_object* object{};
  std::vector<bpf_link*> links;
  int histogram_fd{-1};
  int possible_cpus{};

  ~Impl() {
    for (auto* link : links) bpf_link__destroy(link);
    if (object != nullptr) bpf_object__close(object);
  }
};

BlockIoLatencyProbe::BlockIoLatencyProbe() = default;
BlockIoLatencyProbe::~BlockIoLatencyProbe() = default;

bool BlockIoLatencyProbe::Open(
    const std::filesystem::path& bpf_object_path) {
  impl_.reset();
  previous_read_.fill(0);
  previous_write_.fill(0);
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
      bpf_object__find_map_fd_by_name(impl->object, "block_latency_slots");
  if (impl->histogram_fd < 0) {
    last_error_ = "block_latency_slots map was not found";
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

std::optional<BlockIoLatencySummary> BlockIoLatencyProbe::Collect() {
  if (!impl_) {
    last_error_ = "block I/O latency probe is not open";
    return std::nullopt;
  }

  LatencyHistogram current_read{};
  LatencyHistogram current_write{};
  std::vector<std::uint64_t> per_cpu(
      static_cast<std::size_t>(impl_->possible_cpus));
  for (std::uint32_t map_slot = 0;
       map_slot < kLatencyHistogramSlots * 2; ++map_slot) {
    std::fill(per_cpu.begin(), per_cpu.end(), 0);
    if (bpf_map_lookup_elem(impl_->histogram_fd, &map_slot,
                            per_cpu.data()) != 0) {
      last_error_ = "read block_latency_slots map: " +
                    std::string(std::strerror(errno));
      return std::nullopt;
    }
    auto& histogram = map_slot < kLatencyHistogramSlots ? current_read
                                                        : current_write;
    const std::size_t slot = map_slot % kLatencyHistogramSlots;
    for (const auto value : per_cpu) histogram[slot] += value;
  }

  BlockIoLatencySummary summary{
      .read = SummarizeLatencyHistogram(current_read, previous_read_),
      .write = SummarizeLatencyHistogram(current_write, previous_write_),
  };
  previous_read_ = current_read;
  previous_write_ = current_write;
  return summary;
}

}  // namespace sentinel
