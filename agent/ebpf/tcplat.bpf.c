// ============================================================================
// tcplat.bpf.c —— TCP 指标探针（eBPF 内核侧）
// ----------------------------------------------------------------------------
// 观测内容：平滑 RTT 直方图、重传次数、收到/发出 RST 次数。
//
// 三类数据通道：
//   1. tcp_rtt_slots（PERCPU_ARRAY，32 桶）：tcp_probe tracepoint 提供
//      平滑 RTT（srtt），按 2 的幂次分桶；
//   2. tcp_counters（PERCPU_ARRAY，3 项）：重传/收RST/发RST 的累计计数；
//   3. tcp_events（RINGBUF，256 KiB）+ tcp_ringbuf_dropped：
//      异常事件的“详情”（进程/时间）推送到用户态；缓冲区满时增加
//      dropped 计数而非阻塞/丢弃静默。
//
// Ring Buffer 可靠性设计（面试重点）：
//   - reserve 失败 -> 计数（bounded、可观测）；
//   - 提交后用户态消费不及 -> 内核自动丢弃但计数；
//   - 用户态单批超 1024 条还会截断计数。两类丢失最终合并上报，
//     保证“丢失”这件事本身是可见的。
// ============================================================================

// SPDX-License-Identifier: GPL-2.0 OR BSD-3-Clause
#include "vmlinux_min.h"
#include "tcp_event.h"
#include <linux/bpf.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

char LICENSE[] SEC("license") = "Dual BSD/GPL";

// 计数器下标约定（与用户态 kTcpCounterCount 对应）
enum tcp_counter {
  kRetransmissions = 0,
  kReceiveResets = 1,
  kSendResets = 2,
  kTcpCounterCount = 3,
};

// RTT 直方图：32 个 2^n 幂次桶（微秒），per-CPU。
struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, 32);
  __type(key, __u32);
  __type(value, __u64);
} tcp_rtt_slots SEC(".maps");

// 重传/收RST/发RST 计数器：per-CPU。
struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, kTcpCounterCount);
  __type(key, __u32);
  __type(value, __u64);
} tcp_counters SEC(".maps");

// 事件 Ring Buffer：256 KiB，内核 -> 用户态的有界通道。
struct {
  __uint(type, BPF_MAP_TYPE_RINGBUF);
  __uint(max_entries, 256 * 1024);
} tcp_events SEC(".maps");

// Ring Buffer reserve 失败次数：per-CPU 计数。
struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, 1);
  __type(key, __u32);
  __type(value, __u64);
} tcp_ringbuf_dropped SEC(".maps");

// 原子自增一个计数器。
static __always_inline void increment_counter(__u32 key) {
  __u64 *value = bpf_map_lookup_elem(&tcp_counters, &key);
  if (value != NULL) *value += 1;
}

// 发射事件到 Ring Buffer；reserve 失败时计入 dropped。
static __always_inline void emit_event(__u32 type) {
  struct sentinel_tcp_event *event =
      bpf_ringbuf_reserve(&tcp_events, sizeof(*event), 0);
  if (event == NULL) {
    // 内核侧丢弃：记 per-CPU 计数，用户态最终可观测
    const __u32 key = 0;
    __u64 *dropped = bpf_map_lookup_elem(&tcp_ringbuf_dropped, &key);
    if (dropped != NULL) *dropped += 1;
    return;
  }
  // 填充事件：单调时钟（用户态换算 Unix 时间）、类型、进程信息
  event->observed_at_monotonic_ns = bpf_ktime_get_ns();
  event->type = type;
  event->process_id = bpf_get_current_pid_tgid() >> 32; // 高 32 位是 PID
  bpf_get_current_comm(event->process_name, sizeof(event->process_name));
  bpf_ringbuf_submit(event, 0);
}

// tcp_probe tracepoint：提供平滑 RTT（srtt），单位微秒。
// 只做分桶累计，不产生事件（RTT 太频繁，进 Ring Buffer 会爆）。
SEC("tracepoint/tcp/tcp_probe")
int handle_tcp_probe(struct trace_event_raw_tcp_probe *context) {
  const __u32 rtt_us = BPF_CORE_READ(context, srtt);
  if (rtt_us == 0) return 0; // 尚未采到有效 RTT

  __u32 slot = 0;
#pragma unroll
  for (int index = 0; index < 31; ++index) {
    if (rtt_us > (1U << index)) slot = index + 1;
  }
  __u64 *count = bpf_map_lookup_elem(&tcp_rtt_slots, &slot);
  if (count != NULL) *count += 1;
  return 0;
}

// 重传：计数 + 事件。
SEC("tracepoint/tcp/tcp_retransmit_skb")
int handle_tcp_retransmit(void *context) {
  (void)context;
  increment_counter(kRetransmissions);
  emit_event(SENTINEL_TCP_RETRANSMIT);
  return 0;
}

// 收到 RST：计数 + 事件。
SEC("tracepoint/tcp/tcp_receive_reset")
int handle_tcp_receive_reset(void *context) {
  (void)context;
  increment_counter(kReceiveResets);
  emit_event(SENTINEL_TCP_RECEIVE_RESET);
  return 0;
}

// 发出 RST：计数 + 事件。
SEC("tracepoint/tcp/tcp_send_reset")
int handle_tcp_send_reset(void *context) {
  (void)context;
  increment_counter(kSendResets);
  emit_event(SENTINEL_TCP_SEND_RESET);
  return 0;
}
