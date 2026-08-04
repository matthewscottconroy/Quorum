# Architecture, Data Flow & Network Reference

Diagrams for the recommended single-instance deployment (see
[DEPLOY-EC2.md](DEPLOY-EC2.md)). GitHub renders these natively; they use
`example.org` as the placeholder domain.

## 1. System architecture — what runs where

```mermaid
flowchart TB
    subgraph internet["Internet"]
        browser["Member's browser"]
        le["Let's Encrypt"]
        smtp["SMTP relay<br/>(e.g. Amazon SES)"]
        s3["S3 bucket<br/>versioned, encrypted"]
        gh["GitHub repo<br/>(public source of truth)"]
    end

    subgraph ec2["EC2 instance — Rocky Linux, encrypted gp3 EBS"]
        caddy["Caddy :443/:80<br/>TLS termination, auto-certs"]
        app["quorum binary :8080 loopback<br/>systemd, User=quorum, hardened"]
        subgraph pod["podman quadlet (rootful manager)"]
            pg["postgres:16-alpine<br/>runs as non-root uid<br/>:5432 loopback"]
        end
        vol[("/var/lib/quorum/pgdata<br/>bind mount on host disk")]
        bk[("/opt/quorum/backups/<br/>AES-256 .pgdump.enc + manifests")]
        timers["systemd timers:<br/>backup 02:00 daily<br/>restore-verify Sun 03:00<br/>audit-chain check 04:00 daily"]
        cron["cron.daily: aws s3 sync"]
    end

    browser -->|HTTPS| caddy
    caddy -->|"HTTP + X-Real-IP<br/>(inbound XFF stripped)"| app
    app -->|"postgres wire, localhost"| pg
    pg --- vol
    caddy <-->|ACME :80/:443| le
    app -->|"STARTTLS :587<br/>reset + notification mail"| smtp
    timers -->|pg_dump inside container| pg
    timers --> bk
    cron -->|IAM instance role, no keys| s3
    bk --> cron
    gh -.->|git clone / deploy.sh| app
```

Key invariants:

- Only Caddy faces the internet. The app and database bind loopback only.
- The app process writes nothing to disk (`ProtectSystem=strict`); backups
  run in separate units.
- The database's state lives on the host bind mount, not in the container
  image — images are replaceable software, the volume is the data.

## 2. Request data flow — one authenticated API call

```mermaid
sequenceDiagram
    participant B as Browser
    participant C as Caddy
    participant M as Middleware chain
    participant H as Handler
    participant R as Repo layer
    participant P as Postgres

    B->>C: HTTPS request + JWT bearer token
    C->>M: HTTP on loopback, X-Real-IP set from socket
    M->>M: security headers, body cap 1 MiB, rate limit
    M->>M: verify JWT signature and expiry
    M->>M: RBAC gate (restricted < member < officer < admin < superadmin)
    M->>H: authenticated request + audit-detail holder
    H->>R: typed call (visibility filters applied here)
    R->>P: parameterized SQL, never string-built
    P-->>R: rows
    R-->>H: models
    H-->>B: JSON
    Note over M,P: Mutations also append an audit row whose hash chains<br/>to the previous row (trigger-computed, tamper-evident).<br/>Denied requests are audited too.
```

Trust boundaries worth remembering:

- **TLS ends at Caddy**; loopback traffic is plaintext but never leaves the
  kernel of the one machine.
- **Identity is the JWT**, minted only by `/auth/login` (+ optional 2FA) and
  refreshed by rotating refresh tokens; token age doubles as the idle-logout
  clock, enforced server-side.
- **Resource visibility** is enforced in the repo layer (groups), so a
  hidden record 404s identically to a missing one.

## 3. Backup & recovery data flow

```mermaid
flowchart LR
    pg["postgres container<br/>quorum-db"] -->|"02:00 pg_dump -Fc<br/>exec'd inside container"| enc["AES-256 encrypt<br/>QUORUM_BACKUP_PASSPHRASE"]
    enc --> disk[("backups/*.pgdump.enc<br/>+ .manifest: sha256, schema version,<br/>audit chain head")]
    disk -->|"cron.daily<br/>aws s3 sync"| s3[("S3, versioned<br/>lifecycle prunes")]
    disk -->|"Sun 03:00"| verify["restore into throwaway DB<br/>sanity-checked, never touches live"]
    s3 -.->|disaster recovery| new["fresh instance<br/>scripts/backup.sh restore"]
```

Chain of custody: each manifest records the audit-chain head at dump time,
so a restored database can be checked for tampering against the last known
head (`quorum -verify-audit`).

## 4. Network diagram — who may talk to whom

```mermaid
flowchart TB
    subgraph world["0.0.0.0/0"]
        anyone["Anyone"]
        admin_ip["Your IP (or SSM session)"]
    end

    subgraph sg["AWS security group: quorum-sg"]
        p443["443 HTTPS — open"]
        p80["80 HTTP — open (redirect + ACME only)"]
        p22["22 SSH — your IP only"]
    end

    subgraph host["Host (+ firewalld where enabled)"]
        caddy2["caddy :443 :80"]
        subgraph loop["127.0.0.1 only — unreachable from any network"]
            app2["quorum :8080"]
            pg2["postgres :5432"]
        end
    end

    subgraph egress["Outbound (unrestricted)"]
        e1["Let's Encrypt ACME"]
        e2["SMTP relay :587"]
        e3["S3 sync (instance role)"]
        e4["dnf + container registries (updates)"]
    end

    anyone --> p443 --> caddy2
    anyone --> p80 --> caddy2
    admin_ip --> p22
    caddy2 --> app2
    app2 --> pg2
    host --> egress
```

Never open: 8080 (app), 5432 (database). If a future need says otherwise,
the answer is a VPN or SSH tunnel, not a security-group rule.
