# Quorum

Quorum is a self-hosted web application for managing organizational operations: dues collection, meeting notes, strategic plans, contacts, and resources.

> **New to Quorum?** The [User & Operations Manual](USER_MANUAL.md) is a task- and role-based guide for every user (members, officers, admins) and for the operator who deploys the system. It exports cleanly to PDF (see its Appendix G).


## Quick start

**With Podman (recommended):**

```sh
# 1. Clone and enter the repo
cd quorum

# 2. Copy environment template
cp .env.example .env
# Edit .env — set QUORUM_DATABASE_URL and QUORUM_JWT_SECRET at minimum

# 3. Generate a JWT secret
make secret   # prints 64 random hex chars; paste into .env

# 4. Start the database and app with Podman Compose
make pod-up

# 5. Create the first admin user (run once)
make bootstrap
```

**With Docker Compose:**

```sh
cp .env.example .env && make secret  # fill in .env
make docker-up
make bootstrap
```

The app is now available at `http://localhost:8080`.

---

## Local development (without Docker)

Requires: Go 1.25+, PostgreSQL 16.

```sh
# Start a local Postgres database separately, then:
make dev          # reads .env, runs go run ./cmd/quorum
```

`make dev` reads `QUORUM_DATABASE_URL` and `QUORUM_JWT_SECRET` directly from `.env` using shell substitution so you don't need to source the file first.

---

## Configuration reference

All settings are read from environment variables. Copy `.env.example` to `.env` and fill in values.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `QUORUM_DATABASE_URL` | yes | — | PostgreSQL DSN, e.g. `postgres://user:pass@host/db?sslmode=disable` |
| `QUORUM_JWT_SECRET` | yes | — | HS256 signing key. Generate with `make secret`. |
| `QUORUM_PORT` | no | `8080` | HTTP listen port |
| `QUORUM_JWT_ACCESS_TTL` | no | `15m` | Access token lifetime (Go duration string) |
| `QUORUM_JWT_REFRESH_TTL` | no | `168h` | Refresh token lifetime (7 days) |
| `QUORUM_BASE_URL` | no | `http://localhost:8080` | Public URL used in email links |
| `QUORUM_TRUST_PROXY_HEADERS` | no | `false` | Key rate limiting on `X-Real-IP`/`X-Forwarded-For`. Enable only behind a trusted proxy/ingress that strips client-supplied forwarding headers. |
| `QUORUM_SMTP_HOST` | no | — | SMTP hostname for email reminders |
| `QUORUM_SMTP_PORT` | no | `587` | SMTP port |
| `QUORUM_SMTP_USER` | no | — | SMTP username |
| `QUORUM_SMTP_PASS` | no | — | SMTP password |
| `QUORUM_SMTP_REQUIRE_TLS` | no | `false` | Require an encrypted (STARTTLS) SMTP session; sending fails rather than falling back to plaintext if the relay does not offer TLS. |
| `QUORUM_EMAIL_FROM` | no | `quorum@localhost` | From address for outbound email |
| `QUORUM_STRIPE_WEBHOOK_SECRET` | no | — | Stripe webhook signing secret (`whsec_…`). When unset, the Stripe webhook endpoint returns 503. |
| `QUORUM_PAYPAL_WEBHOOK_ID` | no | — | PayPal webhook ID. When unset, the PayPal webhook endpoint returns 503. |
| `QUORUM_ALLOW_UNSIGNED_WEBHOOKS` | no | `false` | Local development only: process webhook events without signature verification when the provider's secret is unset. Never enable in production. |
| `QUORUM_DB_MAX_CONNS` | no | `10` | Maximum pooled database connections. Raise on a busy deployment if the pool-saturation alert fires. |
| `QUORUM_DB_STATEMENT_TIMEOUT_MS` | no | `30000` | Server-side per-statement timeout (ms). Caps a runaway query so it can't pin a pooled connection. `0` disables. |
| `QUORUM_BACKUP_REMOTE` | no | — | Off-host backup destination for `scripts/backup.sh create`: `s3://bucket/prefix` (needs `aws` CLI) or `rclone:remote:path` (needs `rclone`). When set, a backup fails unless the dump and its manifest copy off-box — essential disaster-recovery on a single instance. |
| `DB_PASSWORD` | docker only | — | Password for the Postgres container |

---

## First login

After `make bootstrap`, log in using the email and password you supplied:

```
POST /api/v1/auth/login
{"email": "admin@example.com", "password": "yourpassword"}
```

The response contains an `access_token` (short-lived, ~15 min). A `quorum_refresh` HttpOnly cookie is also set automatically — it carries the long-lived refresh token (7 days) and is sent by the browser on every refresh request without JavaScript access.

The browser app handles silent token refresh and session restoration automatically. API clients should store the access token in memory and POST to `/api/v1/auth/refresh` (the cookie is sent automatically by the browser; other clients must handle the cookie jar themselves).

The bootstrap endpoint (`POST /api/v1/auth/bootstrap`) returns `403 Forbidden` once any user exists.

---

## API overview

All API routes are under `/api/v1`. Authenticated routes require `Authorization: Bearer <access_token>`.

### Authentication

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/auth/bootstrap` | — | Create the first admin (one-time) |
| `POST` | `/auth/login` | — | Exchange credentials for access token + refresh cookie |
| `POST` | `/auth/refresh` | — | Issue a new access token using the HttpOnly refresh cookie |
| `POST` | `/auth/logout` | required | Revoke the refresh token and clear the cookie |
| `GET` | `/auth/me` | required | Current user profile |
| `PATCH` | `/auth/me/password` | required | Change own password (invalidates all existing sessions) |

### Users

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/users` | admin | List all users |
| `POST` | `/users` | admin | Create a new user (assigning `superadmin` requires `superadmin`) |
| `PATCH` | `/users/:id` | admin | Update role (granting/revoking `superadmin` requires `superadmin`; cannot change your own role) |
| `DELETE` | `/users/:id` | superadmin | Delete user — requires `?confirm=<email>` and notifies admins |

