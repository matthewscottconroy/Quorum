# Quorum — Organizational Management Web Application
## Design Document v0.1

---

## 1. Overview

**Quorum** is a self-hosted web application for organizations (clubs, associations, cooperatives, small businesses) that need to manage membership dues, record meeting decisions, coordinate plans, and maintain institutional knowledge. It is built with a deliberate technology stack that favors long-term maintainability over ecosystem convenience.

### Goals

- Collect and track membership dues without becoming a payment processor (delegate to Stripe/PayPal/Square).
- Serve as the organization's institutional memory: meeting notes, decisions, action items.
- Maintain a living directory of members, contacts, and resources.
- Remain operable by a single administrator with no DevOps expertise.
- Produce no JavaScript build step and require no NPM dependency tree.

### Non-Goals

- Not a general-purpose CRM or accounting system.
- Does not process payment card data directly (PCI scope is zero).
- Does not replace a full project-management tool; planning support is intentionally lightweight.
- No mobile native applications (responsive web is sufficient).

---

## 2. Technology Stack

| Layer | Choice | Rationale |
|---|---|---|
| Backend language | Go (1.23+) | Single binary deployment, strong stdlib, fast compile times |
| HTTP router | `chi` | Thin wrapper on `net/http`; no magic, no lock-in |
| Database driver | `pgx/v5` | Native PostgreSQL protocol, prepared statements, pgx pool |
| Database | PostgreSQL 16 | ACID, JSON columns where schema flexibility helps, full-text search |
| Auth tokens | JWT (HS256) signed with server secret | Stateless; refresh tokens stored in DB for revocation |
| Email | SMTP via `net/smtp` + configurable relay | Dues reminders, action-item notifications |
| Frontend | Vanilla JS + Web Components (Custom Elements v1) | Zero framework overhead; browser-native APIs |
| CSS | Plain CSS with custom properties; no preprocessor | One file per component, loaded as `<link>` |
| Asset serving | Go `embed.FS` | Static files compiled into binary; one artifact to deploy |
| Payment providers | Stripe (primary), PayPal (optional) | Webhooks only; no card data ever touches Quorum |
| Container | Docker (optional) | Single-stage build from official Go image |

---

## 3. Architecture

```
┌─────────────────────────────────────────────────────┐
│                     Browser                         │
│  ┌──────────────────────────────────────────────┐   │
│  │  <app-shell> (Web Component layout host)     │   │
│  │  ┌────────────┐  ┌────────────────────────┐  │   │
│  │  │  <nav-bar> │  │  <page-outlet>         │  │   │
│  │  └────────────┘  │  (swaps page components)│  │   │
│  │                  └────────────────────────┘  │   │
│  └──────────────────────────────────────────────┘   │
└────────────────────┬────────────────────────────────┘
                     │  HTTPS / REST JSON
┌────────────────────▼────────────────────────────────┐
│                  Go HTTP Server                      │
│  ┌──────────┐  ┌────────────┐  ┌─────────────────┐  │
│  │  Auth    │  │  API v1    │  │  Webhook        │  │
│  │  handler │  │  handlers  │  │  receiver       │  │
│  └──────────┘  └────────────┘  └─────────────────┘  │
│  ┌──────────────────────────────────────────────┐   │
│  │          Service / Business Logic layer      │   │
│  └──────────────────────────────────────────────┘   │
│  ┌──────────────────────────────────────────────┐   │
│  │          Repository layer (pgx queries)      │   │
│  └──────────────────────────────────────────────┘   │
└────────────────────┬────────────────────────────────┘
                     │
┌────────────────────▼────────────────────────────────┐
│                PostgreSQL 16                         │
└─────────────────────────────────────────────────────┘

External:
  Stripe / PayPal ──→ POST /api/v1/webhooks/stripe
                      POST /api/v1/webhooks/paypal
```

### Request lifecycle

1. Browser routes are handled client-side by a minimal hash-based router (`#/members`, `#/meetings`, etc.).
2. Each route swaps a Web Component into `<page-outlet>`.
3. The component fetches data from `/api/v1/...` using `fetch()` with the JWT in the `Authorization: Bearer` header.
4. The server validates the JWT, runs the handler, queries PostgreSQL, and returns JSON.
5. The component renders received data via its own DOM update logic (no virtual DOM).

### Directory layout (Go project)

