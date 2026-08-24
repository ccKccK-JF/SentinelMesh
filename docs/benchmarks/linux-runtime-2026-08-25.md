# Linux 运行时与故障注入报告

## 1. 结论

本次回归验证了资源指标、三个 eBPF 探针、异常事件通道和 Agent 自身开销测量链路：

- stress-ng 压力能够被 CPU 采集器稳定观察；
- fio 负载能够产生分读写的块 I/O P95/P99；
- netem、iperf3 与 RST 负载能够触发 RTT、重传和异常关闭指标；
- 正常 TCP 故障窗口没有丢失内核事件；刻意过载时，Ring Buffer 和批次上限能报告丢失数量；
- 10 秒短窗口内，Agent 的 CPU 消耗约为单核 0.1%，全 eBPF 模式峰值 RSS 约 23.63 MiB。

这些结论证明功能闭环和测量口径成立。由于环境运行在 WSL2 与 Docker 虚拟化层，绝对延迟不能外推为物理机性能。

## 2. 环境与口径

| 项目 | 值 |
|---|---|
| 日期 | 2026-08-25 |
| 内核 | `6.6.87.2-microsoft-standard-WSL2` |
| 用户态 | Ubuntu 24.04.4 LTS，x86_64 |
| 容器可见 CPU | 16 |
| 容器可见内存 | 7,739,572 KiB |
| 编译器 | GCC 13.3、Clang 18.1 |
| 构建系统 | CMake 3.28.3 |
| 权限 | Docker 特权容器，挂载 tracefs |

Agent CPU 使用率通过 `/proc/<pid>/stat` 的用户态与内核态 tick 差值计算，结果表示“占用一个逻辑核的百分比”，不是整机 16 核归一化值。10 秒窗口、`CLK_TCK=100` 时分辨率为 0.1%，因此 0.1% 只应理解为约一个 tick。

峰值内存读取 `/proc/<pid>/status` 的 `VmHWM`。P95/P99 来自 32 桶、以 2 为底的对数直方图上界，是近似分位数而不是保存全部样本后的精确分位数。

## 3. 资源压力

命令：

```bash
AGENT_BINARY=build/agent/sentinel-agent \
  ./scripts/test-resource-pressure.sh
```

脚本先采集空载基线，再运行 CPU 50% 目标负载和 256 MiB 内存 worker，并连续采样 5 秒。

| 指标 | 结果 |
|---|---:|
| 基线 CPU | 0.50% |
| 压力窗口最大 CPU | 56.54% |
| 增量 | 56.04 个百分点 |
| 采样数 | 5 |

测试断言最大 CPU 不低于 20%，且相对基线至少增加 10 个百分点。断言使用宽松边界，避免把调度噪声和虚拟机资源竞争当成功能失败。

## 4. Agent 自身开销

| 模式 | 窗口 | 单核 CPU | 测量分辨率 | 峰值 RSS | 输出批次 |
|---|---:|---:|---:|---:|---:|
| procfs | 10 s | 0.10% | 0.10% | 11,520 KiB（11.25 MiB） | 11 |
| procfs + 全部 eBPF | 10 s | 0.10% | 0.10% | 24,192 KiB（23.63 MiB） | 11 |

命令：

```bash
DURATION_SECONDS=10 ./scripts/measure-agent-overhead.sh
DURATION_SECONDS=10 \
  AGENT_ARGS="--enable-ebpf --stdout --interval 1" \
  sudo ./scripts/measure-agent-overhead.sh
```

这是短窗口空载测量，不能替代长稳测试。更严格的生产评估应在物理机上运行数小时，记录平均值、最大值、上下文切换、Map 大小和不同事件速率下的开销曲线。

## 5. 块 I/O 故障注入

fio 使用 64 MiB 临时文件、4 KiB 随机混合读写、队列深度 16、direct I/O，运行 5 秒。Agent 以 1 秒窗口读取块请求直方图。

| 指标 | 窗口峰值 |
|---|---:|
| 读事件 | 24,403 |
| 读延迟 P95 | 1,024 μs |
| 读延迟 P99 | 1,024 μs |
| 写事件 | 24,378 |
| 写延迟 P95 | 16,384 μs |
| 写延迟 P99 | 16,384 μs |

写延迟明显高于读延迟，只描述本次虚拟磁盘窗口。验证重点是 `block_rq_issue` 与 `block_rq_complete` 的 request 关联、读写分类、跨 CPU 聚合和窗口增量正确。

## 6. TCP 故障注入

脚本对回环接口注入 15 ms 延迟和 30% 丢包，运行 4 路 iperf3 长连接，并持续建立带 `SO_LINGER=0` 的连接制造 RST。

| 指标 | 窗口峰值/累计 |
|---|---:|
| RTT 样本 | 84 |
| RTT P95 | 2,097,152 μs |
| RTT P99 | 2,097,152 μs |
| 重传 | 26 |
| 接收 RST | 2 |
| 发送 RST | 1 |
| 用户态收到内核事件 | 65 |
| Ring Buffer 丢失 | 0 |
| 事件丢失率 | 0% |

2,097,152 μs 是对数直方图桶上界。30% 丢包会触发 TCP 超时和退避，因此该值用于证明异常可见性，不是网络服务 SLO。

## 7. Ring Buffer 过载

64 个客户端并发制造 RST，持续 5 秒，刻意超过 256 KiB Ring Buffer 与单批 1,024 条事件边界。

| 指标 | 结果 |
|---|---:|
| 批次保留事件 | 1,024 |
| 丢失事件 | 27,642 |
| 丢失率 | 96.43% |

丢失率按 `dropped / (delivered + dropped)` 计算。该测试的目标就是制造溢出，不能把 96.43% 当成 Agent 正常运行质量。正常 TCP 故障注入窗口的对应丢失率为 0%。

## 8. 自适应路由对照

确定性模型运行 12,000 个请求，节点 `game-b` 在请求 2,000–6,999 期间故障，控制面有 200 请求检测延迟，并在故障结束后 300 请求开始恢复。

| 策略 | 错误率 | P95 | P99 |
|---|---:|---:|---:|
| Round Robin | 5.2083% | 211 ms | 219 ms |
| Adaptive | 0.2000% | 15 ms | 16 ms |

相对 Round Robin，自适应策略在该固定模型中将错误数从 625 降到 24，减少 96.16%；P99 从 219 ms 降到 16 ms，减少 92.69%。这是算法回归数据，不是线上 A/B 实验。模型和完整分配结果见 [路由策略对照报告](routing-synthetic.md)。

## 9. 可复现命令

```bash
ctest --test-dir build/agent --output-on-failure
AGENT_BINARY=build/agent/sentinel-agent ./scripts/test-resource-pressure.sh
AGENT_BINARY=build/agent/sentinel-agent ./scripts/measure-agent-overhead.sh
AGENT_BINARY=build/agent/sentinel-agent sudo ./scripts/test-blockio.sh
AGENT_BINARY=build/agent/sentinel-agent sudo ./scripts/test-tcp.sh
AGENT_BINARY=build/agent/sentinel-agent sudo ./scripts/test-ring-buffer.sh
go run ./cmd/routing-benchmark
```

故障注入脚本都注册了退出清理：fio 临时文件会删除，iperf3/负载进程会停止，`tc netem` 规则会移除。
