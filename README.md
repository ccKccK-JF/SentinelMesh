# SentinelMesh

[![CI](https://github.com/ccKccK-JF/SentinelMesh/actions/workflows/ci.yml/badge.svg)](https://github.com/ccKccK-JF/SentinelMesh/actions/workflows/ci.yml)

SentinelMesh 是一个面向 Linux 游戏服务器与通用后端节点的性能诊断和自适应路由平台。它使用 **C++ Node Agent** 完成低开销资源采集与 eBPF 内核观测，使用 **Go Control Plane** 汇总节点状态、执行健康状态机并生成路由权重，通过 **gRPC + Protobuf** 建立跨语言遥测链路。

项目解决的不只是“服务器用了多少 CPU”，而是一条完整闭环：

1. 从 procfs 与内核事件中发现节点异常；
2. 区分 CPU 调度、块 I/O、TCP 网络和资源容量问题；
3. 对指标进行平滑、硬门槛判断、滞回和冷却；
4. 摘除异常节点，并对恢复节点渐进增权；
5. 用 Prometheus/Grafana、故障注入和对照实验验证结果。

## 系统架构

```text
Linux worker node
┌──────────────────────────────────────────────────────────────┐
│ procfs: CPU / memory / load / network                        │
│ eBPF: run queue latency / block I/O latency / TCP RTT & loss │
│                              │                               │
│                       C++ Node Agent                         │
│       window aggregation · event bound · reconnect          │
└──────────────────────────────┬───────────────────────────────┘
                               │ gRPC bidirectional stream
                               │ Protobuf batches + ACK
┌──────────────────────────────▼───────────────────────────────┐
│                        Go Control Plane                      │
│ ingest → in-memory snapshot → health state machine           │
│                         → routing policy → versioned snapshot │
└───────────────┬───────────────────────┬──────────────────────┘
                │                       │
       HTTP /v1/nodes           HTTP /metrics, Grafana
       HTTP /v1/routing                  │
                │                       ▼
                └──────────────► gateway / operator
```

设计原则：Agent 只负责单机事实采集与窗口聚合；控制面持有全局视图并统一决策。这样不会让各节点基于局部数据产生互相矛盾的路由结果。

详细组件边界、数据流和故障处理见 [架构设计](docs/architecture.md)。

## 已实现功能

| 模块 | 实现内容 |
|---|---|
| Linux 资源采集 | 解析 `/proc/stat`、`/proc/meminfo`、`/proc/loadavg`、`/proc/net/dev`，用相邻采样差值计算 CPU 与网络速率 |
| 调度诊断 | eBPF 跟踪 `sched_wakeup/sched_wakeup_new/sched_switch`，统计运行队列等待延迟 P95/P99 |
| 块 I/O 诊断 | 以 `struct request *` 关联 issue/complete，分别统计读写延迟与事件数 |
| TCP 诊断 | 统计平滑 RTT、重传、接收 RST、发送 RST，并输出异常内核事件 |
| 事件可靠性 | 256 KiB Ring Buffer、单批 1,024 条上限、内核 reserve 失败与用户态截断统一计数 |
| 跨语言遥测 | gRPC 双向流、Hello 握手、严格递增序列、ACK、同 Boot ID 断线续传、1–30 秒指数退避 |
| 节点状态 | 并发安全内存快照、Boot ID 生命周期、重复/乱序批次拒绝、HTTP 查询 API |
| 健康状态机 | EWMA、原始值硬门槛、恶化/改善滞回、30 秒恢复冷却、新 Boot 状态重置 |
| 自适应路由 | 异常节点零权重、degraded 惩罚、恢复 10% 起步并在 60 秒渐进增权、总权重严格归一到 10,000 |
| 网关消费 | 带单调版本号的路由快照、ETag/304、平滑加权轮询、无可用节点显式失败 |
| 可观测性 | Prometheus 指标导出、9 面板 Grafana Dashboard、节点/内核/路由指标 |
| 工程验证 | Go race/vet、C++ CTest、跨语言 E2E、路由 E2E、stress-ng/fio/netem 故障注入 |

## 关键数据

以下是 2026-08-25 在 WSL2 Linux 6.6、Ubuntu 24.04 特权容器中的回归结果。它们用于证明采集、诊断和调度链路有效，不代表物理生产服务器的性能上限。

| 场景 | 结果 |
|---|---|
| stress-ng CPU/内存压力 | CPU 指标从 0.50% 上升到 56.54%，变化 56.04 个百分点 |
| Agent procfs 模式 | 10 秒窗口单核 CPU 约 0.10%，峰值 RSS 11.25 MiB |
| Agent 全 eBPF 模式 | 10 秒窗口单核 CPU 约 0.10%，峰值 RSS 23.63 MiB |
| fio 4 KiB randrw | 读 P95/P99 1,024/1,024 μs；写 P95/P99 16,384/16,384 μs |
| netem + iperf3 | RTT P99 2,097,152 μs，重传 26，接收/发送 RST 2/1，正常窗口事件丢失率 0% |
| Ring Buffer 过载 | 单批保留 1,024 条、丢弃 27,642 条，故障注入窗口丢失率 96.43% |
| 12,000 请求确定性对照 | Round Robin 错误率 5.2083%、P99 219 ms；自适应路由错误率 0.2000%、P99 16 ms |

完整环境、口径和限制见 [Linux 运行时实验报告](docs/benchmarks/linux-runtime-2026-08-25.md) 与 [路由策略对照报告](docs/benchmarks/routing-synthetic.md)。

## 代码规模

截至 2026-08-25，按物理行统计：

| 类别 | 文件数 | 行数 |
|---|---:|---:|
| Go 生产代码 | 13 | 1,613 |
| Go 测试 | 8 | 575 |
| C++ 生产代码 | 14 | 1,689 |
| C++ 测试 | 4 | 134 |
| eBPF C/头文件 | 5 | 276 |
| Protobuf | 1 | 64 |
| Shell 构建与验证脚本 | 8 | 675 |
| **手写核心代码、测试与脚本合计** | **53** | **5,026** |
| Protobuf 生成 Go 代码（不计入手写量） | 2 | 794 |

统计不包含 Markdown、Dashboard JSON、构建产物和第三方依赖。物理行包含空行与注释，适合描述仓库规模，不等价于复杂度指标。

## 快速开始

### Go 控制面与模拟 Agent

要求 Go 1.25.x：

```bash
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 generate
go test ./...
go run ./cmd/control-plane
```

另开终端发送可控指标：

```bash
go run ./cmd/sim-agent --node-id game-1
curl http://127.0.0.1:8080/v1/nodes
curl http://127.0.0.1:8080/v1/routing
```

### C++ Agent

要求 Linux、CMake 3.20+、C++20、Clang、libbpf、Protobuf 和 gRPC C++：

```bash
sudo apt install -y cmake g++ clang libbpf-dev libgrpc++-dev protobuf-compiler-grpc
cmake -S agent -B build/agent -DSENTINEL_BUILD_TESTS=ON
cmake --build build/agent --parallel
ctest --test-dir build/agent --output-on-failure

# 仅采集真实 procfs 并输出 JSON
./build/agent/sentinel-agent --stdout --once

# 连接控制面并上传一个批次
./build/agent/sentinel-agent \
  --manager-address 127.0.0.1:50051 \
  --node-id game-1 \
  --once

# 需要 root 或收敛后的 BPF/PERFMON 能力
sudo ./build/agent/sentinel-agent --enable-ebpf --stdout --once
```

### 验证与压测

```bash
./scripts/test-e2e.sh
./scripts/test-routing.sh

sudo apt install -y stress-ng fio iperf3 iproute2 python3
./scripts/test-resource-pressure.sh
sudo ./scripts/test-ebpf.sh
sudo ./scripts/test-blockio.sh
sudo ./scripts/test-tcp.sh
sudo ./scripts/test-ring-buffer.sh
sudo ./scripts/measure-agent-overhead.sh
```

### Prometheus 与 Grafana

```bash
docker compose -f deploy/observability.compose.yml up -d --build
```

- Control Plane：`http://127.0.0.1:8080`
- Prometheus：`http://127.0.0.1:9090`
- Grafana：`http://127.0.0.1:3000`

本地 Compose 默认允许 Grafana 匿名只读访问，不能原样暴露到公网。

## API

| 地址 | 用途 |
|---|---|
| `GET /healthz` | 控制面存活检查 |
| `GET /v1/nodes` | 节点快照列表 |
| `GET /v1/nodes/{id}` | 单节点资源、内核指标与健康状态 |
| `GET /v1/routing` | 版本化路由权重；支持 `If-None-Match` 与 304 |
| `GET /metrics` | Prometheus 文本指标 |

## 面试讲解入口

建议按“问题—架构—难点—数据—边界”讲项目，而不是逐项背技术栈：

- 1 分钟和 3 分钟项目介绍；
- procfs、eBPF、CO-RE、per-CPU Map、Ring Buffer 原理；
- gRPC 双向流、ACK 与幂等语义；
- EWMA、滞回、冷却和渐进增权；
- 平滑加权轮询与最大余数归一化；
- 高频追问、易错回答和可继续演进项。

完整内容见 [面试讲解手册](docs/interview-guide.md)。

快速上手：带逐行中文注释的代码 + 学习路线见 [快速学习指南](docs/quick-learn.md)。

## 文档导航

- [架构设计](docs/architecture.md)：组件边界、数据流、一致性和故障处理
- [自适应调度](docs/scheduling.md)：评分状态机、路由权重和网关算法
- [eBPF 探针](agent/ebpf/README.md)：内核挂载点、Map 与 CO-RE
- [开发指南](docs/development.md)：构建、运行和测试命令
- [可观测性](docs/observability.md)：Prometheus 指标与 Grafana Dashboard
- [验证记录](docs/verification.md)：各层测试与验收范围
- [面试讲解手册](docs/interview-guide.md)：项目陈述、八股与追问
- [交付状态](docs/roadmap.md)：已经完成的能力与后续工程化方向

## 工程边界

当前仓库是可运行、可测试的工程项目，但不是可直接公网部署的商业控制面。生产化仍需补充 mTLS 与节点身份、持久化元数据、多副本一致性、细粒度 RBAC、告警通知、Agent 包发布和最小内核权限。文档明确区分“已实现并验证”和“后续可演进”，面试时不要把规划项说成已完成。

## License

[MIT](LICENSE)
