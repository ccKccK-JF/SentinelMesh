# 验证与验收记录

## 1. 验证策略

项目使用六层验证，避免用“编译成功”代替功能正确：

1. 算法单测：解析、窗口增量、P95/P99、评分和路由；
2. 并发与静态检查：Go race detector 与 vet；
3. 构建测试：Clang 编译 BPF ELF，CMake 构建 C++ Agent；
4. 跨语言 E2E：真实 C++ Agent 经 gRPC 上报到 Go 控制面；
5. 特权内核回归：根据目标内核 BTF 完成 CO-RE 重定位并挂载 tracepoint；
6. 故障注入与对照：stress-ng、fio、netem/iperf3、Ring Buffer 过载和路由 benchmark。

## 2. Go 控制面

```bash
go test -race ./...
go vet ./...
```

覆盖范围：

- 健康评分、EWMA、硬门槛、恶化/恢复连续样本；
- 30 秒冷却、恢复开始时间和新 Boot ID 重置；
- 重复 sequence 拒绝与同 Boot ID 重连；
- 路由权重归一、不可用节点排除、恢复 ramp；
- 平滑加权轮询分配；
- HTTP 路由版本、ETag 与 304；
- Prometheus 指标名称、标签和节点状态导出；
- Round Robin 与自适应策略结果边界。

## 3. C++ Agent

```bash
cmake -S agent -B build/agent -DSENTINEL_BUILD_TESTS=ON
cmake --build build/agent --parallel
ctest --test-dir build/agent --output-on-failure
```

CTest 包含 4 组测试：procfs 解析、调度直方图、块 I/O 直方图和 TCP 直方图/计数器。2026-08-25 回归为 4/4 通过。

## 4. 跨语言遥测

```bash
./scripts/test-e2e.sh
```

脚本启动独立 Go 控制面，运行两个 C++ Agent 进程。第二个进程使用相同 Boot ID 重新连接，Hello ACK 返回此前接受的 sequence 1，随后从 sequence 2 继续；最终 HTTP 节点快照确认 `last_sequence=2`。

这条测试覆盖 Protobuf C++/Go 兼容、gRPC 双向流、Hello、ACK、重连序列和 HTTP 查询，不依赖模拟 Agent。

## 5. 调度、块 I/O 与 TCP

```bash
sudo ./scripts/test-ebpf.sh
sudo ./scripts/test-blockio.sh
sudo ./scripts/test-tcp.sh
```

- 调度脚本确认三个 sched tracepoint 均可挂载，并输出运行队列事件、P95 与 P99；
- 块 I/O 脚本用 fio 产生 4 KiB 随机混合读写，确认 request 关联、读写分类与窗口分位数；
- TCP 脚本用 netem 注入 15 ms 延迟和 30% 丢包，iperf3 产生持续流量，`SO_LINGER=0` 产生 RST，确认 RTT、重传、收发 RST 与三类内核事件。

2026-08-25 实测环境和数值见 [Linux 运行时实验报告](benchmarks/linux-runtime-2026-08-25.md)。

## 6. Ring Buffer 边界

```bash
sudo ./scripts/test-ring-buffer.sh
```

64 个并发客户端在 5 秒内持续制造 RST。回归确认单批严格限制为 1,024 条事件，内核 reserve 失败和用户态截断会合并到 dropped 指标。

2026-08-25 过载窗口保留 1,024 条、丢弃 27,642 条，丢失率 96.43%；同日普通 TCP 故障窗口消费 65 条事件、丢失 0 条。过载结果证明计数和容量边界有效，不代表常态丢失率。

## 7. 资源压力与 Agent 开销

```bash
./scripts/test-resource-pressure.sh
./scripts/measure-agent-overhead.sh
AGENT_ARGS="--enable-ebpf --stdout --interval 1" \
  sudo ./scripts/measure-agent-overhead.sh
```

stress-ng 回归中 CPU 从 0.50% 升到 56.54%。10 秒空载测量中，procfs 与全 eBPF 模式的单核 CPU 均约 0.10%；峰值 RSS 分别为 11.25 MiB 和 23.63 MiB。CPU tick 分辨率为 0.10%，因此不能从该短窗口声称低于 0.1%。

## 8. 自适应路由

```bash
./scripts/test-routing.sh
go run ./cmd/routing-benchmark
```

路由 E2E 启动 `game-good` 与持续 100% CPU 的 `game-hot`。控制面产生版本 4 快照，权重分别为 10,000 和 0；假网关执行 10,000 次选择，全部进入健康节点。

确定性 12,000 请求对照中：

| 策略 | 错误数 | 错误率 | P95 | P99 |
|---|---:|---:|---:|---:|
| Round Robin | 625 | 5.2083% | 211 ms | 219 ms |
| Adaptive | 24 | 0.2000% | 15 ms | 16 ms |

CI 把 benchmark 输出与 [golden 报告](benchmarks/routing-synthetic.md) 做逐行 diff，算法结果变化必须显式更新报告。

## 9. Prometheus 与 Grafana

运行时检查包含：

- Control Plane、Prometheus 和 Grafana 容器健康；
- Prometheus target `control-plane:8080/metrics` 为 up；
- 模拟 Agent 上报后，PromQL 返回带 `node_id` 的 CPU 样本；
- Grafana 数据源与 UID `sentinelmesh-overview` Dashboard 自动发现；
- Dashboard JSON 与 Compose 在 CI 中通过语法校验。

## 10. CI 门禁

GitHub Actions 每次 push 和 pull request 执行：

- `go test -race ./...`
- `go vet ./...`
- Dashboard JSON 与 Docker Compose 配置校验
- 路由 E2E 与 benchmark golden diff
- CMake 构建三个 BPF 对象和 C++ Agent
- CTest 4 组测试
- C++ Agent 到 Go 控制面的跨语言 E2E

公共 Runner 不具备稳定的特权 eBPF 环境，因此真实加载与故障注入由 Linux 特权脚本完成。公共 CI 仍会编译 BPF ELF 和测试用户态聚合算法。