```
quorum/
├── cmd/
│   └── quorum/
│       └── main.go          # Wires everything together
├── internal/
│   ├── auth/                # JWT issue/verify, password hashing
│   ├── config/              # Environment / config file loading
│   ├── db/                  # pgx pool init, migrations
│   ├── handler/             # HTTP handlers (thin, delegate to service)
│   │   ├── auth.go
│   │   ├── members.go
│   │   ├── dues.go
│   │   ├── meetings.go
│   │   ├── plans.go
│   │   ├── contacts.go
│   │   ├── resources.go
│   │   └── webhooks.go
│   ├── model/               # Plain Go structs matching DB schema
│   ├── repo/                # SQL queries, one file per domain
│   ├── service/             # Business logic (dues aging, notifications)
│   └── webhook/             # Stripe/PayPal signature verification + processing
├── migrations/              # Numbered SQL files (up/down)
├── web/                     # Embedded static assets
│   ├── index.html
│   ├── app.js               # Router + app-shell registration
│   ├── components/
│   │   ├── nav-bar.js
│   │   ├── member-card.js
│   │   ├── dues-table.js
│   │   ├── meeting-editor.js
│   │   ├── decision-log.js
│   │   ├── plan-board.js
│   │   ├── contact-card.js
│   │   └── resource-list.js
│   └── css/
│       ├── base.css
│       └── components/
└── docker-compose.yml
```

---

## 4. Data Model

### 4.1 Entity Relationship Summary

```
members ──< dues_invoices ──< transactions
        ──< meeting_attendees >── meetings ──< meeting_decisions
                                            ──< action_items
members ──< action_items (assignee)
plans ──< plan_decisions
      ──< action_items
contacts (standalone directory)
resources (standalone directory)
users (authentication identities; may overlap members)
```

### 4.2 Table Definitions

> **Authoritative schema:** the SQL migrations `internal/db/migrations/0001`–`0006` are the source of truth; the `CREATE TABLE` blocks below are illustrative and already fold in the notable deltas from later migrations:
> - **Roles (0004, 0005):** a `users.role` CHECK constraint, widened in 0005 to `('restricted','member','officer','admin','superadmin')`. CHECK constraints also back the enum columns on `members.status`, `dues_invoices.status`, `meetings.status`, `meeting_decisions.outcome`, `action_items.status`/`priority`, and `plans.status`.
> - **FK delete behaviour (0003):** informational references are `ON DELETE SET NULL` so deleting a meeting/plan/user is not blocked — `action_items.meeting_id`, `action_items.plan_id`, `transactions.recorded_by`, `plan_decisions.decided_by`, and `audit_log.user_id`. Case-insensitive email uniqueness is enforced via a `lower(email)` unique index.
> - **Reminder escalation (0005):** `dues_invoices` gains `reminder_stage INT NOT NULL DEFAULT 0` and `last_reminder_at TIMESTAMPTZ`.
> - **Money (0006):** `dues_invoices.amount` and `transactions.amount` are `BIGINT` integer minor units (see §4.3).

