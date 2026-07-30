# Production Readiness

This document tracks the remaining work to get Quorum from "works end-to-end in
a smoke test" to "confidently running in production." It is a living checklist,
not a release gate — triage by your own risk tolerance and deployment scale.

Last reviewed: **2026-07-30** (evidence-integrity pass).

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
- **Done (tooling):** `scripts/backup.sh` (+ `make backup` / `backup-verify` /
  `restore`) takes custom-format `pg_dump` backups with timestamped filenames and
  a keep-N retention policy, works both against the local Podman container and a
  production `QUORUM_DATABASE_URL`, and — critically — `verify` restores a dump
  into a throwaway database and checks it, so "untested backups are not backups"
  is enforceable in CI/cron. Full runbook: **[BACKUP.md](BACKUP.md)**.
- **Done (units shipped):** `ops/systemd/` carries ready-to-install timers for
  the nightly backup, weekly restore-verification, and daily audit-chain
  verification. Still deployment-side: install them and **ship the dumps
  off-box**.
  For a tighter RPO than nightly dumps, use a managed Postgres with WAL archiving
  / PITR and keep these logical dumps as the portable, test-restored safety net.
- Decide a retention policy for the audit log and soft-deleted members.

### 4. Two-factor & account-recovery hardening
- **Done:** break-glass for a locked-out sole admin — `quorum -unlock-2fa
  <email>` clears TOTP + recovery codes and revokes that user's sessions, so they
  can sign in with their password and re-enroll. It needs shell access to the
  deployment (the intended bar for bypassing a second factor) and is not exposed
  over HTTP. See [RUNBOOK.md](RUNBOOK.md#recover-a-locked-out-admin).
- **Done:** `POST /auth/login/2fa` is now throttled **per-account** (5 failed
  attempts / 15 min → temporary lockout) on top of the per-IP login limiter, so
  an attacker rotating IPs can no longer get unlimited guesses at one account's
  6-digit code. The throttle counts only failures and clears on a successful
  login. It is **in-process** (like the rate limiter — see §6): a multi-replica
  deployment should move this to a shared store or enforce it at the ingress.
- **Done:** enrolling in 2FA now requires the account password, matching disable
  — a hijacked session can no longer bind an attacker's authenticator.
- **Done:** "Regenerate recovery codes" (password-gated) in Settings; the new set
  replaces the old, so previously-issued codes stop working immediately.

### 5. Observability
- **Done:** structured JSON request logging via `slog` — one object per request
  with method, path (token-redacted), status, `duration_ms`, and a `request_id`
  (also echoed in the `X-Request-Id` response header; an inbound one from a
  trusted proxy is honored). Level is `QUORUM_LOG_LEVEL` (debug/info/warn/error).
- **Done:** a dependency-free Prometheus exposition at `/metrics`, gated by
  `QUORUM_METRICS_TOKEN` (disabled when unset): `quorum_http_requests_total`
  (by method/route-pattern/status), a request-latency histogram,
  `quorum_http_requests_in_flight`, `quorum_http_panics_total`, and DB pool
  gauges (acquired/idle/total/max). Panics are recovered, counted, and logged
  with a stack + request id by the app's own recoverer.
- **Done (rules shipped):** `ops/prometheus-alerts.yml` carries the alert pack
  (down, 5xx rate, panics, DB pool saturation, failed-login and 2FA-failure
  spikes, p99 latency) plus the scrape-config example; loading it into your
  Prometheus is the remaining deployment step. Optionally forward panics to an
  error aggregator (Sentry) — the structured `http_panic` log line is the hook.
- The metrics are **in-process** (per replica); aggregate across replicas in
  Prometheus. `/healthz` (liveness) and `/readyz` (readiness, checks the DB)
  exist — wire them to your orchestrator's probes.

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
- **Done:** CI runs a dedicated migrations job that takes the schema up, back
  down to zero, and up again on a real Postgres, asserting no table survives the
  rollback — so every `.down.sql` is verified, not assumed. Integration tests run
  across `./internal/...`, and `go vet -tags integration` keeps tag-gated files
  from rotting (it had already caught one stale signature).
- **Done:** `govulncheck` and `golangci-lint` run in CI; Dependabot covers gomod
  and github-actions weekly.
- Still open: **third-party review / pen-test** of the auth surface (above).
- Consider CSRF defenses if you ever move auth off the `Authorization` header:
  today the API is bearer-token driven (not cookie-authenticated for state
  changes), so classic CSRF does not apply, but the refresh cookie is
  `SameSite=Strict` — keep it that way.

### 9. Data-protection / compliance (if handling EU/CA members)
- **Done:** the JSON personal-data export covers portability, and
  `POST /members/{id}/erase` (superadmin, confirm-gated) covers erasure — it
  anonymizes the member in place so invoices, payments, attendance, and votes
  stay consistent with no link to a natural person. See
  [RUNBOOK.md](RUNBOOK.md#fulfil-a-data-request).
- **Done:** the retention schedule is configurable —
  `QUORUM_AUDIT_RETENTION_DAYS` (default 365) drives the nightly audit prune;
  processed webhook events are kept 90 days; expired tokens are pruned nightly.
- A counsel-review-ready **privacy policy template** now exists
  (PRIVACY_POLICY_TEMPLATE.md) with the technical claims pre-filled and accurate.
  Still open (deployment-side): counsel review of that template, and
  **encryption at rest** for the database volume.
- **New — evidence integrity ([COMPLIANCE.md](COMPLIANCE.md)):** the audit log
  is hash-chained and append-only at the database level, the payments ledger is
  immutable, invoice identity is frozen, denied actions and auth failures by
  known accounts are recorded, and third parties can verify exports
  independently (`ops/verify-audit-export.py`).

### 10. Operational runbooks
- **Done:** [RUNBOOK.md](RUNBOOK.md) covers rotating `QUORUM_JWT_SECRET`,
  recovering a locked-out admin (all three cases), rolling back a migration,
  restoring from backup, fulfilling data requests, tracing a request by id,
  health/metrics checks, the nightly job, and what does not aggregate across
  replicas. Backup/DR detail lives in [BACKUP.md](BACKUP.md).

---

## Deferred architectural work (larger efforts) — now delivered

These five were previously deferred as multi-day efforts touching schema, API,
and UI. All have now landed; each is summarized with where it lives and what
remains deployment-side.

1. **Multi-currency reporting / FX conversion.** ✅ **Done.** Migration 0015 adds
   an org `reporting_currency` and an effective-dated `fx_rates` table;
   `model.Converter` does exact (big.Rat) conversion across currency exponents;
   analytics and budget aggregation group by currency and convert at the
   boundary, reporting any `unconvertible_currencies`. Admin UI under Currencies.
   With no rates configured the behavior degrades to the old single-currency
   assumption (and flags it), so it is safe by default.

2. **CRUD-handler generics refactor.** ✅ **Done.** `internal/handler/crud.go`
   carries the shared List envelope (`writePage`), `genericGet`,
   `filterAllowedFields`, and a typed `crudDelete`/`deleteSpec` for the
   confirm→delete→notify flow. The contacts/resources/plans/action-items/
   meetings handlers now delegate the mechanical parts and keep only their
   bespoke validation — ~185 lines of duplication removed. Chosen over a single
   closure-driven mega-handler, which would have traded duplication for
   indirection and buried each resource's validation.

3. **Observability: metrics + structured logging + error aggregation.** ✅
   **Done.** See §5 — `slog` JSON logs with request IDs, a dependency-free
   Prometheus exposition at `/metrics` (token-gated), and a panic-counting
   recoverer. Wiring a scrape + alerts is the remaining deployment-side step.

4. **Backups / disaster recovery.** ✅ **Done.** See §3 and
   [BACKUP.md](BACKUP.md). Scheduling the job and shipping dumps off-box remain
   deployment-side.

5. **Automatic notifications system.** ✅ **Done.** Migration 0016 adds
   `notifications` + `notification_preferences`; a bounded-worker
   `NotificationService` records in-app notices and sends opt-out email off the
   request path; events wired for motion opened/decided, meeting scheduled, and
   action-item assigned; frontend bell + Notifications page. Extending it to more
   events is now a one-line `NotifyMembers`/`NotifyMember` call per event.

---

## Nice-to-have / future

- Email verification on account creation.
- Configurable password policy (length is enforced at 10; consider a breached-
  password check via HaveIBeenPwned k-anonymity API).
- **Done:** session management — Settings shows your active session count and
  "Sign out other devices" revokes every session but the current one.
- **Done:** audit-log viewer in the admin UI (filterable, paginated).
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
- [ ] Automated + test-restored DB backups — *tooling ready ([BACKUP.md](BACKUP.md)); schedule it and ship dumps off-box*
- [ ] Liveness/readiness probes wired
- [ ] Metrics + error alerting in place — *`/metrics` ready; wire the scrape + alert rules*
- [x] Break-glass admin-recovery runbook written ([RUNBOOK.md](RUNBOOK.md))
- [x] Migrations run up/down in CI; `govulncheck` clean
- [ ] First bootstrap superadmin created, then bootstrap endpoint confirmed closed
