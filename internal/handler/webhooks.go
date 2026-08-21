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
	"quorum/internal/repo"
)

// WebhooksHandler processes inbound payment-provider webhook events.
type WebhooksHandler struct {
	receipts            func(invoiceID string, amountMinor int64)
	dues                duesRepo
	stripeWebhookSecret string
	paypalWebhookID     string
	// allowUnsigned permits processing without signature verification when a
	// provider's secret is unset. Local development only; when false (the
	// default), unconfigured providers return 503 instead of accepting
	// unverifiable events.
	allowUnsigned bool
}

// SetReceiptSender attaches the payment-receipt email hook.
func (h *WebhooksHandler) SetReceiptSender(fn func(string, int64)) { h.receipts = fn }

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
	case "payment_intent.succeeded", "charge.succeeded", "charge.captured":
		if err := h.handleStripePayment(r, event.ID, event.Data); err != nil {
			// Do not mark the event processed: returning 5xx makes Stripe
			// retry, so a transient DB failure cannot silently drop a payment.
			log.Printf("stripe: event %s: %v", event.ID, err)
			writeError(w, http.StatusInternalServerError, "processing error", "internal_error")
			return
		}
	case "charge.refunded":
		if err := h.handleStripeRefund(r, event.ID, event.Data); err != nil {
			log.Printf("stripe: refund event %s: %v", event.ID, err)
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

// handleStripeRefund records a refund as a negative transaction against the
// original invoice (the GL trigger posts the reversing entry). The invoice is
// resolved from metadata or by matching the charge/payment-intent id to the
// original payment's provider reference; if it can't be resolved the event is
// acknowledged and logged for manual handling rather than dropped.
func (h *WebhooksHandler) handleStripeRefund(r *http.Request, eventID string, data json.RawMessage) error {
	var obj struct {
		Object struct {
			ID             string `json:"id"`
			AmountRefunded int64  `json:"amount_refunded"`
			Currency       string `json:"currency"`
			PaymentIntent  string `json:"payment_intent"`
			Metadata       struct {
				InvoiceID string `json:"quorum_invoice_id"`
			} `json:"metadata"`
		} `json:"object"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		log.Printf("stripe: parse refund object error: %v", err)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}
	invoiceID := obj.Object.Metadata.InvoiceID
	if invoiceID != "" && !isValidUUID(invoiceID) {
		invoiceID = ""
	}
	if invoiceID == "" {
		for _, ref := range []string{obj.Object.PaymentIntent, obj.Object.ID} {
			if ref == "" {
				continue
			}
			id, err := h.dues.FindInvoiceByProviderRef(r.Context(), ref)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if id != "" {
				invoiceID = id
				break
			}
		}
	}
	if invoiceID == "" || obj.Object.AmountRefunded <= 0 {
		log.Printf("stripe: refund %s not linked to an invoice — record manually", eventID)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}
	status := "refunded"
	pid := obj.Object.ID
	// amount_refunded is Stripe's CUMULATIVE total for the charge, and every
	// partial refund fires a fresh event id — recording it verbatim would
	// post $30 then $70 for refunds of $30 and $40. The repo records only
	// the delta beyond what this charge has already refunded, atomically.
	_, err := h.dues.RecordWebhookRefund(r.Context(), eventID, &model.Transaction{
		InvoiceID:           &invoiceID,
		Currency:            strings.ToUpper(obj.Object.Currency),
		Provider:            "stripe",
		ProviderReferenceID: &pid,
		ProviderStatus:      &status,
		OccurredAt:          time.Now(),
	}, obj.Object.AmountRefunded)
	if errors.Is(err, repo.ErrInvoiceNotPayable) {
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}
	if errors.Is(err, repo.ErrExceedsPaid) {
		log.Printf("stripe: refund %s exceeds recorded payments on invoice %s — record manually", eventID, invoiceID)
		return nil // the event claim committed with the refusal
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Valid-UUID metadata pointing at an invoice we don't have (another
		// environment, hand-set metadata): a permanent condition, not a
		// transient failure — 500ing makes the provider retry this poison
		// event until it disables the endpoint.
		log.Printf("stripe: refund %s names unknown invoice %s — record manually", eventID, invoiceID)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}
	return err
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
			// AmountReceived (payment intents) and Captured (charges) guard
			// against recording money that never actually landed: separate-
			// capture charges fire charge.succeeded at AUTHORIZATION, and a
			// partial capture's `amount` overstates what was received.
			AmountReceived *int64 `json:"amount_received"`
			AmountCaptured *int64 `json:"amount_captured"`
			Captured       *bool  `json:"captured"`
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
	if obj.Object.Captured != nil && !*obj.Object.Captured {
		// An authorization, not money: the capture (or expiry) comes later.
		log.Printf("stripe: charge %s not captured yet — skipping", obj.Object.ID)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}

	currency := strings.ToUpper(obj.Object.Currency)
	// Stripe amounts are already in the currency's minor units, which is exactly
	// how Quorum stores money — no conversion needed. Prefer amount_received
	// (what actually landed) when the object carries it.
	amountMinor := obj.Object.Amount
	if obj.Object.AmountReceived != nil {
		amountMinor = *obj.Object.AmountReceived // payment intents: what landed
	} else if obj.Object.AmountCaptured != nil {
		amountMinor = *obj.Object.AmountCaptured // charges: partial captures overstate `amount`
	}
	if amountMinor <= 0 {
		log.Printf("stripe: event %s has no received amount — skipping", eventID)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}
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

	already, err := h.dues.RecordWebhookPayment(r.Context(), claimKey, &model.Transaction{
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
	if err == nil && !already && invPtr != nil && h.receipts != nil {
		h.receipts(*invPtr, amountMinor) // thank the member, with their new balance
	}
	if errors.Is(err, repo.ErrInvoiceNotPayable) {
		// Payment arrived for a waived invoice — acknowledged to the provider
		// (no retry storm) but not posted; an officer must reconcile it.
		log.Printf("stripe: payment for waived invoice %v needs manual reconciliation (provider ref %s)", invPtr, providerID)
		return nil
	}
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

	switch event.EventType {
	case "PAYMENT.CAPTURE.COMPLETED":
		if err := h.handlePayPalCapture(r, event.ID, event.Resource); err != nil {
			log.Printf("paypal: event %s: %v", event.ID, err)
			writeError(w, http.StatusInternalServerError, "processing error", "internal_error")
			return
		}
	case "PAYMENT.CAPTURE.REFUNDED", "PAYMENT.CAPTURE.REVERSED":
		// These used to be silently marked processed while Stripe refunds
		// flowed to the ledger — money left the bank with no journal entry.
		if err := h.handlePayPalRefund(r, event.ID, event.Resource); err != nil {
			log.Printf("paypal: refund event %s: %v", event.ID, err)
			writeError(w, http.StatusInternalServerError, "processing error", "internal_error")
			return
		}
	default:
		if err := h.dues.MarkEventProcessed(r.Context(), event.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "db error", "internal_error")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handlePayPalRefund records a refund as a negative transaction. Unlike
// Stripe, PayPal's refund resource carries THIS refund's amount (a delta),
// so it can post directly; the event-id claim dedupes retries. The original
// invoice resolves via custom_id or the captured payment's provider ref
// (resource.links carry the capture id in "up", but custom_id and the
// capture reference cover the flows Quorum initiates).
func (h *WebhooksHandler) handlePayPalRefund(r *http.Request, eventID string, raw json.RawMessage) error {
	var resource struct {
		ID     string `json:"id"`
		Amount struct {
			Value        string `json:"value"`
			CurrencyCode string `json:"currency_code"`
		} `json:"amount"`
		CustomID string `json:"custom_id"`
		Links    []struct {
			Rel  string `json:"rel"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if err := json.Unmarshal(raw, &resource); err != nil {
		log.Printf("paypal: parse refund resource error: %v", err)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}
	currency := strings.ToUpper(resource.Amount.CurrencyCode)
	amountMinor, err := model.ParseMoney(resource.Amount.Value, currency)
	if err != nil || amountMinor <= 0 {
		log.Printf("paypal: unparseable refund amount %q: %v", resource.Amount.Value, err)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}
	var invoiceID string
	if strings.HasPrefix(resource.CustomID, "invoice:") {
		if id := strings.TrimPrefix(resource.CustomID, "invoice:"); isValidUUID(id) {
			invoiceID = id
		}
	}
	if invoiceID == "" {
		// The refund resource's own id was never stored anywhere — the
		// provider ref on file is the CAPTURE id, which the refund links to
		// via rel="up" (…/v2/payments/captures/{id}). Follow that.
		if capID := paypalUpID(resourceLinks(resource.Links)); capID != "" {
			id, err := h.dues.FindInvoiceByProviderRef(r.Context(), capID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			invoiceID = id
		}
	}
	if invoiceID == "" {
		log.Printf("paypal: refund %s not linked to an invoice — record manually", eventID)
		return h.dues.MarkEventProcessed(r.Context(), eventID)
	}
	status := "refunded"
	pid := resource.ID
	_, err = h.dues.RecordWebhookPayment(r.Context(), eventID, &model.Transaction{
		InvoiceID:           &invoiceID,
		AmountMinor:         -amountMinor, // negative → reversing entry
		Currency:            currency,
		Provider:            "paypal",
		ProviderReferenceID: &pid,
		ProviderStatus:      &status,
		OccurredAt:          time.Now(),
	})
	if errors.Is(err, repo.ErrExceedsPaid) {
		log.Printf("paypal: refund %s exceeds recorded payments on invoice %s — record manually (ref %s)", eventID, invoiceID, pid)
		return nil
	}
	if errors.Is(err, repo.ErrInvoiceNotPayable) {
		log.Printf("paypal: refund for waived invoice %s needs manual reconciliation (ref %s)", invoiceID, pid)
		return nil
	}
	return err
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

	already2, err := h.dues.RecordWebhookPayment(r.Context(), eventID, &model.Transaction{
		InvoiceID:           invoiceID,
		AmountMinor:         amountMinor,
		Currency:            currency,
		Provider:            "paypal",
		ProviderReferenceID: &providerID,
		ProviderStatus:      &status,
		PaymentMethodType:   &pmType,
		OccurredAt:          time.Now(),
	})
	if err == nil && !already2 && invoiceID != nil && h.receipts != nil {
		h.receipts(*invoiceID, amountMinor)
	}
	if errors.Is(err, repo.ErrInvoiceNotPayable) {
		log.Printf("paypal: payment for waived invoice %v needs manual reconciliation (provider ref %s)", invoiceID, providerID)
		return nil
	}
	return err
}

// resourceLinks flattens the anonymous links struct into rel→href pairs.
func resourceLinks(links []struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
}) map[string]string {
	out := make(map[string]string, len(links))
	for _, l := range links {
		out[l.Rel] = l.Href
	}
	return out
}

// paypalUpID extracts the trailing path segment of a rel="up" link — for a
// refund that is the capture id the original payment was recorded under.
func paypalUpID(links map[string]string) string {
	href, ok := links["up"]
	if !ok {
		return ""
	}
	href = strings.TrimRight(href, "/")
	if i := strings.LastIndex(href, "/"); i >= 0 && i+1 < len(href) {
		return href[i+1:]
	}
	return ""
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
