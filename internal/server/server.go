package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"logd/internal/admin"
	"logd/internal/config"
	"logd/internal/monitor"
	"logd/internal/partition"
	"logd/internal/store"
)

func New(
	cfg config.ServerConfig,
	db *store.DB,
	partitions *partition.Manager,
	ingestHandler http.Handler,
	adminHandler *admin.Handler,
	monitorManager *monitor.Manager,
	logger *slog.Logger,
) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/logs/batch", ingestHandler)
	adminHandler.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !partitions.Ready() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			logger.Warn("readiness database check failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		monitorManager.Metrics().WritePrometheus(w)
	})

	return &http.Server{
		Addr:              cfg.Listen,
		Handler:           recoverPanic(logger, mux),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout(),
		ReadTimeout:       cfg.ReadTimeout(),
		WriteTimeout:      cfg.WriteTimeout(),
		IdleTimeout:       cfg.IdleTimeout(),
	}
}

func recoverPanic(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				logger.Error("http handler panic", "panic", value, "path", r.URL.Path)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
