package http

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

// InternalAuth devolve um middleware que rejeita 401 quando o header
// X-Internal-Token não corresponde ao INTERNAL_SHARED_SECRET configurado.
//
// Loopback-only já mitiga o atacante externo, mas o token é defense-in-depth
// contra bypass acidental (containers, port-forward, kubectl exec, etc).
//
// O middleware NÃO protege /internal/health (liveness probe do systemd
// e do orchestrator do monolito) — esse endpoint precisa estar acessível
// sem segredo pra debugging. Webhooks externos (Stripe, Heleket, Woovi)
// também ficam fora: eles têm validação própria por assinatura.
func InternalAuth(secret string) func(http.Handler) http.Handler {
	expected := []byte(secret)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := []byte(r.Header.Get("X-Internal-Token"))
			if len(got) == 0 || subtle.ConstantTimeCompare(got, expected) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "unauthorized",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