```sql
-- Authentication identities (separate from member profiles)
CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'member',  -- CHECK: 'restricted'|'member'|'officer'|'admin'|'superadmin' (0005)
    member_id     UUID REFERENCES members(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

CREATE TABLE refresh_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked    BOOLEAN NOT NULL DEFAULT FALSE
);

-- Core membership directory
CREATE TABLE members (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name  TEXT NOT NULL,
    email         TEXT,
    phone         TEXT,
    address       TEXT,
    tier          TEXT NOT NULL DEFAULT 'standard', -- 'standard' | 'associate' | 'honorary' | etc.
    status        TEXT NOT NULL DEFAULT 'active',   -- 'active' | 'inactive' | 'suspended'
    joined_at     DATE NOT NULL DEFAULT CURRENT_DATE,
    notes         TEXT,
    metadata      JSONB,   -- extensible key/value bag for org-specific fields
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Dues invoices (one per billing period per member)
CREATE TABLE dues_invoices (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id   UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    amount      BIGINT NOT NULL,   -- integer minor units (e.g. cents); see money note below
    currency    TEXT NOT NULL DEFAULT 'USD',
    period_label TEXT NOT NULL,  -- e.g. "2026 Q1", "Annual 2026"
    due_date    DATE NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending', -- 'pending' | 'paid' | 'partial' | 'overdue' | 'waived'
    reminder_stage   INT NOT NULL DEFAULT 0,  -- 0=none 1=first 2=7-day 3=final (30-day); added 0005
    last_reminder_at TIMESTAMPTZ,             -- added 0005
    notes       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Payment transactions (recorded from payment provider webhooks)
CREATE TABLE transactions (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id            UUID REFERENCES dues_invoices(id),
    member_id             UUID REFERENCES members(id),
    amount                BIGINT NOT NULL,   -- integer minor units; see money note below
    currency              TEXT NOT NULL DEFAULT 'USD',
    provider              TEXT NOT NULL,  -- 'stripe' | 'paypal' | 'square' | 'manual'
    provider_reference_id TEXT,           -- Stripe charge_id, PayPal order_id, etc.
    provider_status       TEXT,           -- raw status from provider
    payment_method_type   TEXT,           -- 'card' | 'ach' | 'check' | 'cash'
    recorded_by           UUID REFERENCES users(id) ON DELETE SET NULL, -- null if from webhook; set if manual entry (0003)
    occurred_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    notes                 TEXT,
    raw_payload           JSONB           -- full provider webhook payload for audit
);

-- Meetings
CREATE TABLE meetings (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title        TEXT NOT NULL,
    scheduled_at TIMESTAMPTZ NOT NULL,
    location     TEXT,
    agenda       TEXT,
    notes        TEXT,   -- free-form markdown meeting minutes
    status       TEXT NOT NULL DEFAULT 'scheduled', -- 'scheduled' | 'completed' | 'cancelled'
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE meeting_attendees (
    meeting_id UUID NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    member_id  UUID NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    present    BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (meeting_id, member_id)
);

-- Formal decisions recorded per meeting
CREATE TABLE meeting_decisions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id  UUID NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    summary     TEXT NOT NULL,
    detail      TEXT,
    vote_for    INT,
    vote_against INT,
    vote_abstain INT,
    outcome     TEXT NOT NULL DEFAULT 'passed', -- 'passed' | 'failed' | 'tabled' | 'noted'
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Action items (cross-cutting: may belong to meeting, plan, or be standalone)
CREATE TABLE action_items (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title        TEXT NOT NULL,
    description  TEXT,
    assignee_id  UUID REFERENCES members(id),
    meeting_id   UUID REFERENCES meetings(id) ON DELETE SET NULL,  -- 0003
    plan_id      UUID REFERENCES plans(id) ON DELETE SET NULL,     -- 0003
    due_date     DATE,
    status       TEXT NOT NULL DEFAULT 'open', -- 'open' | 'in_progress' | 'done' | 'cancelled'
    priority     TEXT NOT NULL DEFAULT 'normal', -- 'low' | 'normal' | 'high'
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Plans / initiatives
CREATE TABLE plans (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       TEXT NOT NULL,
    description TEXT,
    status      TEXT NOT NULL DEFAULT 'draft', -- 'draft' | 'active' | 'completed' | 'archived'
    owner_id    UUID REFERENCES members(id),
    target_date DATE,
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Formal decisions attached to a plan (separate from meeting decisions)
CREATE TABLE plan_decisions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id    UUID NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    summary    TEXT NOT NULL,
    rationale  TEXT,
    decided_by UUID REFERENCES users(id) ON DELETE SET NULL,  -- 0003
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Contact directory (people and organizations outside the membership)
CREATE TABLE contacts (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    organization TEXT,
    email        TEXT,
    phone        TEXT,
    address      TEXT,
    category     TEXT,   -- 'vendor' | 'partner' | 'government' | 'legal' | etc.
    tags         TEXT[],
    notes        TEXT,
    created_by   UUID NOT NULL REFERENCES users(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Resource library (links, documents, references)
CREATE TABLE resources (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       TEXT NOT NULL,
    description TEXT,
    url         TEXT,
    category    TEXT,
    tags        TEXT[],
    added_by    UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Full-text search helper (maintained by triggers or periodic refresh)
CREATE INDEX idx_members_fts   ON members   USING gin(to_tsvector('english', display_name || ' ' || coalesce(email,'')));
CREATE INDEX idx_meetings_fts  ON meetings  USING gin(to_tsvector('english', title || ' ' || coalesce(notes,'')));
CREATE INDEX idx_contacts_fts  ON contacts  USING gin(to_tsvector('english', name || ' ' || coalesce(organization,'')));
CREATE INDEX idx_resources_fts ON resources USING gin(to_tsvector('english', title || ' ' || coalesce(description,'')));
```

