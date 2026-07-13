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

	"quorum/internal/model"
)

// WebhooksHandler processes inbound payment-provider webhook events.
type WebhooksHandler struct {
	dues                duesRepo
	stripeWebhookSecret string
	paypalWebhookID     string
}

// NewWebhooksHandler constructs a WebhooksHandler.
func NewWebhooksHandler(d duesRepo, stripeSecret, paypalWebhookID string) *WebhooksHandler {
	return &WebhooksHandler{dues: d, stripeWebhookSecret: stripeSecret, paypalWebhookID: paypalWebhookID}
}

func (h *WebhooksHandler) Stripe(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, 400, "read error", "bad_request")
		return
	}

	if h.stripeWebhookSecret != "" {
		sig := r.Header.Get("Stripe-Signature")
		if !verifyStripeSignature(body, sig, h.stripeWebhookSecret) {
			writeError(w, 400, "invalid signature", "bad_signature")
			return
		}
	}

	var event struct {
		ID   string          `json:"id"`
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, 400, "invalid JSON", "bad_request")
		return
	}

	already, err := h.dues.EventProcessed(r.Context(), event.ID)
	if err != nil {
		writeError(w, 500, "db error", "internal_error")
		return
	}
	if already {
		w.WriteHeader(200)
		return
	}

	switch event.Type {
	case "payment_intent.succeeded", "charge.succeeded":
		h.handleStripePayment(r, event.Data)
	default:
		log.Printf("stripe: unhandled event type %s", event.Type)
	}

	h.dues.MarkEventProcessed(r.Context(), event.ID) //nolint:errcheck
	w.WriteHeader(200)
}

func (h *WebhooksHandler) handleStripePayment(r *http.Request, data json.RawMessage) {
	var obj struct {
		Object struct {
			ID       string `json:"id"`
			Amount   int64  `json:"amount"`
			Currency string `json:"currency"`
			Metadata struct {
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
		return
	}

	amount := float64(obj.Object.Amount) / 100.0
	currency := strings.ToUpper(obj.Object.Currency)
	providerID := obj.Object.ID
	status := "succeeded"
	pmType := obj.Object.PaymentMethodDetails.Type

	invoiceID := obj.Object.Metadata.InvoiceID
	memberID := obj.Object.Metadata.MemberID

	if invoiceID == "" && providerID != "" {
		id, _ := h.dues.FindInvoiceByProviderRef(r.Context(), providerID)
		invoiceID = id
	}

	var invPtr, memPtr *string
	if invoiceID != "" {
		invPtr = &invoiceID
	}
	if memberID != "" {
		memPtr = &memberID
	}

	if _, err := h.dues.CreateTransaction(r.Context(), &model.Transaction{
		InvoiceID:           invPtr,
		MemberID:            memPtr,
		Amount:              amount,
		Currency:            currency,
		Provider:            "stripe",
		ProviderReferenceID: &providerID,
		ProviderStatus:      &status,
		PaymentMethodType:   &pmType,
		OccurredAt:          time.Now(),
	}); err != nil {
		log.Printf("stripe: create transaction error: %v", err)
		return
	}

	if invoiceID != "" {
		h.dues.RecomputeInvoiceStatus(r.Context(), invoiceID) //nolint:errcheck
	}
}

func (h *WebhooksHandler) PayPal(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, 400, "read error", "bad_request")
		return
	}

	if h.paypalWebhookID != "" {
		transmissionID := r.Header.Get("PAYPAL-TRANSMISSION-ID")
		timestamp := r.Header.Get("PAYPAL-TRANSMISSION-TIME")
		certURL := r.Header.Get("PAYPAL-CERT-URL")
		sig := r.Header.Get("PAYPAL-TRANSMISSION-SIG")
		if !verifyPayPalSignature(body, transmissionID, timestamp, certURL, sig, h.paypalWebhookID) {
			writeError(w, 400, "invalid signature", "bad_signature")
			return
		}
	}

	var event struct {
		ID        string          `json:"id"`
		EventType string          `json:"event_type"`
		Resource  json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, 400, "invalid JSON", "bad_request")
		return
	}

	already, err := h.dues.EventProcessed(r.Context(), event.ID)
	if err != nil {
		writeError(w, 500, "db error", "internal_error")
		return
	}
	if already {
		w.WriteHeader(200)
		return
	}

	if event.EventType == "PAYMENT.CAPTURE.COMPLETED" {
		var resource struct {
			ID     string `json:"id"`
			Amount struct {
				Value        string `json:"value"`
				CurrencyCode string `json:"currency_code"`
			} `json:"amount"`
			CustomID string `json:"custom_id"`
		}
		if err := json.Unmarshal(event.Resource, &resource); err == nil {
			amount, _ := strconv.ParseFloat(resource.Amount.Value, 64)
			providerID := resource.ID
			status := "succeeded"
			pmType := "paypal"

			var invoiceID *string
			if strings.HasPrefix(resource.CustomID, "invoice:") {
				id := strings.TrimPrefix(resource.CustomID, "invoice:")
				invoiceID = &id
			}

			h.dues.CreateTransaction(r.Context(), &model.Transaction{ //nolint:errcheck
				InvoiceID:           invoiceID,
				Amount:              amount,
				Currency:            resource.Amount.CurrencyCode,
				Provider:            "paypal",
				ProviderReferenceID: &providerID,
				ProviderStatus:      &status,
				PaymentMethodType:   &pmType,
				OccurredAt:          time.Now(),
			})
			if invoiceID != nil {
				h.dues.RecomputeInvoiceStatus(r.Context(), *invoiceID) //nolint:errcheck
			}
		}
	}

	h.dues.MarkEventProcessed(r.Context(), event.ID) //nolint:errcheck
	w.WriteHeader(200)
}

func verifyStripeSignature(payload []byte, header, secret string) bool {
	parts := strings.Split(header, ",")
	var ts, sig string
	for _, p := range parts {
		if strings.HasPrefix(p, "t=") {
			ts = strings.TrimPrefix(p, "t=")
		}
		if strings.HasPrefix(p, "v1=") {
			sig = strings.TrimPrefix(p, "v1=")
		}
	}
	if ts == "" || sig == "" {
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
	return hmac.Equal([]byte(expected), []byte(sig))
}

var paypalCertCache sync.Map

func verifyPayPalSignature(body []byte, transmissionID, timestamp, certURL, sig, webhookID string) bool {
	if transmissionID == "" || timestamp == "" || certURL == "" || sig == "" {
		return false
	}
	u, err := url.Parse(certURL)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".paypal.com") {
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
	if v, ok := paypalCertCache.Load(certURL); ok {
		return v.(*x509.Certificate), nil
	}
	resp, err := http.Get(certURL) //nolint:gosec — URL already validated against *.paypal.com
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("paypal: no PEM block in cert response")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	paypalCertCache.Store(certURL, cert)
	return cert, nil
}
