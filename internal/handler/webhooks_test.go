package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ---- verifyStripeSignature ----

func stripeSig(secret, ts string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(payload)))
	return hex.EncodeToString(mac.Sum(nil))
}

func stripeHeader(secret string, payload []byte) string {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	return "t=" + ts + ",v1=" + stripeSig(secret, ts, payload)
}

func TestVerifyStripeSignature_Valid(t *testing.T) {
	payload := []byte(`{"id":"evt_1","type":"payment_intent.succeeded"}`)
	secret := "whsec_test"
	header := stripeHeader(secret, payload)
	if !verifyStripeSignature(payload, header, secret) {
		t.Error("expected signature to be valid")
	}
}

func TestVerifyStripeSignature_WrongSecret(t *testing.T) {
	payload := []byte(`{"id":"evt_1"}`)
	header := stripeHeader("correct-secret", payload)
	if verifyStripeSignature(payload, header, "wrong-secret") {
		t.Error("expected signature to be invalid with wrong secret")
	}
}

func TestVerifyStripeSignature_TamperedPayload(t *testing.T) {
	payload := []byte(`{"id":"evt_1","amount":100}`)
	header := stripeHeader("secret", payload)
	tampered := []byte(`{"id":"evt_1","amount":999}`)
	if verifyStripeSignature(tampered, header, "secret") {
		t.Error("expected signature to be invalid after payload tamper")
	}
}

func TestVerifyStripeSignature_MissingTimestamp(t *testing.T) {
	if verifyStripeSignature([]byte("payload"), "v1=abc123", "secret") {
		t.Error("missing timestamp should fail")
	}
}

func TestVerifyStripeSignature_MissingV1(t *testing.T) {
	if verifyStripeSignature([]byte("payload"), "t=12345", "secret") {
		t.Error("missing v1 signature should fail")
	}
}

func TestVerifyStripeSignature_Empty(t *testing.T) {
	if verifyStripeSignature([]byte("payload"), "", "secret") {
		t.Error("empty header should fail")
	}
}

func TestVerifyStripeSignature_MultipleV1Entries(t *testing.T) {
	// Stripe sends several v1 entries during secret rotation: any single
	// matching signature must verify, regardless of position.
	payload := []byte(`{"id":"evt_rot"}`)
	secret := "whsec_current"
	ts := fmt.Sprintf("%d", time.Now().Unix())
	good := stripeSig(secret, ts, payload)
	bad := stripeSig("whsec_old", ts, payload)

	wrongThenRight := "t=" + ts + ",v1=" + bad + ",v1=" + good
	if !verifyStripeSignature(payload, wrongThenRight, secret) {
		t.Error("header with wrong v1 followed by correct v1 should verify")
	}
	rightThenWrong := "t=" + ts + ",v1=" + good + ",v1=" + bad
	if !verifyStripeSignature(payload, rightThenWrong, secret) {
		t.Error("header with correct v1 followed by wrong v1 should verify")
	}
	allWrong := "t=" + ts + ",v1=" + bad + ",v1=" + stripeSig("whsec_older", ts, payload)
	if verifyStripeSignature(payload, allWrong, secret) {
		t.Error("header with only wrong v1 entries should fail")
	}
}

// ---- Stripe webhook handler ----

func stripeEventJSON(eventID, eventType string, data any) []byte {
	dataBytes, _ := json.Marshal(data)
	return []byte(fmt.Sprintf(`{"id":%q,"type":%q,"data":%s}`, eventID, eventType, dataBytes))
}

func stripePaymentObject(invoiceID, memberID string) map[string]any {
	return map[string]any{
		"object": map[string]any{
			"id":       "ch_abc",
			"amount":   5000,
			"currency": "usd",
			"metadata": map[string]any{
				"quorum_invoice_id": invoiceID,
				"quorum_member_id":  memberID,
			},
			"payment_method_details": map[string]any{"type": "card"},
		},
	}
}