### 4.3 Money representation

All monetary amounts (`dues_invoices.amount`, `transactions.amount`) are stored as `BIGINT` **integer minor units** — the smallest unit of the currency (cents for USD, whole yen for JPY, thousandths for BHD). This keeps every sum and comparison exact integer arithmetic, eliminating binary-float rounding error. Converting to a human-readable major-unit value requires the currency's exponent (2 for most currencies, 0 for zero-decimal currencies like JPY/KRW, 3 for a few like BHD/KWD), applied consistently in the Go layer (`model.CurrencyExponent/ParseMoney/FormatMoney`) and the frontend. The API exposes amounts as the integer field `amount_minor` alongside `currency`.

---

## 5. API Design

All endpoints are under `/api/v1/`. Responses are JSON. Errors follow:
```json
{ "error": "human-readable message", "code": "machine_code" }
```

Authentication uses `Authorization: Bearer <jwt>`. Public endpoints: `POST /api/v1/auth/login`.

### 5.1 Auth

| Method | Path | Description |
|---|---|---|
| POST | `/api/v1/auth/login` | Exchange credentials for access + refresh tokens |
| POST | `/api/v1/auth/refresh` | Exchange refresh token for new access token |
| POST | `/api/v1/auth/logout` | Revoke current refresh token |

### 5.2 Members

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/members` | List members (filterable by status, tier; searchable) |
| POST | `/api/v1/members` | Create member |
| GET | `/api/v1/members/:id` | Get member detail |
| PATCH | `/api/v1/members/:id` | Update member fields |
| DELETE | `/api/v1/members/:id` | Soft-delete (sets status=inactive) |
| GET | `/api/v1/members/:id/dues` | Dues history for a member |
| GET | `/api/v1/members/:id/action-items` | Open action items assigned to member |

### 5.3 Dues & Billing

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/dues` | List invoices (filterable by status, period, member) |
| POST | `/api/v1/dues` | Create invoice(s); supports bulk creation for a period |
| GET | `/api/v1/dues/:id` | Invoice detail with transactions |
| PATCH | `/api/v1/dues/:id` | Update status (e.g. mark as waived) |
| POST | `/api/v1/dues/:id/transactions` | Record a manual transaction against an invoice |
| GET | `/api/v1/dues/transactions` | Global transaction log with filters |

### 5.4 Meetings

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/meetings` | List meetings (past and upcoming) |
| POST | `/api/v1/meetings` | Create meeting |
| GET | `/api/v1/meetings/:id` | Full meeting detail (agenda, notes, attendees, decisions, action items) |
| PATCH | `/api/v1/meetings/:id` | Update meeting (agenda, notes, status) |
| PUT | `/api/v1/meetings/:id/attendees` | Set attendance roster |
| POST | `/api/v1/meetings/:id/decisions` | Record a decision |
| PATCH | `/api/v1/meetings/:id/decisions/:did` | Update a decision |
| DELETE | `/api/v1/meetings/:id/decisions/:did` | Remove a decision |

### 5.5 Action Items

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/action-items` | List (filterable by assignee, status, meeting, plan) |
| POST | `/api/v1/action-items` | Create |
| PATCH | `/api/v1/action-items/:id` | Update (status, assignee, due date) |
| DELETE | `/api/v1/action-items/:id` | Delete |

### 5.6 Plans

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/plans` | List plans |
| POST | `/api/v1/plans` | Create plan |
| GET | `/api/v1/plans/:id` | Plan detail (decisions, action items) |
| PATCH | `/api/v1/plans/:id` | Update plan |
| POST | `/api/v1/plans/:id/decisions` | Record a decision on a plan |
| PATCH | `/api/v1/plans/:id/decisions/:did` | Update plan decision |

### 5.7 Contacts

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/contacts` | List (searchable, filterable by category/tag) |
| POST | `/api/v1/contacts` | Create |
| GET | `/api/v1/contacts/:id` | Detail |
| PATCH | `/api/v1/contacts/:id` | Update |
| DELETE | `/api/v1/contacts/:id` | Delete |

