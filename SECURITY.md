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
- `Secure` — Set only when the request arrives over TLS (i.e. in production behind a reverse proxy).
- `Path=/api/v1/auth` — Scoped to the auth sub-path, not sent with every API call.

---

## Authorisation

Three roles with strictly ascending privileges:

| Role | Rank | Can do |
|------|------|--------|
| `member` | 1 | Read-only access to all organisational data |
| `officer` | 2 | Create and update records; cannot manage users or delete core data |
| `admin` | 3 | Full access including user management and destructive operations |

- Roles are encoded in the JWT and enforced in `RequireRole` middleware before reaching any handler.
- The `roleAtLeast` function checks `rank[current] >= rank[required]`; there are no gaps in the hierarchy.
- The first admin account can only be created via `POST /api/v1/auth/bootstrap`, which returns 403 once any user exists, preventing re-bootstrapping.

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

The `Secure` flag on the refresh cookie is set only when `r.TLS != nil`, so it is active when TLS terminates at the Go process itself, and should be enforced at the proxy via HTTPS redirect when terminating upstream.

---

## Rate limiting

- Login attempts are rate-limited to **10 per minute per client IP** using a sliding-window in-process rate limiter.
- The IP is taken from `r.RemoteAddr` after chi's `RealIP` middleware has processed `X-Forwarded-For` headers. The `LoginRateLimit` handler does not re-read `X-Forwarded-For` directly, preventing header injection to bypass the limit.
- The rate limiter is in-process only. For deployments with multiple replicas, a shared counter (e.g. Redis) should replace the built-in limiter.

---

## Payment webhook security

### Stripe

Stripe webhook events are verified with an HMAC-SHA256 signature over `timestamp.payload` using the `QUORUM_STRIPE_WEBHOOK_SECRET`. If the secret is unset, verification is skipped (intended for local development with the Stripe CLI). **Always set the secret in production.**

### PayPal

PayPal webhook events are verified with an RSA-PKCS1v15 signature over `transmissionID|timestamp|webhookID|CRC32(body)`. The signing certificate is fetched from the `PAYPAL-CERT-URL` header, validated against the `*.paypal.com` domain, and cached in memory to avoid repeated fetches. If `QUORUM_PAYPAL_WEBHOOK_ID` is unset, verification is skipped.

### Idempotency

Both webhook handlers check a `processed_events` table before acting. Duplicate deliveries (retries from the provider) are silently discarded, preventing double-counting of payments.

---

## Audit logging

All mutating HTTP requests (`POST`, `PATCH`, `PUT`, `DELETE`) that return a 2xx status and carry an authenticated user ID are written to the `audit_log` table with `user_id`, the HTTP method and path, and a timestamp. Read operations (`GET`) are not logged.

---

## Dependency security

| Dependency | Purpose | Notes |
|------------|---------|-------|
| `github.com/golang-jwt/jwt/v5` | JWT | Signing method validated on parse |
| `golang.org/x/crypto/bcrypt` | Password hashing | Cost 12 |
| `github.com/jackc/pgx/v5` | PostgreSQL | Parameterised queries throughout |
| `github.com/go-chi/chi/v5` | Routing | `RealIP`, `Recoverer`, `Logger` middleware |

Run `go list -m -json all | jq .` and cross-reference with `govulncheck ./...` to check for known CVEs in dependencies.

---

## Operator responsibilities

| Responsibility | Guidance |
|----------------|---------|
| **TLS** | Run behind a reverse proxy that enforces HTTPS and sets `Strict-Transport-Security`. |
| **JWT secret** | Generate with `make secret` (64 random hex chars). Rotate by restarting the server with a new value — all existing sessions will be invalidated. |
| **Database access** | Restrict the PostgreSQL user to the `quorum` database only. Do not grant superuser. |
| **Secrets management** | Inject `QUORUM_JWT_SECRET`, `QUORUM_DATABASE_URL`, and SMTP/payment secrets via environment variables or a secrets manager, not baked into container images. |
| **Stripe/PayPal secrets** | Always configure `QUORUM_STRIPE_WEBHOOK_SECRET` and `QUORUM_PAYPAL_WEBHOOK_ID` in production. Without them, any caller can inject fake payment events. |
| **Backups** | Back up the PostgreSQL database independently of the application container. |
| **Updates** | Monitor `govulncheck` and rebuild/redeploy promptly when critical CVEs are disclosed in dependencies. |
| **Network isolation** | The database should not be reachable from the public internet. Place it in a private subnet or use a Unix socket. |

---

## Reporting a vulnerability

Please report security issues privately by emailing the project maintainers rather than opening a public issue. Include a description of the vulnerability, the steps needed to reproduce it, and the potential impact. We aim to respond within 48 hours.
