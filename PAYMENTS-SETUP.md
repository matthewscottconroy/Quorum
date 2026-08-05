# Payments Setup, From Zero

Quorum records payments; it never processes cards (PCI scope: zero). Until
you configure a provider, **manual recording already works** — officers
enter cash/check/wire payments on any invoice, and the append-only ledger,
statuses, and reminders all function. This guide wires up the automatic
paths. Every step says where you are and what you should see.

## 0. Decide what you need

| You accept… | Configure |
|---|---|
| Cash / checks / bank transfer only | Nothing — record manually (5.2.4 in the manual) |
| Cards / online via Stripe | Part A |
| PayPal (and Venmo through it, see roadmap) | Part B |

Both providers are **fail-closed**: until their secret is set, the webhook
endpoint answers 503 and processes nothing, so a half-configured system
cannot accept forged payment events.

## Part A — Stripe

1. Create/log into your Stripe account (dashboard.stripe.com). Complete
   business verification so you can accept live payments.
2. Left sidebar → **Developers → Webhooks → Add endpoint**.
   - Endpoint URL: `https://chuposkamountain.com/api/v1/webhooks/stripe`
   - Events: **`payment_intent.succeeded`** (add `charge.succeeded` if you
     use older-style Charges).
   - Click **Add endpoint**.
3. On the new endpoint's page, reveal the **Signing secret** — it starts
   with `whsec_`. Copy it; this is how Quorum proves an event really came
   from Stripe.
4. On the server:
   ```
   sudo nano /opt/quorum/.env      # add:
   QUORUM_STRIPE_WEBHOOK_SECRET=whsec_paste-it-here
   sudo systemctl restart quorum
   ```
5. **Linking payments to invoices.** Stripe tells Quorum *that* money
   arrived; metadata tells it *which invoice*. When you create the payment
   (Payment Link, Invoice, or PaymentIntent), add metadata:
   - key `quorum_invoice_id`, value = the invoice's UUID (shown in the
     invoice's detail view / API).
   - Payment Links: create the link → **Metadata** section → add the key
     there, one link per invoice.
   Without metadata Quorum cannot attach the payment — you'd record it
   manually instead, so always set it.
6. **Test**: dashboard → your endpoint → **Send test event** →
   `payment_intent.succeeded`. Then on the server:
   `sudo journalctl -u quorum | grep -i -m3 stripe` — you should see the
   event received and processed (a test event carries no metadata, so
   "no invoice reference" is the expected, correct outcome).

## Part B — PayPal

1. Log into **developer.paypal.com** → My Apps & Credentials → your live
   app (create one if needed).
2. Scroll to **Webhooks → Add webhook**.
   - URL: `https://chuposkamountain.com/api/v1/webhooks/paypal`
   - Event: **`PAYMENT.CAPTURE.COMPLETED`**.
3. Save, then copy the **Webhook ID** (an alphanumeric id shown on the
   webhook's row — not a secret key; PayPal verification works by fetching
   PayPal's signing certificate and checking the event's signature against
   this id).
4. On the server:
   ```
   sudo nano /opt/quorum/.env      # add:
   QUORUM_PAYPAL_WEBHOOK_ID=paste-the-webhook-id
   sudo systemctl restart quorum
   ```
5. **Linking**: when creating the PayPal order, set
   `custom_id` = `invoice:<the-invoice-uuid>`.
6. Note: the server must be able to reach PayPal's API hosts outbound
   (it can, unless you've added egress filtering).

## Verification checklist (both)

- [ ] `curl -s https://chuposkamountain.com/api/v1/webhooks/stripe -X POST`
      returns **400** (bad signature) — not 503 — once configured.
- [ ] A real test payment with metadata lands as a transaction on the right
      invoice, status recomputes, and an audit entry records it.
- [ ] `QUORUM_ALLOW_UNSIGNED_WEBHOOKS` is **absent** from `.env` (it is a
      local-development flag; in production it must never be set).

## Rules the system enforces regardless

- A payment must match its invoice's **currency** or it will not count
  toward paid status; mismatched manual entries are rejected.
- The transactions table is **append-only** — corrections are offsetting
  entries, never edits (the database refuses).
- Duplicate webhook deliveries are processed **once**.
- Refunds: execute them at the provider, then record an offsetting manual
  transaction (negative correction entry) with a note.
