package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Viralefy/viralefy_payments/internal/application"
	"github.com/Viralefy/viralefy_payments/internal/domain"
)

// Deps agrupa as dependências dos handlers HTTP. Composto pelo main e
// passado pra NewRouter — assim os handlers não conhecem a estrutura
// concreta da camada de persistência.
type Deps struct {
	Registry             *application.PaymentRegistry
	Gateways             *application.GatewayService
	Methods              *application.MethodsService
	Plans                *application.PlanReader
	Currencies           *application.CurrencyReader
	StripeEvents         StripeEventsRecorder
	InternalSharedSecret string
	// APIInternalCallbackURL — base URL do monólito (ex: http://127.0.0.1:8080).
	// O webhook handler bate em {url}/internal/v1/payment-confirmed após
	// validar signature. Vazio = callback desabilitado (útil pra HML/dev
	// sem o monólito rodando).
	APIInternalCallbackURL string
}

// StripeEventsRecorder é a porta de persistência mínima usada pelo webhook
// Stripe pra idempotência. Implementada por postgres.StripeEventsRepo.
type StripeEventsRecorder interface {
	Record(ctx context.Context, eventID, eventType, orderID string) (bool, error)
}

// writeJSON serializa v em JSON com o status code dado. Helper interno usado
// por todos os handlers.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr serializa um erro como {error: msg}. mapErrStatus traduz erros
// canônicos (ErrInvalidInput → 422, ErrNotFound → 404) — qualquer outro
// erro vira 500 (com mensagem genérica pra não vazar detalhe interno).
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	case errors.Is(err, domain.ErrInvalidInput):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_input"})
	case errors.Is(err, domain.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
	}
}

// ─── /internal/v1/methods ──────────────────────────────────────────────────

// methodsHandler responde GET /internal/v1/methods?plan_id=&display_currency=&country=.
// Espelha o handler /v1/checkout/methods do monólito — mesma shape de retorno
// pra que o front continue funcionando após o reverse-proxy.
func (d *Deps) methodsHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	planID := strings.TrimSpace(q.Get("plan_id"))
	display := strings.TrimSpace(q.Get("display_currency"))
	country := strings.TrimSpace(q.Get("country"))
	if planID == "" {
		writeErr(w, domain.ErrInvalidInput)
		return
	}
	options, err := d.Methods.ListMethods(r.Context(), planID, display, country)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"methods": options})
}

// ─── /internal/v1/charge ───────────────────────────────────────────────────

// chargeRequest é o body do POST /internal/v1/charge. Inclui a info suficiente
// pra rodar o provider: gateway alvo (id), customer, e plan/amount info.
// O monólito é dono da decisão de "qual gateway" (já fez a query) e passa o
// id resolvido + o snapshot do que precisa pra cobrar.
type chargeRequest struct {
	OrderID     string                      `json:"order_id"`
	PlanID      string                      `json:"plan_id"`
	GatewayID   string                      `json:"gateway_id"`
	PayCurrency string                      `json:"pay_currency"`
	Description string                      `json:"description"`
	Customer    application.PaymentCustomer `json:"customer"`
	// Amount/Currency são opcionais — quando preenchidos, sobrescrevem o
	// cálculo do plano. Útil pra checkouts com discount/credits onde o
	// monólito já decidiu o valor final.
	Amount   string `json:"amount,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// chargeResponse devolve o que o provider produziu. Same shape que o
// monólito esperava do provider in-memory.
type chargeResponse struct {
	Provider     string            `json:"provider"`
	ExternalRef  string            `json:"external_ref"`
	PaymentURL   string            `json:"payment_url"`
	PaymentExtra map[string]string `json:"payment_extra"`
}

func (d *Deps) chargeHandler(w http.ResponseWriter, r *http.Request) {
	var in chargeRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, domain.ErrInvalidInput)
		return
	}
	in.OrderID = strings.TrimSpace(in.OrderID)
	in.GatewayID = strings.TrimSpace(in.GatewayID)
	in.PlanID = strings.TrimSpace(in.PlanID)
	if in.OrderID == "" || in.GatewayID == "" {
		writeErr(w, domain.ErrInvalidInput)
		return
	}
	gw, err := d.Gateways.GetByID(r.Context(), in.GatewayID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !gw.Active {
		writeErr(w, domain.ErrInvalidInput)
		return
	}
	// Resolve amount/currency. Caller pode passar explícito (amount+currency)
	// ou só plan_id + opcional pay_currency e a gente computa a partir do
	// plano. Sem nada → 422.
	amount := strings.TrimSpace(in.Amount)
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if amount == "" || currency == "" {
		if in.PlanID == "" {
			writeErr(w, domain.ErrInvalidInput)
			return
		}
		plan, err := d.Plans.GetByID(r.Context(), in.PlanID)
		if err != nil {
			writeErr(w, err)
			return
		}
		// Pra providers multi-currency, usa a pay_currency escolhida pelo
		// cliente. Pra single-currency, pega a primeira accepted que existe
		// na tabela de currencies. Fallback final: settlement do display.
		pickCur := strings.ToUpper(strings.TrimSpace(in.PayCurrency))
		if pickCur == "" || !gwAccepts(gw, pickCur) {
			for _, c := range gw.AcceptedCurrencies {
				pickCur = strings.ToUpper(strings.TrimSpace(c))
				break
			}
		}
		if pickCur == "" {
			writeErr(w, domain.ErrInvalidInput)
			return
		}
		amt, code, ok := d.Methods.AmountInCurrency(r.Context(), plan, pickCur)
		if !ok {
			writeErr(w, domain.ErrInvalidInput)
			return
		}
		amount = amt
		currency = code
	}
	provider, ok := d.Registry.Get(gw.Provider)
	if !ok {
		writeErr(w, domain.ErrInvalidInput)
		return
	}
	desc := in.Description
	if desc == "" {
		desc = "Order " + in.OrderID
	}
	charge, err := provider.CreateCharge(r.Context(), application.PaymentChargeInput{
		OrderID:     in.OrderID,
		Description: desc,
		Amount:      amount,
		Currency:    currency,
		Customer:    in.Customer,
		Config:      gw.Config,
	})
	if err != nil {
		// Erros do provider são "user-facing" (config errada, key inválida,
		// API do gateway 4xx) — 422 mantém a semântica de "input rejeitado
		// pelo upstream". 500 fica reservado pra panic/db down.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, chargeResponse{
		Provider:     gw.Provider,
		ExternalRef:  charge.ExternalRef,
		PaymentURL:   charge.PaymentURL,
		PaymentExtra: charge.Extra,
	})
}

// gwAccepts replica a util do payment_methods.go sem expor o helper privado.
func gwAccepts(g *domain.PaymentGateway, code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, c := range g.AcceptedCurrencies {
		if strings.ToUpper(strings.TrimSpace(c)) == code {
			return true
		}
	}
	return false
}

// ─── /internal/v1/gateways CRUD ────────────────────────────────────────────

func (d *Deps) listGatewaysHandler(w http.ResponseWriter, r *http.Request) {
	list, err := d.Gateways.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"gateways": list})
}

func (d *Deps) createGatewayHandler(w http.ResponseWriter, r *http.Request) {
	var in application.CreateGatewayInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, domain.ErrInvalidInput)
		return
	}
	g, err := d.Gateways.Create(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (d *Deps) updateGatewayHandler(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in application.UpdateGatewayInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, domain.ErrInvalidInput)
		return
	}
	in.ID = id
	g, err := d.Gateways.Update(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (d *Deps) deleteGatewayHandler(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := d.Gateways.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Deps) getGatewayHandler(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	g, err := d.Gateways.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}
