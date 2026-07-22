package handler

import (
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"quorum/internal/model"
)

// WebhooksHandler processes inbound payment-provider webhook events.
type WebhooksHandler struct {
	dues                duesRepo
	stripeWebhookSecret string
	paypalWebhookID     string
	// allowUnsigned permits processing without signature verification when a
	// provider's secret is unset. Local development only; when false (the
	// default), unconfigured providers return 503 instead of accepting
	// unverifiable events.
	allowUnsigned bool
}

// NewWebhooksHandler constructs a WebhooksHandler.
func NewWebhooksHandler(d duesRepo, stripeSecret, paypalWebhookID string, allowUnsigned bool) *WebhooksHandler {
	return &WebhooksHandler{
		dues:                d,
		stripeWebhookSecret: stripeSecret,
		paypalWebhookID:     paypalWebhookID,
		allowUnsigned:       allowUnsigned,
	}
}

// Stripe handles inbound Stripe webhook events.
func (h *WebhooksHandler) Stripe(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read error", "bad_request")
		return
	}

	if h.stripeWebhookSecret == "" {
		if !h.allowUnsigned {
			writeError(w, http.StatusServiceUnavailable,
				"stripe webhook secret not configured", "not_configured")
			return
		}
	} else {
		sig := r.Header.Get("Stripe-Signature")
		if !verifyStripeSignature(body, sig, h.stripeWebhookSecret) {
			writeError(w, http.StatusBadRequest, "invalid signature", "bad_signature")
			return
		}
	}

	var event struct {
		ID   string          `json:"id"`
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil || event.ID == "" {
		writeError(w, http.StatusBadRequest, "invalid JSON", "bad_request")
		return
	}

	switch event.Type {
	case "payment_intent.succeeded", "charge.succeeded":
		if err := h.handleStripePayment(r, event.ID, event.Data); err != nil {
			// Do not mark the event processed: returning 5xx makes Stripe
			// retry, so a transient DB failure cannot silently drop a payment.
			log.Printf("stripe: event %s: %v", event.ID, err)
			writeError(w, http.StatusInternalServerError, "processing error", "internal_error")
			return
		}
	default:
		log.Printf("stripe: unhandled event type %s", event.Type)
		if err := h.dues.MarkEventProcessed(r.Context(), event.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "db error", "internal_error")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handleStripePayment records a Stripe payment transaction. Malformed payloads
// are permanent failures: they are logged, marked processed, and return nil so
// the provider stops retrying. Database errors are returned to the caller.
func (h *WebhooksHandler) handleStripePayment(r *http.Request, eventID string, data json.RawMessage) error {
	var obj struct {
		Object struct {
			ID       string `json:"id"`
			Amount   int64  `json:"amount"`
			Currency string `json:"currency"`
			// PaymentIntent is set on charge objects and links a charge back to
			// its payment intent; used to deduplicate the two events a single
			// payment can emit.
			PaymentIntent string `json:"payment_intent"`
			Metadata      struct {
				InvoiceID string `json:"quorum_invoice_id"`
				MemberID  string `json:"quorum_member_id"`
			} `json:"metadata"`
			PaymentMethodDetails struct {
				Type string `json:"type"`
			} `json:"payment_method_details"`
		} `json:"object"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		log.Printf("stripe: parse object error: %v", err)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}

	currency := strings.ToUpper(obj.Object.Currency)
	// Stripe amounts are already in the currency's minor units, which is exactly
	// how Quorum stores money — no conversion needed.
	amountMinor := obj.Object.Amount
	providerID := obj.Object.ID
	status := "succeeded"
	pmType := obj.Object.PaymentMethodDetails.Type

	// A single Stripe payment can fire both payment_intent.succeeded and
	// charge.succeeded — distinct events with distinct object ids that
	// event-id dedup would miss, double-crediting the invoice. Claim on the
	// payment-intent id (shared by both) so only the first records; fall back
	// to the object id for legacy charges with no payment intent.
	claimKey := obj.Object.PaymentIntent
	if claimKey == "" {
		claimKey = providerID
	}

	invoiceID := obj.Object.Metadata.InvoiceID
	memberID := obj.Object.Metadata.MemberID
	// Non-UUID metadata would fail the ::uuid cast and poison the insert, so
	// treat it as an unlinked payment rather than erroring forever.
	if invoiceID != "" && !isValidUUID(invoiceID) {
		log.Printf("stripe: ignoring non-UUID quorum_invoice_id %q", invoiceID)
		invoiceID = ""
	}
	if memberID != "" && !isValidUUID(memberID) {
		log.Printf("stripe: ignoring non-UUID quorum_member_id %q", memberID)
		memberID = ""
	}

	if invoiceID == "" && providerID != "" {
		id, err := h.dues.FindInvoiceByProviderRef(r.Context(), providerID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			// A transient lookup failure must not silently record an unlinked
			// payment and claim the event — return so the provider retries.
			return err
		}
		invoiceID = id
	}

	var invPtr, memPtr *string
	if invoiceID != "" {
		invPtr = &invoiceID
	}
	if memberID != "" {
		memPtr = &memberID
	}

	_, err := h.dues.RecordWebhookPayment(r.Context(), claimKey, &model.Transaction{
		InvoiceID:           invPtr,
		MemberID:            memPtr,
		AmountMinor:         amountMinor,
		Currency:            currency,
		Provider:            "stripe",
		ProviderReferenceID: &providerID,
		ProviderStatus:      &status,
		PaymentMethodType:   &pmType,
		OccurredAt:          time.Now(),
	})
	return err
}

// PayPal handles inbound PayPal webhook events.
func (h *WebhooksHandler) PayPal(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read error", "bad_request")
		return
	}

	if h.paypalWebhookID == "" {
		if !h.allowUnsigned {
			writeError(w, http.StatusServiceUnavailable,
				"paypal webhook id not configured", "not_configured")
			return
		}
	} else {
		transmissionID := r.Header.Get("PAYPAL-TRANSMISSION-ID")
		timestamp := r.Header.Get("PAYPAL-TRANSMISSION-TIME")
		certURL := r.Header.Get("PAYPAL-CERT-URL")
		sig := r.Header.Get("PAYPAL-TRANSMISSION-SIG")
		if !verifyPayPalSignature(body, transmissionID, timestamp, certURL, sig, h.paypalWebhookID) {
			writeError(w, http.StatusBadRequest, "invalid signature", "bad_signature")
			return
		}
	}

	var event struct {
		ID        string          `json:"id"`
		EventType string          `json:"event_type"`
		Resource  json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(body, &event); err != nil || event.ID == "" {
		writeError(w, http.StatusBadRequest, "invalid JSON", "bad_request")
		return
	}

	if event.EventType == "PAYMENT.CAPTURE.COMPLETED" {
		if err := h.handlePayPalCapture(r, event.ID, event.Resource); err != nil {
			log.Printf("paypal: event %s: %v", event.ID, err)
			writeError(w, http.StatusInternalServerError, "processing error", "internal_error")
			return
		}
	} else {
		if err := h.dues.MarkEventProcessed(r.Context(), event.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "db error", "internal_error")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handlePayPalCapture records a PayPal capture transaction. Like the Stripe
// path, malformed payloads are marked processed and dropped; DB errors
// propagate so the provider retries.
func (h *WebhooksHandler) handlePayPalCapture(r *http.Request, eventID string, raw json.RawMessage) error {
	var resource struct {
		ID     string `json:"id"`
		Amount struct {
			Value        string `json:"value"`
			CurrencyCode string `json:"currency_code"`
		} `json:"amount"`
		CustomID string `json:"custom_id"`
	}
	if err := json.Unmarshal(raw, &resource); err != nil {
		log.Printf("paypal: parse resource error: %v", err)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}
	// PayPal reports the amount as a major-unit decimal string ("10.00"); parse
	// it to minor units without a float so no precision is lost.
	currency := strings.ToUpper(resource.Amount.CurrencyCode)
	amountMinor, err := model.ParseMoney(resource.Amount.Value, currency)
	if err != nil {
		log.Printf("paypal: unparseable amount %q: %v", resource.Amount.Value, err)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}

	providerID := resource.ID
	status := "succeeded"
	pmType := "paypal"

	var invoiceID *string
	if strings.HasPrefix(resource.CustomID, "invoice:") {
		id := strings.TrimPrefix(resource.CustomID, "invoice:")
		if isValidUUID(id) {
			invoiceID = &id
		} else {
			log.Printf("paypal: ignoring non-UUID custom_id %q", resource.CustomID)
		}
	}

	_, err = h.dues.RecordWebhookPayment(r.Context(), eventID, &model.Transaction{
		InvoiceID:           invoiceID,
		AmountMinor:         amountMinor,
		Currency:            currency,
		Provider:            "paypal",
		ProviderReferenceID: &providerID,
		ProviderStatus:      &status,
		PaymentMethodType:   &pmType,
		OccurredAt:          time.Now(),
	})
	return err
}

func verifyStripeSignature(payload []byte, header, secret string) bool {
	parts := strings.Split(header, ",")
	var ts string
	var sigs []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "t=") {
			ts = strings.TrimPrefix(p, "t=")
		}
		// Stripe sends multiple v1 entries during secret rotation; any one
		// matching signature is valid.
		if strings.HasPrefix(p, "v1=") {
			sigs = append(sigs, strings.TrimPrefix(p, "v1="))
		}
	}
	if ts == "" || len(sigs) == 0 {
		return false
	}
	tsUnix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if time.Since(time.Unix(tsUnix, 0)) > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(payload)))
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, sig := range sigs {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			return true
		}
	}
	return false
}

// paypalCertHosts is the exact allowlist of hosts PayPal serves webhook
// signing certificates from. Suffix matching is deliberately avoided.
var paypalCertHosts = map[string]bool{
	"api.paypal.com":           true,
	"api-m.paypal.com":         true,
	"api.sandbox.paypal.com":   true,
	"api-m.sandbox.paypal.com": true,
}

var (
	paypalCertMu    sync.Mutex
	paypalCertCache = map[string]*x509.Certificate{}
)

// paypalCertCacheMax bounds the cert cache; PAYPAL-CERT-URL is
// attacker-influenced, so the cache must not grow without limit.
const paypalCertCacheMax = 32

// paypalHTTPClient never follows redirects: the URL is validated against the
// host allowlist before fetching, and a redirect would escape that check.
var paypalHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func verifyPayPalSignature(body []byte, transmissionID, timestamp, certURL, sig, webhookID string) bool {
	if transmissionID == "" || timestamp == "" || certURL == "" || sig == "" {
		return false
	}
	u, err := url.Parse(certURL)
	if err != nil || u.Scheme != "https" || !paypalCertHosts[strings.ToLower(u.Hostname())] {
		log.Printf("paypal: invalid cert URL %q", certURL)
		return false
	}

	cert, err := paypalFetchCert(certURL)
	if err != nil {
		log.Printf("paypal: fetch cert error: %v", err)
		return false
	}

	rsaKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return false
	}

	checksum := crc32.ChecksumIEEE(body)
	message := fmt.Sprintf("%s|%s|%s|%d", transmissionID, timestamp, webhookID, checksum)
	digest := sha256.Sum256([]byte(message))

	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	return rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, digest[:], sigBytes) == nil
}

func paypalFetchCert(certURL string) (*x509.Certificate, error) {
	paypalCertMu.Lock()
	if cert, ok := paypalCertCache[certURL]; ok {
		paypalCertMu.Unlock()
		return cert, nil
	}
	paypalCertMu.Unlock()

	resp, err := paypalHTTPClient.Get(certURL) //nolint:gosec — host allowlisted, https-only, redirects disabled
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("paypal: cert fetch returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}

	// The response may contain the full chain: first block is the leaf,
	// the rest are intermediates used for chain verification.
	var leaf *x509.Certificate
	intermediates := x509.NewCertPool()
	for block, rest := pem.Decode(data); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		if leaf == nil {
			leaf = cert
		} else {
			intermediates.AddCert(cert)
		}
	}
	if leaf == nil {
		return nil, fmt.Errorf("paypal: no certificate in response")
	}

	// Verify chain of trust against the system root pool; this also rejects
	// expired or not-yet-valid certificates.
	if _, err := leaf.Verify(x509.VerifyOptions{Intermediates: intermediates}); err != nil {
		return nil, fmt.Errorf("paypal: cert chain verification failed: %w", err)
	}

	paypalCertMu.Lock()
	if len(paypalCertCache) >= paypalCertCacheMax {
		paypalCertCache = map[string]*x509.Certificate{}
	}
	paypalCertCache[certURL] = leaf
	paypalCertMu.Unlock()
	return leaf, nil
}
