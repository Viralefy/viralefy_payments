// Package payment — AbacatePay PIX provider.
//
// AbacatePay é um processor brasileiro especializado em PIX (BR-only). API
// REST v2, auth Bearer. Substitui Woovi em volume alto se preço bater.
//
// Diferencial vs ManualPIX: aqui o BR Code é gerado pelo processor (não pela
// chave PIX estática), QR é único por cobrança e o status é confirmado via
// webhook automático — fecha o loop sem mark-as-paid manual.
//
// Doc: https://docs.abacatepay.com/pages/transparents/create
package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Viralefy/viralefy_payments/internal/application"
)

const abacatePayDefaultBaseURL = "https://api.abacatepay.com"

// AbacatePay implementa application.PaymentProvider.
type AbacatePay struct {
	client *http.Client
}

func NewAbacatePay() *AbacatePay {
	return &AbacatePay{client: &http.Client{Timeout: 20 * time.Second}}
}

func (*AbacatePay) Provider() string { return "abacatepay" }

// validateAbacatePayKey aceita só formatos reais. Como Stripe, isso evita o
// modo de falha mais comum (admin cola key errada e descobre só na primeira
// venda real).
func validateAbacatePayKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("abacatepay: missing api_key in config")
	}
	// Keys live começam com "abc_live_" e dev com "abc_dev_" (heurística do
	// dashboard — confirmar no portal). Aceitamos prefixo Bearer "abc_" pra
	// não chumbar variante.
	if !strings.HasPrefix(key, "abc_") {
		return errors.New("abacatepay: api_key has unrecognized prefix — expected abc_live_… or abc_dev_…")
	}
	return nil
}

type abacatePayReq struct {
	Method string                `json:"method"`
	Data   abacatePayChargeData `json:"data"`
}

type abacatePayChargeData struct {
	Amount      int                          `json:"amount"`
	Description string                       `json:"description,omitempty"`
	ExpiresIn   int                          `json:"expiresIn,omitempty"`
	ExternalID  string                       `json:"externalId,omitempty"`
	Customer    *abacatePayCustomer          `json:"customer,omitempty"`
	Metadata    map[string]string            `json:"metadata,omitempty"`
}

type abacatePayCustomer struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	TaxID     string `json:"taxId"`
	Cellphone string `json:"cellphone"`
}

