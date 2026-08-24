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

输出会增加：

- `scheduler.run_queue.latency.p95.microseconds`
- `scheduler.run_queue.latency.p99.microseconds`
- `scheduler.run_queue.events`

容器验证需要访问宿主机内核能力；仅限开发环境时可使用`--privileged`，正式部署不应长期授予完整特权。

## 提交约定

- `feat:` 新能力
- `fix:` 缺陷修复
- `docs:` 文档
- `test:` 测试
- `build:` 构建与依赖

每次提交至少运行 `go test ./...`；修改 Agent 时还要运行 CMake/CTest。
