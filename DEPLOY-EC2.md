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

**Amazon Linux?** Amazon Linux 2023 is a fine choice *unless you want
podman*: current PostgreSQL packages, dnf, long AWS support, no license cost.
Its honest caveats: the repos carry **no podman and no certbot** (AWS ships
Docker instead of podman, and sideloading podman onto AL2023 is a build-from-
source maintenance burden — treat it as unavailable). The certbot gap vanishes
with Caddy; the podman gap does not. So:

- **Want podman → Rocky Linux 9.** Podman is the RHEL-native container stack:
  `dnf install podman`, quadlets (systemd-native container units), SELinux
  integration, rootless by default. Official free AMIs from the Rocky
  Enterprise Software Foundation. Ubuntu 24.04 LTS is the runner-up
  (`apt install podman`, v4.9).
- **Don't need podman → AL2023 + Caddy** and skip the ceremony.

Either way the app itself deploys the same: one static binary under systemd.

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

## Rocky Linux 9 variant (the podman path)

Everything above holds; only the platform steps differ. Deltas from the
AL2023 walkthrough:

```sh
# 0. Rocky quirks AL2023 doesn't have
sudo dnf install -y https://s3.amazonaws.com/ec2-downloads-windows/SSMAgent/latest/linux_amd64/amazon-ssm-agent.rpm
sudo systemctl enable --now amazon-ssm-agent        # SSM agent is NOT preinstalled on Rocky AMIs
sudo firewall-cmd --permanent --add-service=http --add-service=https && sudo firewall-cmd --reload
#   firewalld is on by default — the security group alone is not enough here

# 1. Packages
sudo dnf install -y dnf-automatic podman epel-release
sudo systemctl enable --now dnf-automatic.timer
sudo dnf install -y podman-compose               # optional, from EPEL, for the dev-style compose stack

# 2. PostgreSQL 16 comes from the AppStream module, not a versioned package
sudo dnf module enable -y postgresql:16
sudo dnf install -y postgresql-server
sudo postgresql-setup --initdb && sudo systemctl enable --now postgresql

# 6. Caddy from the project's COPR (per the Caddy docs)
sudo dnf install -y 'dnf-command(copr)'
sudo dnf copr enable -y @caddy/caddy && sudo dnf install -y caddy
sudo cp ops/Caddyfile /etc/caddy/Caddyfile && sudo systemctl enable --now caddy
```

SELinux is enforcing on Rocky: the quorum binary under systemd and Caddy both
run fine as-is, but if you choose **Apache or nginx** instead, allow the proxy
to reach 127.0.0.1:8080 with `sudo setsebool -P httpd_can_network_connect 1`.
Prefer building the binary off-box (`CGO_ENABLED=0 go build`) — it's fully
static, and distro Go packages can lag the version this module requires.

Using **Apache instead of Caddy**: `dnf install httpd mod_ssl`, drop
`ops/apache-quorum.conf.example` into `/etc/httpd/conf.d/quorum.conf`, and get
certificates with certbot (`pip install certbot` on AL2023 — its repos don't
package it — or use Ubuntu where `apt install certbot python3-certbot-apache`
just works).

## Containerized PostgreSQL (podman quadlet)

Prefer the database in a container? On the podman path (Rocky) that's a
first-class option, and it does **not** mean baking data into an image: the
container image is only software, and the data directory is a bind mount on
the host (`/var/lib/quorum/pgdata`, sitting on the encrypted EBS volume).
Data survives image pulls, container re-creation, and reboots — no golden
image anywhere.

Instead of step 4's `dnf` install, use `ops/quadlet/quorum-postgres.container`
(install instructions in its header). It's a **quadlet** — podman's
systemd-native container unit — so the database is managed like any other
service (`systemctl status quorum-postgres`), starts at boot, and reports
"started" only once `pg_isready` passes, which makes the app unit's
`After=quorum-postgres.service` ordering wait for a genuinely ready database.

Details that keep the rest of this guide working:

- The container is named `quorum-db`, which is exactly what
  `scripts/backup.sh`'s podman mode expects. Either flip the two backup units
  to `Environment=QUORUM_BACKUP_MODE=podman` (pg_dump runs inside the
  container, client always matches server — recommended here) or keep `url`
  mode and install host client tools (`dnf install postgresql`).
- Port 5432 is published on **loopback only**; `QUORUM_DATABASE_URL` in
  `.env` is unchanged.
- `AutoUpdate=registry` + `systemctl enable --now podman-auto-update.timer`
  pulls 16.x minor/security releases automatically — the container-world
  equivalent of dnf-automatic. Major upgrades (16 → 17) are never automatic;
  those need `pg_upgrade` or dump/restore, same as any Postgres.
- Trade-off vs the distro package: the quadlet pins the exact server version
  and isolates it; the package gets patched by dnf-automatic with everything
  else. Both are sound — pick one and don't run both.

## Upgrading the running service

