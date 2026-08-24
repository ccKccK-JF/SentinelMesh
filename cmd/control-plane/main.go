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
	grpcAddress := flag.String("grpc-address", "127.0.0.1:50051", "gRPC listen address")
	httpAddress := flag.String("http-address", "127.0.0.1:8080", "HTTP listen address")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	memory := store.NewMemory(scoring.New(scoring.DefaultConfig()))
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(4*1024*1024),
		grpc.MaxConcurrentStreams(2048),
	)
	sentinelv1.RegisterTelemetryServiceServer(grpcServer, ingest.NewServer(memory))

	listener, err := net.Listen("tcp", *grpcAddress)
	if err != nil {
		logger.Error("listen for gRPC", "error", err)
		os.Exit(1)
	}
	httpServer := &http.Server{
		Addr:              *httpAddress,
		Handler:           httpapi.New(memory).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("gRPC server started", "address", *grpcAddress)
		errCh <- grpcServer.Serve(listener)
	}()
	go func() {
		logger.Info("HTTP server started", "address", *httpAddress)
		errCh <- httpServer.ListenAndServe()
	}()

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
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
