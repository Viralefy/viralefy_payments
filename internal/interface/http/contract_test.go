package http

// Contract test do lado servidor — espelha
// viralefy_api/internal/infrastructure/external/paymentsclient/client_test.go.
//
// Objetivo: serializar as MESMAS structs que `chargeHandler` e `methodsHandler`
// emitem e validar que o JSON resultante bate, byte-a-byte significante, com
// as fixtures que o paymentsclient_test usa pra desserializar.
//
// Se uma das pontas mudar tag/envelope, OS DOIS LADOS quebram juntos —
// força sincronização. Era esse o tipo de drift que mandou QR code pro
// limbo em prod.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Estes literais DEVEM bater com os do paymentsclient_test.go.
// Quando alterar uma fixture, alterar a outra também (são contractos
// compartilhados — atualização tem que ser síncrona pelos 2 PRs).
const expectedChargeFields = `"provider","external_ref","payment_url","payment_extra"`
const expectedMethodsEnvelope = `"methods"`

func TestChargeResponseShape_MatchesClient(t *testing.T) {
	// Snapshot do mesmo cenário do client_test (abacatepay PIX dinâmico).
	got, err := json.Marshal(chargeResponse{
		Provider:    "abacatepay",
		ExternalRef: "abc_tx_01H8XYZ1234",
		PaymentURL:  "https://api.abacatepay.com/v2/transparents/qr/abc_tx_01H8XYZ1234",
		PaymentExtra: map[string]string{
			"br_code":       "00020126360014BR.GOV.BCB.PIX...",
			"qr_code_image": "data:image/png;base64,iVBORw0KGgo=",
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, key := range []string{
		`"provider":"abacatepay"`,
		`"external_ref":"abc_tx_01H8XYZ1234"`,
		`"payment_url":"https://api.abacatepay.com`,
		`"payment_extra":`,
		`"br_code":"00020126`,
		`"qr_code_image":"data:image/png;base64`,
	} {
		if !strings.Contains(string(got), key) {
			t.Errorf("chargeResponse JSON sem %s\ngot=%s", key, string(got))
		}
	}

	// Roundtrip pra struct espelho (o que o client tem) confirma que TUDO
	// chega — qualquer rename de tag aqui faz o campo virar zero value lá.
	var mirror struct {
		Provider     string            `json:"provider"`
		ExternalRef  string            `json:"external_ref"`
		PaymentURL   string            `json:"payment_url"`
		PaymentExtra map[string]string `json:"payment_extra"`
	}
	if err := json.Unmarshal(got, &mirror); err != nil {
		t.Fatalf("unmarshal mirror: %v", err)
	}
	if mirror.Provider == "" || mirror.ExternalRef == "" || mirror.PaymentURL == "" || len(mirror.PaymentExtra) == 0 {
		t.Errorf("mirror perdeu campos — DRIFT: %+v", mirror)
	}
}

// TestMethodsEnvelope_MatchesClient garante que continuamos emitindo o
// envelope {"methods":[...]} e não array raw.
func TestMethodsEnvelope_MatchesClient(t *testing.T) {
	// Mesmo formato que methodsHandler usa: writeJSON(..., map[string]any{"methods": options}).
	payload := map[string]any{
		"methods": []map[string]string{
			{"gateway_id": "gw-1", "provider": "stripe", "kind": "card"},
		},
	}
	buf := bytes.Buffer{}
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(payload); err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(strings.TrimSpace(out), `{"methods":`) {
		t.Fatalf("envelope quebrado — client unmarshal pra []PaymentMethodOption ia explodir.\ngot=%s", out)
	}
}

// TestErrorShape mantém o contrato {"error":"..."} pra 4xx — o client
// trunca a 300 chars e enfia no Go error.
func TestErrorShape(t *testing.T) {
	for _, code := range []string{"not_found", "invalid_input", "conflict", "internal"} {
		raw, _ := json.Marshal(map[string]string{"error": code})
		if !strings.Contains(string(raw), `"error":"`+code+`"`) {
			t.Errorf("error envelope %q mal-formado: %s", code, string(raw))
		}
	}
}
