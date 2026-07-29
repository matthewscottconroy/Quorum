# Production Readiness

This document tracks the remaining work to get Quorum from "works end-to-end in
a smoke test" to "confidently running in production." It is a living checklist,
not a release gate — triage by your own risk tolerance and deployment scale.

Last reviewed: **2026-07-29**.

---

## What is already done

These were completed and verified end-to-end against a real PostgreSQL 16
instance (bootstrap → login → member/invoice/payment → restricted-role scoping,
plus the items below):

- **Two-factor authentication (TOTP, RFC 6238).** Self-enroll from Settings
  (secret + `otpauth://` URI), confirm-with-code to enable, one-time recovery
  codes issued at enrollment. Login becomes a two-step flow (`mfa_required` →
  `POST /auth/login/2fa`). The interim MFA token (`purpose: "mfa"`) is rejected
  by the API middleware; recovery codes are single-use; disabling 2FA
  re-verifies the account password. No new dependency — built on the stdlib.
- **Password recovery.**
  - Self-service: `POST /auth/forgot-password` (always 200, no account
    enumeration) emails a single-use, 1-hour, SHA-256-hashed reset token;
    `POST /auth/reset-password` consumes it and revokes all of that user's
    sessions.
  - Admin: `POST /users/{id}/reset-password` sets a new password or generates a
    strong temporary one (returned once). Resetting a **superadmin** requires a
    superadmin actor.
- **Data export.** CSV for members (`member`+), dues and transactions
  (`officer`+); a personal-data JSON export (`GET /auth/me/export`) available to
  any authenticated user (including `restricted`, scoped to their own record).
  Downloads carry the bearer token via a dedicated `apiDownload` helper.