An upgrade is: build the static binary, swap it, restart. Migrations are
embedded in the binary and apply themselves at startup under a Postgres
advisory lock, so **a deploy is the migration** — there is no separate step.
`ops/deploy.sh user@host` does the whole thing from your dev machine or CI:
build, upload, atomic swap (keeping the previous binary as `quorum.prev`),
restart, poll `/readyz` for 30 s, and roll the binary back automatically if
the new one never becomes ready. The restart blip is a second or two;
`Restart=always` in the unit covers crashes thereafter. Before a release that
changes the schema, take a backup (`make backup`); `quorum -migrate-down N`
exists for a deliberate schema rollback, but restoring a backup is usually the
saner path.

## CI/CD without GitHub

`.github/workflows/ci.yml` is written in GitHub Actions syntax, which is also
what **Forgejo/Gitea Actions execute** — so the pipeline you already have runs
nearly unchanged on a self-hosted forge:

1. Install **Forgejo** (community fork of Gitea; a single Go binary + SQLite,
   happy in ~300 MB RAM) on the instance that already holds the bare repo, and
   import the repo:

   ```sh
   sudo useradd --system --create-home git
   # download the forgejo binary for linux-amd64 from https://forgejo.org/download/
   sudo install -m 755 forgejo-*-linux-amd64 /usr/local/bin/forgejo
   # forgejo's docs ship a systemd unit; enable it, open the web setup,
   # choose SQLite, then: New Migration -> Local path -> your bare repo
   ```

   Make sure Actions are on in `app.ini`: `[actions] ENABLED = true`.

2. Add one **forgejo-runner**. The CI's `services: postgres` container means
   the runner host needs docker or a podman socket:

   ```sh
   sudo systemctl enable --now podman.socket
   # download forgejo-runner, then register it against your forge:
   forgejo-runner register   # URL + registration token from the forge admin UI
   DOCKER_HOST=unix:///run/podman/podman.sock forgejo-runner daemon
   ```

3. Add a deploy job gated on CI success that runs `ops/deploy.sh` against the
   app instance over SSH (private key stored as a forge secret):

   ```yaml
   deploy:
     needs: [backend, web]        # job names from ci.yml
     if: github.ref == 'refs/heads/main'
     runs-on: ubuntu-latest
     steps:
       - uses: actions/checkout@v4
       - uses: actions/setup-go@v5
         with: { go-version: '1.23' }
       - run: |
           mkdir -p ~/.ssh && echo "${{ secrets.DEPLOY_KEY }}" > ~/.ssh/id_ed25519
           chmod 600 ~/.ssh/id_ed25519
           echo "${{ secrets.KNOWN_HOSTS }}" > ~/.ssh/known_hosts
           ops/deploy.sh deploy@your-app-host
   ```

### Publishing on GitHub: two shapes for public + private

Both compose cleanly with the forge pipeline above; pick by where you want
the source of truth.

**Push-mirror (private forge is upstream).** You push to the forge; it
mirror-pushes to a public GitHub repo (repo Settings → Mirror). Public
visibility, zero pipeline dependency on GitHub, deploys fire the moment you
push. Prefer this if the private copy is where development happens.

**Pull-mirror (public GitHub is upstream).** GitHub is the source of truth;
the forge holds a *pull mirror* that syncs on an interval and drives CI +
deploy from its copy of `main`. What changes versus the push shape:

- **Deploy lag**: the default mirror interval is hours. Shorten it in the
  mirror settings, click "Synchronize now", or add a GitHub webhook that pokes
  the forge for near-instant sync.
- **Keep the fork at zero private commits.** Everything machine-specific
  already lives outside git (`.env`, `.db.env`, forge secrets), so the
  private copy can be a pure mirror — no drift, no rebasing, upgrades are
  just "sync happened". If you must carry private patches, keep them on one
  private branch continually rebased onto upstream `main`, and treat every
  extra private commit as debt.
- **Public CI for free**: `ci.yml` runs unchanged on GitHub's free public
  runners too, so contributors get checks without touching your forge.

Before the repo goes public, once: confirm `.env*` never entered history
(`git log --all -- .env` is empty here), run a proper secret scanner
(`gitleaks git .`) rather than trusting eyeballs, and note the license
implications — AGPL-3.0 means anyone offering a modified Quorum as a network
service must publish their changes, which protects the project; your own
private, unpublished deployment config is unaffected.

**GitLab CE** works too but wants 4 GB+ RAM for itself and monthly care —
oversized for a small team. The zero-new-software alternative: keep the bare
repo and put `go test ./... && ops/deploy.sh …` in a `post-receive` hook;
crude, but honest CI/CD for one person. Publishing a public GitHub mirror
later composes fine with either: the forge stays the private source of truth
and mirror-pushes to GitHub.

**Kubernetes: no.** `deploy/` carries a complete k8s/Helm/Tekton/Argo CD kit
for the day this outgrows one box, but on a single instance Kubernetes adds a
control plane to operate and nothing you don't already get from systemd +
`Restart=always` + this deploy script. Revisit only at multi-node scale.

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
