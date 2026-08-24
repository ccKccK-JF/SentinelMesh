# eBPF probes

`runqlat.bpf.c` 是 M2 的内核侧原型，用以统计任务从被唤醒到真正被调度运行的等待时间直方图。

构建前需要从当前构建环境生成 `vmlinux.h`：

```bash
bpftool btf dump file /sys/kernel/btf/vmlinux format c > agent/ebpf/vmlinux.h
clang -O2 -g -target bpf -D__TARGET_ARCH_x86 \
  -Iagent/ebpf -c agent/ebpf/runqlat.bpf.c -o build/runqlat.bpf.o
bpftool gen skeleton build/runqlat.bpf.o > build/runqlat.skel.h
```

当前文件尚未连接 C++ 用户态 Loader，因此 README 主表将其标记为“开发中”。
