# Security

This document describes how Quorum protects data and operations, the threat model it is designed against, and the responsibilities of operators deploying it.

---

## Threat model

Quorum is a private, self-hosted application for a single organisation. The primary threats it is designed to resist are:

- **Credential theft** — stolen passwords or tokens used to impersonate users
- **Privilege escalation** — members accessing officer or admin operations
- **Injection** — SQL injection or XSS via untrusted input
- **Webhook forgery** — fabricated payment events manipulating financial records
- **Server-side request forgery** — via the PayPal certificate fetch path
- **Brute-force login** — automated credential guessing

It is **not** designed to defend against a compromised host OS, a malicious database administrator, or insider threats from users who hold admin role.

---

## Authentication

### Passwords

- Bcrypt with cost factor 12 (`golang.org/x/crypto/bcrypt`).
- Minimum 10 characters enforced at every creation and change point.
- Passwords are never returned in API responses or logged.

### Access tokens

- HS256 JWTs signed with `QUORUM_JWT_SECRET` (minimum 32 characters, validated at startup).
- Default lifetime: 15 minutes (`QUORUM_JWT_ACCESS_TTL`).
- Claims carry `user_id` and `role`; role is re-read from the DB on each token issue, not on each request.
- Signing method is validated on parse to prevent algorithm-confusion attacks.

### Refresh tokens

- 32-byte cryptographically random values (`crypto/rand`).
- The **plain token** is sent to the browser once (via the `quorum_refresh` HttpOnly cookie) and never stored server-side.
- The SHA-256 digest is stored in the `refresh_tokens` table. A stolen database dump does not yield usable tokens.
- Tokens are revoked (set `revoked = TRUE`) on logout.
- **All** refresh tokens for a user are revoked when that user changes their password, terminating all other active sessions.
- Tokens expire after 7 days (`QUORUM_JWT_REFRESH_TTL`).

### Session cookie

```
Set-Cookie: quorum_refresh=<token>; Path=/api/v1/auth; HttpOnly; SameSite=Strict; Secure
```

- `HttpOnly` — JavaScript cannot read the cookie, protecting against XSS-based token theft.
- `SameSite=Strict` — Cookie is not sent on cross-site requests, preventing CSRF.
- `Secure` — Driven by `QUORUM_BASE_URL`: set only when it begins with `https://` (`config.SecureCookies = strings.HasPrefix(BaseURL, "https://")`), **not** from `r.TLS`. When TLS terminates at an upstream proxy the Go process sees plain HTTP, so operators **must** set `QUORUM_BASE_URL=https://…` or the refresh cookie ships without `Secure`.
- `Path=/api/v1/auth` — Scoped to the auth sub-path, not sent with every API call.

---

## Authorisation

Five roles with strictly ascending privileges (`roleRank` in `internal/handler/handler.go`):

| Role | Rank | Can do |
|------|------|--------|
| `restricted` | 1 | Sees only its own linked member record (own profile, dues, assigned action items); 403 on every shared/org-wide resource |
| `member` | 2 | Read-only access to all shared organisational data |
| `officer` | 3 | Create and update records; cannot manage users or delete core data |
| `admin` | 4 | Officer plus user management and member deactivation (reversible soft-delete) |
| `superadmin` | 5 | Full access, including permanent (destructive) deletion of core records and user accounts |

