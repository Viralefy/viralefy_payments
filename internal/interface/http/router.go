package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewRouter monta o chi.Router com middlewares padrão do projeto:
// RequestID, RealIP, OTel HTTP, Recoverer. Os endpoints /internal/*
// (exceto health e webhooks públicos) ficam atrás de InternalAuth.
//
// Wave 1: só /internal/health está exposto. Resto vem na Wave 2.
func NewRouter(internalSecret string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "viralefy-payments",
			otelhttp.WithSpanNameFormatter(func(_ string, req *http.Request) string {
				return req.Method + " " + req.URL.Path
			}),
		)
	})
	r.Use(middleware.Recoverer)

	// Health fica fora do auth — probe do systemd / orchestrator precisa.
	r.Get("/internal/health", health)

	// Demais rotas internas serão registradas aqui (Wave 2), todas atrás
	// do middleware InternalAuth. Ex.:
	//
	//   r.Group(func(r chi.Router) {
	//       r.Use(InternalAuth(internalSecret))
	//       r.Get("/internal/methods", h.ListMethods)
	//       r.Post("/internal/charge", h.CreateCharge)
	//       ...
	//   })
	//
	// Webhooks externos (Stripe, Heleket, Woovi) ficam fora do InternalAuth
	// pois entram via reverse-proxy do Caddy e validam por assinatura.
	_ = internalSecret

	return r
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