### Members

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/members` | member | List/search members |
| `POST` | `/members` | officer | Create member |
| `GET` | `/members/:id` | member¹ | Get member |
| `PATCH` | `/members/:id` | officer | Update member |
| `DELETE` | `/members/:id` | admin | Soft-delete (marks inactive) |
| `GET` | `/members/:id/dues` | member¹ | Member's invoices |
| `GET` | `/members/:id/action-items` | member¹ | Member's action items |
| `GET` | `/members/ids?min_role=` | officer | Active member IDs whose linked user holds at least the given role (feeds roster bulk-select) |
| `POST` | `/members/batch` | officer | Bulk-set `status`/`tier` on `{ids}` |
| `POST` | `/members/import?commit=` | admin | CSV import: dry-run report (default) or `commit=true` to insert (multipart `file`, or text/csv body) |

¹ A `restricted` user may call these three endpoints, but only for their own linked member record (the id must match their account's member link); `member` and above may view any member.

Query parameters for `GET /members`: `search`, `status`, `tier`, `limit`, `offset`.

### Dues

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/dues` | officer | List invoices |
| `POST` | `/dues` | officer | Create invoice(s) |
| `GET` | `/dues/:id` | officer | Get invoice + transactions |
| `PATCH` | `/dues/:id` | officer | Update invoice status |
| `POST` | `/dues/:id/transactions` | officer | Record a payment |
| `POST` | `/dues/batch` | officer | Bulk-set status on `{ids}` (waive / re-open) |
| `POST` | `/dues/:id/refund` | officer | Record a refund — a reversing GL entry in the invoice's currency (`{amount_minor, provider, note?}`) |
| `GET` / `PUT` | `/dues/:id/installments` | officer | Read / set a payment plan (tracking only): `{installments:[{amount_minor, due_date}]}`, empty clears it |
| `POST` | `/dues/:id/report-payment` | member¹ | Member self-reports a manual payment (Zelle/check) for officer confirmation |
| `GET` | `/payment-reports` | officer | Pending payment-report queue |
| `POST` | `/payment-reports/:id/confirm` \| `/dismiss` | officer | Confirm (records the payment) or dismiss a report |
| `GET` | `/dues/transactions` | officer | List transactions |

Members see their own dues via `GET /members/:id/dues`; the `/dues` endpoints above are the officer-facing billing views.

**Money is integer minor units.** Amounts are carried as the integer field `amount_minor` (the smallest unit of the currency — cents for USD, whole yen for JPY) plus `currency`, both in responses and in create/transaction request bodies. Convert to a display value by dividing by 10^exponent, where the exponent is 2 for most currencies, 0 for zero-decimal currencies (JPY, KRW, …), and 3 for a few (BHD, KWD, …). This keeps all arithmetic exact.

**Bulk invoice creation**: include `member_ids` (array) instead of `member_id` to create one invoice per member in a single request.

**Invoicing an outside customer**: pass `contact_id` (a Contacts entry) instead of `member_ids`. The invoice posts to the `income.services` rule instead of `income.dues`, and payments/waivers work exactly as for member invoices. An invoice has exactly one counterparty — member or contact — enforced by a DB CHECK.

### Dashboard, activity & personal

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/dashboard` | member | Home-screen counts and recent items |
| `GET` | `/setup-status` | admin | First-run checklist state (members/schedule/how-to-pay/SMTP/2FA/meeting) |
| `GET` | `/activity` | member | Recent org-visible activity feed |
| `GET` | `/auth/me/statement.pdf?year=` | member¹ | The caller's annual member statement (invoices + payments) |
| `POST` | `/calendar/subscription` \| `/calendar/rotate` | member | Get / rotate the personal ICS subscription URL |
| `GET` | `/calendar/:token.ics` | — (token) | Public read-only meeting calendar feed |
| `GET` / `PUT` | `/me/report-subscriptions` | officer | Scheduled report digests (ar_aging/ap_aging/income_statement × weekly/monthly) |

### Meetings

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/meetings` | member | List meetings (`?upcoming=true` for future only) |
| `POST` | `/meetings` | officer | Create meeting |
| `GET` | `/meetings/:id` | member | Get meeting + attendees + decisions |
| `PATCH` | `/meetings/:id` | officer | Update meeting |
| `DELETE` | `/meetings/:id` | superadmin | Delete meeting — requires `?confirm=<title>`, notifies admins |
| `PUT` | `/meetings/:id/attendees` | officer | Replace full attendance list (UI offers all/none/officers+/tier/group/RSVP bulk-select) |
| `GET` / `PUT` | `/meetings/:id/rsvp` | member | Read the RSVP tally + own response / set own response (yes/no/maybe) |
| `GET` | `/meetings/:id/rsvp-yes` | officer | Member IDs who RSVP'd yes (seed the roster) |
| `POST` | `/meetings/:id/decisions` | officer | Add a decision |
| `PATCH` | `/meetings/:id/decisions/:did` | officer | Update a decision |
| `DELETE` | `/meetings/:id/decisions/:did` | officer | Delete a decision |

### Plans

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/plans` | member | List plans |
| `POST` | `/plans` | officer | Create plan |
| `GET` | `/plans/:id` | member | Get plan + decision log |
| `PATCH` | `/plans/:id` | officer | Update plan |
| `DELETE` | `/plans/:id` | superadmin | Delete plan — requires `?confirm=<title>`, notifies admins |
| `POST` | `/plans/:id/decisions` | officer | Log a decision |
| `PATCH` | `/plans/:id/decisions/:did` | officer | Edit a decision |
| `DELETE` | `/plans/:id/decisions/:did` | officer | Delete a decision |

### Action items

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/action-items` | member | List (`?status=`, `?assignee_id=`, etc.) |
| `POST` | `/action-items` | officer | Create |
| `PATCH` | `/action-items/:id` | officer | Update |
| `PUT` | `/action-items/:id/contributors` | officer | Replace the card's additional-contributor roster (`{member_ids}`) |
| `DELETE` | `/action-items/:id` | superadmin | Delete — requires `?confirm=<title>`, notifies admins |

