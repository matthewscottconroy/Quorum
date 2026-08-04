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

## Upgrade the application (single instance)

**When:** deploying a new version of the code to the systemd deployment
described in [DEPLOY-EC2.md](DEPLOY-EC2.md).

**On the server** (the usual path — pulls from GitHub, no local Go needed):

```sh
sudo ops/upgrade.sh                    # newest origin/main; or: sudo ops/upgrade.sh <tag|sha>
```

**Or from a dev machine / CI** with Go installed, shipping your local checkout:

```sh
ops/deploy.sh deploy@app-host          # remote dir defaults to /opt/quorum
```

Full walkthrough with expected output: [UPGRADING.md](UPGRADING.md).

This builds the static binary, uploads it, swaps it atomically (keeping the
previous binary as `quorum.prev`), restarts `quorum.service`, and polls
`/readyz` for 30 seconds. If the new binary never becomes ready it prints the
journal tail and **rolls back to the previous binary automatically**.

Migrations are embedded and apply themselves at startup under an advisory
lock — a deploy *is* the migration. If the release changes the schema, take a
`make backup` first; a binary rollback after a schema change may then also
need "Roll back a bad migration" below, because the old binary must match the
old schema.

User-visible effect: one restart blip of a second or two. Active sessions
survive (auth is stateless JWT + refresh tokens in the database).

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
recovered from the response. Denied attempts (`DENIED(403) …`), failed logins
against real accounts, and failed 2FA codes are recorded too — the
insider-threat signals.

The log is hash-chained and append-only ([COMPLIANCE.md](COMPLIANCE.md)).
Check its integrity any time:

```sh
/opt/quorum/quorum -verify-audit     # exit 0 intact, 1 broken (cron-able; ops/systemd/ has a daily timer)
```

A broken chain is a drop-everything incident: preserve database backups
immediately (they contain the untampered history and its head hashes) before
touching anything else.

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

---

## Onboard a new user

**When:** someone joins the organization. There is no self-signup by design —
an admin creates every account.

Your part (as admin, in the UI):

1. **Members → Add member** — create their member record first (name, email,
   tier). This is the organizational identity: dues, attendance, votes.
2. **Settings → Users → Add user** — create the login: email, initial
   password, role (see the ladder below), and **link it to the member record**
   from step 1. Unlinked accounts see a "not linked yet" banner and almost
   nothing else.
3. Hand them the initial password over a channel you already trust (in
   person, phone, existing chat — not a sticky note on the internet).

Their part:

4. Sign in, change the password (Account → password), and set up two-factor
   (Account → security) — encourage this at onboarding, it's painless then
   and a chore later.
5. If SMTP is configured they can skip your handed password ceremony:
   create the account with any strong throwaway, tell them to click
   **Forgot password** immediately, and they set their own from the email.

Role ladder (each includes everything below it):

| Role | Grants |
|---|---|
| `restricted` | own member record only (self-service portal) |
| `member` | read the shared org: directory, meetings, resources, dashboard |
| `officer` | operate: members, dues, meetings/minutes, resources, boards |
| `admin` | govern: users, groups, audit log, exports, settings |
| `superadmin` | destroy: delete users, erase members (right-to-erasure) |

Give the lowest role that works; promote later in **Settings → Users** (one
PATCH, takes effect on their next token refresh, ~1 minute).

---

## Lock out or offboard a user

**When:** someone leaves, a device is stolen, or an account looks compromised.
There is no "disabled" flag — lockout is done with the three controls that
exist, in escalating order:

**Soft lock (reversible, 1 minute):**

1. **Settings → Users** → change their role to `restricted` — they now see
   only their own record.
2. **Reset their password** (same screen) to a value you don't share — their
   credentials stop working, and the reset revokes refresh tokens so open
   sessions die at the next refresh (within minutes, bounded by the idle
   timeout).