type abacatePayResp struct {
	Data struct {
		ID           string `json:"id"`
		Amount       int    `json:"amount"`
		Status       string `json:"status"`
		BRCode       string `json:"brCode"`
		BRCodeBase64 string `json:"brCodeBase64"`
		ExpiresAt    string `json:"expiresAt"`
	} `json:"data"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// CreateCharge gera o PIX dinâmico. Amount vai em centavos.
//
// IMPORTANTE: o customer object exige TODOS os 4 subfields (name + email +
// taxId + cellphone). Como o checkout viralefy não captura taxId/cellphone
// no flow normal, OMITIMOS o customer object e mandamos só description.
// AbacatePay aceita isso — customer fica anônimo no dashboard mas a venda
// sai. ExternalID é nosso order_id pra idempotency + reconciliação.
func (a *AbacatePay) CreateCharge(ctx context.Context, in application.PaymentChargeInput) (application.PaymentCharge, error) {
	apiKey := strings.TrimSpace(in.Config["api_key"])
	if err := validateAbacatePayKey(apiKey); err != nil {
		return application.PaymentCharge{}, err
	}
	baseURL := strings.TrimRight(in.Config["base_url"], "/")
	if baseURL == "" {
		baseURL = abacatePayDefaultBaseURL
	}

	// expiresIn default 1h (3600s). PIX dinâmico expira; cliente que não
	// pagou em 1h precisa gerar novo. Configurável via gateway.config.
	expiresIn := 3600
	if v, err := strconv.Atoi(strings.TrimSpace(in.Config["expires_in"])); err == nil && v > 0 {
		expiresIn = v
	}

	cents, err := amountToMinorUnitsAP(in.Amount)
	if err != nil {
		return application.PaymentCharge{}, fmt.Errorf("abacatepay: amount: %w", err)
	}

	body, _ := json.Marshal(abacatePayReq{
		Method: "PIX",
		Data: abacatePayChargeData{
			Amount:      int(cents),
			Description: truncStr(in.Description, 500),
			ExpiresIn:   expiresIn,
			ExternalID:  in.OrderID,
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/v2/transparents/create", bytes.NewReader(body))
	if err != nil {
		return application.PaymentCharge{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return application.PaymentCharge{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return application.PaymentCharge{}, fmt.Errorf("abacatepay: HTTP %d: %s", resp.StatusCode, truncStr(string(respBody), 300))
	}
	var out abacatePayResp
	if err := json.Unmarshal(respBody, &out); err != nil {
		return application.PaymentCharge{}, fmt.Errorf("abacatepay: parse: %w", err)
	}
	if out.Error != "" {
		return application.PaymentCharge{}, fmt.Errorf("abacatepay: %s", out.Error)
	}
	// Extras mantêm shape compatível com PIX manual + Woovi (br_code +
	// qr_code_image) pro CheckoutModal renderizar igual sem branching.
	extra := map[string]string{
		"method_kind":   "pix",
		"provider":      "abacatepay",
		"br_code":       out.Data.BRCode,
		"qr_code_image": out.Data.BRCodeBase64,
	}
	if out.Data.ExpiresAt != "" {
		extra["expires_at"] = out.Data.ExpiresAt
	}
	return application.PaymentCharge{
		ExternalRef: out.Data.ID,
		PaymentURL:  "", // PIX dinâmico não tem hosted page — só BR Code
		Extra:       extra,
	}, nil
}

// =========================
// Webhook signature (HMAC SHA256 + base64)
// =========================

// VerifyAbacatePayWebhook valida o header X-Webhook-Signature:
//   computed = base64( HMAC-SHA256(webhook_secret, raw_body) )
// Constant-time compare evita timing attack.
func VerifyAbacatePayWebhook(body []byte, signatureHeader, webhookSecret string) error {
	if webhookSecret == "" {
		return errors.New("abacatepay webhook: missing webhook_secret")
	}
	if strings.TrimSpace(signatureHeader) == "" {
		return errors.New("abacatepay webhook: missing X-Webhook-Signature header")
	}
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signatureHeader))) {
		return errors.New("abacatepay webhook: signature mismatch")
	}
	return nil
}

// AbacatePayEvent — shape parcial dos eventos webhook. Doc cobre
// transparent.completed + transparent.expired; aqui só precisamos identificar
// PAID + extrair externalId (nosso order_id).
type AbacatePayEvent struct {
	Event string `json:"event"`
	Data  struct {
		Transparent struct {
			ID         string `json:"id"`
			ExternalID string `json:"externalId"`
			Status     string `json:"status"`
			PaidAmount int    `json:"paidAmount"`
			Amount     int    `json:"amount"`
		} `json:"transparent"`
	} `json:"data"`
}

func ParseAbacatePayEvent(body []byte) (AbacatePayEvent, error) {
	var ev AbacatePayEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return ev, fmt.Errorf("abacatepay event: parse: %w", err)
	}
	if ev.Event == "" {
		return ev, errors.New("abacatepay event: missing event field")
	}
	return ev, nil
}

// IsPaid retorna true sse evento de pagamento confirmado (transparent.completed
// com status PAID). Outros eventos (expired, refunded) caem em false e
// o handler responde 200 sem chamar MarkOrderPaid.
func (e AbacatePayEvent) IsPaid() bool {
	if e.Event != "transparent.completed" {
		return false
	}
	return strings.ToUpper(e.Data.Transparent.Status) == "PAID"
}

// OrderID retorna o externalId que setamos no CreateCharge — nosso
// order_id, usado pelo handler pra chamar /internal/v1/payment-confirmed
// no monolito.
func (e AbacatePayEvent) OrderID() string {
	return strings.TrimSpace(e.Data.Transparent.ExternalID)
}

// ExternalRef retorna o id do AbacatePay (pix_char_…). Pode ser usado pra
// matching alternativo quando externalId não vier (deve sempre vir, mas
// defensivo).
func (e AbacatePayEvent) ExternalRef() string {
	return strings.TrimSpace(e.Data.Transparent.ID)
}

// =========================
// helpers
// =========================

// amountToMinorUnitsAP converte "9.90" → 990 (assumindo 2 decimals, BRL).
// Espelha amountToMinorUnits do Stripe, separado pra cada provider poder
// evoluir independente (ex.: JPY zero-decimal no futuro).
func amountToMinorUnitsAP(amount string) (int64, error) {
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return 0, errors.New("empty amount")
	}
	parts := strings.SplitN(amount, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	var dec int64
	if len(parts) == 2 {
		d := parts[1]
		if len(d) > 2 {
			d = d[:2]
		}
		for len(d) < 2 {
			d += "0"
		}
		dec, err = strconv.ParseInt(d, 10, 64)
		if err != nil {
			return 0, err
		}
	}
	return whole*100 + dec, nil
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