### Contacts

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/contacts` | member | List/search contacts |
| `POST` | `/contacts` | officer | Create |
| `GET` | `/contacts/:id` | member | Get |
| `PATCH` | `/contacts/:id` | officer | Update |
| `DELETE` | `/contacts/:id` | superadmin | Delete — requires `?confirm=<name>`, notifies admins |

### Resources

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/resources` | member | List/search resources |
| `POST` | `/resources` | officer | Create |
| `GET` | `/resources/:id` | member | Get |
| `PATCH` | `/resources/:id` | officer | Update (incl. `visible_min_role`: null\|member\|officer\|admin — role bar applies to everyone, even officers/admins below it) |
| `DELETE` | `/resources/:id` | superadmin | Delete — requires `?confirm=<title>`, notifies admins |
| `POST` | `/resources/:id/file` | officer | Upload/replace the document (multipart `file`, 25 MiB max) |
| `GET` | `/resources/:id/file` | member | Download: refused for preview-only docs; watermarked where the format allows; ledgered (who/when/IP/sha) + audited as EXPORT |
| `GET` | `/resources/:id/preview` | member | Original bytes for the in-app viewer (audited as PREVIEW) |
| `GET` | `/resources/:id/downloads` | officer | The document's download ledger |
| `GET` | `/downloads/verify?sha256=` | admin | Provenance: `original` / `download` (who, when, where) / `unknown` (altered or foreign) |
| `GET` | `/folders` | member | List folders (nested via `parent_id`) |
| `POST` / `PATCH` / `DELETE` | `/folders[/:id]` | officer | Create / rename / move / delete (cycle-safe; delete requires `?confirm=<name>`; contents return to the root) |

### Visibility groups

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/groups` | member | List groups |
| `POST` / `GET` / `PATCH` / `DELETE` | `/groups[/:id]` | admin | Manage groups |
| `PUT` | `/groups/:id/members` | admin | Replace a group's member list |
| `GET` | `/groups/:id/member-ids` | officer | Just the member IDs (feeds roster bulk-select, e.g. meeting attendance) |
| `GET` / `PUT` | `/resources/:id/groups` | officer | Groups attached to a resource |

### Accounting (general ledger — Phase A)

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/accounting/trial-balance` | officer | Per-account/per-currency balances, AR reconciliation status, recent postings. Postings happen via DB triggers (migration 0031). |
| `GET` | `/accounting/statements?from=&to=&basis=` | officer | Income statement (accrual or cash basis), balance sheet + net income to date + AR and AP aging (as of `to`), per currency |
| `GET` / `POST close|reopen` | `/accounting/periods[...]` | officer / admin | Closed months; closing locks posting dates (DB trigger); reopen is audited |
| `GET` / `POST` / `PATCH` | `/accounting/accounts[/:id]` | officer / admin | Chart of accounts; code+type freeze once posted (DB trigger) |
| `POST` | `/accounting/entries` | admin | Adjusting journal entry (balanced per currency or the DB refuses) |
| `GET` / `PUT` | `/accounting/posting-rules` | officer / admin | The automatic-posting account mappings, admin-editable (incl. `cash.provider.<name>` per payment provider); future postings only |
| `GET` | `/reports/accounting-pack.zip?from=&to=` | admin | CPA export: sealed statements PDF + trial balance/GL/statements/funds/AR-aging + bills/AP-aging CSVs + evidence file; ZIP SHA-256 audited |
| `GET` / `PUT` | `/settings/org` | member / admin | Allowlisted org settings (`fiscal_year_start_month`, `how_to_pay`, `require_2fa` = off\|admin\|officer\|member, `lapse_after_days`, `arcade_visible` = on\|off; admin-only: `infrastructure_facts`, `continuity_*`, `audit_legal_hold`, `monthly_report_email`) |
| `GET`/`POST`/`PATCH`/`DELETE` + `/attest` | `/continuity/custody[...]` | admin | Secret-custody registry (locations/holders, never values) with recorded attestations |
| `GET` | `/continuity/checks` | admin | Bus-factor health: superadmin count, custody staleness, watchdog config, TLS days-left |
| `GET` | `/reports/continuity-pack.zip` | superadmin | The successor's map: generated README, infrastructure facts, org snapshot, custody CSV — sealed & audited |

### Funds & purchases (accounting Phase B)

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/funds` | member | Funds with GL-derived balances, policies, signers |
| `POST` / `PATCH` | `/funds[/:id]` | admin | Create (auto GL cash account) / edit policy (voids in-flight approvals) |
| `POST` | `/funds/:id/transfers` | admin | Move money operating↔fund (posts to books; overdraft refused) |
| `GET` / `POST` | `/purchases` | member / officer | List (`?fund_id=&status=`) / file a request |
| `POST` | `/purchases/:id/approve` | member | Sign: password re-entry required; requester and non-signers refused; records who/when/IP |
| `POST` | `/purchases/:id/reject` \| `/cancel` \| `/complete` | officer / requester / officer | Reject; withdraw; execute — completion posts DR Expenses / CR fund-cash in the same transaction |

### Vendor bills (accounts payable)

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/bills?status=` | member | List bills (`open`, `paid`, `void`) |
| `POST` | `/bills` | officer | Record a bill: accrues DR expense / CR Accounts Payable immediately (`contact_id`, `amount_minor`, `currency`, `expense_account_id`, optional `bill_date`/`due_date`/`memo`) |
| `POST` | `/bills/:id/pay` | officer | Settle: DR A/P / CR cash — from a fund (`fund_id`, balance-guarded) or the provider-routed operating account (`provider`) |
| `POST` | `/bills/:id/void` | admin | Reverse an open bill with a mirroring journal entry |

Bills are permanent records: no deletes, and a paid or void bill is frozen (DB triggers). Cash-basis statements ignore a bill until it is actually paid — the accrual entry touches no cash account. Open bills appear in the AP aging block of `/accounting/statements`.

### Roster, governance records & membership

