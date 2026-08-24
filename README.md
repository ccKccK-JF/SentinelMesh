# SentinelMesh

[![CI](https://github.com/ccKccK-JF/SentinelMesh/actions/workflows/ci.yml/badge.svg)](https://github.com/ccKccK-JF/SentinelMesh/actions/workflows/ci.yml)

面向游戏服务器与通用 Linux 服务的性能诊断和自适应调度平台。项目使用 **Go 构建控制面**、使用 **C++ 构建节点 Agent**，并通过 **C/eBPF** 观测调度、块 I/O 与 TCP 内核路径。

> 当前状态：M1.5 已完成，M2 内核观测开发中。仓库只把已经落地、能够测试的能力标为完成；规划中的能力不会伪装成已实现功能。

## 为什么重新开发

本项目参考了原 `monitor_system` 的指标采集思路，但不直接复制旧工程：

- 普通 CPU、内存、磁盘和网络计数器通过 procfs/cgroup 获取，不为它们加载内核模块。
- 控制面使用 Go，负责并发连接、节点状态、健康评分与后续调度策略。
- 节点 Agent 使用 C++，负责低开销采集、批处理和 eBPF 用户态加载。
- 指标使用长连接 gRPC 双向流上传；MySQL 不再承担高频时序数据存储。
- 节点评分不仅考虑加权值，还将加入硬门槛、EWMA、滞回与恢复冷却。

## 架构

```text
procfs / cgroup     eBPF programs
       \              /
        C++ Node Agent
               |
       gRPC bidirectional stream
               |
        Go Control Plane
        /       |       \
   HTTP API  Health   Prometheus
             Score      Metrics
                 \
             Routing Policy (M3)
```

详细设计见 [docs/architecture.md](docs/architecture.md)，旧项目审计见 [docs/reference-audit.md](docs/reference-audit.md)。

## 当前完成度

| 能力 | 状态 | 说明 |
|---|---|---|
| Protobuf 遥测协议 | ✅ | Hello、指标批次、内核事件、心跳和确认消息 |
| Go gRPC 控制面 | ✅ | 双向流接入、序列号校验、节点快照 |
| 健康评分 | ✅ M1 | CPU、内存、负载和 I/O 的硬门槛与基础评分 |
| HTTP 查询接口 | ✅ | `/healthz`、`/v1/nodes`、`/v1/nodes/{id}` |
| Go 模拟 Agent | ✅ | 用于 Windows/Linux 的端到端开发验证 |
| C++ procfs Agent | ✅ M1 | CPU、内存、Load、网络指标采集与 JSON 输出 |
| C++ gRPC Agent | ✅ M1.5 | 双向流、ACK续传语义、指数退避重连、跨语言E2E |
| eBPF 调度延迟探针 | 🚧 | 内核程序骨架已加入，用户态 Loader 待 M2 接入 |
| Prometheus/Grafana | ⏳ M2 | 尚未实现 |
| 自适应路由权重 | ⏳ M3 | 尚未实现 |

## 快速开始

生成 Protobuf 代码：

```bash
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 generate
```

启动控制面：

```bash
go run ./cmd/control-plane
```

另开一个终端运行模拟 Agent：

```bash
go run ./cmd/sim-agent --node-id game-1
```

查询节点：

```bash
curl http://127.0.0.1:8080/v1/nodes
```

运行 Go 测试：

```bash
go test ./...
```

C++ Agent 需要 Linux、CMake 3.20+、Protobuf 和 gRPC C++：

```bash
sudo apt install -y cmake g++ libgrpc++-dev protobuf-compiler-grpc
cmake -S agent -B build/agent -DSENTINEL_BUILD_TESTS=ON
cmake --build build/agent -j
ctest --test-dir build/agent --output-on-failure

# 控制面启动后，发送一个真实procfs指标批次
./build/agent/sentinel-agent \
  --manager-address 127.0.0.1:50051 \
  --node-id game-1 \
  --once

# 只在终端打印指标，不连接控制面
./build/agent/sentinel-agent --stdout --once
```

跨语言端到端验证：

```bash
./scripts/test-e2e.sh
```

完整环境和故障排查见 [docs/development.md](docs/development.md)。

## 里程碑

- **M1 基础闭环**：协议、Go控制面、节点状态、基础评分、C++ procfs采集。
- **M2 内核观测**：libbpf CO-RE Loader、调度延迟、块 I/O 延迟、TCP RTT/重传、Prometheus。
- **M3 调度闭环**：EWMA、滞回、冷却、权重下发、异常摘除和恢复。
- **M4 实验验证**：stress-ng、fio、iperf3、tc netem 故障注入与性能报告。

## License

[MIT](LICENSE)
