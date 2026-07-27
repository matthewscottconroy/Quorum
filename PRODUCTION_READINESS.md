# Production Readiness

This document tracks the remaining work to get Quorum from "works end-to-end in
a smoke test" to "confidently running in production." It is a living checklist,
not a release gate — triage by your own risk tolerance and deployment scale.

Last reviewed: **2026-07-24**.

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
  bounds request bodies (1 MiB) and SMTP timeouts, but has no global read/write
  HTTP server timeouts configured — **add `ReadHeaderTimeout`, `ReadTimeout`,
  `WriteTimeout`, and `IdleTimeout` to the `http.Server`** (cheap hardening
  against slowloris).
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
- Consider rate-limiting `POST /auth/login/2fa` per-account (today it shares the
  per-IP login limiter). A stolen password + unlimited code attempts is 10^6
  guesses; the ±1 window and per-IP limit mitigate but do not eliminate this.
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
