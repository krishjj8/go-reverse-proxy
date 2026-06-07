package proxy

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/pprof"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func StartAdminServer(addr string) {
	adminMux := http.NewServeMux()

	adminMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	adminMux.Handle("/metrics", promhttp.Handler())

	adminMux.HandleFunc("/debug/pprof/", pprof.Index)
	adminMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	adminMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	adminMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	adminMux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	go func() {
		slog.Info("Admin control plane listening actively", "bind_address", addr)
		if err := http.ListenAndServe(addr, adminMux); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Admin control plane socket crash", "error", err)
		}
	}()
}
