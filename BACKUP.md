# Backups & Disaster Recovery

Quorum keeps all durable state in one PostgreSQL database. Backing that up — and
**testing the restore** — is the whole disaster-recovery story. This document is
the runbook; the tool is [`scripts/backup.sh`](scripts/backup.sh) (wrapped by
`make backup` / `make backup-verify` / `make restore`).

> **Untested backups are not backups.** `scripts/backup.sh verify` restores a
> dump into a throwaway database and checks it, so run it regularly (e.g. weekly,
> and in CI). A backup you have never restored is a guess.

## What a backup contains

A single `pg_dump` in **custom format** (`-Fc`): compressed, and restorable
selectively or in parallel with `pg_restore`. It captures the entire schema and
all data — members, dues, meetings, motions, votes, budgets, FX rates,
notifications, the audit log, everything. It does **not** capture:

- `.env` secrets (`QUORUM_JWT_SECRET`, SMTP/webhook secrets) — store these in
  your secret manager separately. Losing `QUORUM_JWT_SECRET` invalidates all
  live sessions (users log in again) but loses no data.
- In-flight background work (queued notifications) — best-effort, not durable.

## Connection modes

`scripts/backup.sh` runs in one of two modes (set `QUORUM_BACKUP_MODE`):

| Mode | When | How it connects |
|------|------|-----------------|
| `podman` (default) | Local Podman stack | `podman exec` into the `quorum-db` container, so client tools match the server version |
| `url` | Production / CI / managed Postgres | Host `pg_dump`/`pg_restore` against `QUORUM_DATABASE_URL` (or `QUORUM_BACKUP_DATABASE_URL`). Requires postgres client tools on the host, matching the server's major version. |

## Encryption

Set `QUORUM_BACKUP_PASSPHRASE` (in the systemd unit's environment or your
cron's) and every dump is encrypted with AES-256 (`openssl enc -pbkdf2`,
200k iterations) into a `.pgdump.enc` file; `verify` and `restore` decrypt
transparently when the passphrase is set, and refuse clearly when it is not.
Store the passphrase in your secret manager, separately from the backups —
an encrypted backup without its passphrase is gone.

## Taking backups

```sh
make backup                 # dump to backups/, then prune old dumps
scripts/backup.sh list      # what's on disk
```

Backups are named `quorum-<UTC-timestamp>.pgdump` so they sort chronologically.
A dump is written to a `.partial` file and renamed only on success, so a crashed
dump never masquerades as a complete one.

**Retention:** the most recent `QUORUM_BACKUP_KEEP` dumps are kept (default 14);
older ones are pruned on each `create`. Tune it: `QUORUM_BACKUP_KEEP=30 make backup`.

### Scheduling

Pick one and point it at the deployment directory. Ship the dumps off-box
(object storage, another host) — a backup on the same disk as the database does
not survive that disk dying.

**cron:**
```
0 2 * * *  cd /opt/quorum && QUORUM_BACKUP_MODE=url QUORUM_DATABASE_URL=... scripts/backup.sh create >> /var/log/quorum-backup.log 2>&1
0 3 * * 0  cd /opt/quorum && QUORUM_BACKUP_MODE=url QUORUM_DATABASE_URL=... scripts/backup.sh verify  >> /var/log/quorum-backup.log 2>&1
```

**systemd timer** (`quorum-backup.timer` → `quorum-backup.service`):
```ini
[Timer]
OnCalendar=*-*-* 02:00:00
Persistent=true
```
```ini
[Service]
Type=oneshot
WorkingDirectory=/opt/quorum
Environment=QUORUM_BACKUP_MODE=url
EnvironmentFile=/opt/quorum/.env
ExecStart=/opt/quorum/scripts/backup.sh create
```

For a **managed Postgres** (RDS, Cloud SQL, Neon, etc.), prefer the provider's
automated snapshots + point-in-time recovery as the primary mechanism, and use
this tool for portable logical dumps and restore drills.

## Verifying a backup (do this routinely)

```sh
make backup-verify                    # verifies the latest backup
scripts/backup.sh verify backups/quorum-20260101T020000Z.pgdump
```

`verify` creates a scratch database, restores the dump into it, checks that
tables and the `schema_migrations` version came back, then drops the scratch DB.
It never touches the live database. A non-zero exit means the backup is bad —
alert on it.

## Restoring (disaster recovery)

**This overwrites the live database.** You will be prompted to type the database
name to confirm.

```sh
make restore FILE=backups/quorum-20260101T020000Z.pgdump
# or
scripts/backup.sh restore backups/quorum-20260101T020000Z.pgdump
```

The restore uses `pg_restore --clean --if-exists`, so it drops existing objects
and replaces them wholesale. Afterward it prints the restored schema version.

### Full recovery from scratch

1. Provision a new Postgres 16 instance and an empty `quorum` database.
2. Restore the newest good dump (`scripts/backup.sh restore …`, `url` mode against
   the new instance).
3. Bring up the app with the same `QUORUM_JWT_SECRET` (from your secret manager)
   pointed at the restored database. Migrations are idempotent and advisory-
   locked, so a normal startup against an already-current schema is a no-op.
4. Smoke-test: log in, check the dashboard counts and the audit log.

### Rolling back a bad migration

Migrations ship with `.down.sql` pairs, but Quorum applies only `.up` migrations
at startup — there is no auto-rollback. To undo one, restore the pre-deploy
backup (above), or apply the specific `NNNN_*.down.sql` by hand against the
database, lowest-risk first. Always take a fresh `make backup` immediately before
a migration deploy so this path exists.

## Recovery objectives

- **RPO** (max data loss): the backup interval — nightly dumps mean up to ~24h.
  For a tighter RPO, use a managed Postgres with continuous WAL archiving / PITR,
  and keep these logical dumps as the portable, test-restored safety net.
- **RTO** (time to restore): dominated by dump size; a `pg_restore` of a
  Quorum-sized database is minutes. Practice it so the real thing is routine.
