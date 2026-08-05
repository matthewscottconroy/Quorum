# Plan: Zelle & Venmo Support

Status: **draft for discussion** — no code yet.

## The constraint that shapes everything

Stripe and PayPal offer webhooks: the provider *pushes* a signed event when
money arrives, which is why those integrations are clean. **Zelle and Venmo
do not offer this to organizations our size.**

- **Zelle** has no public API. It is a bank-to-bank network; programmatic
  access exists only for large financial institutions. From our side, a
  Zelle payment is visible exactly one way: as a line on the org's bank
  statement.
- **Venmo**'s developer API has been closed to new integrations for years.
  The sanctioned path for businesses is **Venmo as a payment method inside
  PayPal/Braintree checkout** — which matters to us, because we already
  speak PayPal.

So the plan is three phases of increasing automation, not one integration.

## Phase 1 — First-class manual providers (small, do now)

Officers can already record a payment with any provider label. Make Zelle
and Venmo feel supported rather than improvised:

- Provider dropdown on manual entry: `cash, check, wire, zelle, venmo,
  other` + reference field ("Zelle conf #", "Venmo transaction id").
- A member-facing **"How to pay"** panel (configurable text per method) on
  My Account: the org's Zelle-registered email/phone, Venmo handle, and the
  instruction to put the **invoice period + member name in the payment
  memo** — the memo is the only linking key these networks give us.
- Reports/analytics already group by provider string; verify labels flow
  through.

Effort: half a day. Risk: none.

## Phase 2 — Venmo through the existing PayPal integration (medium)

PayPal Checkout can present **Venmo as a funding source** (US-only). The
capture event arrives on our **existing** `PAYMENT.CAPTURE.COMPLETED`
webhook with the same signature verification and `custom_id` linking.

Work: enable Venmo funding in the PayPal button configuration we document;
inspect the capture payload's funding-source field and label the recorded
transaction `venmo` when present; extend PAYMENTS-SETUP.md; add an
integration test with a captured sample payload. If the org later wants
card + Venmo in one embedded flow, Braintree is the fallback — but it's a
second vendor onboarding for little gain over PayPal Checkout.

Effort: 1–2 days. Risk: low (rides proven rails). Constraint: Venmo pays
into the org's PayPal balance, not the bank directly.

## Phase 3 — Zelle via bank-statement import & matching (the real feature)

Since Zelle only exists on the bank statement, build **statement
reconciliation** — useful far beyond Zelle (checks and wires benefit too):

- **Import**: officer uploads the bank's CSV/OFX export. New tables
  `import_batches` / `import_rows` (raw line, amount, date, memo, hash of
  the line for duplicate-import protection). Import is idempotent: the same
  statement uploaded twice changes nothing.
- **Matching engine**: for each unmatched credit row, propose invoice
  matches ranked by (exact amount + open status), memo similarity to member
  name/period label, and date proximity. Nothing auto-posts: an officer
  confirms each match, which records a normal `zelle`-provider transaction
  carrying the import row's reference — so the append-only ledger and audit
  trail work unchanged. Unmatched rows can be marked "not dues" (donations,
  refunds, noise).
- **UI**: a Reconciliation tab under Dues: batch list, per-row suggestions,
  one-click confirm, progress ("14 of 17 rows resolved").
- Audit: batch upload, each confirmation, and each dismissal are logged;
  the batch links to its rows forever.

Effort: ~1–2 weeks including tests. Risk: CSV formats vary by bank — start
with the org's actual bank and keep the parser pluggable.

## Explicitly rejected (for now)

- **Plaid/bank-aggregator polling**: automates Phase 3's import step but
  adds a paid third party holding the org's bank credentials — a trust and
  cost decision the board should make deliberately, not a default.
- **Venmo/Zelle scraping or personal-account automation**: violates their
  terms, brittle, and undermines the evidence-grade posture. Never.

## Sequencing recommendation

Phase 1 immediately; Phase 2 when PayPal is configured in production;
Phase 3 when monthly Zelle/check volume makes manual entry a real cost
(rule of thumb: >20 statement lines/month). Phase 3's reconciliation is
also a prerequisite-quality feature for the CPA-accounting roadmap — the
two plans reinforce each other.