func TestStripeWebhook_NotConfigured(t *testing.T) {
	// Empty secret with allowUnsigned=false must 503 without touching the
	// repo (nil mock fns panic if any processing happens).
	h := NewWebhooksHandler(&mockDuesRepo{}, "", "", false)
	payload := stripeEventJSON("evt_1", "payment_intent.succeeded", stripePaymentObject("", ""))
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 503 {
		t.Errorf("status: got %d, want 503 when secret unset and unsigned not allowed", rr.Code)
	}
}

func TestStripeWebhook_AllowUnsignedProcessesWithoutSecret(t *testing.T) {
	recorded := false
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, eventID string, tx *model.Transaction) (bool, error) {
			recorded = true
			return false, nil
		},
		FindInvoiceByProviderRefFn: func(_ context.Context, _ string) (string, error) { return "", nil },
	}, "", "", true) // empty secret, unsigned allowed — dev mode

	payload := stripeEventJSON("evt_new", "charge.succeeded", stripePaymentObject(testUUID, testUUID2))
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if !recorded {
		t.Error("expected RecordWebhookPayment to be called")
	}
}

func TestStripeWebhook_InvalidSignature(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{}, "real-secret", "", false)
	payload := []byte(`{"id":"evt_1","type":"payment_intent.succeeded","data":{}}`)
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	req.Header.Set("Stripe-Signature", "t=12345,v1=badsig")
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for bad signature", rr.Code)
	}
}

func TestStripeWebhook_IdempotentAlreadyProcessed(t *testing.T) {
	// RecordWebhookPayment reporting already=true (duplicate delivery) must
	// still return 200 so the provider stops retrying.
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, _ string, _ *model.Transaction) (bool, error) {
			return true, nil
		},
		FindInvoiceByProviderRefFn: func(_ context.Context, _ string) (string, error) { return "", nil },
	}, "", "", true)
	payload := stripeEventJSON("evt_dup", "payment_intent.succeeded", stripePaymentObject(testUUID, testUUID2))
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200 for duplicate event", rr.Code)
	}
}

func TestStripeWebhook_RecordPaymentError(t *testing.T) {
	// A transactional failure must return 500 (provider retries) and must NOT
	// mark the event processed (nil MarkEventProcessedFn panics if called).
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, _ string, _ *model.Transaction) (bool, error) {
			return false, errors.New("db down")
		},
		FindInvoiceByProviderRefFn: func(_ context.Context, _ string) (string, error) { return "", nil },
	}, "", "", true)
	payload := stripeEventJSON("evt_fail", "charge.succeeded", stripePaymentObject(testUUID, testUUID2))
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500 so the provider retries", rr.Code)
	}
}

func TestStripeWebhook_UnhandledEventType(t *testing.T) {
	marked := false
	h := NewWebhooksHandler(&mockDuesRepo{
		MarkEventProcessedFn: func(_ context.Context, eventID string) error {
			marked = true
			if eventID != "evt_x" {
				t.Errorf("event id: got %q, want evt_x", eventID)
			}
			return nil
		},
	}, "", "", true)
	payload := stripeEventJSON("evt_x", "customer.created", map[string]any{})
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
	if !marked {
		t.Error("expected MarkEventProcessed for unhandled event type")
	}
}

func TestStripeWebhook_MarkEventProcessedError(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{
		MarkEventProcessedFn: func(_ context.Context, _ string) error { return errors.New("db down") },
	}, "", "", true)
	payload := stripeEventJSON("evt_x", "customer.created", map[string]any{})
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500 when marking processed fails", rr.Code)
	}
}

func TestStripeWebhook_MalformedPaymentObject(t *testing.T) {
	// A payment object that cannot be parsed is a permanent failure: mark the
	// event processed, return 200, never record a payment (nil
	// RecordWebhookPaymentFn panics if called).
	marked := false
	h := NewWebhooksHandler(&mockDuesRepo{
		MarkEventProcessedFn: func(_ context.Context, _ string) error { marked = true; return nil },
	}, "", "", true)
	// amount as a string fails the int64 unmarshal.
	payload := []byte(`{"id":"evt_bad","type":"charge.succeeded","data":{"object":{"amount":"lots"}}}`)
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200 for permanently malformed payload", rr.Code)
	}
	if !marked {
		t.Error("expected MarkEventProcessed for malformed payment object")
	}
}

