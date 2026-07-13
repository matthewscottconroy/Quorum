package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quorum/internal/model"
	"quorum/internal/repo"
)

// ---- verifyStripeSignature ----

func stripeHeader(secret string, payload []byte) string {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(payload)))
	sig := hex.EncodeToString(mac.Sum(nil))
	return "t=" + ts + ",v1=" + sig
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

// ---- Stripe webhook handler ----

func stripeEventJSON(eventID, eventType string, data any) []byte {
	dataBytes, _ := json.Marshal(data)
	return []byte(fmt.Sprintf(`{"id":%q,"type":%q,"data":%s}`, eventID, eventType, dataBytes))
}

func TestStripeWebhook_IdempotencySkipsAlreadyProcessed(t *testing.T) {
	createCalled := false
	h := NewWebhooksHandler(&mockDuesRepo{
		EventProcessedFn: func(_ context.Context, _ string) (bool, error) { return true, nil },
		CreateTransactionFn: func(_ context.Context, _ *model.Transaction) (*model.Transaction, error) {
			createCalled = true
			return nil, nil
		},
	}, "", "")
	payload := stripeEventJSON("evt_dup", "payment_intent.succeeded", map[string]any{"object": map[string]any{}})
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
	if createCalled {
		t.Error("CreateTransaction should not be called for duplicate event")
	}
}

func TestStripeWebhook_InvalidSignature(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{}, "real-secret", "")
	payload := []byte(`{"id":"evt_1","type":"payment_intent.succeeded","data":{}}`)
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	req.Header.Set("Stripe-Signature", "t=12345,v1=badsig")
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400 for bad signature", rr.Code)
	}
}

func TestStripeWebhook_SkipsSignatureWhenSecretEmpty(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{
		EventProcessedFn:    func(_ context.Context, _ string) (bool, error) { return false, nil },
		MarkEventProcessedFn: func(_ context.Context, _ string) error { return nil },
		CreateTransactionFn: func(_ context.Context, t *model.Transaction) (*model.Transaction, error) {
			return t, nil
		},
		RecomputeInvoiceStatusFn: func(_ context.Context, _ string) error { return nil },
		FindInvoiceByProviderRefFn: func(_ context.Context, _ string) (string, error) { return "", nil },
	}, "", "") // empty secret — skip sig check

	obj := map[string]any{
		"object": map[string]any{
			"id":       "ch_abc",
			"amount":   5000,
			"currency": "usd",
			"metadata": map[string]any{
				"quorum_invoice_id": "inv-1",
				"quorum_member_id":  "mem-1",
			},
			"payment_method_details": map[string]any{"type": "card"},
		},
	}
	payload := stripeEventJSON("evt_new", "charge.succeeded", obj)
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d; body: %s", rr.Code, rr.Body)
	}
}

func TestStripeWebhook_UnhandledEventType(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{
		EventProcessedFn:    func(_ context.Context, _ string) (bool, error) { return false, nil },
		MarkEventProcessedFn: func(_ context.Context, _ string) error { return nil },
	}, "", "")
	payload := stripeEventJSON("evt_x", "customer.created", map[string]any{})
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	// Unhandled type should still return 200 and mark processed.
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
}

func TestStripeWebhook_BadJSON(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{}, "", "")
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader("{bad json"))
	rr := httptest.NewRecorder()
	h.Stripe(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestStripeWebhook_CreatesTransactionWithCorrectAmount(t *testing.T) {
	var capturedTx model.Transaction
	h := NewWebhooksHandler(&mockDuesRepo{
		EventProcessedFn:    func(_ context.Context, _ string) (bool, error) { return false, nil },
		MarkEventProcessedFn: func(_ context.Context, _ string) error { return nil },
		CreateTransactionFn: func(_ context.Context, t *model.Transaction) (*model.Transaction, error) {
			capturedTx = *t
			return t, nil
		},
		RecomputeInvoiceStatusFn: func(_ context.Context, _ string) error { return nil },
		FindInvoiceByProviderRefFn: func(_ context.Context, _ string) (string, error) { return "", nil },
	}, "", "")

	obj := map[string]any{
		"object": map[string]any{
			"id":       "ch_xyz",
			"amount":   2500, // cents
			"currency": "usd",
			"metadata": map[string]any{"quorum_invoice_id": "inv-1", "quorum_member_id": "mem-1"},
			"payment_method_details": map[string]any{"type": "card"},
		},
	}
	payload := stripeEventJSON("evt_amt", "charge.succeeded", obj)
	req := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(string(payload)))
	h.Stripe(httptest.NewRecorder(), req)

	if capturedTx.Amount != 25.0 {
		t.Errorf("amount: got %.2f, want 25.00 (cents → dollars)", capturedTx.Amount)
	}
	if capturedTx.Provider != "stripe" {
		t.Errorf("provider: got %q, want stripe", capturedTx.Provider)
	}
	if capturedTx.Currency != "USD" {
		t.Errorf("currency: got %q, want USD", capturedTx.Currency)
	}
}

