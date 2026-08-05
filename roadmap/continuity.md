# Plan: Organizational Continuity (the Bus Factor)

Status: **draft for discussion** — no code yet.

## The problem, stated honestly

Today one person (the operator/CTO) is the single point of failure for:
AWS account access, the GitHub repository, server SSH keys, the backup
passphrase, the domain registrar, SES, and — above all — *knowing how it
all fits together*. The org's data would survive (encrypted backups in S3,
code public on GitHub, documentation in the repo), but its **ability to
operate, recover, and adapt** would not. Continuity is partly a software
problem and partly an organizational one; this plan covers both and is
explicit about which is which. Per the project principle, everything
software-side is org-agnostic and configurable — no feature assumes who
the successor is or how the org is structured.

## What already exists (inventory, so we don't rebuild it)

- **Knowledge**: DEPLOY-EC2, RUNBOOK, UPGRADING/DOWNGRADING, BACKUP (full
  DR from a dead machine), ARCHITECTURE, EMAIL-SETUP, USER_MANUAL — a
  competent successor can learn the system from the repo alone.
- **Recoverability**: nightly encrypted backups with weekly restore-
  verification, off-box S3 sync, `quorum -verify-audit`, `-unlock-2fa`
  break-glass, migrations embedded in the binary.
- **Access hygiene**: two-superadmin guidance (manual §7), admin password
  resets, session revocation.
- **The gap**: all of it assumes someone already has the keys and knows
  the documents exist. Nothing *watches* for continuity decay, nothing
  packages the org-specific facts (which bucket? which domain? where is
  the passphrase?), and MY-DEPLOYMENT.md — the one file with those facts —
  lives untracked on the operator's laptop, which is itself a bus.

## Software features

### Phase E1 — the Continuity Pack (highest value, ~3–4 days)

An admin-exportable sealed ZIP, sibling of the CPA pack: everything a
successor needs that the public repo cannot carry, generated fresh so it
is never stale.

- `SUCCESSOR-README.md` — generated walkthrough: "you have inherited this
  system; here is what it is, where it runs, and your first five moves"
  (verify backups, gain server access, rotate secrets, check audit chain,
  read RUNBOOK).
- `org-configuration.csv/json` — settings, posting rules, chart, funds +
  signer policies, visibility groups, user roster with roles, channel
  list, integration status (SMTP configured? which webhooks live?).
- `infrastructure.md` — from a new allowlisted org-settings section the
  admin fills in ONCE (registrar, DNS host, cloud account owner email,
  bucket name, server address, where each secret custody lives — never
  secret VALUES, only locations and holders).
- `secret-custody.csv` — the custody registry (Phase E2) snapshot.
- Standard export controls: watermark, ZIP SHA-256 in header + EXPORT
  audit entry. Superadmin-only, because this is the org's skeleton key
  *map* (not the keys).

Delivery guidance in-doc: print one copy for the org's records after any
material change; store beside the backup passphrase's offline copy.

### Phase E2 — continuity health + custody registry (~1 week)

- **Secret custody registry** (new table, admin UI): one row per critical
  secret — name (backup passphrase, AWS root, registrar…), where it
  lives, who holds it (member links), last-verified date. Metadata only;
  values never enter the system. Each row supports a one-click
  **attestation** ("I verified this copy exists today") recorded with
  who/when — the same evidence pattern as purchase approvals.
- **Continuity checks on the dashboard** (admin section), all computed,
  none configurable-off silently:
  - superadmin count < 2 → warning ("bus risk: one superadmin")
  - newest backup age, newest restore-verify result (already in DB reach?
    surfaced via a small ops signal endpoint the timers write to)
  - custody attestations older than N days (setting, default 90)
  - DR-drill attestation older than N days (default 365)
  - TLS certificate expiry (server-side check of its own cert via the
    public endpoint)
- Nightly job emails admins when any check goes red (reuses the digest).

### Phase E3 — inactivity watchdog (~2–3 days, design care needed)

A dead-man's switch that **notifies, never grants**:

- Setting: `continuity_watch_days` (default 30) + designated **continuity
  contacts** (users). If **no superadmin authenticates** for that many
  days, the nightly job emails the contacts: "no superadmin activity for
  N days; if this is unexpected, here is the succession procedure"
  (pointing at the org's continuity pack and RUNBOOK Case 3).
- Explicitly rejected: automatic privilege escalation to a successor.
  Software cannot verify a death, and an auto-promotion path is a
  standing attack vector. Gaining superadmin without one remains what it
  is today: shell access to the server (RUNBOOK, "no usable admin"),
  which is exactly the bar it should be — possession of the
  infrastructure keys the org custody-plans in E2.

### Phase E4 — successor drill mode (~2 days, optional)

A guided "continuity drill" checklist page (admin): walks a second person
through read-only verification tasks (open the audit chain verify, list
backups, download the continuity pack, locate RUNBOOK) and records a
completion attestation. Cheap rehearsal, real evidence.

## Organizational checklist (no code can do these)

1. **Two superadmins**, today (manual §7.3). The E2 check will nag; do it
   before the software exists to nag.
2. **AWS**: root email → an org-controlled alias (not a personal inbox);
   MFA device custody recorded; create a second IAM admin user; billing
   contact = org treasurer.
3. **GitHub**: move the repo to an organization with two owners, or add a
   second owner-equivalent collaborator + a machine-user deploy key noted
   in the custody registry.
4. **Registrar/DNS**: second authorized contact; auto-renew on; expiry
   dates into the custody registry.
5. **Password manager**: org vault holding ALL of it (backup passphrase,
   AWS, registrar, SES SMTP creds, server key), with emergency-access /
   legacy-contact configured for two org officers.
6. **Sealed envelope** (or lawyer-held letter): where the vault is and
   who can open it. The continuity pack tells the successor *what*
   exists; the envelope gets them *in*.
7. **One DR drill with a second human** driving, operator watching only.
   The plan's cheapest, highest-value line item.

## Sequencing & the honest core

E1 → E2 → E3, with the organizational checklist started immediately (it
gates nothing and beats every feature here on value-per-hour). The honest
core: software can *package knowledge, watch for decay, and raise alarms*
— custody of trust itself is organizational. This plan keeps each on the
right side of that line.

## Open questions

1. Who are the two superadmins and the continuity contacts? (Names, not
   design inputs — E3 takes any users.)
2. Does the org want backup-timer results surfaced into the app (small
   ops-signal endpoint) or is journalctl-level visibility acceptable for
   E2's backup checks?
3. Should the continuity pack include an encrypted copy of
   MY-DEPLOYMENT.md-style operator notes (passphrase-protected, custody
   registered), or remain strictly non-secret? Default: non-secret.
