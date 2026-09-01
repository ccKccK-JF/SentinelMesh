// ============================================================================
// runqlat.bpf.c —— 调度运行队列延迟探针（eBPF 内核侧）
// ----------------------------------------------------------------------------
// 目标问题：任务从“可运行（runnable）”到“真正拿到 CPU”等了多久？
// 这比 CPU 利用率更接近“调度竞争”的真实症状。
//
// 实现原理：
//   1. enqueue_time（HASH map）：sched_wakeup / sched_wakeup_new 时，
//      以 PID 为 key 记录当前单调时钟；
//   2. sched_switch 时，如果 next 任务有记录，则计算
//      delta = 现在 - 记录时间，按 2 的幂次放入 latency_slots 桶；
//   3. 消费后删除 key，避免 HASH map 无限增长。
//
// CO-RE：用 BPF_CORE_READ 读取 task_struct.pid，Clang 在编译期记录
// 字段偏移，加载时由 libbpf 根据目标内核 BTF 修正（Compile Once,
// Run Everywhere）。
// ============================================================================

// SPDX-License-Identifier: GPL-2.0 OR BSD-3-Clause
// M2 kernel-side prototype: scheduler run-queue latency histogram.
#include "vmlinux_min.h"
#include <linux/bpf.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

char LICENSE[] SEC("license") = "Dual BSD/GPL";

// 任务入队时间表：pid -> 入队时刻（单调纳秒）。
// 用 HASH 是因为并发任务数不定；必须限制 max_entries 防滥用。
struct {
  __uint(type, BPF_MAP_TYPE_HASH);
  __uint(max_entries, 32768);
  __type(key, __u32);
  __type(value, __u64);
} enqueue_time SEC(".maps");

// 延迟直方图：32 个 2^n 幂次桶，per-CPU 保存。
// 关键：PERCPU_ARRAY 让每个 CPU 各自累加，无需原子操作，
// 避免多 CPU 同时写同一 cache line 的竞争；用户态读取后求和。
struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, 32);
  __type(key, __u32);
  __type(value, __u64);
} latency_slots SEC(".maps");

// 记录任务入队时间（pid==0 是内核线程/空闲任务，跳过）。
static __always_inline void remember_enqueue(__u32 pid) {
  if (pid == 0) return;
  const __u64 now = bpf_ktime_get_ns();
  bpf_map_update_elem(&enqueue_time, &pid, &now, BPF_ANY);
}

// sched_wakeup：任务被唤醒进入可运行队列。
SEC("tp_btf/sched_wakeup")
int BPF_PROG(handle_sched_wakeup, struct task_struct *task) {
  remember_enqueue(BPF_CORE_READ(task, pid));
  return 0;
}

// sched_wakeup_new：新创建的任务首次唤醒。
SEC("tp_btf/sched_wakeup_new")
int BPF_PROG(handle_sched_wakeup_new, struct task_struct *task) {
  remember_enqueue(BPF_CORE_READ(task, pid));
  return 0;
}

// sched_switch：CPU 切换任务。
//   - 若 next 有入队记录：计算等待延迟并放入直方图桶，删除记录；
//   - 若 preempt（被抢占）：previous 任务回到可运行状态，也要记入队时间。
SEC("tp_btf/sched_switch")
int BPF_PROG(handle_sched_switch, bool preempt, struct task_struct *previous,
             struct task_struct *next) {
  const __u32 next_pid = BPF_CORE_READ(next, pid);
  const __u64 *started = bpf_map_lookup_elem(&enqueue_time, &next_pid);
  if (started != NULL) {
    // 延迟 = 现在 - 入队时间，换算成微秒
    const __u64 delta_us = (bpf_ktime_get_ns() - *started) / 1000;
    __u32 slot = 0;
    // 2 的幂次分桶：delta > 2^n 则 slot = n+1。
    // 手动 unroll 让 verifier 能验证循环边界。
#pragma unroll
    for (int index = 0; index < 31; ++index) {
      if (delta_us > (1ULL << index)) slot = index + 1;
    }
    __u64 *count = bpf_map_lookup_elem(&latency_slots, &slot);
    // This is a PERCPU_ARRAY, so this CPU can update its own slot without an
    // atomic operation. User space aggregates the per-CPU values.
    // （注释保留原文）这是 per-CPU 数组，本 CPU 直接自增即可，无需原子操作。
    if (count != NULL) *count += 1;
    bpf_map_delete_elem(&enqueue_time, &next_pid); // 消费即删，防止 map 膨胀
  }

  if (preempt) remember_enqueue(BPF_CORE_READ(previous, pid));
  return 0;
}