func TestStripeWebhook_BadJSON(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{}, "", "", true)
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader("{bad json"))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestStripeWebhook_MissingEventID(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{}, "", "", true)
	req := httptest.NewRequest("POST", "/webhooks/stripe",
		strings.NewReader(`{"id":"","type":"charge.succeeded","data":{}}`))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for empty event id", rr.Code)
	}
}

func TestStripeWebhook_CreatesTransactionWithCorrectAmount(t *testing.T) {
	var capturedTx model.Transaction
	var capturedClaimKey string
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, claimKey string, tx *model.Transaction) (bool, error) {
			capturedClaimKey = claimKey
			capturedTx = *tx
			return false, nil
		},
	}, "", "", true)

	obj := map[string]any{
		"object": map[string]any{
			"id":                     "ch_xyz",
			"amount":                 2500, // cents
			"currency":               "usd",
			"metadata":               map[string]any{"quorum_invoice_id": testUUID, "quorum_member_id": testUUID2},
			"payment_method_details": map[string]any{"type": "card"},
		},
	}
	payload := stripeEventJSON("evt_amt", "charge.succeeded", obj)
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	h.Stripe(httptest.NewRecorder(), req)

	// No payment_intent on the charge → claim key falls back to the object id.
	if capturedClaimKey != "ch_xyz" {
		t.Errorf("claim key: got %q, want ch_xyz", capturedClaimKey)
	}
	// Stripe amounts are already minor units and stored as-is, no division.
	if capturedTx.AmountMinor != 2500 {
		t.Errorf("amount_minor: got %d, want 2500 (stored as-is)", capturedTx.AmountMinor)
	}
	if capturedTx.Provider != "stripe" {
		t.Errorf("provider: got %q, want stripe", capturedTx.Provider)
	}
	if capturedTx.Currency != "USD" {
		t.Errorf("currency: got %q, want USD", capturedTx.Currency)
	}
	if capturedTx.InvoiceID == nil || *capturedTx.InvoiceID != testUUID {
		t.Errorf("invoice_id: got %v, want %s", capturedTx.InvoiceID, testUUID)
	}
	if capturedTx.MemberID == nil || *capturedTx.MemberID != testUUID2 {
		t.Errorf("member_id: got %v, want %s", capturedTx.MemberID, testUUID2)
	}
}

// TestStripeWebhook_ZeroDecimalCurrency verifies a JPY charge is stored as-is
// (no division) end-to-end through the handler.
func TestStripeWebhook_ZeroDecimalCurrency(t *testing.T) {
	var capturedTx model.Transaction
	h := NewWebhooksHandler(&mockDuesRepo{
		FindInvoiceByProviderRefFn: func(_ context.Context, _ string) (string, error) { return "", pgx.ErrNoRows },
		RecordWebhookPaymentFn: func(_ context.Context, _ string, tx *model.Transaction) (bool, error) {
			capturedTx = *tx
			return false, nil
		},
	}, "", "", true)

	obj := map[string]any{"object": map[string]any{
		"id": "ch_jpy", "amount": 5000, "currency": "jpy",
		"metadata": map[string]any{}, "payment_method_details": map[string]any{"type": "card"},
	}}
	payload := stripeEventJSON("evt_jpy", "charge.succeeded", obj)
	h.Stripe(httptest.NewRecorder(), httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload))))

	if capturedTx.AmountMinor != 5000 {
		t.Errorf("amount_minor: got %d, want 5000 (stored as-is)", capturedTx.AmountMinor)
	}
	if capturedTx.Currency != "JPY" {
		t.Errorf("currency: got %q, want JPY", capturedTx.Currency)
	}
}