// ---- PayPal webhook handler ----

func TestPayPalWebhook_CaptureCompleted(t *testing.T) {
	var capturedTx model.Transaction
	h := NewWebhooksHandler(&mockDuesRepo{
		EventProcessedFn:    func(_ context.Context, _ string) (bool, error) { return false, nil },
		MarkEventProcessedFn: func(_ context.Context, _ string) error { return nil },
		CreateTransactionFn: func(_ context.Context, t *model.Transaction) (*model.Transaction, error) {
			capturedTx = *t
			return t, nil
		},
		RecomputeInvoiceStatusFn: func(_ context.Context, _ string) error { return nil },
	}, "", "")

	resource := map[string]any{
		"id": "pp_cap_1",
		"amount": map[string]any{
			"value":         "75.50",
			"currency_code": "USD",
		},
		"custom_id": "invoice:inv-abc",
	}
	payload := map[string]any{
		"id":         "pp_evt_1",
		"event_type": "PAYMENT.CAPTURE.COMPLETED",
		"resource":   resource,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/webhooks/paypal", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d; body: %s", rr.Code, rr.Body)
	}
	if capturedTx.Provider != "paypal" {
		t.Errorf("provider: got %q, want paypal", capturedTx.Provider)
	}
	if capturedTx.Amount != 75.50 {
		t.Errorf("amount: got %.2f, want 75.50", capturedTx.Amount)
	}
	if capturedTx.InvoiceID == nil || *capturedTx.InvoiceID != "inv-abc" {
		t.Errorf("invoice_id: got %v", capturedTx.InvoiceID)
	}
}

func TestPayPalWebhook_Idempotency(t *testing.T) {
	createCalled := false
	h := NewWebhooksHandler(&mockDuesRepo{
		EventProcessedFn: func(_ context.Context, _ string) (bool, error) { return true, nil },
		CreateTransactionFn: func(_ context.Context, _ *model.Transaction) (*model.Transaction, error) {
			createCalled = true
			return nil, nil
		},
	}, "", "")

	payload := map[string]any{"id": "pp_dup", "event_type": "PAYMENT.CAPTURE.COMPLETED", "resource": map[string]any{}}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/webhooks/paypal", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)
	if createCalled {
		t.Error("CreateTransaction should not be called for duplicate event")
	}
}

func TestPayPalWebhook_BadJSON(t *testing.T) {
	h := NewWebhooksHandler(&mockDuesRepo{}, "", "")
	req := httptest.NewRequest("POST", "/webhooks/paypal", strings.NewReader("{bad"))
	rr := httptest.NewRecorder()
	h.PayPal(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestPayPalWebhook_NoInvoicePrefix(t *testing.T) {
	// custom_id without "invoice:" prefix → no invoice linked
	var capturedTx model.Transaction
	h := NewWebhooksHandler(&mockDuesRepo{
		EventProcessedFn:    func(_ context.Context, _ string) (bool, error) { return false, nil },
		MarkEventProcessedFn: func(_ context.Context, _ string) error { return nil },
		CreateTransactionFn: func(_ context.Context, t *model.Transaction) (*model.Transaction, error) {
			capturedTx = *t
			return t, nil
		},
		RecomputeInvoiceStatusFn: func(_ context.Context, _ string) error { return nil },
	}, "", "")

	resource := map[string]any{
		"id":        "pp_cap_2",
		"amount":    map[string]any{"value": "10.00", "currency_code": "USD"},
		"custom_id": "some-other-reference",
	}
	payload := map[string]any{
		"id":         "pp_evt_2",
		"event_type": "PAYMENT.CAPTURE.COMPLETED",
		"resource":   resource,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/webhooks/paypal", strings.NewReader(string(body)))
	h.PayPal(httptest.NewRecorder(), req)

	if capturedTx.InvoiceID != nil {
		t.Errorf("expected nil invoice_id when custom_id has no 'invoice:' prefix, got %q", *capturedTx.InvoiceID)
	}
}

// Compile-time check that mockDuesRepo satisfies duesRepo interface.
var _ duesRepo = (*mockDuesRepo)(nil)

// Also check repo.InvoiceFilter is usable (prevents import pruning).
var _ = repo.InvoiceFilter{}
