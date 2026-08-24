# 验证记录

## 1. 验证层次

项目将验证拆成四层，避免只用“编译成功”证明内核观测正确：

1. **算法单测**：直方图增量、窗口事件数、P95/P99分桶。
2. **构建验证**：Clang生成BPF ELF对象，CMake构建C++ Loader和Agent。
3. **特权加载验证**：libbpf根据目标内核BTF完成CO-RE重定位，并attach到实际tracepoint。
4. **链路验证**：C++ Agent读取per-CPU Map、生成指标、经gRPC发送到Go控制面，再由HTTP查询节点快照。

## 2. 调度延迟

运行：

```bash
sudo ./scripts/test-ebpf.sh
```

验证项：

- `sched_wakeup`、`sched_wakeup_new`和`sched_switch`均能attach；
- 采样窗口能产生调度事件；
- 输出包含调度等待延迟P95、P99和事件数。

## 3. 块I/O延迟

运行：

```bash
sudo ./scripts/test-blockio.sh
```

脚本使用64MiB临时文件执行4KiB、队列深度16、直接I/O的随机混合读写。验证结束后删除临时文件。

2026-08-24在Docker Desktop Linux内核与Ubuntu 24.04特权容器中的一次样例窗口：

| 指标 | 样例值 |
|---|---:|
| 读请求数 | 19,991 |
| 读延迟P95 | 1,024 μs |
| 读延迟P99 | 1,024 μs |
| 写请求数 | 20,020 |
| 写延迟P95 | 512 μs |
| 写延迟P99 | 1,024 μs |

这些数值只证明请求关联、读写分类、直方图聚合和分位数链路正确。虚拟化和宿主机负载会显著影响绝对延迟，因此不能把该样例直接写成性能优化结论。

## 4. TCP网络质量

运行：

```bash
sudo ./scripts/test-tcp.sh
```

脚本在回环接口使用`tc netem`注入15ms延迟和30%丢包，使用4路iperf3长连接触发数据段重传，并通过`SO_LINGER=0`制造连接复位。2026-08-25的一次样例窗口峰值如下：

| 指标 | 样例值 |
|---|---:|
| RTT样本数 | 84 |
| RTT P95 | 524,288 μs |
| RTT P99 | 524,288 μs |
| TCP重传 | 26 |
| 接收RST | 1 |
| 发送RST | 2 |

高丢包会同时抬高RTT估计与重传次数；样例值用于证明tracepoint、per-CPU聚合和窗口增量链路，不代表生产网络基线。

## 5. Ring Buffer容量与丢失统计

运行：

```bash
sudo ./scripts/test-ring-buffer.sh
```

脚本在一个5秒采集窗口内用64个并发客户端持续制造RST。2026-08-25的一次回归中，C++ Agent按协议边界保留1024条事件，并将内核reserve失败和用户态截断合计为25,399条丢失事件。

同一负载通过gRPC上报后，Go HTTP节点快照显示`kernel_event_count=1024`、`kernel.ring_buffer.dropped=18,342`、`last_sequence=2`。两次压力值不同属于并发负载的正常波动；测试断言关注容量边界和计数非零，不依赖固定数值。

## 6. 跨语言遥测

```bash
./scripts/test-e2e.sh
```

脚本连续启动两个C++ Agent进程。第二个进程使用同一Boot ID重新连接，Hello ACK返回此前接受的序列1，随后从序列2继续。测试最终从Go HTTP接口确认`last_sequence=2`。

块I/O特权链路还完成了以下人工回归：fio运行期间启动带eBPF的C++ Agent，连续上报4个批次；Go节点快照同时包含读写延迟、调度延迟、CPU、内存和网络指标。

TCP指标也已通过C++ Agent的protobuf批次发送到Go控制面，并可在HTTP节点快照中查询。一次逐窗口轮询回归捕获到RTT样本44、重传11、接收RST 1和发送RST 4，证明非零指标跨越了完整链路。

## 7. Prometheus和Grafana

完整Compose经过以下运行时检查：

- 控制面、Prometheus 3.13.2和Grafana 12.4.0三个容器均正常启动；
- Prometheus的`control-plane:8080/metrics`目标状态为`up`；
- 模拟Agent上报后，PromQL查询`sentinelmesh_cpu_utilization_percent`返回带`node_id="observability-e2e"`标签的样本；
- Grafana`/api/health`返回数据库`ok`，预置Dashboard UID `sentinelmesh-overview`可通过搜索API发现。

## 8. 健康状态机

状态机测试使用确定性时间验证：

- `alpha=0.5`时CPU从20%跳到80%，EWMA为50%，评分使用平滑值；
- 普通恶化的第1个样本保持原状态，第2个连续样本才迁移；
- CPU达到100%时不等待EWMA或连续样本，立即进入unhealthy；
- 冷却截止前禁止恢复，截止后连续3个健康样本才恢复；
- Agent以新Boot ID连接后，旧EWMA和恢复截止时间被清空。

## 9. CI门禁

GitHub Actions对每次提交执行：

- `go test -race ./...`
- `go vet ./...`
- Dashboard JSON和Docker Compose配置校验
- CMake构建三个BPF对象和C++ Agent
- CTest解析器、调度、块I/O和TCP直方图测试
- C++ Agent到Go控制面的跨语言E2E

特权eBPF加载不放在公共Runner中执行，使用本地显式脚本验证，避免把公共CI环境的内核权限误当成代码失败。
