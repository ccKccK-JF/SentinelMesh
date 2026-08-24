# eBPF probes

`runqlat.bpf.c`跟踪`sched_wakeup`、`sched_wakeup_new`与`sched_switch`，统计任务从进入可运行状态到真正获得CPU的等待时间。

`blocklat.bpf.c`跟踪`block_rq_issue`与`block_rq_complete`，以`struct request *`为键关联同一个块请求，并按读写方向统计从下发到完成的延迟。

`tcplat.bpf.c`使用`tcp_probe`采集平滑RTT，并通过`tcp_retransmit_skb`、`tcp_receive_reset`和`tcp_send_reset`统计重传与异常关闭。

## 数据结构

- `enqueue_time`：以PID为键保存任务进入运行队列的时间。
- `latency_slots`：32槽per-CPU对数直方图，用户态读取时再跨CPU聚合。
- `block_in_flight`：保存块请求的下发时间和读写方向。
- `block_latency_slots`：读、写各32槽的per-CPU对数直方图。
- `tcp_rtt_slots`：32槽per-CPU RTT对数直方图。
- `tcp_counters`：per-CPU重传、接收RST和发送RST计数器。
- `tcp_events`：256KiB Ring Buffer，传递重传与RST异常事件。
- `tcp_ringbuf_dropped`：per-CPU reserve失败计数；C++还会合并超过1024条批次边界的用户态截断数。

内核侧使用per-CPU数组，因此递增当前CPU槽位时不需要原子操作。C++ Loader计算相邻采样窗口的增量，并输出P95、P99和事件数。

## CO-RE策略

`vmlinux_min.h`只声明探针实际访问的`task_struct.pid`和`tcp_probe.srtt`上下文字段。Clang保留CO-RE重定位信息，libbpf在加载时根据目标内核BTF修正字段偏移。这样无需提交机器生成的整份`vmlinux.h`，也无需在构建时运行bpftool生成Skeleton。

## 构建和运行

CMake自动编译BPF对象并复制到`sentinel-agent`旁边：

```bash
cmake -S agent -B build/agent
cmake --build build/agent --parallel
sudo ./build/agent/sentinel-agent --enable-ebpf --stdout --once
sudo ./scripts/test-blockio.sh
sudo ./scripts/test-tcp.sh
sudo ./scripts/test-ring-buffer.sh
```

加载内核程序需要root或等价的最小内核能力。生产部署应收敛权限，不应直接使用完整privileged容器。
