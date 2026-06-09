package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidateAbacatePayKey_AcceptsLiveAndDev(t *testing.T) {
	for _, k := range []string{"abc_live_xxx", "abc_dev_yyy"} {
		if err := validateAbacatePayKey(k); err != nil {
			t.Fatalf("expected ok for %q, got %v", k, err)
		}
	}
}

func TestValidateAbacatePayKey_RejectsEmpty(t *testing.T) {
	if err := validateAbacatePayKey(""); err == nil {
		t.Fatal("empty key should be rejected")
	}
}

func TestValidateAbacatePayKey_RejectsUnknownPrefix(t *testing.T) {
	for _, k := range []string{"sk_live_x", "pk_live_x", "whsec_x", "garbage"} {
		if err := validateAbacatePayKey(k); err == nil {
			t.Fatalf("%q should be rejected", k)
		}
	}
}

func signAbacate(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestVerifyAbacatePayWebhook_OK(t *testing.T) {
	secret := "shh"
	body := []byte(`{"event":"transparent.completed"}`)
	hdr := signAbacate(body, secret)
	if err := VerifyAbacatePayWebhook(body, hdr, secret); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestVerifyAbacatePayWebhook_TamperedBodyFails(t *testing.T) {
	secret := "shh"
	hdr := signAbacate([]byte(`{"event":"transparent.completed"}`), secret)
	if err := VerifyAbacatePayWebhook([]byte(`{"event":"tampered"}`), hdr, secret); err == nil {
		t.Fatal("expected signature mismatch when body changes")
	}
}

func TestVerifyAbacatePayWebhook_MissingSecret(t *testing.T) {
	if err := VerifyAbacatePayWebhook([]byte(`{}`), "abc=", ""); err == nil {
		t.Fatal("missing secret should be rejected")
	}
}

func TestVerifyAbacatePayWebhook_MissingHeader(t *testing.T) {
	if err := VerifyAbacatePayWebhook([]byte(`{}`), "", "shh"); err == nil {
		t.Fatal("missing header should be rejected")
	}
}

func TestParseAbacatePayEvent_PaidExtractsOrderID(t *testing.T) {
	body := []byte(`{
		"event":"transparent.completed",
		"data":{"transparent":{"id":"char_xyz","externalId":"order-abc","status":"PAID","amount":5000,"paidAmount":5000}}
	}`)
	ev, err := ParseAbacatePayEvent(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ev.IsPaid() {
		t.Fatal("expected IsPaid true for transparent.completed + PAID")
	}
	if ev.OrderID() != "order-abc" {
		t.Fatalf("expected order-abc, got %q", ev.OrderID())
	}
	if ev.ExternalRef() != "char_xyz" {
		t.Fatalf("expected char_xyz, got %q", ev.ExternalRef())
	}
}

func TestParseAbacatePayEvent_IgnoresNonCompleted(t *testing.T) {
	body := []byte(`{
		"event":"transparent.expired",
		"data":{"transparent":{"id":"char_x","externalId":"o","status":"EXPIRED"}}
	}`)
	ev, _ := ParseAbacatePayEvent(body)
	if ev.IsPaid() {
		t.Fatal("expired event should not be IsPaid")
	}
}

func TestParseAbacatePayEvent_RejectsMissingEvent(t *testing.T) {
	if _, err := ParseAbacatePayEvent([]byte(`{}`)); err == nil {
		t.Fatal("missing event field should be rejected")
	}
}

func TestAmountToMinorUnitsAP_RoundTrip(t *testing.T) {
	cases := map[string]int64{
		"9.90":   990,
		"100.00": 10000,
		"0.01":   1,
		"50":     5000,
	}
	for amt, want := range cases {
		got, err := amountToMinorUnitsAP(amt)
		if err != nil {
			t.Fatalf("amount %q: %v", amt, err)
		}
		if got != want {
			t.Fatalf("amount %q: got %d want %d", amt, got, want)
		}
	}
}

func TestAmountToMinorUnitsAP_RejectsEmpty(t *testing.T) {
	if _, err := amountToMinorUnitsAP(""); err == nil {
		t.Fatal("empty amount should be rejected")
	}
	if _, err := amountToMinorUnitsAP(""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatal("expected empty amount error")
	}
}