// TestStripeWebhook_DedupesAcrossEventTypes verifies that payment_intent.succeeded
// and charge.succeeded for the SAME payment claim on the shared payment-intent
// id, so the idempotency table (keyed on the claim key) records the payment once
// instead of double-crediting the invoice.
func TestStripeWebhook_DedupesAcrossEventTypes(t *testing.T) {
	claimKeys := []string{}
	h := NewWebhooksHandler(&mockDuesRepo{
		FindInvoiceByProviderRefFn: func(_ context.Context, _ string) (string, error) { return "", pgx.ErrNoRows },
		RecordWebhookPaymentFn: func(_ context.Context, claimKey string, _ *model.Transaction) (bool, error) {
			claimKeys = append(claimKeys, claimKey)
			return false, nil
		},
	}, "", "", true)

	// payment_intent.succeeded: object id IS the payment intent.
	piObj := map[string]any{"object": map[string]any{
		"id": "pi_123", "amount": 1000, "currency": "usd",
		"metadata": map[string]any{}, "payment_method_details": map[string]any{"type": "card"},
	}}
	pi := stripeEventJSON("evt_pi", "payment_intent.succeeded", piObj)
	h.Stripe(httptest.NewRecorder(), httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(pi))))

	// charge.succeeded for the same payment: distinct object id, but carries the
	// payment_intent link → same claim key.
	chObj := map[string]any{"object": map[string]any{
		"id": "ch_456", "payment_intent": "pi_123", "amount": 1000, "currency": "usd",
		"metadata": map[string]any{}, "payment_method_details": map[string]any{"type": "card"},
	}}
	ch := stripeEventJSON("evt_ch", "charge.succeeded", chObj)
	h.Stripe(httptest.NewRecorder(), httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(ch))))

	if len(claimKeys) != 2 || claimKeys[0] != "pi_123" || claimKeys[1] != "pi_123" {
		t.Errorf("both events must claim on the shared payment-intent id pi_123, got %v", claimKeys)
	}
}

// TestStripeWebhook_LinkLookupOutageRetries verifies a transient error from the
// invoice-linking lookup returns 500 (so Stripe retries) without recording an
// unlinked payment or claiming the event.
func TestStripeWebhook_LinkLookupOutageRetries(t *testing.T) {
	recorded := false
	h := NewWebhooksHandler(&mockDuesRepo{
		FindInvoiceByProviderRefFn: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("db down")
		},
		RecordWebhookPaymentFn: func(_ context.Context, _ string, _ *model.Transaction) (bool, error) {
			recorded = true
			return false, nil
		},
	}, "", "", true)

	// No invoice metadata → handler falls back to the provider-ref lookup, which errors.
	obj := map[string]any{"object": map[string]any{
		"id": "ch_x", "amount": 500, "currency": "usd",
		"metadata": map[string]any{}, "payment_method_details": map[string]any{"type": "card"},
	}}
	payload := stripeEventJSON("evt_lookup", "charge.succeeded", obj)
	rr := httptest.NewRecorder()
	h.Stripe(rr, httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload))))

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500 so the provider retries", rr.Code)
	}
	if recorded {
		t.Error("payment must not be recorded when the linking lookup fails transiently")
	}
}

func TestStripeWebhook_NonUUIDMetadataUnlinked(t *testing.T) {
	// Non-UUID metadata would poison the ::uuid cast, so the payment is
	// recorded with nil invoice/member instead of failing forever.
	var capturedTx model.Transaction
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, _ string, tx *model.Transaction) (bool, error) {
			capturedTx = *tx
			return false, nil
		},
		FindInvoiceByProviderRefFn: func(_ context.Context, _ string) (string, error) { return "", nil },
	}, "", "", true)

	payload := stripeEventJSON("evt_meta", "charge.succeeded", stripePaymentObject("inv-1", "mem-1"))
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if capturedTx.InvoiceID != nil {
		t.Errorf("invoice_id: got %q, want nil for non-UUID metadata", *capturedTx.InvoiceID)
	}
	if capturedTx.MemberID != nil {
		t.Errorf("member_id: got %q, want nil for non-UUID metadata", *capturedTx.MemberID)
	}
}

