package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/Viralefy/viralefy_payments/internal/config"
	"github.com/Viralefy/viralefy_payments/internal/infrastructure/persistence/postgres"
	httpiface "github.com/Viralefy/viralefy_payments/internal/interface/http"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	// slog JSON — mesmo padrão do viralefy_api pra alimentar Alloy → Loki.
	version := os.Getenv("APP_VERSION")
	if version == "" {
		version = "dev"
	}
	env := os.Getenv("APP_ENV")
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).With(
		slog.String("service", "viralefy-payments"),
		slog.String("version", version),
	)
	slog.SetDefault(logger)

	// Sentry — no-op quando SENTRY_DSN vazio (HML/dev).
	if cfg.SentryDSN != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:         cfg.SentryDSN,
			Release:     "viralefy-payments@" + version,
			Environment: env,
		}); err != nil {
			logger.Warn("sentry init failed; continuing without it", "error", err.Error())
		} else {
			defer sentry.Flush(3 * time.Second)
		}
	} else {
		logger.Info("sentry disabled (SENTRY_DSN empty)")
	}

	ctx := context.Background()
	db, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connect failed", "error", err.Error())
		log.Fatal("database:", err)
	}
	defer db.Close()
	// pool reservado pros repos da Wave 2 (gateway_repo, payment_attempts...).
	_ = db.Pool()

	router := httpiface.NewRouter(cfg.InternalSharedSecret)
	addr := cfg.BindHost + ":" + cfg.Port
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("viralefy_payments listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("listen failed", "error", err.Error())
			log.Fatal(err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