- Roles are encoded in the JWT and enforced in `RequireRole` middleware before reaching any handler.
- The `roleAtLeast` function checks `rank[current] >= rank[required]`; there are no gaps in the hierarchy.
- Permanent deletion of core records and users is reserved for `superadmin` (see [Authorisation and destructive actions](#authorisation-and-destructive-actions) below); `admin` cannot destructively delete.
- The first account can only be created via `POST /api/v1/auth/bootstrap`, which mints a `superadmin` and returns 403 once any user exists, preventing re-bootstrapping.

---

## Input validation and injection prevention

### SQL injection

- All database queries use parameterised statements with `$n` placeholders via pgx/v5.
- PostgreSQL type casts (`::uuid`, `::text`) reject malformed identifiers at the database layer.
- Dynamic `UPDATE SET` clauses (members, plans, action items) validate field names against explicit allowlists before interpolation — user-supplied map keys are never used directly as column names.
- Full-text search uses `plainto_tsquery()`, which treats input as plain text and cannot inject SQL operators.

### Cross-site scripting (XSS)

- All user-controlled values rendered in the frontend pass through an `esc()` function that HTML-encodes `&`, `<`, `>`, and `"`.
- The `payment-status-badge` component uses `textContent` (not `innerHTML`) to render the `status` attribute.
- Resource URLs are validated with `safeUrl()` before being placed in `href` attributes; only `http:` and `https:` schemes are permitted, blocking `javascript:` and `data:` URIs.
- The Content Security Policy header restricts script sources to `'self'`, blocking injected inline scripts and external script loads.

### Request size limiting

All inbound request bodies are capped at 1 MiB via `http.MaxBytesReader` applied as a global middleware, preventing memory exhaustion from oversized payloads.

---

## Transport security

### HTTP headers

Every response includes:

```
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
```

- `X-Content-Type-Options: nosniff` prevents MIME-type sniffing attacks.
- `X-Frame-Options: DENY` blocks the app from being embedded in iframes (clickjacking).

### TLS

Quorum listens on plain HTTP and is expected to sit behind a TLS-terminating reverse proxy (nginx, Caddy, or a cloud load balancer). TLS certificates and HTTPS enforcement are the operator's responsibility.

The `Secure` flag on the refresh cookie is derived from `QUORUM_BASE_URL` — it is set only when that URL begins with `https://` — **not** from `r.TLS` (which is always nil behind a TLS-terminating proxy). Operators terminating TLS upstream **must** set `QUORUM_BASE_URL=https://…`; otherwise the refresh cookie is sent without `Secure` and can leak over a downgraded connection. HTTPS should additionally be enforced at the proxy via an HTTP→HTTPS redirect.

---

## Rate limiting

- Login attempts are rate-limited to **10 per minute per client key** using a sliding-window in-process rate limiter (token refresh has a separate, more permissive limiter at 60/min).
- Authenticated API calls are additionally capped at **1200 per minute per user** (keyed on the authenticated user id, applied right after auth and before audit logging). The ceiling sits far above any legitimate single-page-app burst; its purpose is to stop a pathological client — e.g. a loop hammering above-role endpoints, each denial writing an audit row — from amplifying load or the audit chain without bound.
- **Spreadsheet formula injection:** every user-influenced text field in a CSV export (member/contact names, notes, emails, memos, provider strings, audit detail, etc.) is neutralised — a leading `=`, `+`, `-`, `@`, tab, or CR is prefixed with an apostrophe so Excel/Sheets treat the cell as literal text rather than an executable formula.
- **IP sourcing:** chi's `RealIP` middleware is deliberately **not** used. By default `clientIP()` keys on the raw socket address (`r.RemoteAddr`), so a client cannot spoof its rate-limit key with a forged header. Only when `QUORUM_TRUST_PROXY_HEADERS=true` does it read the proxy-supplied client IP — `X-Real-IP` first, then the leftmost `X-Forwarded-For` entry. Enable that flag **only** behind a trusted reverse proxy/ingress that sets and sanitises those headers; otherwise leave it off (direct exposure) so headers cannot be forged, and note that with it off all clients behind a proxy would share a single bucket.
- The rate limiter is in-process only. For deployments with multiple replicas, a shared counter (e.g. Redis) or ingress-level rate limiting should replace the built-in limiter.

---

## Payment webhook security

### Stripe

Stripe webhook events are verified with an HMAC-SHA256 signature over `timestamp.payload` using the `QUORUM_STRIPE_WEBHOOK_SECRET`. Multiple `v1=` signatures in the header are accepted (any one match passes), so secret rotation is safe. If the secret is unset, the endpoint **fails closed** and returns `503` — events are only processed unverified when `QUORUM_ALLOW_UNSIGNED_WEBHOOKS=true` is set explicitly (intended for local development with the Stripe CLI; never set it in production). Processed payment events are recorded atomically: the idempotency claim, transaction insert, and invoice-status recompute commit in one database transaction, and processing failures return `5xx` so the provider retries instead of silently dropping the payment.

### PayPal

PayPal webhook events are verified with an RSA-PKCS1v15 signature over `transmissionID|timestamp|webhookID|CRC32(body)`. The signing certificate is fetched from the `PAYPAL-CERT-URL` header, which must be HTTPS and match an exact allowlist of PayPal API hosts (no suffix matching); the fetch never follows redirects, uses a 10-second timeout, and the certificate's chain of trust and validity window are verified against the system root pool before use. Verified certificates are cached in memory (bounded cache). If `QUORUM_PAYPAL_WEBHOOK_ID` is unset, the endpoint fails closed with `503` unless `QUORUM_ALLOW_UNSIGNED_WEBHOOKS=true`.

### Idempotency

Both webhook handlers check a `processed_events` table before acting. Duplicate deliveries (retries from the provider) are silently discarded, preventing double-counting of payments.

---

## Authorisation and destructive actions

Access is controlled by five roles encoded in the JWT — `restricted` < `member` < `officer` < `admin` < `superadmin` — enforced per route in middleware. The JWT also carries the account's `member_id`; a `restricted` user may read only its own linked member record (profile, dues, action items) and receives 403 on every shared/org-wide resource.

Permanent deletion of core records (meetings, plans, contacts, resources, action items) and of user accounts is reserved for `superadmin` and is defence-in-depth gated:

- **Type-to-confirm**: the request must carry `?confirm=<exact record name>` (matched case-insensitively) or it is rejected with 400 before anything is deleted.
- **Audit**: the delete is recorded in `audit_log` like any mutating request.
- **Notification**: when SMTP is configured, all admins/superadmins are emailed a record of what was deleted and by whom.

Assigning or revoking the `superadmin` role is itself a superadmin-only action, and no user may change their own role — preventing both privilege self-escalation and accidental self-lockout of the last superadmin.

## Email

Outbound email (dues reminders, admin digests, deletion notices) is sent over SMTP with STARTTLS when the server offers it. Recipient addresses and subjects are rejected if they contain a CR/LF, preventing header injection; message bodies follow the header/body separator and cannot forge headers.

## Audit logging

All mutating HTTP requests (`POST`, `PATCH`, `PUT`, `DELETE`) that return a 2xx status and carry an authenticated user ID are written to the `audit_log` table with `user_id`, the HTTP method and path, and a timestamp. Read operations (`GET`) are not logged. Audit rows are pruned after one year by the nightly job.

---

## Dependency security

| Dependency | Purpose | Notes |
|------------|---------|-------|
| `github.com/golang-jwt/jwt/v5` | JWT | Signing method validated on parse |
| `golang.org/x/crypto/bcrypt` | Password hashing | Cost 12 |
| `github.com/jackc/pgx/v5` | PostgreSQL | Parameterised queries throughout |
| `github.com/go-chi/chi/v5` | Routing | `Recoverer` and `Logger` middleware (`RealIP` deliberately omitted — see Rate limiting) |

Run `go list -m -json all | jq .` and cross-reference with `govulncheck ./...` to check for known CVEs in dependencies.

---

## Operator responsibilities

| Responsibility | Guidance |
|----------------|---------|
| **TLS** | Run behind a reverse proxy that enforces HTTPS and sets `Strict-Transport-Security`. |
| **JWT secret** | Generate with `make secret` (64 random hex chars). Rotate by restarting the server with a new value — all existing sessions will be invalidated. |
| **Database access** | Restrict the PostgreSQL user to the `quorum` database only. Do not grant superuser. |
| **Secrets management** | Inject `QUORUM_JWT_SECRET`, `QUORUM_DATABASE_URL`, and SMTP/payment secrets via environment variables or a secrets manager, not baked into container images. |
| **Stripe/PayPal secrets** | Configure `QUORUM_STRIPE_WEBHOOK_SECRET` and `QUORUM_PAYPAL_WEBHOOK_ID` in production. An unconfigured provider fails closed (`503`); never set `QUORUM_ALLOW_UNSIGNED_WEBHOOKS=true` in production, as it would let any caller inject unverified payment events. |
| **Backups** | Back up the PostgreSQL database independently of the application container. |
| **Updates** | Monitor `govulncheck` and rebuild/redeploy promptly when critical CVEs are disclosed in dependencies. |
| **Network isolation** | The database should not be reachable from the public internet. Place it in a private subnet or use a Unix socket. |

---

## Reporting a vulnerability

Please report security issues privately by emailing the project maintainers rather than opening a public issue. Include a description of the vulnerability, the steps needed to reproduce it, and the potential impact. We aim to respond within 48 hours.

## Sessions & idle logout

Access tokens live 15 minutes (`QUORUM_JWT_ACCESS_TTL`); refresh tokens live 7
days (`QUORUM_JWT_REFRESH_TTL`), are stored hashed, and rotate on every use.
Idle logout is enforced **server-side**: rotation makes a refresh token's age
equal the time since the session's last activity, so a token older than
`QUORUM_IDLE_TIMEOUT_MINUTES` (default 30) is refused and revoked, and the
event is audited (`auth.session_idle_timeout`). The UI mirrors the window —
warning at 29 minutes of inactivity, signing out at 30 — but the guarantee is
the server's, not the browser's.

## Screen capture, printing, and exports

Browser printing of application screens is disabled (a print attempt produces
a notice pointing to the audited path): screen views are live, unwatermarked,
and leave no record, while **Reports → PDF** exports are watermarked with the
exporter and time, sealed with a verifiable SHA-256, and logged to the audit
chain.

Screenshots **cannot be prevented** by a web application — the OS captures
rendered pixels, outside the page's reach — and any vendor claiming otherwise
is selling theater. Quorum's posture is attribution, not prevention: every
authenticated view (including modals) carries a faint tiled watermark of the
signed-in account and date, so a captured screen identifies who was looking at
it and when. It is a deterrent an insider can defeat with effort (edit the
pixels, style-strip the page) — the point is that casual leaks become
attributable, while the *legitimate* export path stays watermarked, sealed,
and audit-logged. Treat true capture prevention as a device-management concern
(MDM, managed browsers), not an application feature.

## Encryption at rest and in transit

- **Client ↔ server:** terminate TLS in front of the app (reverse proxy);
  cookies are `Secure` when `QUORUM_BASE_URL` is https.
- **Server ↔ database:** governed by `sslmode` in `QUORUM_DATABASE_URL`.
  Same-host/pod traffic (the default local stack) never crosses a network;
  for a remote database set `sslmode=require` (or `verify-full`) — the server
  logs a loud startup warning if a remote connection is configured without it.
- **Database at rest:** not encrypted by Quorum — use full-disk/volume
  encryption (LUKS) or your managed database's at-rest encryption.
- **Backups at rest:** set `QUORUM_BACKUP_PASSPHRASE` and dumps are encrypted
  with AES-256 (PBKDF2, 200k iterations); verify/restore decrypt
  transparently. Keep the passphrase in a secret manager — an encrypted backup
  without it is unrecoverable. See BACKUP.md.
