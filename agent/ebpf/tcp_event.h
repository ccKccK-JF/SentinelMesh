#pragma once

#include <linux/types.h>

enum sentinel_tcp_event_type {
  SENTINEL_TCP_RETRANSMIT = 1,
  SENTINEL_TCP_RECEIVE_RESET = 2,
  SENTINEL_TCP_SEND_RESET = 3,
};

struct sentinel_tcp_event {
  __u64 observed_at_monotonic_ns;
  __u32 type;
  __u32 process_id;
  char process_name[16];
};
