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

要求 Linux、CMake 3.20+ 和支持 C++20 的编译器。M1 默认只构建 procfs 采集器，不要求 gRPC 或 libbpf：

```bash
cmake -S agent -B build/agent -DSENTINEL_BUILD_TESTS=ON
cmake --build build/agent -j
ctest --test-dir build/agent --output-on-failure
```

`--proc-root` 可以指向测试夹具或容器挂载的宿主机 procfs：

```bash
./build/agent/sentinel-agent --proc-root /proc --once
```

## eBPF

M2 将使用 libbpf CO-RE。构建机需要 BTF、Clang、bpftool、libbpf、libelf 和 zlib。不要把某台机器生成的 `vmlinux.h` 提交进仓库；应从目标构建环境的 `/sys/kernel/btf/vmlinux` 生成。

## 提交约定

- `feat:` 新能力
- `fix:` 缺陷修复
- `docs:` 文档
- `test:` 测试
- `build:` 构建与依赖

每次提交至少运行 `go test ./...`；修改 Agent 时还要运行 CMake/CTest。