These are organizational **records**, not access grants — the permission role remains the only security boundary. Office titles and committees describe the org chart; recusals and the application queue are auditable governance workflow.

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/office-terms` | member | Office holders with history (`?current=true` for present holders) |
| `POST` | `/office-terms` | admin | Record a member holding an office (`{member_id, title, started_on?}`) |
| `POST` | `/office-terms/:id/end` | admin | Close an open term (`{ended_on?}`, defaults to today) |
| `GET` / `POST` | `/committees` | member / admin | List committees (with chair + member count) / create (`{name, purpose?, chair_id?}`) |
| `GET` / `PATCH` / `DELETE` | `/committees/:id` | member / admin / admin | Get (with roster) / edit / delete |
| `PUT` | `/committees/:id/members` | admin | Replace the committee roster (`{member_ids:[…]}`) |
| `GET` / `POST` | `/recusals/:id` | member | List (`?type=motion\|purchase`) / record the caller recusing (`{type, reason?}`) from a motion or purchase for conflict of interest |
| `POST` | `/public/join-request` | — (public) | Anyone applies to join (`{name, email, message?}`); rate-limited |
| `GET` | `/join-requests` | officer | Membership-application queue (`?status=pending\|approved\|rejected`) |
| `POST` | `/join-requests/:id/approve` | officer | Approve — atomically creates a member (`{tier?}`) and returns `{member_id}` |
| `POST` | `/join-requests/:id/reject` | officer | Decline an application |
| `GET` | `/budgets/:id/vs-actual?from=&to=&basis=` | officer | Budget vs. actual: scenario-currency-only (others reported, never summed), accrual or cash basis, per-account category rows for GL-linked lines, and straight-line proration when the scenario has dates |
| `GET` | `/budgets/remaining?account_id=` | officer | Active-budget envelope for one GL account: budget, actuals over the scenario period, remaining — the read-only hint shown when filing purchases and bills |

Recusals recorded against a motion or purchase are returned inline on that motion (`GET /meetings/:id/motions`) and purchase (`GET /purchases`), so the recusal is visible wherever the vote or approval is.

**Budget guardrails**: a scenario's currency freezes once it has lines (409 — clone instead); `archived` scenarios are read-only except un-archiving; deleting a scenario is admin-only with `?confirm=<name>`; budget lines may link to income/expense GL accounts (kind-checked) which powers per-category variance and the spend-time remaining hint; dues seeding reports every tier it could NOT project (`skipped`) instead of dropping them silently. Plans carry an optional cost estimate, list their linked action items with a progress bar, and the dashboard counts active/overdue plans.

### Top Secret arcade

A basement arcade behind the **Top Secret** sidebar section: nine original cabinets written in Rust (Bevy engine, core crate only) and compiled to WebAssembly, served same-origin from `web/arcade/`. Credits are **play tokens, not money** — inserting one records a play against the user's account and never touches the ledger. Games: Chess (vs a built-in machine opponent, hotseat, or online — full draw rules: 50-move, threefold repetition, insufficient material; resign with R-R even off-turn online; a per-turn stall clock; a live material tally; online Black sees a flipped board), Go (vs its own machine opponent, hotseat, or online — after two passes a dead-stone marking phase settles the count honestly, superko stops repetition cycles, and R-R resigns), Comet Buster (a hunting saucer that gets meaner as your score climbs), Penny Pincher, Brickfall (ghost piece, hold slot, drop scoring), Powder Keg and Hexfection (up to 12 seats each — local humans + bots, or online rooms; Powder Keg has a keg-kick perk, fuse-blink telegraphs, and a post-sudden-death settle timer so rounds always end; Hexfection spaces starts so nobody dies before their first turn, calls sealed-endgame shuffles on standings, and survives a host disconnect by handing bot duty to the next seat), Hold 'Em (tournament Texas hold 'em against 1-5 house bots — the game is public domain, our cards and table are original, and the chips are play tokens like every other credit here; blinds climb, side pots resolve properly, and the last stack keeps the table; local only by design, since hole cards are secrets and the room relay deliberately isn't a dealer), and Interns (a walkers-and-jobs cabinet: assign seven jobs to a stream of clueless new hires and hit the rescue quota — 1P, 2P local with a keyboard cursor for the second player, 2P online, and a **level editor** whose creations are stored server-side and shared org-wide; levels run up to 1080 cells wide with a scrolling camera window, edge/A-D scrolling, and a minimap; hover highlights the walker your click would address, active jobs are reassignable, quitters keep moving through their countdown, undermined supervisors fall, and you can still hand out jobs while paused. The editor loads any level as a template, cycles widths, has per-skill pools, undo, a Shift-click line tool, and a mirror mode for symmetric 2P maps; finished test-plays return to the canvas without scoring). **Level editors are not just for Interns**: Powder Keg has a cellar editor (paint walls and crates around the fixed spawn rings), Hexfection has a dish editor (punch holes in the board — local rounds only), and Chess has a **position editor** for building puzzles and absurd starting configurations (place any pieces, pick who moves, validated for one king a side and no back-rank pawns) — all saved to the same community shelf. Chess also deals **Fischer Random** starts (bishops opposite, king between rooks, mirrored; castling off as a stated house rule), and custom positions play in every mode — vs the machine (it takes whichever side you don't), hotseat, and online (the host relays the start). All cabinets have chip-tune sound (a WebAudio square-wave synth in the page — no assets, no autoplay issues since the coin click is the enabling gesture) and Esc pauses local rounds. The chess machine is a perft-verified negamax searcher in `arcade/logic` — beatable on purpose, but it finds short mates and refuses free-piece blunders (unit-tested for both).

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/arcade/stats` | member | Per-cabinet totals: plays, house high score + holder, your plays/best |
| `GET` | `/arcade/players` | member | Everyone with an arcade footprint (the service-record roster) |
| `GET` | `/arcade/player-stats?user=` | member | Any member's full service record, grouped by cabinet (default: your own) |
| `POST` | `/arcade/:game/stats-report` | member | End-of-round counter report from the cartridge (allowlisted names, capped deltas) |
| `GET` | `/arcade/:game/scores` | member | Leaderboard — each player's best score |
| `POST` | `/arcade/:game/credit` | member | Insert a credit (records a play; returns your lifetime count) |
| `POST` | `/arcade/:game/score` | member | Record a final score (refused without a prior credited play; clamped) |
| `GET`/`POST` | `/arcade/:game/levels` | member | Community level shelf: list / save (48 KB JSON cap, unique name per game; re-saving your own name updates it) |
| `GET`/`DELETE` | `/arcade/levels/:id` | member | Fetch one level with data / delete (author always; admins moderate) |
| `GET` | `/arcade/ws` | member¹ | WebSocket room relay for online play (chess, go, powder-keg, hexfection, interns) |

