package main

import (
	"log/slog"
	"net/http"

	"github.com/krishjj8/go-reverse-proxy/internal/config"
	"github.com/krishjj8/go-reverse-proxy/internal/logger"
	"github.com/krishjj8/go-reverse-proxy/internal/proxy"
)

func main() {
	// 1. Fire up our production structured JSON logging matrix
	logger.InitLogger()

	// 2. Load config settings from disk
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		// slog.Error allows key-value pairing attributes
		slog.Error("Configuration subsystem boot failure", "error", err)
		panic(err)
	}

	// 3. Initialize our custom Reverse Proxy routing engine
	engine := proxy.NewEngine(cfg)

	slog.Info("Proxy engine boot cycle complete", "bind_address", cfg.Server.ListenAddress)

	// 4. Bind to the network socket
	err = http.ListenAndServe(cfg.Server.ListenAddress, engine)
	if err != nil {
		slog.Error("Network socket bind failure", "error", err)
		panic(err)
	}
}
