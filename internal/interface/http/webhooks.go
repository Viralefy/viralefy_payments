package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/Viralefy/viralefy_payments/internal/infrastructure/external/payment"
)

// Webhook handlers — PÚBLICOS (montados pelo Caddy via reverse-proxy).
// Stripe/Heleket/Woovi NÃO conhecem o INTERNAL_SHARED_SECRET, então o
// InternalAuth middleware NÃO se aplica. Em vez disso, cada handler valida
// a signature do próprio provider usando o webhook_secret cadastrado no
// gateway. Defense-in-depth contra spoofer: timing-safe HMAC check.
//
// Fluxo:
//  1. carrega body (limitado a 1 MiB pra evitar DoS por payload gigante)
//  2. lookup gateway ativo do provider → busca webhook_secret/api_key
//  3. valida signature do provider (falha → 400, sem detalhe)
//  4. checa idempotência (Stripe: stripe_events_processed)
//  5. POST callback no monólito: /internal/v1/payment-confirmed
//  6. responde 200 ao provider (rápido — re-entrega é caro)

const webhookBodyLimit = 1 << 20 // 1 MiB

// stripeWebhookHandler valida Stripe-Signature e dispara callback.
func (d *Deps) stripeWebhookHandler(w nethttp.ResponseWriter, r *nethttp.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookBodyLimit))
	if err != nil {
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	gw, err := d.Gateways.GetActiveByProvider(r.Context(), "stripe")
	if err != nil || gw == nil {
		// Sem gateway Stripe ativo, não temos secret pra validar — rejeita.
		// Stripe vai entender 400 como "config errada na minha conta", não
		// como rede caída.
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	secret := strings.TrimSpace(gw.Config["webhook_secret"])
	if err := payment.VerifyStripeWebhook(body, r.Header.Get("Stripe-Signature"), secret); err != nil {
		slog.Default().Warn("stripe webhook signature failed", "err", err.Error())
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	ev, err := payment.ParseStripeEvent(body)
	if err != nil {
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	// Idempotência: marca event_id como processed. Falsa = replay → 200 noop.
	if d.StripeEvents != nil && ev.ID != "" {
		inserted, err := d.StripeEvents.Record(r.Context(), ev.ID, ev.Type, ev.OrderID())
		if err != nil {
			slog.Default().Error("stripe event record failed", "err", err.Error())
			// Não bloqueia o callback — DB transitório não pode segurar pagamento
			// (Stripe re-entrega e a próxima entrega pega o lock).
		} else if !inserted {
			// Replay: já vimos. Stripe quer 2xx pra parar de retentar.
			writeJSON(w, nethttp.StatusOK, map[string]string{"status": "duplicate"})
			return
		}
	}
	if !ev.IsPaid() {
		// Outros tipos de event (refund, dispute, etc.) ainda não tratamos;
		// 200 ok pra Stripe parar de mandar.
		writeJSON(w, nethttp.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	d.postCallback(r.Context(), "stripe", ev.OrderID(), ev.Data.Object.ID)
	writeJSON(w, nethttp.StatusOK, map[string]string{"status": "ok"})
}

// heleketWebhookHandler valida sign embutido + dispara callback.
func (d *Deps) heleketWebhookHandler(w nethttp.ResponseWriter, r *nethttp.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookBodyLimit))
	if err != nil {
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	gw, err := d.Gateways.GetActiveByProvider(r.Context(), "heleket")
	if err != nil || gw == nil {
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	apiKey := strings.TrimSpace(gw.Config["api_key"])
	if err := payment.VerifyHeleketWebhook(body, apiKey); err != nil {
		slog.Default().Warn("heleket webhook signature failed", "err", err.Error())
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	ev, err := payment.ParseHeleketEvent(body)
	if err != nil {
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	if !ev.IsPaid() {
		writeJSON(w, nethttp.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	d.postCallback(r.Context(), "heleket", ev.OrderID, ev.UUID)
	writeJSON(w, nethttp.StatusOK, map[string]string{"status": "ok"})
}

// wooviWebhookHandler valida x-webhook-signature + dispara callback.
func (d *Deps) wooviWebhookHandler(w nethttp.ResponseWriter, r *nethttp.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookBodyLimit))
	if err != nil {
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	gw, err := d.Gateways.GetActiveByProvider(r.Context(), "woovi")
	if err != nil || gw == nil {
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	secret := strings.TrimSpace(gw.Config["webhook_secret"])
	if err := payment.VerifyWooviWebhook(body, r.Header.Get("x-webhook-signature"), secret); err != nil {
		slog.Default().Warn("woovi webhook signature failed", "err", err.Error())
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	ev, err := payment.ParseWooviEvent(body)
	if err != nil {
		w.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	if !ev.IsPaid() {
		writeJSON(w, nethttp.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	d.postCallback(r.Context(), "woovi", ev.Charge.CorrelationID, ev.Charge.Identifier)
	writeJSON(w, nethttp.StatusOK, map[string]string{"status": "ok"})
}

// postCallback dispara POST {API_INTERNAL_CALLBACK_URL}/internal/v1/payment-confirmed
// com X-Internal-Token. Timeout curto (5s) — webhook response ao provider
// não pode esperar demais (Stripe corta em 10s). Falha aqui é loggada mas
// NÃO derruba o webhook — caller já validou signature, perder o callback
// significa o monólito vai pegar via reconciliation cron / polling.
func (d *Deps) postCallback(parent context.Context, provider, orderID, externalRef string) {
	if d.APIInternalCallbackURL == "" {
		slog.Default().Warn("payment callback skipped: API_INTERNAL_CALLBACK_URL empty",
			"provider", provider, "order_id", orderID)
		return
	}
	if orderID == "" {
		slog.Default().Warn("payment callback skipped: empty order_id",
			"provider", provider, "external_ref", externalRef)
		return
	}
	payload := map[string]string{
		"provider":     provider,
		"order_id":     orderID,
		"external_ref": externalRef,
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	url := strings.TrimRight(d.APIInternalCallbackURL, "/") + "/internal/v1/payment-confirmed"
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.Default().Error("payment callback build failed", "err", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", d.InternalSharedSecret)
	client := &nethttp.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Default().Error("payment callback request failed", "err", err.Error(),
			"provider", provider, "order_id", orderID)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Default().Error("payment callback non-2xx",
			"status", resp.StatusCode, "provider", provider, "order_id", orderID)
	}
}
