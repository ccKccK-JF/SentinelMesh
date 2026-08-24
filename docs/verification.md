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

## 4. 跨语言遥测

```bash
./scripts/test-e2e.sh
```

脚本连续启动两个C++ Agent进程。第二个进程使用同一Boot ID重新连接，Hello ACK返回此前接受的序列1，随后从序列2继续。测试最终从Go HTTP接口确认`last_sequence=2`。

块I/O特权链路还完成了以下人工回归：fio运行期间启动带eBPF的C++ Agent，连续上报4个批次；Go节点快照同时包含读写延迟、调度延迟、CPU、内存和网络指标。

## 5. CI门禁

GitHub Actions对每次提交执行：

- `go test -race ./...`
- `go vet ./...`
- CMake构建两个BPF对象和C++ Agent
- CTest解析器、调度直方图和块I/O直方图测试
- C++ Agent到Go控制面的跨语言E2E

特权eBPF加载不放在公共Runner中执行，使用本地显式脚本验证，避免把公共CI环境的内核权限误当成代码失败。