// ---- PayPal webhook handler ----

func paypalCaptureJSON(eventID string, resource map[string]any) []byte {
	payload := map[string]any{
		"id":         eventID,
		"event_type": "PAYMENT.CAPTURE.COMPLETED",
		"resource":   resource,
	}
	body, _ := json.Marshal(payload)
	return body
}

func TestPayPalWebhook_NotConfigured(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{}, "", "", false)
	body := paypalCaptureJSON("pp_evt_0", map[string]any{})
	req := httptest.NewRequest("POST", "/webhooks/paypal", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)
	if rr.Code != 503 {
		t.Errorf("status: got %d, want 503 when webhook id unset and unsigned not allowed", rr.Code)
	}
}

func TestPayPalWebhook_CaptureCompleted(t *testing.T) {
	var capturedTx model.Transaction
	var capturedEventID string
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, eventID string, tx *model.Transaction) (bool, error) {
			capturedEventID = eventID
			capturedTx = *tx
			return false, nil
		},
	}, "", "", true)

	resource := map[string]any{
		"id": "pp_cap_1",
		"amount": map[string]any{
			"value":         "75.50",
			"currency_code": "USD",
		},
		"custom_id": "invoice:" + testUUID,
	}
	req := httptest.NewRequest("POST", "/webhooks/paypal",
		strings.NewReader(string(paypalCaptureJSON("pp_evt_1", resource))))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if capturedEventID != "pp_evt_1" {
		t.Errorf("event id: got %q, want pp_evt_1", capturedEventID)
	}
	if capturedTx.Provider != "paypal" {
		t.Errorf("provider: got %q, want paypal", capturedTx.Provider)
	}
	// "75.50" USD parses to 7550 minor units.
	wantMinor, _ := model.ParseMoney("75.50", "USD")
	if capturedTx.AmountMinor != wantMinor {
		t.Errorf("amount_minor: got %d, want %d (ParseMoney of 75.50 USD)", capturedTx.AmountMinor, wantMinor)
	}
	if capturedTx.AmountMinor != 7550 {
		t.Errorf("amount_minor: got %d, want 7550", capturedTx.AmountMinor)
	}
	if capturedTx.InvoiceID == nil || *capturedTx.InvoiceID != testUUID {
		t.Errorf("invoice_id: got %v, want %s", capturedTx.InvoiceID, testUUID)
	}
}

func TestPayPalWebhook_ParsesUSDAmountToMinorUnits(t *testing.T) {
	var capturedTx model.Transaction
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, _ string, tx *model.Transaction) (bool, error) {
			capturedTx = *tx
			return false, nil
		},
	}, "", "", true)

	resource := map[string]any{
		"id":     "pp_cap_usd",
		"amount": map[string]any{"value": "10.00", "currency_code": "USD"},
	}
	req := httptest.NewRequest("POST", "/webhooks/paypal",
		strings.NewReader(string(paypalCaptureJSON("pp_usd", resource))))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if capturedTx.AmountMinor != 1000 {
		t.Errorf("amount_minor: got %d, want 1000 (10.00 USD → 1000 cents)", capturedTx.AmountMinor)
	}
	if capturedTx.Currency != "USD" {
		t.Errorf("currency: got %q, want USD", capturedTx.Currency)
	}
}

