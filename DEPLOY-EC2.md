# Deploying Quorum on a single EC2 instance

The whole system is one Go binary (API + embedded web UI on :8080), PostgreSQL,
a reverse proxy for TLS, and the systemd units in `ops/`. A small org runs
comfortably on one box.

## The three questions, answered

**Do I need a reverse proxy / should it be Apache?** You need *a* reverse
proxy — the app deliberately does not terminate TLS. Apache works fine
(config: `ops/apache-quorum.conf.example`), and if you already know it, use
it. If you don't have a preference, **Caddy is the least-friction choice on
EC2**: it's a single static binary that gets and renews Let's Encrypt
certificates automatically (`ops/Caddyfile` is the entire config). nginx is
the middle path (`ops/nginx-quorum.conf.example`). Whatever you pick, three
rules from this codebase:

1. The app listens on `127.0.0.1:8080` only — never exposed directly.
2. The proxy must **set `X-Real-IP` from the socket address and drop inbound
   `X-Forwarded-For`** (the example configs do this; Apache in particular
   *appends* to inbound XFF, which would let clients spoof their rate-limit
   key). Then set `QUORUM_TRUST_PROXY_HEADERS=true`.
3. Don't override response headers — the app sets its own CSP/security set.

**Amazon Linux?** Amazon Linux 2023 is a fine choice: current PostgreSQL
packages, dnf, long AWS support, no license cost. Two honest caveats: its
repos carry **no certbot and no podman** (Ubuntu 24.04 LTS is the better pick
if you want either from packages). Both caveats vanish if you use Caddy —
which needs neither. Recommendation: **AL2023 + Caddy**, or Ubuntu LTS if your
team lives in the Debian world. Either works; don't overthink it.

**What gets installed?** See the walkthrough below. The full list on AL2023:
`postgresql16-server`, the Caddy binary (or `httpd`/`nginx`), the `quorum`
binary, the `ops/systemd/` units, and `dnf-automatic` for security updates.

## Instance shape

- **t3.small** (2 GB) is plenty; **t3.micro** works for a small org. The app
  idles at tens of MB; PostgreSQL takes the rest.
- **gp3 EBS volume, encrypted** — check the box at launch. This is your
  database **encryption at rest** (see SECURITY.md), so do not skip it.
- Security group: inbound **443 and 80 only** (80 just for the TLS redirect /
  ACME), SSH restricted to your IP or via SSM Session Manager (better: no SSH
  port at all). **Never** open 8080 or 5432.
- An **instance role** with write access to one S3 bucket, for off-box backups.

## Walkthrough (Amazon Linux 2023 + Caddy)

```sh
# 1. Packages
sudo dnf install -y postgresql16-server postgresql16 dnf-automatic git
sudo systemctl enable --now dnf-automatic.timer          # unattended security updates

# 2. PostgreSQL (local, listens on localhost by default — leave it that way)
sudo postgresql-setup --initdb
sudo systemctl enable --now postgresql
sudo -u postgres createuser quorum
sudo -u postgres createdb -O quorum quorum
sudo -u postgres psql -c "ALTER USER quorum PASSWORD '<generated>'"

# 3. App user + directory
sudo useradd --system --home /opt/quorum quorum
sudo git clone <your-remote> /opt/quorum && cd /opt/quorum
# build on the box (dnf install golang) or copy a CI-built linux/amd64 binary in
go build -o quorum ./cmd/quorum

# 4. Configuration
cp .env.example .env && vi .env
#   QUORUM_DATABASE_URL=postgres://quorum:<pw>@localhost:5432/quorum?sslmode=disable   # same host: fine
#   QUORUM_JWT_SECRET=$(make secret)
#   QUORUM_BASE_URL=https://quorum.example.org        # drives Secure cookies + email links
#   QUORUM_TRUST_PROXY_HEADERS=true                   # proxy sets X-Real-IP (see configs)
#   QUORUM_BACKUP_PASSPHRASE=<from your secret manager>
#   QUORUM_METRICS_TOKEN=<random>                     # if you scrape /metrics
#   plus SMTP settings — password reset and notices need them
sudo chown -R quorum:quorum /opt/quorum && sudo chmod 600 .env

# 5. Services: the app + the backup/verify/audit timers
sudo cp ops/systemd/quorum.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now quorum
sudo make backups-install                              # nightly backup, weekly restore-verify, daily chain-verify
curl -s localhost:8080/readyz                          # {"status":"ready"}

# 6. Caddy (TLS): single binary, auto-certificates
#    https://caddyserver.com/download → /usr/local/bin/caddy (+ its systemd unit from the docs)
sudo cp ops/Caddyfile /etc/caddy/Caddyfile             # set your domain first
sudo systemctl enable --now caddy

# 7. Ship backups off-box (a backup on the database's own disk is not a backup)
#    e.g. drop this in /etc/cron.daily/quorum-backup-sync, using the instance role:
#      aws s3 sync /opt/quorum/backups/ s3://YOUR-BUCKET/quorum-backups/
#    Turn on S3 versioning + a lifecycle rule; the bucket is encrypted by default.

# 8. First login
make bootstrap                                         # creates the first superadmin, once
# then verify the bootstrap endpoint is closed and work through
# PRODUCTION_READINESS.md's pre-deploy checklist.
```

Using **Apache instead of Caddy**: `dnf install httpd mod_ssl`, drop
`ops/apache-quorum.conf.example` into `/etc/httpd/conf.d/quorum.conf`, and get
certificates with certbot (`pip install certbot` on AL2023 — its repos don't
package it — or use Ubuntu where `apt install certbot python3-certbot-apache`
just works).

## Local PostgreSQL vs RDS

Local PostgreSQL on the instance is a sound starting point *because this repo
ships the discipline RDS would otherwise provide*: tested backups with
restore-verification, retention, encryption, manifests anchored to the audit
chain, and a daily chain check — all installed by `make backups-install`.
Move to RDS when you want point-in-time recovery, or multi-instance app
servers. If you do: `sslmode=require` in the URL (the app warns loudly at
startup if a remote database is configured without TLS), keep using
`scripts/backup.sh` in `url` mode for portable, verifiable logical dumps.

## What the app already handles (don't duplicate in the proxy)

Security headers (CSP, frame-deny, nosniff, permissions policy), request body
caps (1 MiB), rate limiting, session idle logout, HTTP timeouts, structured
logs with request IDs, `/healthz`–`/readyz` probes, and Prometheus metrics
behind `QUORUM_METRICS_TOKEN`.