¹ Browsers cannot set an Authorization header on a WebSocket, so `/arcade/ws` authenticates via its **first message** (`{"op":"auth","token":<JWT>}`, member+) on a 5-second deadline; the route sits outside the normal auth middleware by necessity. After auth: `create {game, seats}` or `join {code}` → 4-letter room code, host (`seat 0`) sends `start`, then `msg {data}` frames are fanned out to the other seats with the sender's seat stamped **server-side**. The server is a seat-managing relay, not a referee — rules run in every player's cartridge (the same perft-tested logic crate); the host's cartridge is authoritative for the real-time cabinet (Powder Keg guests send inputs, the host broadcasts ~20 Hz snapshots), and turn-based cabinets validate every relayed move locally before applying it. Empty seats in a started room are host-driven bots; a player who disconnects mid-game becomes one — and can reclaim their own seat by rejoining with the same room code (the seat un-botifies on their first input). Turn-based cabinets detect relay desyncs and end the round honestly ("SYNC LOST") instead of hanging both clients; online chess runs a generous per-turn clock, and Hexfection flags AFK humans over to the bots after 35 seconds. Rooms live in process memory (single-replica assumption, like the rate limiters); idle rooms are swept after 30 minutes.

**The cartridge is an optional build artifact** — it is *not* in the source repo. Without it the app runs normally and the arcade page reports "cartridge not installed". To build it (on any machine with Rust):

```bash
rustup target add wasm32-unknown-unknown
cargo install wasm-bindgen-cli --version 0.2.126   # must match arcade/cart/Cargo.toml
make arcade        # → web/arcade/ ; then `make build` (or image build) embeds it
make arcade-test   # runs the game-rules test suite
```

`web/arcade/` is gitignored; the Go binary embeds whatever is there at build time via `//go:embed web`, so build the cartridge **before** building the binary or container image on the host that ships it. Optional `wasm-opt` (binaryen) shrinks the download ~40%; the server gzips `application/wasm` itself (~4 MB over the wire).

Deployments that build **on the server** (`ops/upgrade.sh`) have two options. Give the server a Rust toolchain once and the upgrade script rebuilds the cartridge by itself whenever an upgrade touches `arcade/` (and skips the rebuild otherwise — a failed cartridge build never blocks the app upgrade). Or keep the server Rust-free: because `web/arcade/` is ignored by git, a cartridge built elsewhere and copied into the checkout once survives every `git` fast-forward and is re-embedded by each upgrade's `go build`. Both paths, with exact commands, in **UPGRADING.md → "The arcade cartridge"**. `ops/deploy.sh` builds locally, so there the usual rule applies: run `make arcade` first and the binary it ships carries the cartridge.

Admins can switch the whole floor off from **Settings → Organization settings → Top Secret arcade** (`arcade_visible`): the sidebar section disappears for everyone and the arcade API + websocket answer 404 (checked server-side, cached ~30s). Nothing is deleted — plays, scores, and community levels survive the outage.

The game-rules layer (`arcade/logic`) is dependency-free and unit-tested — chess legal-move generation is perft-verified, go covers captures/ko/suicide/area scoring, hexfection covers clone/jump/conversion. Scores are self-reported by the wasm client and lightly validated (bounds + a credited play required); treat leaderboards as friendly, not forensic. The same goes for **service records** — every cabinet counts its player's deeds (bullets fired, kegs planted, auditors bitten, hyperdrive self-inflictions, takebacks begged, gravity lessons taught…) and reports them at game over into lifetime per-member counters, browsable by everyone on the arcade floor. Counter names are allowlisted per cabinet and deltas capped server-side.

### Discussions

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` / `POST` | `/channels` | member | List all channels (membership flagged) / create one (creator auto-joins) |
| `GET` | `/channels/users` | member | Addable accounts for the people picker |
| `GET` / `PATCH` / `DELETE` | `/channels/:id` | member | Channel + roster (members or admin) / edit / delete (creator or admin; `?confirm=<name>`) |
| `POST` / `DELETE` | `/channels/:id/members[/:uid]` | member | Any member adds; self-leave, creator/admin removes |
| `GET` / `POST` | `/channels/:id/messages` | member | Roots or `?thread=<id>` replies / post (`parent_id`, `resource_id` optional; author-visible docs only; no nesting) |
| `DELETE` | `/channels/:id/messages/:mid` | member | Author deletes own; admin moderates |

### Board

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/board/columns` | member | Columns in board order |
| `POST` | `/board/columns` | officer | Add a column (`maps_to_status` optional: set = advances card status on drop) |
| `PATCH` | `/board/columns/:id` | officer | Rename / reposition |
| `DELETE` | `/board/columns/:id` | officer | Delete — requires `?confirm=<name>`; cards fall back to their status lane |
| `GET` | `/action-items/:id/comments` | member | A card's conversation, oldest first |
| `POST` | `/action-items/:id/comments` | member | Add a comment |
| `DELETE` | `/action-items/:id/comments/:cid` | member | Author deletes own; admin moderates |
| `GET` | `/action-items/:id/links` | member | Relationships touching the card (both directions) |
| `POST` | `/action-items/:id/links` | officer | Link cards: `{to_id, kind: depends_on\|blocked_by\|related_to}` |
| `DELETE` | `/action-items/:id/links/:lid` | officer | Unlink |
| `GET` | `/sprints/:id/analytics` | member | Sprint health: cards/points totals, done %, blocked, by type/status/assignee |
| `GET` | `/reports/sprints/:id.pdf` | officer | Sprint report PDF (watermarked, integrity-sealed, audited) |