### 5.8 Resources

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/resources` | List (searchable, filterable by category/tag) |
| POST | `/api/v1/resources` | Create |
| GET | `/api/v1/resources/:id` | Detail |
| PATCH | `/api/v1/resources/:id` | Update |
| DELETE | `/api/v1/resources/:id` | Delete |

### 5.9 Webhooks

Webhooks are unauthenticated but signature-verified using the provider's secret.

| Method | Path | Description |
|---|---|---|
| POST | `/api/v1/webhooks/stripe` | Receive Stripe events; verify `Stripe-Signature` header |
| POST | `/api/v1/webhooks/paypal` | Receive PayPal IPN/webhook events |

---

## 6. Payment Integration

Quorum never touches card data. The integration is entirely webhook-driven.

### Flow: Stripe (primary)

```
Administrator creates invoice in Quorum
  → Quorum stores invoice (status: pending)
  → Admin generates a Stripe Payment Link or sends a Stripe invoice via Stripe Dashboard
  → Member pays via Stripe-hosted page
  → Stripe fires `payment_intent.succeeded` or `invoice.paid` webhook
  → POST /api/v1/webhooks/stripe
  → Quorum verifies HMAC signature
  → Quorum looks up invoice by metadata or reference id
  → Quorum inserts transaction record
  → Quorum updates invoice status to 'paid' or 'partial'
```

### Webhook handler responsibilities

1. Verify provider signature (reject immediately if invalid — return 400).
2. Check event idempotency key; skip if already processed.
3. Extract: amount, currency, provider reference id, payment method type, timestamp.
4. Match to a `dues_invoice` via `provider_reference_id` stored in invoice metadata, or a lookup table if needed.
5. Insert `transactions` row; store `raw_payload` for audit trail.
6. Recompute invoice status based on sum of successful transactions vs. invoice amount.
7. Trigger email notification to member and/or admin if configured.

### Manual transaction recording

Admins can manually record a transaction (check, cash, wire) via `POST /api/v1/dues/:id/transactions` with `provider: "manual"` and `recorded_by` set to the admin user. This covers legacy payments and edge cases.

---

## 7. Frontend Architecture

### Principles

- No build toolchain. Files are served as-is. JS uses ES modules (`type="module"`).
- Each Web Component is a self-contained `.js` file that registers a custom element.
- Shadow DOM is used for style encapsulation; slots are used for composition.
- Components fetch their own data from the API; no shared state store.
- Light use of browser `CustomEvent` for inter-component communication (e.g., `dues-paid`, `meeting-saved`).

### Routing

A minimal client-side router in `app.js` listens to `hashchange` and populates `<page-outlet>`:

```js
const routes = {
  '#/dashboard':   '<page-dashboard>',
  '#/members':     '<page-members>',
  '#/dues':        '<page-dues>',
  '#/meetings':    '<page-meetings>',
  '#/plans':       '<page-plans>',
  '#/contacts':    '<page-contacts>',
  '#/resources':   '<page-resources>',
  '#/settings':    '<page-settings>',
};
```

### Component inventory

| Component | Description |
|---|---|
| `<app-shell>` | Top-level layout: sidebar nav + content area |
| `<nav-bar>` | Sidebar navigation; highlights active route |
| `<page-dashboard>` | Summary: overdue dues count, upcoming meetings, open action items |
| `<page-members>` | Searchable member list + inline add/edit |
| `<member-card>` | Compact member display with dues-status badge |
| `<member-detail-modal>` | Full member profile, dues history, action items |
| `<page-dues>` | Invoice list with period filter; bulk create invoices |
| `<dues-table>` | Sortable/filterable table of invoices |
| `<invoice-detail-modal>` | Invoice with transactions; manual transaction form |
| `<payment-status-badge>` | Color-coded pill: pending / overdue / paid / waived |
| `<page-meetings>` | Meeting list (upcoming + past) |
| `<meeting-card>` | Meeting summary with status, date, attendee count |
| `<meeting-editor>` | Full meeting form: agenda (textarea), notes (markdown), attendance checklist, decisions list |
| `<decision-item>` | Inline form for capturing a single decision with vote counts |
| `<action-item-list>` | Filterable action items; inline status updates |
| `<action-item-row>` | Single action item with assignee, due date, status toggle |
| `<page-plans>` | Plans list; create plan button |
| `<plan-detail>` | Plan page: description, decision log, linked action items |
| `<page-contacts>` | Searchable contact directory |
| `<contact-card>` | Contact display; click to expand detail |
| `<page-resources>` | Resource library with category tabs |
| `<resource-row>` | Resource with title, tags, external link |
| `<search-bar>` | Global full-text search across all entity types |
| `<confirm-dialog>` | Reusable confirmation modal |
| `<toast-notification>` | Transient success / error messages |

### Example component skeleton

```js
// web/components/member-card.js
class MemberCard extends HTMLElement {
  static observedAttributes = ['member-id'];

