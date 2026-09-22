package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"logd/internal/admin"
	"logd/internal/config"
	"logd/internal/ingest"
	"logd/internal/monitor"
	"logd/internal/partition"
	"logd/internal/queue"
	"logd/internal/server"
	"logd/internal/store"
	"logd/internal/web"
	"logd/internal/writer"
	"logd/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("logd stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "config.local.yml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	location := config.SystemLocation()
	time.Local = location

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	dsn, err := cfg.Database.DSN()
	if err != nil {
		return fmt.Errorf("build database DSN: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(
		ctx,
		dsn,
		cfg.Database.MaxConns,
		cfg.Database.MinConns,
	)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx, migrations.Files); err != nil {
		return err
	}

	retentionDays, err := db.RetentionDays(ctx)
	if err != nil {
		return err
	}

	partitions := partition.New(db.Pool, location, logger)
	if err := partitions.Ensure(ctx, retentionDays); err != nil {
		return err
	}

	tokens, err := db.LoadTokenCache(ctx)
	if err != nil {
		return err
	}

	events := queue.New(cfg.Queue.Capacity)
	metrics := monitor.NewMetrics(events)
	eventWriter := writer.New(events, db, cfg.Queue, logger, metrics)
	metrics.SetLastWriteProvider(eventWriter.LastSuccessfulWrite)
	writerCtx, stopWriter := context.WithCancel(context.Background())
	defer stopWriter()
	go eventWriter.Run(writerCtx)

	monitorManager := monitor.NewManager(
		db,
		events,
		metrics,
		eventWriter.LastSuccessfulWrite,
		logger,
	)
	go monitorManager.Run(ctx)

	ingestHandler := ingest.New(events, tokens, retentionDays, metrics)
	renderer, err := web.New()
	if err != nil {
		return err
	}
	adminHandler, err := admin.New(admin.Options{
		DB:            db,
		Renderer:      renderer,
		Tokens:        tokens,
		Queue:         events,
		Partitions:    partitions,
		Monitor:       monitorManager,
		Location:      location,
		Password:      cfg.Admin.Password,
		SessionSecret: cfg.Admin.SessionSecret,
		OnRetentionChange: func(days int) {
			ingestHandler.SetRetentionDays(days)
		},
		LastWrite: eventWriter.LastSuccessfulWrite,
		Logger:    logger,
	})
	if err != nil {
		return err
	}

	go runPartitionMaintenance(ctx, db, partitions, logger)

	httpServer := server.New(
		cfg.Server,
		db,
		partitions,
		ingestHandler,
		adminHandler,
		monitorManager,
		logger,
	)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("logd http server started", "listen", cfg.Server.Listen)
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			cfg.Server.ShutdownTimeout(),
		)
		defer cancel()
		shutdownErr := httpServer.Shutdown(shutdownCtx)

		events.Close()
		drainTimer := time.NewTimer(cfg.Queue.ShutdownDrain())
		select {
		case <-eventWriter.Done():
			stopTimer(drainTimer)
		case <-drainTimer.C:
			logger.Warn("event queue drain timed out")
			stopWriter()
			<-eventWriter.Done()
		}

		if shutdownErr != nil {
			return fmt.Errorf("shutdown http server: %w", shutdownErr)
		}
		logger.Info("logd stopped")
		return nil
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve http: %w", err)
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func runPartitionMaintenance(
	ctx context.Context,
	db *store.DB,
	partitions *partition.Manager,
	logger *slog.Logger,
) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			retentionDays, err := db.RetentionDays(ctx)
			if err != nil {
				logger.Error("load retention days for partition maintenance", "error", err)
				continue
			}
			if err := partitions.Ensure(ctx, retentionDays); err != nil {
				logger.Error("partition maintenance failed", "error", err)
			}
		}
	}
}