**Departure (the normal case):** do the soft lock, and mark their member
record appropriately (status change or soft-delete under Members — this keeps
their dues/vote history, which the org's records need).

**Full removal (superadmin, irreversible):**

3. Delete the user account (Settings → Users → delete; superadmin only).
4. If they exercise the right to erasure: **Members → Erase** strips personal
   data in place while keeping financial/governance rows intact. Read the
   confirmation carefully; there is no undo.

Audit note: all of these actions are themselves recorded in the audit log —
who locked whom, when.

---

## Restrict who can see what

Three independent mechanisms, from coarse to fine:

- **Roles** (above) gate *actions* — who can operate vs read vs self-serve.
- **Visibility groups** (Settings → Visibility groups) gate *library
  resources*: a resource tagged with groups is visible only to members of
  those groups (officers+ always see everything; an untagged resource is
  visible to all members). Hidden means *invisible* — a filtered-out member
  gets the same 404 as for a nonexistent document.
- **Ownership scoping** protects records like member details and dues:
  `restricted` users reach only their own.

Litmus test after changing groups: log in as (or shoulder-surf) an affected
member and confirm the resource list looks right. Deleting a group **widens**
visibility — resources restricted only by it become visible to all members;
the UI warns, believe it.

---

## Backup management (the routine)

Everything is automated; your job is a monthly two-minute audit that the
automation is real:

| What | When | Prove it happened |
|---|---|---|
| Encrypted dump to `backups/` | daily 02:00 | `make backup-list` shows today |
| Sync to S3 | daily (cron) | S3 console shows today's file |
| Restore-verify into scratch DB | Sun 03:00 | `journalctl -u quorum-backup-verify -n 20` says OK |
| Audit-chain verify | daily 04:00 | `journalctl -u quorum-verify-audit -n 5` |
| Prune old local dumps | with each backup | list stays ≤ retention count |

Monthly, run one manual `make backup-verify` and watch it succeed with your
own eyes. Quarterly, confirm the backup passphrase copy outside the server
still exists (password manager + offline copy). An encrypted backup without
the passphrase is a paperweight — this is the single most important line in
this runbook.

---

## Disaster recovery: the machine is gone

**When:** the instance is unrecoverable (deleted, region incident, corrupted).
Full detail: [BACKUP.md](BACKUP.md). The shape, so future-you doesn't panic:

1. Launch a fresh instance per [DEPLOY-EC2.md](DEPLOY-EC2.md), through the
   database step.
2. Pull the newest dump from S3: `aws s3 cp s3://YOUR-BUCKET/quorum-backups/<newest> backups/`.
3. `QUORUM_BACKUP_PASSPHRASE=... scripts/backup.sh restore backups/<file>` —
   decrypts and restores.
4. Verify integrity of the restored history: `./quorum -verify-audit` and
   compare the chain head against the manifest.
5. Start the app, point DNS at the new Elastic IP, wait for Caddy to fetch a
   certificate. Log in; check the audit log's most recent entries look sane.

What you need for this to work, none of it on the dead server: the S3 bucket,
the backup passphrase, your domain's DNS control, and this repository.
Practice once before you need it — a DR drill on a throwaway instance turns a
crisis into a checklist.

---

## Suspected breach: forensics & response

**When:** something smells wrong — an unexpected admin action, a member
reports activity they didn't do, an export nobody remembers.

**First, preserve; don't reboot, don't "clean up":**

1. Snapshot the EBS volume (Console → EC2 → Volumes → Create snapshot) —
   frozen evidence, timestamped.
2. Export the audit evidence now, before anything else changes:
   `/audit/export.csv` from the UI (Audit page), or copy `backups/` +
   the newest dump off-box.

**Read the record — this is what the audit chain is for:**

3. Verify the chain: Audit page → Verify, or `./quorum -verify-audit`.
   *Intact* means the log itself is trustworthy; *broken* tells you where
   history was altered — everything before the break is still evidence.
4. Hunt in the Audit page (admins): filter `DENIED` (repeated 401/403 =
   someone probing), `EXPORT` (what left, who, when — PDFs carry their
   SHA-256 in the entry), `auth.` events (logins, resets, 2FA changes,
   session revocations), and any writes by the suspect account.
5. Correlate with transport logs: `journalctl -u quorum` (structured, with
   request IDs and client IPs — X-Real-IP is trustworthy because the proxy
   strips inbound forgeries) and `journalctl -u caddy` for the raw HTTP line.

**Contain:**

6. Lock the suspect account (see "Lock out or offboard", above).
7. Rotate `QUORUM_JWT_SECRET` (see its section) — this instantly voids every
   token in existence; all users re-login. Rotate the DB password and backup
   passphrase if server compromise is plausible, not just account compromise.
8. Review **Settings → Users** for accounts you didn't create, and the
   member list for records you don't recognize.

**Afterwards:** write down the timeline while it's fresh; the evidence CSV +
`ops/verify-audit-export.py` lets a third party verify your record without
trusting your server.

---

## Litmus tests: is everything actually fine?

**The 30-second daily glance** (or whenever you're curious):

```sh
systemctl is-active quorum quorum-postgres caddy   # three lines of "active"
curl -s localhost:8080/readyz                       # {"status":"ready"}
```

**The 5-minute weekly pass:**

```sh
make backup-list                                    # newest dump is < 25h old
journalctl -u quorum-backup-verify -n 3 --no-pager  # last Sunday: success
journalctl -u quorum-verify-audit -n 3 --no-pager   # chain: intact
journalctl -u quorum --since -7d | grep -ci panic   # 0
sudo dnf needs-restarting -r; echo "reboot-needed: $?"  # 1 = reboot when convenient
```

Plus, from a browser *off* the server: the login page loads with a valid
padlock (Caddy renews certificates ~30 days early; a padlock warning means
renewal has been failing for weeks — check `journalctl -u caddy`).

**The quarterly hour:** DR drill on a throwaway instance (above), passphrase
custody check, user list review, and skim `PRODUCTION_READINESS.md` for
anything newly relevant.