  connectedCallback() {
    this.attachShadow({ mode: 'open' });
    this.shadowRoot.innerHTML = `
      <link rel="stylesheet" href="/css/components/member-card.css">
      <div class="card"><slot name="loading">Loading...</slot></div>
    `;
    if (this.hasAttribute('member-id')) this.#load();
  }

  async #load() {
    const res = await fetch(`/api/v1/members/${this.getAttribute('member-id')}`, {
      headers: { Authorization: `Bearer ${getToken()}` }
    });
    const member = await res.json();
    this.#render(member);
  }

  #render(member) {
    this.shadowRoot.querySelector('.card').innerHTML = `
      <span class="name">${member.display_name}</span>
      <payment-status-badge status="${member.dues_status}"></payment-status-badge>
    `;
  }
}

customElements.define('member-card', MemberCard);
```

---

## 8. Role-Based Access Control

Five roles are enforced at the handler layer (ascending privilege):

| Role | Capabilities |
|---|---|
| `restricted` | Sees only its own linked member record: own profile, dues, and assigned action items. No access to the shared directory, meetings, plans, contacts, resources, or dashboard. Must be linked to a member (`member_id`) to see anything. |
| `member` | Read-only on all shared resources (directory, meetings, plans, contacts, resources, dashboard). |
| `officer` | Read/write meetings, plans, contacts, resources, dues, action items; read members; cannot manage users. |
| `admin` | Officer plus user management and member deactivation (a reversible soft-delete). |
| `superadmin` | Full access, including permanent deletion of core records and user accounts. |

RBAC is checked in middleware that reads the `role` claim from the JWT; the JWT also carries the account's `member_id`, which scopes what a `restricted` user may read. No UI is shown for actions the user cannot perform (frontend hides controls; backend enforces).

**Design note (v1 resolution):** an earlier draft scoped `member` to "own data only." That capability now lives in the dedicated `restricted` role; the default `member` role has full read visibility, matching how a staff-facing back-office tool is used. Grant `restricted` per user for people (e.g. the general membership) who should see only their own record.

**Destructive deletes** of core records (meetings, plans, contacts, resources, action items) and user accounts are reserved for `superadmin` and are heavily gated: a type-to-confirm step (the request must echo the record's exact name), an audit-log entry, and an email notification to all admins/superadmins. Assigning the `superadmin` role is itself superadmin-only, and no user can change their own role.

---

## 9. Configuration

The application is configured via environment variables (12-factor):

```
QUORUM_PORT=8080
QUORUM_DATABASE_URL=postgres://user:pass@localhost:5432/quorum
QUORUM_JWT_SECRET=<random 64 bytes hex>
QUORUM_JWT_ACCESS_TTL=15m
QUORUM_JWT_REFRESH_TTL=168h   # Go duration; "7d" is NOT accepted by time.ParseDuration

QUORUM_SMTP_HOST=smtp.example.com
QUORUM_SMTP_PORT=587
QUORUM_SMTP_USER=...
QUORUM_SMTP_PASS=...
QUORUM_EMAIL_FROM=quorum@example.org

QUORUM_STRIPE_WEBHOOK_SECRET=whsec_...
QUORUM_PAYPAL_WEBHOOK_ID=...
QUORUM_ALLOW_UNSIGNED_WEBHOOKS=false   # dev only; process webhooks without signature verification
QUORUM_TRUST_PROXY_HEADERS=false       # key rate limiting on X-Real-IP/X-Forwarded-For (trusted proxy only)