### Dashboard

| Method | Path | Min role | Description |
|--------|------|----------|-------------|
| `GET` | `/dashboard` | member | Overdue/pending counts, upcoming meetings, open action items |

### Webhooks

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/webhooks/stripe` | Stripe event receiver |
| `POST` | `/webhooks/paypal` | PayPal event receiver |

### Health probes

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Liveness: 200 while the process is running |
| `GET` | `/readyz` | Readiness: 200 when the database responds, 503 otherwise |

Health endpoints are unauthenticated and live at the root (not under `/api/v1`).

Webhook endpoints are not authenticated with JWT. Stripe events are validated via HMAC-SHA256 signature verification; PayPal events via RSA certificate-based signature. See [SECURITY.md](SECURITY.md) for details.

---

## Role-based access control

Five roles with ascending privileges:

| Role | Can do |
|------|--------|
| `restricted` | Sees only its own linked member record — own profile, dues, and assigned action items. No access to the directory, meetings, plans, contacts, resources, or the dashboard. |
| `member` | Read-only access to all shared resources (directory, meetings, plans, contacts, resources, dashboard). |
| `officer` | Read + write most resources; cannot manage users or delete core records. |
| `admin` | Officer plus user management (create/list/update users) and member deactivation. |
| `superadmin` | Full access, including permanent deletion of core records and user accounts. |

Roles are encoded in the JWT and enforced per-route in middleware.

**A `restricted` user** must be linked to a member record (`member_id`) to see anything; link it when creating the user or via the member's account. Everyone at `member` and above keeps full read visibility by default — `restricted` is opt-in per user for people who should only see their own data.

**Assigning `superadmin`** is itself a superadmin-only action, and no one can change their own role (this prevents self-lockout and privilege self-escalation). The bootstrap (founder) account is created as `superadmin`; on upgrade of an existing install, the earliest-created admin is promoted to `superadmin` automatically.

**Destructive deletes** of meetings, plans, contacts, resources, action items, and user accounts require `superadmin` and are heavily gated: the request must echo the record's exact name via `?confirm=<name>` (a type-to-confirm step in the UI), the action is written to the audit log, and — when SMTP is configured — a notification is emailed to all admins and superadmins. Member "deletion" is a reversible soft-delete (deactivation) and remains an `admin` action.

---

## Payment integration

Quorum does not process payments itself — it records the result of payments made through Stripe or PayPal.

### Stripe

1. Create a webhook endpoint in the Stripe dashboard pointing to `https://your-domain/api/v1/webhooks/stripe`.
2. Select events: `payment_intent.succeeded` and/or `charge.succeeded`.
3. Copy the signing secret (`whsec_…`) into `QUORUM_STRIPE_WEBHOOK_SECRET`.
4. In your payment metadata, include `quorum_invoice_id` and `quorum_member_id` to link payments to invoices automatically.

When `QUORUM_STRIPE_WEBHOOK_SECRET` is empty, the webhook endpoint returns `503` (fail closed). For local testing with the Stripe CLI you can set `QUORUM_ALLOW_UNSIGNED_WEBHOOKS=true` to process events without verification — never enable this in production.

### PayPal

1. Point a PayPal webhook at `https://your-domain/api/v1/webhooks/paypal`.
2. Subscribe to `PAYMENT.CAPTURE.COMPLETED`.
3. Set `custom_id` to `invoice:<uuid>` in your PayPal order to auto-link the payment.

All webhook events are deduplicated via the `processed_events` table, so retries from the provider are safe.

### Manual recording

Officers can also record payments manually via `POST /api/v1/dues/:id/transactions`, which automatically recomputes the invoice status after recording.

---

## Email reminders

When `QUORUM_SMTP_HOST` is set, Quorum emails a nightly overdue-dues digest to admin users.

## Nightly maintenance

A background job runs nightly (2 AM local). These steps run **unconditionally**, whether or not SMTP is configured:

- **Ages** pending invoices whose due date has passed to `overdue`.
- **Prunes** bookkeeping tables so they do not grow without bound: expired/revoked refresh tokens are deleted immediately, processed webhook events after 90 days, and audit-log entries after one year.

The remaining steps run **only when SMTP is configured** (`QUORUM_SMTP_HOST` set):

- **Reminds members** whose dues are overdue, escalating through three notices — a first notice, a 7-day follow-up, and a 30-day final notice — tracked per invoice so each member gets each notice once. An invoice's stage only advances after a successful send, so a transient SMTP failure is retried the next night.
- **Digests** the overdue picture to all admins and superadmins.

---

## Architecture

```
cmd/quorum/         main entry point
internal/
  auth/             JWT, bcrypt, refresh token helpers
  config/           environment variable loading
  db/               pgxpool connection, advisory-locked migration runner
    migrations/     numbered *.up.sql migration files
  handler/          HTTP handlers and middleware
  model/            shared data types
  repo/             PostgreSQL repository implementations
static.go           embeds web/ into the binary
web/                vanilla JS frontend (no build step)
  app.js            router, API client, auth state
  css/base.css      design tokens and shared component styles
  components/       one Web Component per page/feature
```

**Database**: PostgreSQL 16 with pgxpool. Migrations run automatically at startup using an advisory lock to prevent parallel execution. All UUID columns are cast to `::text` in SELECT statements to allow scanning into Go strings without requiring pgx UUID types.

**Frontend**: Pure ES modules, Web Components (Custom Elements v1), hash-based client-side routing. No build step, no npm, no framework. The entire `web/` directory is embedded into the binary via `//go:embed`.

**Authentication**: HS256 JWTs (15-minute access tokens) + opaque refresh tokens (SHA-256 hashed before storage, 7-day TTL). Refresh tokens are revoked on logout and validated against the database on each refresh.

---

## Security

See [SECURITY.md](SECURITY.md) for a full description of the authentication model, authorisation controls, injection prevention, webhook verification, and operator responsibilities.

---

## Development guide