- **Root-route fix.** `/` now serves `index.html` (previously 404'd).
- **Audit trail with entity attribution.** Every mutating request is logged with
  the acting user, the resource type, and the affected row id — including POST
  creates, whose id is recovered from the response body (migration 0014 adds
  `entity_type` and an `(entity_type, entity_id)` index). Auth events
  (login/bootstrap/password-reset) self-log. An admin audit-log **viewer UI** is
  still to build (see future list).
- **Governance-history integrity.** Deleting a meeting that recorded decisions
  or a decided motion is refused with 409 (cancel it instead), so a hard delete
  can no longer cascade away governance history.
- **Per-account 2FA lockout.** See §4 below.
- **Frontend money-helper tests.** The JS minor-unit money math (which mirrors
  the Go parser) has unit tests run in CI via Node's built-in test runner
  (`make test-web`).

Constraint honored: **no SSO** — recovery is fully in-app.

---

## Recommended before a real production launch

### 1. Secrets & configuration hygiene (highest priority, lowest effort)
- Generate a real `QUORUM_JWT_SECRET` (`make secret`); never ship the template
  value. The config loader already rejects `CHANGEME`, but confirm it in your
  deploy pipeline.
- Set `QUORUM_BASE_URL` to the real HTTPS URL. This drives `Secure` cookies
  **and** the reset-link host in emails — a wrong value silently breaks both.
- Provide real SMTP settings (`QUORUM_SMTP_*`, `QUORUM_EMAIL_FROM`). Until then,
  password-reset emails are **silently dropped** (the endpoint still returns 200).
  Consider surfacing an admin-visible warning when SMTP is unconfigured.
- Set `QUORUM_SMTP_REQUIRE_TLS=true` in production so mail is never sent in
  cleartext.
- Leave `QUORUM_ALLOW_UNSIGNED_WEBHOOKS` unset (false). Configure the Stripe /
  PayPal signing secrets before accepting live payment webhooks.
- Only set `QUORUM_TRUST_PROXY_HEADERS=true` when actually behind a proxy that
  strips client-supplied `X-Forwarded-For` — otherwise it lets clients spoof
  their rate-limit key.

### 2. Transport & deployment
- Terminate TLS in front of the app (reverse proxy / ingress). The app sets HSTS
  expectations via `Secure` cookies but does not itself do TLS.
- Run behind a proxy that sets timeouts and a sane max connection count. The app
  bounds request bodies (1 MiB), SMTP timeouts, and its `http.Server` read/header/
  write/idle timeouts (see `cmd/quorum/main.go`), so basic slowloris hardening is
  in place; a proxy is still recommended for connection limits and TLS.
- Pin the container image by digest; run as non-root (the Dockerfile should
  already; verify).

### 3. Backups & data durability
- Automated, tested PostgreSQL backups (pg_dump or WAL archiving) with a
  documented restore procedure. **Untested backups are not backups.**
- Decide a retention policy for the audit log and soft-deleted members.

### 4. Two-factor & account-recovery hardening
- **Break-glass for a locked-out sole admin.** If the only superadmin loses both
  their authenticator and recovery codes, there is currently no recovery except
  direct DB access. Document a runbook (SQL to clear `totp_enabled` for a user)
  or add a CLI/`make` target.
- **Done:** `POST /auth/login/2fa` is now throttled **per-account** (5 failed
  attempts / 15 min → temporary lockout) on top of the per-IP login limiter, so
  an attacker rotating IPs can no longer get unlimited guesses at one account's
  6-digit code. The throttle counts only failures and clears on a successful
  login. It is **in-process** (like the rate limiter — see §6): a multi-replica
  deployment should move this to a shared store or enforce it at the ingress.
- Consider requiring password re-entry (or a fresh login) before **enabling**
  2FA, not just disabling it. Today enrollment relies on the existing session.
- Recovery codes are shown once and stored hashed — confirm the UI copy tells
  users to save them, and consider a "regenerate recovery codes" action.

### 5. Observability
- Structured request logging is present (chi Logger). Add:
  - Metrics endpoint (Prometheus) for request rates, latencies, error rates,
    DB pool saturation.
  - Alerting on 5xx rate, failed-login spikes, and scheduler failures (dues
    aging / reminders).
  - Error aggregation (Sentry or similar) for panics recovered by
    `chimiddleware.Recoverer`.
- `/healthz` (liveness) and `/readyz` (readiness, checks the DB) exist — wire
  them to your orchestrator's probes.

### 6. Rate limiting at scale
- The current limiter is **in-process** (per-instance sliding window). Running
  more than one replica multiplies the effective limit by the replica count and
  loses the bucket on restart. For multi-instance deployments, move to a shared
  store (Redis) or enforce limits at the ingress.
- The **nightly job is safe to run multi-replica**: it acquires a Postgres
  advisory lock (`WithLeaderLock`) so exactly one instance generates recurring
  dues / sends reminders per window. The rate limiter above is the remaining
  per-instance component.

### 7. Email deliverability
- Configure SPF / DKIM / DMARC for `QUORUM_EMAIL_FROM`'s domain, or reset and
  reminder emails will land in spam.
- Consider a transactional email provider (SES, Postmark, SendGrid) instead of
  raw SMTP for better deliverability and bounce handling.

### 8. Security review & testing depth
- **Third-party review / pen-test** of the auth surface before handling real
  member PII and payments.
- Add integration tests (the `//go:build integration` harness exists) covering
  the new reset and 2FA repo methods against real Postgres in CI.
- Add a CI job that runs migrations up **and down** against a scratch Postgres
  on every PR (both directions were validated manually here).
- Dependency scanning (`govulncheck`, Dependabot) in CI.
- Consider CSRF defenses if you ever move auth off the `Authorization` header:
  today the API is bearer-token driven (not cookie-authenticated for state
  changes), so classic CSRF does not apply, but the refresh cookie is
  `SameSite=Strict` — keep it that way.

### 9. Data-protection / compliance (if handling EU/CA members)
- The JSON personal-data export supports data-portability requests. Also define:
  - A **data-deletion** path (right to erasure) beyond the existing member
    soft-delete + superadmin hard-delete.
  - A privacy policy and a data-retention schedule.
  - Encryption at rest for the database volume.

### 10. Operational runbooks
- Document: how to rotate `QUORUM_JWT_SECRET` (invalidates all live access
  tokens — expected), how to reset a locked-out admin, how to restore from
  backup, and how to roll back a migration.

---

## Deferred architectural work (larger efforts)

These are known, scoped, and deliberately **not** attempted in the recent
hardening passes because each is a multi-day effort touching schema, API, and UI
— too large to land safely alongside bounded fixes. Listed with why-deferred and
a rough shape so they can be picked up as their own projects.

1. **Multi-currency reporting / FX conversion.** *The one with real correctness
   impact.* Invoices, payments, and budget lines each carry their own currency,
   but analytics and budget totals sum across rows without converting (see the
   `mixed_currencies` warning banner — a stopgap, not a fix). Real support needs:
   a rate source (stored `fx_rates` table, effective-dated, manual entry at
   minimum), a chosen reporting currency per org, and conversion at the
   aggregation boundary in the analytics/budget repos. Until then: **keep an org
   on a single currency for accurate rollups.**

2. **CRUD-handler generics refactor.** The eight resource handlers (members,
   contacts, resources, action-items, plans, …) repeat the same list/get/create/
   update/delete + filter/paginate shape. A generic handler (Go 1.23 generics +
   a small per-resource descriptor) would cut a few hundred lines and make
   cross-cutting changes (like the audit/confirm gates) land in one place.
   Deferred because it is pure refactor with wide blast radius and no user-facing
   change — best done behind the existing handler test suite in one focused pass.

3. **Observability: metrics + structured logging + error aggregation.** §5
   above. Prometheus metrics (request rate/latency/error, DB pool saturation),
   Sentry-style panic aggregation, and switching the chi logger to structured
   JSON (`slog`) with a request-id field. Deferred because it is deployment-infra
   work (needs a metrics backend and alerting rules to be useful) rather than a
   code change in isolation.

4. **Backups / disaster recovery.** §3 above. Automated `pg_dump` or WAL
   archiving, a **tested** restore runbook, retention policy for the audit log
   and soft-deleted rows. Deferred because it is operational tooling around the
   deployment, not application code.

5. **Automatic notifications system.** Today deletions notify affected members
   and the dues scheduler emails reminders (`reminder_stage`, see DESIGN.md
   §13). A general in-app + email notification system (motion opened, ballot
   requested, meeting scheduled, quorum reached) would need a `notifications`
   table, per-user preferences, a delivery worker, and UI. Deferred because it is
   a feature area of its own, not a hardening item.

---

## Nice-to-have / future

- Email verification on account creation.
- Configurable password policy (length is enforced at 10; consider a breached-
  password check via HaveIBeenPwned k-anonymity API).
- Session management UI ("sign out all other devices" — the backend already
  supports revoking all refresh tokens).
- Audit-log viewer in the admin UI.
- Bulk import (CSV) to complement the CSV export.
- Internationalization / currency display polish (money is already stored in
  minor units and formatted per-currency).
- Multi-currency reporting / FX conversion — see **Deferred architectural work
  §1** above (the analytics `mixed_currencies` banner is the current stopgap).
- WebAuthn / passkeys as a stronger second factor (or passwordless) later.

---

## Quick pre-deploy checklist

- [ ] Real `QUORUM_JWT_SECRET`, `QUORUM_BASE_URL` (https), SMTP configured
- [ ] `QUORUM_SMTP_REQUIRE_TLS=true`, webhook secrets set, unsigned webhooks off
- [ ] TLS terminated in front; HTTP server timeouts added
- [ ] Automated + test-restored DB backups
- [ ] Liveness/readiness probes wired
- [ ] Metrics + error alerting in place
- [ ] Break-glass admin-recovery runbook written
- [ ] Migrations run up/down in CI; `govulncheck` clean
- [ ] First bootstrap superadmin created, then bootstrap endpoint confirmed closed
