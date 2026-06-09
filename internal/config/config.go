package config

import (
	"fmt"
	"os"
	"strings"
)

// Config carrega as variáveis necessárias pro viralefy_payments.
// Defaults seguros pra HML/dev; em produção tudo é setado por
// /etc/viralefy/.env (gerado pelo installer do viralefy_ops).
type Config struct {
	Port                 string // PAYMENTS_PORT (default 8081)
	BindHost             string // BIND_HOST (default 127.0.0.1)
	DatabaseURL          string // DATABASE_URL — obrigatório
	InternalSharedSecret string // INTERNAL_SHARED_SECRET — header X-Internal-Token
	APICallbackURL       string // API_CALLBACK_URL — back-compat (preferir API_INTERNAL_CALLBACK_URL)
	// APIInternalCallbackURL — base URL do monólito (viralefy_api) usado pelo
	// webhook handler depois de validar signature: POST {url}/internal/v1/payment-confirmed
	// com X-Internal-Token. Wave 2 introduziu — preferir esse sobre APICallbackURL.
	APIInternalCallbackURL string
	SiteURL                string // SITE_URL — URL pública da loja, base de return URLs
	SentryDSN              string // SENTRY_DSN — vazio = Sentry no-op
}

func Load() (Config, error) {
	cfg := Config{
		Port:                   getenv("PAYMENTS_PORT", "8081"),
		BindHost:               getenv("BIND_HOST", "127.0.0.1"),
		DatabaseURL:            getenv("DATABASE_URL", ""),
		InternalSharedSecret:   getenv("INTERNAL_SHARED_SECRET", ""),
		APICallbackURL:         getenv("API_CALLBACK_URL", ""),
		APIInternalCallbackURL: getenv("API_INTERNAL_CALLBACK_URL", getenv("API_CALLBACK_URL", "")),
		SiteURL:                getenv("SITE_URL", getenv("NEXT_PUBLIC_SITE_URL", "https://viralefy.com")),
		SentryDSN:              getenv("SENTRY_DSN", ""),
	}
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return cfg, fmt.Errorf("DATABASE_URL is required")
	}
	if strings.TrimSpace(cfg.InternalSharedSecret) == "" {
		return cfg, fmt.Errorf("INTERNAL_SHARED_SECRET is required")
	}
	if len(cfg.InternalSharedSecret) < 16 {
		return cfg, fmt.Errorf("INTERNAL_SHARED_SECRET must be at least 16 characters")
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
