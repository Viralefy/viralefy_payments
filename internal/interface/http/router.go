package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewRouter monta o chi.Router com middlewares padrão do projeto:
// RequestID, RealIP, OTel HTTP, Recoverer.
//
// Routing scheme:
//   /internal/health                — público (probe)
//   /internal/v1/webhooks/{p}       — público (signature por provider)
//   /internal/v1/...                — protegido por InternalAuth
//
// Wave 2 expandiu o scaffold da Wave 1 com os endpoints de charge, methods,
// gateways CRUD e webhooks dos 3 providers automáticos.
func NewRouter(d *Deps) http.Handler {
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

	r.Get("/internal/health", health)

	// Webhooks externos — PÚBLICOS. Stripe/Heleket/Woovi não conhecem o
	// INTERNAL_SHARED_SECRET, então signature check é a única defesa.
	r.Post("/internal/v1/webhooks/stripe", d.stripeWebhookHandler)
	r.Post("/internal/v1/webhooks/heleket", d.heleketWebhookHandler)
	r.Post("/internal/v1/webhooks/woovi", d.wooviWebhookHandler)

	// Rotas internas — protegidas por X-Internal-Token.
	r.Group(func(r chi.Router) {
		r.Use(InternalAuth(d.InternalSharedSecret))

		r.Get("/internal/v1/methods", d.methodsHandler)
		r.Post("/internal/v1/charge", d.chargeHandler)

		r.Get("/internal/v1/gateways", d.listGatewaysHandler)
		r.Post("/internal/v1/gateways", d.createGatewayHandler)
		r.Get("/internal/v1/gateways/{id}", d.getGatewayHandler)
		r.Put("/internal/v1/gateways/{id}", d.updateGatewayHandler)
		r.Delete("/internal/v1/gateways/{id}", d.deleteGatewayHandler)
	})

	return r
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
