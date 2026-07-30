# Operations Runbook

Procedures for running Quorum in production. Each entry states when you'd need
it, the exact commands, and what the user-visible effect is. Backups and
disaster recovery have their own document: **[BACKUP.md](BACKUP.md)**.

Assumptions: the deployment directory is `/opt/quorum`, the app is the `quorum`
binary (or container) configured through environment variables, and you have
shell access to the host and the database.

---

## Rotate `QUORUM_JWT_SECRET`

**When:** the secret leaked, an admin with deploy access left, or on a scheduled
rotation.

**Effect:** every access token signed with the old secret becomes invalid
immediately, and every refresh cookie stops verifying. **All users are signed
out and must log in again.** No data is lost. Password hashes are unaffected
(bcrypt, unrelated to this secret).

```sh
make secret                     # generate a new 32-byte hex secret
# put it in the deployment's environment (secret manager / .env), then restart:
systemctl restart quorum        # or: podman restart quorum-app / kubectl rollout restart
```

Verify: `curl -s localhost:8080/healthz` returns `{"status":"ok"}`, and a fresh
login succeeds. Stale sessions will get 401 and be bounced to the login screen —
expected.

Optionally clear the now-useless refresh tokens:

```sql
UPDATE refresh_tokens SET revoked = TRUE WHERE revoked = FALSE;
```

---

## Recover a locked-out admin

### Case 1 — lost their authenticator and recovery codes (2FA lockout)

**When:** a user (worst case, the only superadmin) can enter their password but
cannot produce a TOTP code or a recovery code.

```sh
/opt/quorum/quorum -unlock-2fa admin@example.org
```

This clears their TOTP secret, the enabled flag, and all recovery codes, and
revokes their sessions. They can then sign in with their password alone and
re-enroll from **Settings → Two-factor authentication**.

It deliberately requires shell access to the deployment — that is the bar for
bypassing someone's second factor. It is not exposed over HTTP.

Verify: the user logs in without being prompted for a code; `SELECT totp_enabled
FROM users WHERE email = '…'` is `false`.

### Case 2 — forgot their password

Any other admin can reset it from **Settings → User accounts → Reset PW** (a
superadmin's password can only be reset by another superadmin). Self-service:
the user clicks *Forgot your password?* — this requires working SMTP.

### Case 3 — no usable admin account at all

Promote an existing account directly in the database:

```sql
UPDATE users SET role = 'superadmin' WHERE email = 'someone@example.org';
```

Then rotate that account's password through the UI. If there are no accounts at
all, the bootstrap endpoint is only open while the users table is empty — see
`make bootstrap`.

---

## Roll back a bad migration

**When:** a deploy applied a migration that broke something, and you need the
previous schema back.

Take a backup first — a rollback runs `DROP`/`ALTER` statements:

```sh
make backup
```

Roll back to the last-known-good version (e.g. back out `0016`, leaving `0015`
applied):

```sh
/opt/quorum/quorum -migrate-down 15
```

Then deploy the previous application version — **the old binary must match the
old schema**. Rolling the schema back under a new binary that expects the new
columns will fail at runtime.

Notes:

- Every migration's `.down.sql` is exercised in CI (full up → down-to-zero → up
  cycle against a real Postgres), so reversibility is verified, not assumed.
- `-migrate-down 0` unwinds everything. That empties the database; only do it on
  a scratch instance.
- The rollback takes the same advisory lock as the forward migration, so it is
  safe to run while other replicas are up — but they will be running against a
  schema they don't expect, so stop or roll them back too.
- Migrations apply automatically on startup; there is no auto-rollback.

Verify: `SELECT max(version) FROM schema_migrations;` shows the target version,
and the app starts cleanly.

---

## Restore from backup

See **[BACKUP.md](BACKUP.md)** for the full procedure. Short form:

```sh
scripts/backup.sh list
make restore FILE=backups/quorum-20260101T020000Z.pgdump   # prompts for confirmation
```

Prove a backup is restorable *before* you need it:

```sh
make backup-verify        # restores into a throwaway DB, checks it, drops it
```

---

## Fulfil a data request

**Portability / access:** the member (or an admin on their behalf) downloads
**Settings → Export my data** — a JSON file of their account, profile, dues, and
payments. Any authenticated user can export their own data, including
`restricted` accounts.

**Erasure ("right to be forgotten"):** superadmin, from the API:

```sh
curl -X POST "https://quorum.example.org/api/v1/members/<id>/erase?confirm=<Display%20Name>" \
  -H "Authorization: Bearer <token>"
```

This anonymizes the member in place: name becomes `Erased member <prefix>`,
email/phone/address/notes/metadata are nulled, status goes inactive, and the
linked login is unlinked with its sessions revoked. Invoices, payments,
attendance, and votes are **kept** — the ledger and the minutes stay consistent,
with no link to a natural person. It is irreversible, hence the `?confirm=`
gate. The audit log records who performed it.

**Retention:** audit entries are pruned nightly after
`QUORUM_AUDIT_RETENTION_DAYS` (default 365). Processed webhook events are kept
90 days; expired refresh, ballot, and password-reset tokens are pruned nightly.

---

## Investigate "who changed this record?"

**Audit log** → in the UI at **Audit log** (admin+), filterable by action,
resource, and date range. Every mutating request is recorded with the actor, the
resource type, and the affected row id — including creates, whose id is
recovered from the response.

For correlation with server logs, each request carries a `request_id` (returned
in the `X-Request-Id` header and present on every structured log line):

```sh
journalctl -u quorum | grep '"request_id":"<id>"'
```

---

## Check service health

| Endpoint | Meaning |
|---|---|
| `/healthz` | Liveness — the process is up |
| `/readyz` | Readiness — the database is reachable |
| `/metrics` | Prometheus exposition (requires `QUORUM_METRICS_TOKEN`) |

```sh
curl -s localhost:8080/readyz
curl -s -H "Authorization: Bearer $QUORUM_METRICS_TOKEN" localhost:8080/metrics | head
```

Worth alerting on: 5xx rate (`quorum_http_requests_total{status=~"5.."}`),
`quorum_http_panics_total` increasing, DB pool saturation
(`quorum_db_pool_acquired_conns` approaching `quorum_db_pool_max_conns`),
failed-login spikes, and the nightly job not running.

---

## Nightly job

Dues aging, recurring-invoice generation, escalating reminders, table pruning,
and the admin digest run once nightly at 02:00 local. Across replicas a Postgres
advisory lock elects a single leader, so exactly one instance runs it.

It's safe to let it run; to confirm it did, look for the aging/prune lines in the
log or check `last_reminder_at` on recently-overdue invoices. Email steps are
skipped entirely when SMTP is unconfigured.

---

## Scale to multiple replicas

Safe as-is: the nightly job is leader-elected, and migrations are advisory-locked
at startup.

Per-instance state that does **not** aggregate across replicas — move these to a
shared store or the ingress if you run more than one:

- the login/refresh **rate limiter** (in-process sliding window),
- the per-account **2FA failure throttle**,
- **metrics** (scrape each replica; aggregate in Prometheus).
