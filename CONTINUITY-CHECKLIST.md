# Continuity Setup, Step by Step

Every task needed so your organization survives losing its one technical
person. Do them in order; each says where you are and what you should see.
Budget about two hours total, plus one coffee with another human.
(Background and design reasoning: roadmap/continuity.md.)

## Part 0 — Get the new software running (once)

1. On your computer, in the project folder: `git push github main`.
2. Browser → your GitHub repo → **Actions** tab → wait for the green check.
3. On the server: `cd /opt/quorum && sudo ops/upgrade.sh`
   You see: `upgrade ok — /readyz is green`.
4. In your browser: hard refresh the app (**Ctrl+Shift+R**).

## Part 1 — A second superadmin (10 minutes, do this first)

Right now, if you lose your login, recovering the org requires shell access
to the server. A second superadmin turns that crisis into a two-click reset.

1. Decide WHO: a trusted officer (president, treasurer). Two people total
   is right; three is too many for the role that can erase history.
2. App → **Settings → Users**. If they already have an account, change
   their **role** to `superadmin` and confirm. If not, create the account
   first (role `superadmin`), hand them the initial password securely.
3. Have them log in, change their password, and **set up two-factor**
   (Account → security) — write their recovery codes into the org vault
   (Part 4), not a drawer.
4. Check it worked: **Settings → Organization settings** → the continuity
   health line should stop saying "only 1 superadmin".

## Part 2 — Fill in the org's continuity settings (15 minutes)

App → **Settings → Organization settings** (you must be an admin):

1. **Infrastructure facts** box — type where everything lives. Plain
   sentences; a stranger should understand them. Cover at least:
   - Domain registrar (where the domain is renewed) and the account email
   - DNS host (e.g. Route 53) and which cloud account
   - Cloud provider + the account's owner email
   - Where the server runs (provider, region)
   - The backup bucket's name
   - Where the password vault is (Part 4) and who can open it
2. **Inactivity watchdog**: set days to `30`, and put two officers' emails
   in **continuity contacts** (comma-separated). This is the tripwire: if
   no superadmin signs in for 30 days, those people get an email pointing
   at the succession procedure. (It sends email, so finish EMAIL-SETUP.md
   for it to actually fire.)
3. Click **Save org settings**. You see: "Org settings saved."

## Part 3 — Register secret custody (15 minutes)

Same page, **Continuity: secret custody & health** section. For each row:
what the secret is, WHERE a copy lives, WHO holds it. Never type the
secret itself — this is a map, not a keyring. Add at least these six:

| Name | Location (example) | Holder |
|---|---|---|
| Backup passphrase | Org vault + sealed envelope at attorney | Treasurer |
| Cloud account root login + MFA | Org vault; MFA phone in office safe | You |
| Domain registrar login | Org vault | You |
| Server SSH key | Org vault (file attachment) | You |
| Org password vault master access | Emergency access: president + treasurer | President |
| Email relay (SMTP) credentials | Org vault | You |

Then click **✓ attest** on each row you have personally verified exists
today. Re-attest when the health line nags (every 90 days by default).
Note: if you *edit* a row, its verification resets on purpose — a moved
copy is an unverified copy.

## Part 4 — The org password vault (30 minutes)

This is the single most important non-software step.

1. Pick a password manager with **organization/family plans and
   emergency access** (Bitwarden and 1Password both work; Bitwarden has a
   solid free org tier).
2. Create an **organization vault** (not your personal one) and move every
   credential from Part 3's table into it: backup passphrase, cloud login,
   registrar, SSH key file, SMTP credentials, the second superadmin's
   recovery codes.
3. Invite two officers and configure **emergency access** (Bitwarden:
   Settings → Emergency access; 1Password: family/org recovery) so they
   can request access if you go silent.
4. Update the custody rows in Part 3 to match reality, and attest them.

## Part 5 — Tighten the accounts only you control (30 minutes)

1. **Cloud (AWS)**: log in as root →
   - change the root email to an org-controlled alias (not your personal
     inbox alone — an alias two officers can receive),
   - confirm MFA is on and record WHERE the MFA device is (custody row),
   - IAM → create a second admin **user** for the other superadmin-human,
     with its own MFA, credentials into the vault,
   - Billing → set the billing contact to the treasurer's email.
2. **GitHub**: repo → Settings → Collaborators → add a second person with
   **Admin**. (Better long-term: create a GitHub *organization*, transfer
   the repo, two owners — do this version when you have 20 spare minutes.)
3. **Registrar (GoDaddy)**: turn **auto-renew ON** for the domain, add the
   org's card as payment, and add the account credentials to the vault.
   Note the domain's expiry date in a custody row's location text.

## Part 6 — The sealed envelope (15 minutes)

Paper survives every outage. Write one page by hand or print:

> "Quorum continuity. The organization's systems are documented in the
> continuity pack stored [WHERE]. All credentials are in [VAULT], which
> [NAMES] can access via emergency access. The server can be rebuilt from
> scratch using the public code repository plus the backups in [BUCKET] —
> the procedure is RUNBOOK.md, 'Disaster recovery'."

Seal it. Give it to the org's president or attorney. Log it as a custody
row ("Sealed continuity letter / attorney's office / President").

## Part 7 — Generate and place the Continuity Pack (5 minutes)

1. App → Settings → continuity section → **⬇ Continuity pack** (you must
   be superadmin). A ZIP downloads.
2. Open it once yourself: read SUCCESSOR-README.md as if you were the
   stranger. If anything makes you say "but they won't know X" — go put X
   into the infrastructure facts box and regenerate.
3. Print SUCCESSOR-README.md + infrastructure.md, staple, and store with
   the sealed envelope (or in the office records). Repeat after any big
   change (new server, new bucket, new registrar).

## Part 8 — The drill (one hour, with another human)

The cheapest, highest-value item on this list. Pick your second superadmin
and run this while you ONLY watch — hands in pockets:

1. They open the continuity pack and read the README aloud.
2. They log into the app and find: the audit chain **Verify** button, the
   Reports page, the custody registry.
3. They SSH (or SSM) into the server using vault credentials and run:
   `cd /opt/quorum && make backup-list` — newest backup under 25h old.
4. They open the S3 bucket in the AWS console and see the same files.
5. Optional gold star: on a throwaway EC2 instance, they follow
   RUNBOOK.md's disaster recovery to restore a backup. If they complete
   this, your bus factor is genuinely 2.
6. Buy them the coffee. Put a custody row in the registry: "DR drill" /
   "completed <date> by <name>" / and attest it — the health check treats
   attestation age as drill freshness.

## Done — how you know it stuck

**Settings → Organization settings** shows: **✓ continuity checks green.**
From then on, the software nags when anything decays: a lone superadmin,
stale attestations, an expiring certificate, a silent month. The nagging
is the feature — continuity fails quietly, and now it can't.
