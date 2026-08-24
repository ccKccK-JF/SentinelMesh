# 开发指南

## Go 控制面

要求 Go 1.25.10+。第一次构建先生成协议代码：

```bash
go run github.com/bufbuild/buf/cmd/buf@v1.72.0 generate
go mod tidy
go test ./...
```

默认端口：

- gRPC：`127.0.0.1:50051`
- HTTP：`127.0.0.1:8080`

可以通过启动参数覆盖：

```bash
go run ./cmd/control-plane --grpc-address 0.0.0.0:50051 --http-address 0.0.0.0:8080
```

## C++ Agent

要求 Linux、CMake 3.20+、支持 C++20 的编译器、Clang、libbpf、Protobuf和gRPC C++。Ubuntu安装命令：

```bash
sudo apt install -y cmake g++ clang libbpf-dev libgrpc++-dev protobuf-compiler-grpc
```

构建与测试：

```bash
cmake -S agent -B build/agent -DSENTINEL_BUILD_TESTS=ON
cmake --build build/agent -j
ctest --test-dir build/agent --output-on-failure
```

`--proc-root` 可以指向测试夹具或容器挂载的宿主机 procfs：

```bash
./build/agent/sentinel-agent --proc-root /proc --stdout --once
```

连接Go控制面并发送一个批次：

```bash
./build/agent/sentinel-agent \
  --manager-address 127.0.0.1:50051 \
  --node-id game-1 \
  --once
```

跨语言端到端测试会启动独立控制面、运行C++ Agent并验证HTTP节点快照：

```bash
./scripts/test-e2e.sh
```

## eBPF

调度延迟探针使用libbpf CO-RE。仓库只声明访问到的`task_struct.pid`，加载时由目标内核BTF完成字段重定位，不提交某台机器生成的整份`vmlinux.h`。CMake会生成`runqlat.bpf.o`并复制到Agent可执行文件旁边。

运行需要root，或经过收敛的`CAP_BPF`、`CAP_PERFMON`等能力：

```bash
sudo ./build/agent/sentinel-agent --enable-ebpf --stdout --once
sudo ./scripts/test-ebpf.sh
```

`--enable-ebpf`同时打开当前所有探针；排查兼容性时也可以分别使用`--enable-runqlat`、`--enable-blockio`或`--enable-tcp`。

输出会增加：

- `scheduler.run_queue.latency.p95.microseconds`
- `scheduler.run_queue.latency.p99.microseconds`
- `scheduler.run_queue.events`
- `block.io.read.latency.p95.microseconds`
- `block.io.read.latency.p99.microseconds`
- `block.io.write.latency.p95.microseconds`
- `block.io.write.latency.p99.microseconds`
- `block.io.read.events`
- `block.io.write.events`
- `tcp.rtt.p95.microseconds`
- `tcp.rtt.p99.microseconds`
- `tcp.rtt.samples`
- `tcp.retransmissions`
- `tcp.receive_resets`
- `tcp.send_resets`
- `kernel.ring_buffer.dropped`

块I/O验证脚本使用fio持续制造直接随机读写，确认请求关联、读写分类和窗口分位数：

```bash
sudo apt install -y fio
sudo ./scripts/test-blockio.sh
```

TCP验证脚本在回环接口注入15ms延迟和30%丢包，通过iperf3持续流量触发重传，并用`SO_LINGER=0`连接触发收发RST。脚本退出时会移除netem规则：

```bash
sudo apt install -y iproute2 iperf3 python3
sudo ./scripts/test-tcp.sh
```

异常TCP事件还会通过Ring Buffer进入`MetricBatch.kernel_events`，类型包括`tcp_retransmit`、`tcp_receive_reset`和`tcp_send_reset`。单个批次最多保留1024条，超过用户态边界或内核Ring Buffer容量的数量统一记录到`kernel.ring_buffer.dropped`：

```bash
sudo ./scripts/test-ring-buffer.sh
```

容器验证需要访问宿主机内核能力；仅限开发环境时可使用`--privileged`，正式部署不应长期授予完整特权。

## Prometheus和Grafana

从仓库根目录启动完整观测栈：

```bash
docker compose -f deploy/observability.compose.yml up -d --build
docker compose -f deploy/observability.compose.yml ps
```

端口：

- 控制面gRPC：`127.0.0.1:50051`
- 控制面HTTP和`/metrics`：`127.0.0.1:8080`
- Prometheus：`127.0.0.1:9090`
- Grafana：`127.0.0.1:3000`

可用模拟Agent快速产生时序数据：

```bash
go run ./cmd/sim-agent --address 127.0.0.1:50051 --node-id observability-demo --count 30
curl http://127.0.0.1:8080/metrics
```

停止环境但保留Prometheus和Grafana命名卷：

```bash
docker compose -f deploy/observability.compose.yml down
```

详细指标映射、Dashboard面板和安全说明见[观测栈文档](observability.md)。

## 自适应路由验证

路由端到端脚本会启动控制面、一个固定健康Agent和一个CPU硬门槛Agent，再让假网关模拟10,000次请求分配：

```bash
./scripts/test-routing.sh
```

手工运行假网关：

```bash
go run ./cmd/fake-gateway \
  --routing-url http://127.0.0.1:8080/v1/routing \
  --requests 10000
```

路由API为`GET /v1/routing`。只有快照内容变化时版本才递增；响应使用版本号作为`ETag`，携带`If-None-Match`轮询时未变化返回HTTP 304。网关应缓存最近一次成功快照，并拒绝用较旧版本覆盖新版本。

## 提交约定

- `feat:` 新能力
- `fix:` 缺陷修复
- `docs:` 文档
- `test:` 测试
- `build:` 构建与依赖

每次提交至少运行 `go test ./...`；修改 Agent 时还要运行 CMake/CTest。