func TestPayPalWebhook_ZeroDecimalCurrencyRespectsExponent(t *testing.T) {
	// JPY has exponent 0, so "10" (no fractional part) parses to 10 minor units,
	// not 1000 — the currency exponent is respected on the PayPal path.
	var capturedTx model.Transaction
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, _ string, tx *model.Transaction) (bool, error) {
			capturedTx = *tx
			return false, nil
		},
	}, "", "", true)

	resource := map[string]any{
		"id":     "pp_cap_jpy",
		"amount": map[string]any{"value": "10", "currency_code": "JPY"},
	}
	req := httptest.NewRequest("POST", "/webhooks/paypal",
		strings.NewReader(string(paypalCaptureJSON("pp_jpy", resource))))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	wantMinor, _ := model.ParseMoney("10", "JPY")
	if capturedTx.AmountMinor != wantMinor {
		t.Errorf("amount_minor: got %d, want %d (ParseMoney of 10 JPY)", capturedTx.AmountMinor, wantMinor)
	}
	if capturedTx.AmountMinor != 10 {
		t.Errorf("amount_minor: got %d, want 10 (JPY zero-decimal)", capturedTx.AmountMinor)
	}
	if capturedTx.Currency != "JPY" {
		t.Errorf("currency: got %q, want JPY", capturedTx.Currency)
	}
}

func TestPayPalWebhook_IdempotentAlreadyProcessed(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, _ string, _ *model.Transaction) (bool, error) {
			return true, nil
		},
	}, "", "", true)

	resource := map[string]any{
		"id":     "pp_cap_dup",
		"amount": map[string]any{"value": "10.00", "currency_code": "USD"},
	}
	req := httptest.NewRequest("POST", "/webhooks/paypal",
		strings.NewReader(string(paypalCaptureJSON("pp_dup", resource))))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200 for duplicate event", rr.Code)
	}
}

func TestPayPalWebhook_RecordPaymentError(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, _ string, _ *model.Transaction) (bool, error) {
			return false, errors.New("db down")
		},
	}, "", "", true)
	resource := map[string]any{
		"id":     "pp_cap_err",
		"amount": map[string]any{"value": "10.00", "currency_code": "USD"},
	}
	req := httptest.NewRequest("POST", "/webhooks/paypal",
		strings.NewReader(string(paypalCaptureJSON("pp_err", resource))))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)
	if rr.Code != 500 {
		t.Errorf("status: got %d, want 500 so the provider retries", rr.Code)
	}
}

func TestPayPalWebhook_UnhandledEventType(t *testing.T) {
	marked := false
	h := NewWebhooksHandler(&mockDuesRepo{
		MarkEventProcessedFn: func(_ context.Context, _ string) error { marked = true; return nil },
	}, "", "", true)
	payload := `{"id":"pp_other","event_type":"BILLING.PLAN.CREATED","resource":{}}`
	req := httptest.NewRequest("POST", "/webhooks/paypal", strings.NewReader(payload))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
	if !marked {
		t.Error("expected MarkEventProcessed for non-payment event type")
	}
}

func TestPayPalWebhook_UnparseableAmountDropped(t *testing.T) {
	// An amount that fails to parse is a permanent failure: mark processed,
	// 200, no payment recorded (nil RecordWebhookPaymentFn panics if called).
	marked := false
	h := NewWebhooksHandler(&mockDuesRepo{
		MarkEventProcessedFn: func(_ context.Context, _ string) error { marked = true; return nil },
	}, "", "", true)
	resource := map[string]any{
		"id":     "pp_cap_bad",
		"amount": map[string]any{"value": "seventy", "currency_code": "USD"},
	}
	req := httptest.NewRequest("POST", "/webhooks/paypal",
		strings.NewReader(string(paypalCaptureJSON("pp_bad_amt", resource))))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200 for unparseable amount", rr.Code)
	}
	if !marked {
		t.Error("expected MarkEventProcessed for unparseable amount")
	}
}

func TestPayPalWebhook_BadJSON(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{}, "", "", true)
	req := httptest.NewRequest("POST", "/webhooks/paypal", strings.NewReader("{bad"))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestPayPalWebhook_MissingEventID(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{}, "", "", true)
	req := httptest.NewRequest("POST", "/webhooks/paypal",
		strings.NewReader(`{"id":"","event_type":"PAYMENT.CAPTURE.COMPLETED","resource":{}}`))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for empty event id", rr.Code)
	}
}

