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

	"github.com/Viralefy/viralefy_payments/internal/application"
	"github.com/Viralefy/viralefy_payments/internal/config"
	"github.com/Viralefy/viralefy_payments/internal/infrastructure/external/payment"
	"github.com/Viralefy/viralefy_payments/internal/infrastructure/observability"
	"github.com/Viralefy/viralefy_payments/internal/infrastructure/persistence/postgres"
	httpiface "github.com/Viralefy/viralefy_payments/internal/interface/http"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

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

	// Prometheus collectors — registrados antes de servir requests pra
	// /internal/metrics não 404ar enquanto handlers ainda warming up.
	observability.InitMetrics()

	ctx := context.Background()
	db, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connect failed", "error", err.Error())
		log.Fatal("database:", err)
	}
	defer db.Close()

	// Migrations idempotentes — não bloqueamos boot se accepted_currencies
	// já existe (caso o monólito tenha rodado a 032 antes da extração).
	if err := db.ApplyMigrations(ctx); err != nil {
		logger.Warn("migration apply warning", "error", err.Error())
	}

	// Providers — registrados na ordem que o registry expõe. siteURL injetado
	// só no Stripe (success_url default). Manuais não precisam de config.
	registry := application.NewPaymentRegistry(
		payment.NewStripe(cfg.SiteURL),
		payment.NewHeleket(),
		payment.NewWoovi(),
		payment.NewAbacatePay(),
		payment.NewManualPIX(),
		payment.NewManualUSDT(),
		payment.NewManualCrypto(),
	)

	gwRepo := postgres.NewGatewayRepo(db)
	gatewayService := application.NewGatewayService(gwRepo)
	currencyReader := application.NewCurrencyReader(db.Pool())
	planReader := application.NewPlanReader(db.Pool())
	methodsService := application.NewMethodsService(planReader, currencyReader, gatewayService)
	stripeEvents := postgres.NewStripeEventsRepo(db)

	deps := &httpiface.Deps{
		Registry:               registry,
		Gateways:               gatewayService,
		Methods:                methodsService,
		Plans:                  planReader,
		Currencies:             currencyReader,
		StripeEvents:           stripeEvents,
		InternalSharedSecret:   cfg.InternalSharedSecret,
		APIInternalCallbackURL: cfg.APIInternalCallbackURL,
	}

	router := httpiface.NewRouter(deps)
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
