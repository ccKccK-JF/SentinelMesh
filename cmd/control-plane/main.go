// ============================================================================
// cmd/control-plane/main.go
// ----------------------------------------------------------------------------
// Go Control Plane 的进程入口（整个系统的“大脑”）。
//
// 职责装配：
//   1. 创建内存 Store（含评分器 Scorer），作为全局唯一的数据中心；
//   2. 启动 gRPC 服务：接收 C++ Agent 的双向流遥测上报；
//   3. 启动 HTTP 服务：对外暴露节点快照、路由权重、健康检查、Prometheus 指标；
//   4. 监听 SIGINT/SIGTERM，实现优雅退出（先停 HTTP，再 GracefulStop gRPC）。
//
// 面试要点：控制面是“无状态计算 + 内存态”的轻量设计，不在进程里存时序历史，
// 长期指标交给 Prometheus，所以 restart 会丢内存状态（文档中明确为边界）。
// ============================================================================

package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	sentinelv1 "github.com/ccKccK-JF/SentinelMesh/gen/go/sentinel/v1"
	"github.com/ccKccK-JF/SentinelMesh/internal/httpapi"
	"github.com/ccKccK-JF/SentinelMesh/internal/ingest"
	"github.com/ccKccK-JF/SentinelMesh/internal/scoring"
	"github.com/ccKccK-JF/SentinelMesh/internal/store"
	"google.golang.org/grpc"
)

func main() {
	// ---- 命令行参数：允许在不同环境指定监听地址 ----
	grpcAddress := flag.String("grpc-address", "127.0.0.1:50051", "gRPC listen address")
	httpAddress := flag.String("http-address", "127.0.0.1:8080", "HTTP listen address")
	flag.Parse()

	// 结构化日志（Go 1.21+ 标准库 slog），便于 grep 和后续接日志系统
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// 内存 Store：内部同时持有评分器（scoring.Scorer）和路由策略（routing.Policy）。
	// 所有节点快照、健康状态、路由权重都收敛到这一个对象上，
	// 保证“HTTP 查询 / Prometheus 导出 / 网关消费”读到的是同一份一致视图。
	memory := store.NewMemory(scoring.New(scoring.DefaultConfig()))

	// ---- gRPC 服务端 ----
	// MaxRecvMsgSize：限制单个 gRPC 消息大小，防止恶意/异常 Agent 撑爆内存；
	// MaxConcurrentStreams：限制并发流数量，防止连接数打满资源。
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(4*1024*1024),
		grpc.MaxConcurrentStreams(2048),
	)
	// 注册遥测服务：ingest.Server 实现了 proto 中的双向流 RPC Stream。
	// 每个 Agent 连接对应一个 goroutine 处理，天然并发安全。
	sentinelv1.RegisterTelemetryServiceServer(grpcServer, ingest.NewServer(memory))

	// 监听 gRPC TCP 端口
	listener, err := net.Listen("tcp", *grpcAddress)
	if err != nil {
		logger.Error("listen for gRPC", "error", err)
		os.Exit(1)
	}

	// ---- HTTP 服务端 ----
	// ReadHeaderTimeout/IdleTimeout 是 http.Server 的基础防护：
	// 防止慢速连接（Slowloris）长期占用连接，避免 keep-alive 连接悬空。
	httpServer := &http.Server{
		Addr:              *httpAddress,
		Handler:           httpapi.New(memory).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// ---- 启动两个服务，任何一方异常退出都通过 errCh 通知主流程 ----
	errCh := make(chan error, 2)
	go func() {
		logger.Info("gRPC server started", "address", *grpcAddress)
		errCh <- grpcServer.Serve(listener)
	}()
	go func() {
		logger.Info("HTTP server started", "address", *httpAddress)
		errCh <- httpServer.ListenAndServe()
	}()

	// ---- 优雅退出：等待信号或服务异常 ----
	// signal.NotifyContext 会在收到 os.Interrupt / SIGTERM 时取消 ctx。
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
		logger.Info("shutdown requested")
	case serveErr := <-errCh:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", serveErr)
		}
	}

	// ---- 关闭顺序：先关 HTTP（停止接收新请求、排空存量请求），再停 gRPC ----
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)

	// GracefulStop 会等待所有在处理的 RPC 完成；若 10 秒内未完成则强制 Stop。
	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		grpcServer.Stop()
	}
}
