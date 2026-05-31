package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/krishjj8/go-reverse-proxy/internal/config"
	"github.com/krishjj8/go-reverse-proxy/internal/logger"
	"github.com/krishjj8/go-reverse-proxy/internal/proxy"
)

func main() {
	logger.InitLogger()

	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		slog.Error("Configuration subsystem boot failure", "error", err)
		panic(err)
	}

	engine := proxy.NewEngine(cfg)
	limiter := proxy.NewRateLimiter(10, 20)

	server := &http.Server{
		Addr:         cfg.Server.ListenAddress,
		Handler:      limiter.Middleware(engine),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("Proxy engine boot cycle complete", "bind_address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Network socket bind failure", "error", err)
			panic(err)
		}
	}()

	shutdownSignalChan := make(chan os.Signal, 1)
	signal.Notify(shutdownSignalChan, os.Interrupt, syscall.SIGTERM)

	caughtSignal := <-shutdownSignalChan
	slog.Warn("Termination signal intercepted by proxy gateway matrix. Initiating safety procedures.", "signal", caughtSignal.String())

	shutdownContext, cancelCloseWindow := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelCloseWindow()

	if err := server.Shutdown(shutdownContext); err != nil {
		slog.Error("Server forced shutdown executed due to timeout boundary expiration", "error", err)
	}

	slog.Info("All user connections completed cleanly. Gateway network perimeter deactivated.")
}
