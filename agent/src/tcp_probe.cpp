#include "sentinel/tcp_probe.hpp"

#include <algorithm>
#include <cerrno>
#include <chrono>
#include <cstring>
#include <utility>
#include <vector>

#include <bpf/bpf.h>
#include <bpf/libbpf.h>

#include "tcp_event.h"

namespace sentinel {
namespace {

std::string LibbpfError(int error) {
  const int positive = error < 0 ? -error : error;
  return std::strerror(positive);
}

std::uint64_t CounterDelta(std::uint64_t current, std::uint64_t previous) {
  return current >= previous ? current - previous : current;
}

std::string EventType(std::uint32_t type) {
  switch (type) {
    case SENTINEL_TCP_RETRANSMIT:
      return "tcp_retransmit";
    case SENTINEL_TCP_RECEIVE_RESET:
      return "tcp_receive_reset";
    case SENTINEL_TCP_SEND_RESET:
      return "tcp_send_reset";
    default:
      return "tcp_unknown";
  }
}

}  // namespace

struct TcpMetricsProbe::Impl {
  static constexpr std::size_t kMaxPendingEvents = 1024;

  bpf_object* object{};
  std::vector<bpf_link*> links;
  ring_buffer* ring{};
  int rtt_fd{-1};
  int counters_fd{-1};
  int ring_buffer_dropped_fd{-1};
  int possible_cpus{};
  std::int64_t monotonic_to_unix_nanos{};
  std::vector<KernelEvent> pending_events;
  std::uint64_t userspace_dropped{};

  static int HandleEvent(void* context, void* data, std::size_t size) {
    auto* impl = static_cast<Impl*>(context);
    if (size < sizeof(sentinel_tcp_event) ||
        impl->pending_events.size() >= kMaxPendingEvents) {
      ++impl->userspace_dropped;
      return 0;
    }
    const auto* event = static_cast<const sentinel_tcp_event*>(data);
    impl->pending_events.push_back(KernelEvent{
        .type = EventType(event->type),
        .observed_at_unix_nano =
            impl->monotonic_to_unix_nanos +
            static_cast<std::int64_t>(event->observed_at_monotonic_ns),
        .process_id = event->process_id,
        .process_name =
            std::string(event->process_name,
                        strnlen(event->process_name, sizeof(event->process_name))),
        .latency_ns = 0,
        .attributes = {},
    });
    return 0;
  }

  ~Impl() {
    if (ring != nullptr) ring_buffer__free(ring);
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
  previous_ring_buffer_dropped_ = 0;
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
  const int events_fd =
      bpf_object__find_map_fd_by_name(impl->object, "tcp_events");
  impl->ring_buffer_dropped_fd = bpf_object__find_map_fd_by_name(
      impl->object, "tcp_ringbuf_dropped");
  if (impl->rtt_fd < 0 || impl->counters_fd < 0 || events_fd < 0 ||
      impl->ring_buffer_dropped_fd < 0) {
    last_error_ = "TCP metrics maps were not found";
    return false;
  }
  impl->possible_cpus = libbpf_num_possible_cpus();
  if (impl->possible_cpus <= 0) {
    last_error_ = "cannot determine possible CPU count";
    return false;
  }
  const auto unix_nanos = std::chrono::duration_cast<std::chrono::nanoseconds>(
                              std::chrono::system_clock::now().time_since_epoch())
                              .count();
  const auto monotonic_nanos =
      std::chrono::duration_cast<std::chrono::nanoseconds>(
          std::chrono::steady_clock::now().time_since_epoch())
          .count();
  impl->monotonic_to_unix_nanos = unix_nanos - monotonic_nanos;
  impl->ring =
      ring_buffer__new(events_fd, Impl::HandleEvent, impl.get(), nullptr);
  const long ring_error = libbpf_get_error(impl->ring);
  if (ring_error != 0 || impl->ring == nullptr) {
    impl->ring = nullptr;
    const int error = ring_error != 0 ? static_cast<int>(ring_error) : -errno;
    last_error_ = "open TCP event ring buffer: " + LibbpfError(error);
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

  const int consumed = ring_buffer__consume(impl_->ring);
  if (consumed < 0) {
    last_error_ = "consume TCP event ring buffer: " + LibbpfError(consumed);
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

  const std::uint32_t dropped_key = 0;
  std::fill(per_cpu.begin(), per_cpu.end(), 0);
  if (bpf_map_lookup_elem(impl_->ring_buffer_dropped_fd, &dropped_key,
                          per_cpu.data()) != 0) {
    last_error_ = "read tcp_ringbuf_dropped map: " +
                  std::string(std::strerror(errno));
    return std::nullopt;
  }
  std::uint64_t current_ring_buffer_dropped = 0;
  for (const auto value : per_cpu) current_ring_buffer_dropped += value;

  TcpMetricsWindow window{
      .rtt = SummarizeLatencyHistogram(current_rtt, previous_rtt_),
      .retransmissions =
          CounterDelta(current_counters[0], previous_counters_[0]),
      .receive_resets =
          CounterDelta(current_counters[1], previous_counters_[1]),
      .send_resets = CounterDelta(current_counters[2], previous_counters_[2]),
      .ring_buffer_dropped =
          CounterDelta(current_ring_buffer_dropped,
                       previous_ring_buffer_dropped_) +
          impl_->userspace_dropped,
      .events = std::move(impl_->pending_events),
  };
  previous_rtt_ = current_rtt;
  previous_counters_ = current_counters;
  previous_ring_buffer_dropped_ = current_ring_buffer_dropped;
  impl_->pending_events.clear();
  impl_->userspace_dropped = 0;
  return window;
}

}  // namespace sentinel