QUORUM_BASE_URL=https://quorum.example.org
```

Configuration is environment-variables only — there is no config file. The authoritative list of variables lives in `internal/config/config.go`.

---

## 10. Notifications

A background goroutine runs a nightly job that:

1. Queries `dues_invoices` where `due_date < now()` and `status = 'pending'`; marks them `overdue`.
2. Sends escalating email reminders to members whose dues are overdue: a first notice, a 7-day follow-up, and a 30-day final notice. Each invoice carries a `reminder_stage` so every member receives each notice at most once; the stage advances only after a successful send.
3. Sends a digest email to all admins and superadmins summarizing the overdue count.
4. Prunes expired/revoked refresh tokens, old processed webhook events, and aged audit-log rows.

Email templates are plain-text by default; HTML email template support is optional and added later if desired.

---

## 11. Security Considerations

- **Passwords**: bcrypt with cost factor 12.
- **JWT**: Short-lived access tokens (15 min); refresh tokens stored as plain SHA-256 digests (not HMAC).
- **Webhook signatures**: Stripe uses `stripe-signature` header (HMAC-SHA256); PayPal uses certificate-based verification. Requests with invalid signatures are rejected with 400 and logged.
- **SQL injection**: All queries use `pgx` prepared statements and `$1` placeholders; no string interpolation in SQL.
- **CORS**: Strict allowlist; same-origin by default since the Go server serves the frontend.
- **CSP**: `Content-Security-Policy` header disallows `eval` and inline scripts; all JS loaded as modules from `/` origin.
- **HTTPS**: TLS termination at a reverse proxy (nginx, Caddy, or cloud load balancer). The Go server binds `:PORT` on all interfaces; restricting exposure to localhost is done by the container port mapping (e.g. `127.0.0.1:8080:8080`), not by the app.
- **Audit log**: Sensitive writes (invoices, transactions, user changes) are logged with `user_id`, `action`, `entity_id`, `before/after` to an `audit_log` table.
- **Rate limiting**: Login endpoint is rate-limited (10 attempts / minute per client key) via a simple in-process sliding-window limiter. By default the key is the raw socket address; set `QUORUM_TRUST_PROXY_HEADERS=true` behind a trusted proxy to key on `X-Real-IP`/`X-Forwarded-For` instead.

---

## 12. Database Migrations

Migrations are numbered SQL files in `migrations/` and applied at startup via an embedded migration runner (no external tool required in production):

```
migrations/
  0001_initial_schema.up.sql
  0001_initial_schema.down.sql
  0002_add_plans.up.sql
  0002_add_plans.down.sql
  ...
```

A `schema_migrations` table tracks which have been applied. The runner acquires a PostgreSQL advisory lock before migrating to prevent race conditions on multi-instance startups.

---

## 13. Deployment

### Single-host (simplest)

```
systemd unit or screen/tmux session
  → ./quorum (single binary, serves API + frontend)
  → PostgreSQL 16 (local or managed)
  → nginx (TLS termination, optional)
```

### Docker Compose

```yaml
services:
  quorum:
    build: .
    env_file: .env
    ports: ["127.0.0.1:8080:8080"]
    depends_on: [db]

  db:
    image: postgres:16-alpine
    volumes: [pgdata:/var/lib/postgresql/data]
    environment:
      POSTGRES_DB: quorum
      POSTGRES_USER: quorum
      POSTGRES_PASSWORD: ${DB_PASSWORD}

volumes:
  pgdata:
```

A Caddy or nginx container handles TLS and proxies to `quorum:8080`.

---

## 14. Future Considerations (Out of Scope for v1)

- Document file uploads (meeting attachments, signed agreements) — would add S3-compatible object storage.
- Member self-service portal: members log in, view their own dues, update contact info.
- Public-facing payment link generation (currently delegated entirely to Stripe dashboard).
- Recurring billing schedules (auto-generate invoices on a cadence).
- Export to CSV / PDF for accountants.
- Granular audit log viewer in the UI.
- Two-factor authentication.
- Multi-organization / tenant support.

---

## 15. Open Questions

1. **Member self-registration**: Should prospective members be able to apply online, or is membership manually entered by an admin only? The v1 design assumes admin-managed.
2. **Payment link generation**: Should Quorum generate Stripe Payment Links via API, or should admins create them in Stripe and paste the reference into Quorum? The v1 design defers to the Stripe dashboard to reduce API surface and key management complexity.
3. **Markdown rendering**: Should meeting notes / plan descriptions render as formatted HTML in the UI? If yes, a client-side markdown parser (e.g., `marked.js`, a single file) would be appropriate.
4. **Dues tiers**: Should different membership tiers have different default dues amounts baked into the application, or is each invoice amount set manually?
5. **Mobile support**: Is a responsive layout sufficient, or is there a need for touch-optimized interactions?