func TestPayPalWebhook_NoInvoicePrefix(t *testing.T) {
	// custom_id without "invoice:" prefix → no invoice linked
	var capturedTx model.Transaction
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, _ string, tx *model.Transaction) (bool, error) {
			capturedTx = *tx
			return false, nil
		},
	}, "", "", true)

	resource := map[string]any{
		"id":        "pp_cap_2",
		"amount":    map[string]any{"value": "10.00", "currency_code": "USD"},
		"custom_id": "some-other-reference",
	}
	req := httptest.NewRequest("POST", "/webhooks/paypal",
		strings.NewReader(string(paypalCaptureJSON("pp_evt_2", resource))))
	h.PayPal(httptest.NewRecorder(), req)

	if capturedTx.InvoiceID != nil {
		t.Errorf("expected nil invoice_id when custom_id has no 'invoice:' prefix, got %q", *capturedTx.InvoiceID)
	}
}

func TestPayPalWebhook_NonUUIDCustomIDUnlinked(t *testing.T) {
	// "invoice:garbage" must not become an invoice reference.
	var capturedTx model.Transaction
	h := NewWebhooksHandler(&mockDuesRepo{
		RecordWebhookPaymentFn: func(_ context.Context, _ string, tx *model.Transaction) (bool, error) {
			capturedTx = *tx
			return false, nil
		},
	}, "", "", true)

	resource := map[string]any{
		"id":        "pp_cap_3",
		"amount":    map[string]any{"value": "10.00", "currency_code": "USD"},
		"custom_id": "invoice:garbage",
	}
	req := httptest.NewRequest("POST", "/webhooks/paypal",
		strings.NewReader(string(paypalCaptureJSON("pp_evt_3", resource))))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if capturedTx.InvoiceID != nil {
		t.Errorf("expected nil invoice_id for non-UUID custom_id, got %q", *capturedTx.InvoiceID)
	}
}

// ---- PayPal cert URL validation ----

func TestPayPalWebhook_RejectsBadCertURLs(t *testing.T) {
	// verifyPayPalSignature must reject cert URLs outside the exact host
	// allowlist (or non-https) WITHOUT fetching anything; with a webhook id
	// configured the request fails signature verification → 400. The repo is
	// an empty mock, so any processing attempt panics.
	cases := []struct {
		name    string
		certURL string
	}{
		{"subdomain of allowlisted host", "https://evil.paypal.com/x"},
		{"http scheme on allowlisted host", "http://api.paypal.com/x"},
		{"suffix-spoofed host", "https://api.paypal.com.evil.example/x"},
		{"unrelated host", "https://example.com/cert"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewWebhooksHandler(&mockDuesRepo{}, "", "wh-id-123", false)
			body := paypalCaptureJSON("pp_evt_sig", map[string]any{})
			req := httptest.NewRequest("POST", "/webhooks/paypal", strings.NewReader(string(body)))
			req.Header.Set("PAYPAL-TRANSMISSION-ID", "tid-1")
			req.Header.Set("PAYPAL-TRANSMISSION-TIME", "2026-01-01T00:00:00Z")
			req.Header.Set("PAYPAL-CERT-URL", tc.certURL)
			req.Header.Set("PAYPAL-TRANSMISSION-SIG", "c2ln") // any base64
			rr := httptest.NewRecorder()
			h.PayPal(rr, req)
			if rr.Code != 400 {
				t.Errorf("cert URL %q: got %d, want 400", tc.certURL, rr.Code)
			}
		})
	}
}

func TestPayPalWebhook_MissingSignatureHeaders(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{}, "", "wh-id-123", false)
	body := paypalCaptureJSON("pp_evt_sig2", map[string]any{})
	req := httptest.NewRequest("POST", "/webhooks/paypal", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 when signature headers are absent", rr.Code)
	}
}

// Compile-time check that mockDuesRepo satisfies duesRepo interface.
var _ duesRepo = (*mockDuesRepo)(nil)

// Also check repo.InvoiceFilter is usable (prevents import pruning).
var _ = repo.InvoiceFilter{}
