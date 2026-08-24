#ifndef SENTINEL_VMLINUX_MIN_H_
#define SENTINEL_VMLINUX_MIN_H_

// Minimal CO-RE type declarations used by runqlat.bpf.c. The kernel BTF
// supplies the real layout at load time, so the repository does not need to
// commit a machine-generated, multi-megabyte vmlinux.h.
#include <linux/types.h>
#include <stdbool.h>

struct task_struct {
  int pid;
} __attribute__((preserve_access_index));

#endif  // SENTINEL_VMLINUX_MIN_H_
