// SPDX-License-Identifier: GPL-2.0 OR BSD-3-Clause
#include "vmlinux_min.h"
#include <linux/bpf.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

char LICENSE[] SEC("license") = "Dual BSD/GPL";

enum io_operation {
  kRead = 0,
  kWrite = 1,
};

enum {
  kRequestOperationMask = 0xff,
};

struct request_start {
  __u64 timestamp_ns;
  __u8 operation;
};

struct {
  __uint(type, BPF_MAP_TYPE_LRU_HASH);
  __uint(max_entries, 32768);
  __type(key, __u64);
  __type(value, struct request_start);
} block_in_flight SEC(".maps");

// Slots 0..31 are reads and 32..63 are writes.
struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, 64);
  __type(key, __u32);
  __type(value, __u64);
} block_latency_slots SEC(".maps");

SEC("tp_btf/block_rq_issue")
int BPF_PROG(handle_block_rq_issue, struct request *request) {
  const __u32 operation =
      BPF_CORE_READ(request, cmd_flags) & kRequestOperationMask;
  if (operation != kRead && operation != kWrite) return 0;

  const __u64 key = (__u64)request;
  const struct request_start start = {
      .timestamp_ns = bpf_ktime_get_ns(),
      .operation = operation,
  };
  bpf_map_update_elem(&block_in_flight, &key, &start, BPF_ANY);
  return 0;
}

SEC("tp_btf/block_rq_complete")
int BPF_PROG(handle_block_rq_complete, struct request *request) {
  const __u64 key = (__u64)request;
  const struct request_start *start =
      bpf_map_lookup_elem(&block_in_flight, &key);
  if (start == NULL) return 0;

  const __u64 delta_us = (bpf_ktime_get_ns() - start->timestamp_ns) / 1000;
  __u32 slot = 0;
#pragma unroll
  for (int index = 0; index < 31; ++index) {
    if (delta_us > (1ULL << index)) slot = index + 1;
  }
  if (start->operation == kWrite) slot += 32;

  __u64 *count = bpf_map_lookup_elem(&block_latency_slots, &slot);
  if (count != NULL) *count += 1;
  bpf_map_delete_elem(&block_in_flight, &key);
  return 0;
}
