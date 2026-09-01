// ============================================================================
// tcp_event.h —— eBPF 事件与用户态共享的 ABI
// ----------------------------------------------------------------------------
// 这个头文件同时被 .bpf.c（内核侧）和 tcp_probe.cpp（用户侧）包含，
// 保证 Ring Buffer 里事件的内存布局两端完全一致。
// 注意：内核侧用 __u64/__u32 等 <linux/types.h> 类型，用户侧也可以编译。
// ============================================================================

#pragma once

#include <linux/types.h>

// 事件类型码
enum sentinel_tcp_event_type {
  SENTINEL_TCP_RETRANSMIT = 1,   // TCP 重传
  SENTINEL_TCP_RECEIVE_RESET = 2, // 收到 RST
  SENTINEL_TCP_SEND_RESET = 3,   // 发出 RST
};

// Ring Buffer 中事件的固定布局：
//  - 内核侧用 bpf_get_current_pid_tgid()/bpf_get_current_comm() 填充；
//  - 用户侧（tcp_probe.cpp 的 HandleEvent）按此结构解析。
struct sentinel_tcp_event {
  __u64 observed_at_monotonic_ns; // 事件发生的单调时钟纳秒（用户态换算 Unix 时间）
  __u32 type;                     // 事件类型码
  __u32 process_id;               // 触发进程 PID
  char process_name[16];          // 进程名（comm，最多 15 字符 + '\0'）
};
