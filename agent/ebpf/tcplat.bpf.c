// SPDX-License-Identifier: GPL-2.0 OR BSD-3-Clause
#include "vmlinux_min.h"
#include "tcp_event.h"
#include <linux/bpf.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

char LICENSE[] SEC("license") = "Dual BSD/GPL";

enum tcp_counter {
  kRetransmissions = 0,
  kReceiveResets = 1,
  kSendResets = 2,
  kTcpCounterCount = 3,
};

struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, 32);
  __type(key, __u32);
  __type(value, __u64);
} tcp_rtt_slots SEC(".maps");

struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, kTcpCounterCount);
  __type(key, __u32);
  __type(value, __u64);
} tcp_counters SEC(".maps");

struct {
  __uint(type, BPF_MAP_TYPE_RINGBUF);
  __uint(max_entries, 256 * 1024);
} tcp_events SEC(".maps");

struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, 1);
  __type(key, __u32);
  __type(value, __u64);
} tcp_ringbuf_dropped SEC(".maps");

static __always_inline void increment_counter(__u32 key) {
  __u64 *value = bpf_map_lookup_elem(&tcp_counters, &key);
  if (value != NULL) *value += 1;
}

static __always_inline void emit_event(__u32 type) {
  struct sentinel_tcp_event *event =
      bpf_ringbuf_reserve(&tcp_events, sizeof(*event), 0);
  if (event == NULL) {
    const __u32 key = 0;
    __u64 *dropped = bpf_map_lookup_elem(&tcp_ringbuf_dropped, &key);
    if (dropped != NULL) *dropped += 1;
    return;
  }
  event->observed_at_monotonic_ns = bpf_ktime_get_ns();
  event->type = type;
  event->process_id = bpf_get_current_pid_tgid() >> 32;
  bpf_get_current_comm(event->process_name, sizeof(event->process_name));
  bpf_ringbuf_submit(event, 0);
}

SEC("tracepoint/tcp/tcp_probe")
int handle_tcp_probe(struct trace_event_raw_tcp_probe *context) {
  const __u32 rtt_us = BPF_CORE_READ(context, srtt);
  if (rtt_us == 0) return 0;

  __u32 slot = 0;
#pragma unroll
  for (int index = 0; index < 31; ++index) {
    if (rtt_us > (1U << index)) slot = index + 1;
  }
  __u64 *count = bpf_map_lookup_elem(&tcp_rtt_slots, &slot);
  if (count != NULL) *count += 1;
  return 0;
}

SEC("tracepoint/tcp/tcp_retransmit_skb")
int handle_tcp_retransmit(void *context) {
  (void)context;
  increment_counter(kRetransmissions);
  emit_event(SENTINEL_TCP_RETRANSMIT);
  return 0;
}

SEC("tracepoint/tcp/tcp_receive_reset")
int handle_tcp_receive_reset(void *context) {
  (void)context;
  increment_counter(kReceiveResets);
  emit_event(SENTINEL_TCP_RECEIVE_RESET);
  return 0;
}

SEC("tracepoint/tcp/tcp_send_reset")
int handle_tcp_send_reset(void *context) {
  (void)context;
  increment_counter(kSendResets);
  emit_event(SENTINEL_TCP_SEND_RESET);
  return 0;
}