```sh
# Build the binary
make build

# Run tests (also available as: make test)
go test ./...

# Run tests with race detector
go test -race ./...

# Lint (requires golangci-lint; also available as: make lint)
golangci-lint run

# Check for known CVEs in dependencies
govulncheck ./...

# Integration tests against a real Postgres (also: make test-integration)
QUORUM_TEST_DATABASE_URL=postgres://quorum:test@localhost:5432/quorum?sslmode=disable \
  go test -tags integration ./internal/...

# Frontend unit tests, no build step (also: make test-web)
node --test web/*.test.js

# Generate a random JWT secret
make secret

# Container workflow (Podman)
make pod-build    # build image
make pod-up       # start stack
make pod-down     # stop stack
```

### Testing approach

Handler tests live alongside source in `internal/handler/*_test.go`. They use lightweight function-field mocks (defined in `testhelpers_test.go`) that satisfy the package-private interfaces in `interfaces.go`. No database is required to run the test suite.

```sh
# Run only handler tests with verbose output
go test -v ./internal/handler/...
```

New handlers should follow the existing pattern:
1. Add a mock struct for the new repo to `testhelpers_test.go`.
2. Create `<handler>_test.go` covering: success path, validation errors, repo errors, and any filter/query parameter passthrough.

### Adding a migration

Create a numbered **pair** in `internal/db/migrations/` — the `.down.sql` is not
optional:

```
internal/db/migrations/0017_add_my_table.up.sql
internal/db/migrations/0017_add_my_table.down.sql
```

The runner applies `.up.sql` files in numeric order at startup, skipping
already-applied ones, under an advisory lock. Restart the server to apply new
migrations.

CI runs the whole ladder **up, back down to zero, and up again** against a real
Postgres and fails if any table survives the rollback — so a `.down.sql` that
doesn't fully reverse its `.up.sql` breaks the build. To roll back by hand:

```sh
./quorum -migrate-down 16     # roll back to version 16
```

### Project structure conventions

- Repository types (`*repo.XRepo`) implement package-private interfaces defined in `internal/handler/interfaces.go`, enabling handler unit tests without a database.
- Handler tests use `mockXRepo` structs (function-field mocks) defined in `testhelpers_test.go`.
- All nullable PostgreSQL columns map to pointer types (`*string`, `*time.Time`, etc.) in Go model structs.

---

## Operations

| Document | Covers |
|---|---|
| **[RUNBOOK.md](RUNBOOK.md)** | Day-2 operations: user onboarding/offboarding, access restriction, secret rotation, locked-out admin recovery, upgrades, migration rollback, backup management, disaster recovery, breach forensics, health litmus tests |
| **[PAYMENTS-SETUP.md](PAYMENTS-SETUP.md)** | Wiring Stripe/PayPal webhooks from zero: signing secrets, invoice-linking metadata, fail-closed verification checklist |
| **[DOWNGRADING.md](DOWNGRADING.md)** | Walking back a successful-but-unwanted upgrade: binary vs schema vs data layers, migrate-down vs restore, rehearsal |
| **[CONTINUITY-CHECKLIST.md](CONTINUITY-CHECKLIST.md)** | Bus-factor setup, step by step: second superadmin, custody registry, org vault, sealed envelope, the successor drill |
| **[UPGRADING.md](UPGRADING.md)** | Moving production to newer code: on-server `ops/upgrade.sh` (backup, build, swap, auto-rollback), workstation deploys, version rollback |
| **[ARCHITECTURE.md](ARCHITECTURE.md)** | Diagrams: system architecture, request/backup data flow, network zones and trust boundaries |
| **[EMAIL-SETUP.md](EMAIL-SETUP.md)** | SMTP from zero: what a relay is, why mail gets spam-blocked (SPF/DKIM/DMARC), Amazon SES walkthrough, Gmail stopgap |
| **[BACKUP.md](BACKUP.md)** | Backup/restore/verify tooling, scheduling, retention, full recovery from scratch, RPO/RTO |
| **[PRODUCTION_READINESS.md](PRODUCTION_READINESS.md)** | What's done, what's left before a real launch, and the pre-deploy checklist |
| **[SECURITY.md](SECURITY.md)** | Security model and reporting |
| **[DEPLOY-EC2.md](DEPLOY-EC2.md)** | Single-instance EC2 deployment: OS choice (Amazon Linux / Rocky / Ubuntu, podman notes), reverse-proxy configs in ops/, install walkthrough, upgrades via `ops/deploy.sh`, self-hosted CI/CD without GitHub |
| **[COMPLIANCE.md](COMPLIANCE.md)** | Why Quorum's records are evidence-grade: the audit hash chain, the append-only ledger, and the third-party verification procedure |

The `roadmap/` directory holds design plans under discussion (payment
providers, CPA-grade accounting, organizational continuity) — plans, not
shipped features.

The `ops/` directory ships deployment collateral: Prometheus alert rules,
systemd units for nightly backups / weekly restore-verification / daily
audit-chain verification, and `verify-audit-export.py` — the standalone script
an accountant or lawyer runs to independently verify an audit evidence export.

Two operator one-shots are built into the binary:

```sh
./quorum -unlock-2fa user@example.org   # break-glass: clear a user's 2FA, revoke their sessions
./quorum -migrate-down 16               # roll the schema back to version 16
```

---

## Deployment

### Podman (local / self-hosted)

Quorum uses Podman as its primary container engine. `make pod-*` targets wrap `podman compose` and `podman build`:

```sh
make pod-up       # Start PostgreSQL + app (podman compose up --build -d)
make pod-down     # Stop and remove containers
make pod-build    # Build the image: IMAGE=localhost/quorum:dev
make pod-push     # Push to a registry: IMAGE=registry.example.com/quorum:latest make pod-push
make pod-run      # Run the app container standalone (no compose)
```

`IMAGE` defaults to `localhost/quorum:dev`. Override on the command line:

```sh
IMAGE=registry.example.com/quorum:v1.2.3 make pod-push
```

Data is persisted in the `pgdata` named volume. The app container binds only to `127.0.0.1:8080` — put a reverse proxy (nginx, Caddy) in front for TLS.

