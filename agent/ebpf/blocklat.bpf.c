// ============================================================================
// blocklat.bpf.c —— 块 I/O 延迟探针（eBPF 内核侧）
// ----------------------------------------------------------------------------
// 目标问题：磁盘请求从下发到完成的延迟分布（读写分开）。
// 吞吐量/IOPS 高不代表请求体验好，少量慢请求会显著抬高 P99。
//
// 实现原理：
//   1. block_in_flight（LRU_HASH）：block_rq_issue 时以 struct request*
//      指针为 key 记录“下发时间 + 读/写方向”；
//   2. block_rq_complete 时用同一个指针查找记录，计算延迟，
//      按方向放进 block_latency_slots 的 64 个桶（0..31 读，32..63 写）；
//   3. 完成即删除 key。用 LRU 而非普通 HASH 是为防止“完成事件缺失”
//     导致 in-flight 表无限增长。
//
// 为什么用 struct request* 当 key？它在 issue 和 complete 两个事件中
// 是同一个指针，天然能关联一个请求的生命周期，无需额外状态。
// ============================================================================

// SPDX-License-Identifier: GPL-2.0 OR BSD-3-Clause
#include "vmlinux_min.h"
#include <linux/bpf.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

char LICENSE[] SEC("license") = "Dual BSD/GPL";

// I/O 操作类型
enum io_operation {
  kRead = 0,
  kWrite = 1,
};

// REQ_OP_MASK：cmd_flags 的低 8 位是操作码
enum {
  kRequestOperationMask = 0xff,
};

// in-flight 请求记录：时间戳 + 方向
struct request_start {
  __u64 timestamp_ns;
  __u8 operation;
};

// in-flight 请求表：request* 指针 -> 下发信息。
// LRU 容量上限 32768，防止异常内核路径下无界增长。
struct {
  __uint(type, BPF_MAP_TYPE_LRU_HASH);
  __uint(max_entries, 32768);
  __type(key, __u64);
  __type(value, struct request_start);
} block_in_flight SEC(".maps");

// 延迟直方图：64 个桶，前 32 个是读、后 32 个是写，per-CPU。
struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, 64);
  __type(key, __u32);
  __type(value, __u64);
} block_latency_slots SEC(".maps");

// 请求下发：记录时间戳与方向。非读写操作直接忽略。
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

// 请求完成：找到下发记录，计算延迟入桶，删除记录。
SEC("tp_btf/block_rq_complete")
int BPF_PROG(handle_block_rq_complete, struct request *request) {
  const __u64 key = (__u64)request;
  const struct request_start *start =
      bpf_map_lookup_elem(&block_in_flight, &key);
  if (start == NULL) return 0; // 没找到记录（可能在 attach 前已下发），忽略

  const __u64 delta_us = (bpf_ktime_get_ns() - start->timestamp_ns) / 1000;
  __u32 slot = 0;
  // 2 的幂次分桶（微秒）
#pragma unroll
  for (int index = 0; index < 31; ++index) {
    if (delta_us > (1ULL << index)) slot = index + 1;
  }
  if (start->operation == kWrite) slot += 32; // 写请求偏移到 32..63

  __u64 *count = bpf_map_lookup_elem(&block_latency_slots, &slot);
  if (count != NULL) *count += 1;
  bpf_map_delete_elem(&block_in_flight, &key); // 消费即删
  return 0;
}
