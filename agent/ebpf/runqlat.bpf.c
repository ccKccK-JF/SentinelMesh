// SPDX-License-Identifier: GPL-2.0 OR BSD-3-Clause
// M2 kernel-side prototype: scheduler run-queue latency histogram.
#include "vmlinux.h"
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

char LICENSE[] SEC("license") = "Dual BSD/GPL";

struct {
  __uint(type, BPF_MAP_TYPE_HASH);
  __uint(max_entries, 32768);
  __type(key, __u32);
  __type(value, __u64);
} enqueue_time SEC(".maps");

struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, 32);
  __type(key, __u32);
  __type(value, __u64);
} latency_slots SEC(".maps");

static __always_inline void remember_enqueue(__u32 pid) {
  if (pid == 0) return;
  const __u64 now = bpf_ktime_get_ns();
  bpf_map_update_elem(&enqueue_time, &pid, &now, BPF_ANY);
}

SEC("tp_btf/sched_wakeup")
int BPF_PROG(handle_sched_wakeup, struct task_struct *task) {
  remember_enqueue(BPF_CORE_READ(task, pid));
  return 0;
}

SEC("tp_btf/sched_switch")
int BPF_PROG(handle_sched_switch, bool preempt, struct task_struct *previous,
             struct task_struct *next) {
  const __u32 next_pid = BPF_CORE_READ(next, pid);
  const __u64 *started = bpf_map_lookup_elem(&enqueue_time, &next_pid);
  if (started != NULL) {
    const __u64 delta_us = (bpf_ktime_get_ns() - *started) / 1000;
    __u32 slot = 0;
#pragma unroll
    for (int index = 0; index < 31; ++index) {
      if (delta_us > (1ULL << index)) slot = index + 1;
    }
    __u64 *count = bpf_map_lookup_elem(&latency_slots, &slot);
    // This is a PERCPU_ARRAY, so this CPU can update its own slot without an
    // atomic operation. User space aggregates the per-CPU values.
    if (count != NULL) *count += 1;
    bpf_map_delete_elem(&enqueue_time, &next_pid);
  }

  if (preempt) remember_enqueue(BPF_CORE_READ(previous, pid));
  return 0;
}