### Docker Compose (alternative)

`make docker-up` / `make docker-down` are provided as aliases for teams still on Docker.

### Binary-only

```sh
make build
QUORUM_DATABASE_URL=postgres://... QUORUM_JWT_SECRET=... ./quorum
```

The binary includes all migrations and frontend assets — no external files needed.

### Reverse proxy (nginx example)

```nginx
server {
    listen 443 ssl;
    server_name quorum.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
    }
}
```

Set `QUORUM_BASE_URL=https://quorum.example.com` so email links are correct.

---

### Kubernetes

Three deployment methods are provided in `deploy/`. Choose one:

| Method | When to use |
|--------|-------------|
| **Kustomize + Tekton + Argo CD** | GitOps, auto-deploy on push (recommended) |
| **Helm** | Imperative install or Argo CD Helm-source mode |
| **Raw manifests** (`deploy/k8s/`) | Simple one-off installs |

#### Kustomize structure

```
deploy/kustomize/
  base/                          # References deploy/k8s/ resources
  overlays/
    production/                  # 2 replicas, production domain, higher limits
      patches/configmap.yaml
      patches/replicas.yaml
      kustomization.yaml
    staging/                     # 1 replica, staging domain, minimal limits
      patches/configmap.yaml
      patches/resources.yaml
      kustomization.yaml
```

Apply an overlay directly:

```sh
kubectl apply -k deploy/kustomize/overlays/production
kubectl apply -k deploy/kustomize/overlays/staging
```

#### Helm chart

```sh
# Install into the quorum namespace
helm upgrade --install quorum deploy/helm/quorum \
  -f deploy/helm/quorum/values.yaml \
  -f deploy/helm/quorum/values-production.yaml \
  --namespace quorum --create-namespace

# Uninstall (Secrets with helm.sh/resource-policy: keep are retained)
helm uninstall quorum -n quorum
```

The chart supports `secrets.existingSecret` to reference a pre-existing secret (created by Sealed Secrets or ESO) instead of letting Helm manage credentials:

```yaml
# values-production.yaml (excerpt)
secrets:
  existingSecret: quorum-secrets   # Helm will not create/delete this secret
```

#### Tekton CI pipeline (Buildah / Podman)

The pipeline builds images with **Buildah** (the Podman build engine) — no Docker daemon required.

```
Git push → GitHub/Gitea webhook
  └─► Tekton EventListener
        └─► quorum-build Pipeline
              1. git-clone      — clone source
              2. quorum-go-test — go test -race ./...
              3. quorum-buildah-build — buildah bud + push (commit-sha + latest tags)
              4. quorum-update-manifest — kustomize edit set image → commit → push GitOps repo
                    └─► Argo CD detects GitOps commit → syncs cluster
```

**Setup:**

```sh
# 1. Store registry credentials (Podman/Docker auth format)
#    podman login registry.example.com
#    kubectl create secret generic registry-credentials \
#      --from-file=.dockerconfigjson=$HOME/.config/containers/auth.json \
#      -n tekton-pipelines

# 2. Store GitOps SSH deploy key
#    kubectl create secret generic gitops-ssh-key \
#      --from-file=id_ed25519=/path/to/deploy_key \
#      -n tekton-pipelines

# 3. Store webhook HMAC secret
#    kubectl create secret generic github-webhook-secret \
#      --from-literal=token=<your-hmac-token> \
#      -n tekton-pipelines

# 4. Apply RBAC, tasks, pipeline, and triggers
kubectl apply -f deploy/tekton/rbac.yaml
kubectl apply -f deploy/tekton/tasks.yaml
kubectl apply -f deploy/tekton/pipeline.yaml
kubectl apply -f deploy/tekton/triggers/event-listener.yaml
```

Edit `deploy/tekton/triggers/event-listener.yaml` to set `image-repository`, `gitops-repo-url`, and `kustomize-overlay-path` for your environment.

**Rootless builds:** `quorum-buildah-build` is rootless by **default** — it already runs as UID 1000 with the `vfs` storage driver, `BUILDAH_ISOLATION=chroot`, only the `SETFCAP` capability, and no privileged container (requires `kernel.unprivileged_userns_clone=1`, the default on modern kernels). No configuration is needed. The opt-in is the other direction: override the `storage-driver` param to `overlay` (mount `/dev/fuse` for fuse-overlayfs) to speed up large builds while staying rootless.

#### Argo CD

Two Application manifests are provided:

| File | Source | Use case |
|------|--------|----------|
| `deploy/argocd/application.yaml` | Kustomize production overlay | GitOps auto-deploy (Tekton writes image tag here) |
| `deploy/argocd/application-helm.yaml` | Helm chart in source repo | Direct Helm-managed deploy |

```sh
# Register the AppProject first, then choose one Application
kubectl apply -f deploy/argocd/project.yaml
kubectl apply -f deploy/argocd/application.yaml        # Kustomize (default)
# — or —
kubectl apply -f deploy/argocd/application-helm.yaml   # Helm chart
```

Both files contain `# replace` comments for repository URLs that must be updated before applying.

#### Secrets management

`deploy/k8s/secret.yaml` is a **template only** — never commit real secrets. In production, manage `quorum-secrets` via:

- [Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets) — encrypt client-side, store SealedSecret in git.
- [External Secrets Operator](https://external-secrets.io) — sync from Vault, AWS Secrets Manager, etc.

---

## License

Quorum is licensed under the **GNU Affero General Public License v3.0 or later**
(AGPL-3.0-or-later). See [LICENSE](LICENSE) for the full text and [NOTICE](NOTICE)
for the copyright notice.

In short: you may run, study, modify, and share Quorum freely. Self-hosting it
for your own organization carries **no obligations**. If you run a *modified*
version as a network service for others, the AGPL requires you to offer those
users the corresponding source of your modified version.

Contributions are accepted under the same license via the Developer Certificate
of Origin (DCO) — sign off your commits with `git commit -s` to certify you
wrote the change and may submit it under AGPL-3.0-or-later.
