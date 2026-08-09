---
title: "Quorum — User & Operations Manual"
subtitle: "Self-hosted membership, dues, meetings, and planning management"
author: "Quorum Project"
date: "Version 1.1 — August 2026"
toc: true
toc-depth: 3
numbersections: false
colorlinks: true
geometry: margin=1in
papersize: letter
---

# Quorum — User & Operations Manual

Quorum is a self-hosted web application that helps an organization — a club,
association, cooperative, board, or small business — run its proceedings:
collect and track membership dues, record meeting decisions, coordinate plans
and action items, and keep a living directory of members, contacts, and
resources.

This manual is written **task-first and role-first**. Each chapter follows a
"user story": it takes one kind of user — a rank-and-file member, an officer, an
administrator, or the DevOps engineer who deploys the system — and walks through
everything that person needs to do, step by step.

> **About this edition.** This manual describes Quorum as of the version noted
> above. Screens are described rather than shown; the application is a single-page
> web app, so exact pixel positions may vary, but the labels, buttons, and flows
> match what you will see.

## How this manual is organized

- **Part 1 — Introduction.** What Quorum is, the people who use it, the shape of
  the data, and the permission model.
- **Part 2 — Getting started.** Signing in, the workspace, sessions, passwords.
- **Parts 3–7 — User stories by role.** Member (self-service), member (viewer),
  officer, administrator, super-administrator.
- **Parts 8–9 — Cross-cutting topics.** Payments integration and the email /
  nightly-automation cycle.
- **Part 10 — The System Administrator / DevOps Engineer.** Deploying,
  configuring, securing, backing up, upgrading, and troubleshooting Quorum.
- **Part 11 — Appendices.** Full permission matrix, value glossaries, environment
  reference, accessibility notes, how to export this manual to PDF, and support.

> **Tip — reading the PDF.** If you generated this document with `pandoc`, a
> clickable table of contents with page numbers appears on the following pages.
> See **Appendix G** for the exact command.

\newpage

# Part 1 — Introduction

## 1.1 What Quorum is (and is not)

Quorum is **institutional memory plus light operations** for an organization:

- **Membership & dues.** A directory of members and the invoices they owe, with
  payment tracking. Quorum records the *result* of payments; it never touches
  card data itself (see Part 8).
- **Meetings.** Agendas, minutes, attendance, and formally recorded decisions
  with vote counts.
- **Plans.** Longer-running initiatives, each with its own decision log.
- **Action items.** Tasks assigned to members, optionally tied to a meeting or a
  plan.
- **Contacts & resources.** A directory of outside people/organizations and a
  library of links and reference material, with per-resource **visibility
  groups** when some material should reach only some members.
- **Governance.** Formal **motions** (with seconds, discussion, and votes), a
  Robert's-Rules **minutes journal** that can be finalized into an immutable
  record, live meeting quorum tracking, and asynchronous email **ballots**
  with proxies.
- **Boards.** A **sprint board** and a **kanban board** for scoping, assigning,
  and tracking work.
- **Reports & accountability.** Watermarked, integrity-sealed **PDF reports**;
  every export — and every change to anything — lands in a tamper-evident,
  hash-chained **audit log**.

Quorum is deliberately **not** a general-purpose CRM, a full accounting package,
or a project-management suite. It aims to be operable by a single administrator
without specialized DevOps expertise, and it deploys as a single binary with a
PostgreSQL database.

## 1.2 Who this manual is for (the personas)

Quorum has five user roles, in ascending order of privilege. This manual maps
each to a "user story":

| Persona | Role | In one sentence |
|---|---|---|
| **Member (self-service)** | `restricted` | Signs in only to see their own dues, profile, and assigned tasks. |
| **Member (viewer)** | `member` | Reads all shared organizational records but changes nothing. |
| **Officer** | `officer` | The operational workhorse: creates and edits members, invoices, meetings, plans, action items, contacts, and resources. |
| **Administrator** | `admin` | An officer who can also manage user accounts and deactivate members. |
| **Super-Administrator** | `superadmin` | Full authority, including permanent deletion of records and granting the top role. |
| **System Administrator / DevOps** | (operates the server) | Deploys, configures, secures, backs up, and upgrades Quorum. |

The first five are *application* roles stored in Quorum. The last is an
*operational* role — the person with shell/cluster access. One human may wear
several hats.

## 1.3 The big picture: how Quorum's data fits together

In plain language:

- The organization has **members**. Each member may owe **dues invoices**, and
  each invoice accumulates **payment transactions** until it is paid.
- The organization holds **meetings**. A meeting has an **attendance** list and
  records formal **decisions** (with vote counts).
- The organization pursues **plans**. A plan has its own **decision log**.
- **Action items** are tasks assigned to a member; each can be linked to a
  meeting or a plan, or stand alone.
- **Contacts** (outside people/orgs) and **resources** (links/documents) are
  standalone directories.
- **User accounts** are the login identities. A user account is *separate* from a
  member profile, but the two can be **linked** (this is what lets a
  self-service member see "their own" data — see 6.3).

## 1.4 Roles and permissions at a glance

The five roles form a strict ladder — each level can do everything the level
below it can, plus more:

`restricted` -> `member` -> `officer` -> `admin` -> `superadmin`

| Capability | restricted | member | officer | admin | superadmin |
|---|:--:|:--:|:--:|:--:|:--:|
| See only own record (profile, dues, tasks) | Yes | Yes | Yes | Yes | Yes |
| Read the full directory & all shared records | | Yes | Yes | Yes | Yes |
| View the dashboard | | Yes | Yes | Yes | Yes |
| Create / edit members, meetings, plans, etc. | | | Yes | Yes | Yes |
| Create invoices & record payments | | | Yes | Yes | Yes |
| Record & edit meeting/plan decisions | | | Yes | Yes | Yes |
| Manage user accounts & link members | | | | Yes | Yes |
| Deactivate (soft-delete) a member | | | | Yes | Yes |
| **Permanently delete** meetings/plans/contacts/resources/action-items/users | | | | | Yes |
| Grant or revoke the `superadmin` role | | | | | Yes |

> **Note.** A `restricted` user is the exception to "each level reads more":
> they read *less* — only their own linked record. Use `restricted` for the
> general membership; use `member` and above for staff/officers who are trusted
> to see everyone's data. A full, endpoint-by-endpoint matrix is in **Appendix A**.

## 1.5 Conventions used in this manual

- **Bold** marks on-screen labels, buttons, and menu items.
- `Monospace` marks values you type, file names, configuration keys, and
  commands.
- Numbered lists are **task recipes** — do the steps in order.
- Callouts:
  > **Note.** Important clarification.
  >
  > **Tip.** A shortcut or best practice.
  >
  > **Warning.** Something that can cause data loss, a security problem, or a
  > support call if ignored.

\newpage

# Part 2 — Getting Started (all users)

## 2.1 Signing in

1. Open the Quorum URL your administrator gave you (for example
   `https://quorum.your-org.example`).
2. Enter your **email** and **password**.
3. Select **Sign in**.

On success you land on the page appropriate to your role: self-service members
land on **My Account**; everyone else lands on the **Dashboard**.

> **Note — no self-registration.** You cannot create your own account. An
> administrator creates it for you and tells you your initial password. There is
> no public "sign up" page by design.

## 2.2 The workspace: navigation & layout

After signing in you see a persistent **sidebar** on the left and a **content
area** on the right. The sidebar shows only the sections your role can use:

- A self-service (`restricted`) member sees **My Account** and **Sign out** —
  nothing else.
- A `member` sees **Dashboard, Members, Meetings, Plans, Contacts, Resources**.
- An `officer` additionally sees **Dues**.
- An `admin`/`superadmin` additionally sees **Settings** (user management).

Selecting a sidebar item swaps the content area to that page. The web address
uses a `#/…` fragment (for example `#/members`); you can bookmark these.

## 2.3 Your session, staying signed in, and signing out

- When you sign in, Quorum issues a short-lived **access token** (about 15
  minutes) and sets a longer-lived **refresh cookie** (about 7 days,
  `HttpOnly`). The app renews your access token silently in the background, so a
  normal working session does not interrupt you.
- If you close the browser and return within the refresh window, you remain
  signed in. After the refresh window expires you must sign in again.
- Select **Sign out** to end your session immediately. This revokes your refresh
  token on the server.
- **Idle timeout.** After roughly **30 minutes of inactivity** you are signed
  out automatically (a warning appears about a minute before). The server
  enforces this — walking away from an unlocked screen has a deadline.
- **The faint watermark.** Signed-in pages carry a barely-visible diagonal
  tiling of *your own email address*. It is deliberate: screenshots and photos
  of the screen become attributable to the account that took them. It does not
  appear in any exported document's content (those get their own stamp, 5.10).

> **Tip.** Changing your password (2.4) signs out **all your other sessions** —
> useful if you think someone else has access.

## 2.4 Changing your password

1. From the account/settings area, choose **Change password**.
2. Enter your **current password**, then your **new password** (minimum 10
   characters), and confirm.
3. Select **Save**.

All of your *other* active sessions are terminated; your current one continues.

## 2.5 If you can't sign in

- **Wrong password.** Quorum gives the same generic "invalid credentials" message
  whether the email is unknown or the password is wrong (this protects privacy).
  Re-check both.
- **Too many attempts.** Sign-in is rate-limited (about 10 attempts per minute
  from one location). Wait a minute and try again.
- **Forgot your password.** Use **Forgot your password?** on the sign-in page:
  enter your email, and follow the single-use link Quorum sends you (it expires
  quickly; the page shows the same message whether or not the email exists, on
  purpose). If your organization has not configured outgoing email, ask an
  administrator — they can set you a temporary password from **Settings →
  Users** (6.6).
- **Lost your second factor.** If you enabled 2FA and lost the authenticator,
  sign in with one of your **recovery codes** (2.6). Lost those too? The
  server operator can clear your 2FA from the command line — deliberately the
  last resort (see `RUNBOOK.md`).

## 2.6 Two-factor authentication (2FA)

A second factor means a stolen password alone cannot open your account.
Quorum uses standard **TOTP** — the six-digit codes from any authenticator app
(Aegis, Google Authenticator, 1Password, …).

**Enable it (recommended for everyone, expected of officers and above):**

1. Open your account/security settings and choose **Two-factor
   authentication → Set up**.
2. Re-enter your **password** (enrolling a second factor is itself a sensitive
   action).
3. Scan the QR code with your authenticator app and type the six-digit code it
   shows to confirm.
4. Quorum displays your **recovery codes — once**. Store them in a password
   manager or print them. Each works exactly one time, and they are the only
   self-service way back in if you lose the authenticator.

**Signing in with 2FA:** password first, then the six-digit code. Repeated
wrong codes lock 2FA attempts briefly (per account) — wait a minute.

**Lost device:** sign in with a recovery code, then disable and re-enroll 2FA
with the new device. You can regenerate a fresh set of recovery codes (this
invalidates the old set) from the same settings area — password required.

### 2.6a When your organization requires 2FA

Admins can mandate two-factor for accounts at or above a chosen role
(Settings → Organization settings → `require_2fa`). Under a mandate, an
account without 2FA can still sign in — but every screen redirects to a
guided setup page until enrollment is complete (about two minutes; the
policy is enforced by the server, not the browser). Enrollment lifts the
requirement immediately. Lost-device recovery is unchanged: recovery codes
first, operator break-glass as the last resort — which clears enrollment,
so the mandate simply walks that person through setup again at next login.

## 2.7 Your active sessions

Your account settings list every device currently signed in as you — with
approximate location/address and last-used time. If anything looks unfamiliar:

1. Select **Sign out other sessions** — every session except your current one
   is revoked within moments.
2. Change your password (2.4), which does the same thing as a side effect.
3. If you suspect more than a stray login, tell an administrator — the audit
   log (6.8) shows exactly what the account did.

## 2.8 Applying to join (no account needed)

Prospective members don't need a login to apply. From the sign-in screen,
**Apply to join** opens a short public form — name, email, and an optional
message. Submitting it drops the application into the organization's review
queue; an officer sees it on the **Roster** page (5.12) and can approve it,
which creates the member record, or decline it. The form is rate-limited to
prevent abuse and never exposes anything about the organization.

\newpage

# Part 3 — User Story: The Member (Self-Service / `restricted`)

> *"As a member of the organization, I want to sign in and see only my own dues
> and tasks, without access to everyone else's information."*

The `restricted` role is designed for the general membership. A restricted user
sees a single page — **My Account** — and nothing else. Every shared/org-wide
screen is off-limits.

## 3.1 The My Account page

**My Account** gathers, read-only, everything that is *yours*:

- **Your profile** — your name and contact details as held in the membership
  directory.
- **Your dues** — each invoice issued to you, its amount and currency, due date,
  and a colored **status** badge (see the status glossary in Appendix B):
  *pending, paid, partial, overdue, waived*.
- **Your action items** — tasks assigned to you, with due dates, priority, and
  status.

## 3.2 What a self-service member can and cannot do

- **Can:** view their own profile, invoices/payment status, and assigned tasks;
  change their own password; sign out.
- **Cannot:** see other members, the directory, meetings, plans, contacts,
  resources, or the dashboard; create or edit anything; record payments.

To *pay* an invoice, follow whatever external instructions your organization
provides (for example a Stripe payment link emailed to you). Quorum records the
payment automatically once it clears (Part 8); your **My Account** status updates
accordingly.

## 3.3 "Your login isn't linked to a member record yet"

If **My Account** shows a message like *"Your login isn't linked to a member
record — contact an administrator,"* it means your user account exists but has
not been connected to your entry in the membership directory. Until an
administrator links them (6.3), Quorum cannot show "your" data. Ask an
administrator to link your account to your member profile.

\newpage

# Part 4 — User Story: The Member (Viewer / `member`)

> *"As a staff member or officer-in-training, I want to read all of the
> organization's records without the ability to change them."*

The `member` role is a **read-only view of everything shared**. Grant it to
people who need visibility across the organization but should not make changes.

## 4.1 The Dashboard

The **Dashboard** is a summary at-a-glance:

- **Overdue invoices** — how many invoices are past due (highlighted if > 0).
- **Pending invoices** — how many are awaiting payment.
- **Active members** — current active headcount.
- **Upcoming meetings** — the next few scheduled meetings.
- **Open action items** — the current open tasks.

## 4.2 Browsing shared records

A viewer can open and read:

- **Members** — the full directory, searchable by name/email and filterable by
  status and tier. Each member shows contact details and dues status.
- **Meetings** — past and upcoming meetings, with agenda, minutes, attendance,
  and recorded decisions.
- **Plans** — initiatives with their descriptions and decision logs.
- **Contacts** — the outside-contacts directory, searchable and filterable by
  category and tag.
- **Resources** — the link/document library, searchable and filterable by
  category and tag.

## 4.3 Discussions (channels & threads)

**Discussions** is the org's own place to talk — channels like a chat tool,
kept inside Quorum where your governance and documents already live.

- **Anyone (member and above) can create a channel** (+ New channel) and
  **any channel member can add people** (+ Add people). You read and post
  only in channels you belong to; other channels are listed by name so you
  know what exists and whom to ask. Admins can open any channel (moderation).
- **Threads.** Reply "in thread" on any message to keep side-conversations
  from drowning the channel — one level deep, like Slack.
- **No file uploads — by design.** The 📄 button on the composer instead
  links a document from the **Resources** library. This is a feature, not a
  gap: documents keep their visibility rules, download ledger, and audit
  trail. A linked document a reader isn't cleared for shows as "a document
  you don't have access to" — not even its title leaks.
- Authors can delete their own messages; admins can moderate. Deleting a
  message removes its thread; deleting a channel (creator or admin,
  type-to-confirm) removes its whole history.

## 4.4 Searching and filtering

Where a page offers a search box or filters, they narrow the list as you type or
select. Search covers the most useful text fields (names, titles,
organizations, descriptions). Filters (status, tier, category, tag) refine
further. Nothing a viewer does changes any data.

\newpage

# Part 5 — User Story: The Officer (Day-to-Day Operations)

> *"As an officer, I want to run the organization's records: enroll members,
> bill dues, record meetings and decisions, track tasks, and maintain our
> directories."*

The `officer` role is where most real work happens. Officers can create and edit
almost everything, but **cannot** manage user accounts, deactivate members, or
permanently delete records (those are admin/superadmin actions).

## 5.1 Managing the membership directory

**Add a member**

1. Go to **Members**.
2. Select **+ Add member**.
3. Fill in **Display name** (required) and any of: email, phone, address,
   **tier** (e.g. `standard`), **status** (`active`, `inactive`, `suspended`),
   join date, and notes.
4. Select **Save**.

**Edit a member**

1. On **Members**, open the member and select **Edit**.
2. Change any fields and **Save**. Only the fields you change are updated.

> **Note.** Officers can create and edit members but cannot *deactivate* them —
> that is an administrator action (6.5). "Deleting" a member is a reversible
> deactivation, never a hard delete.

## 5.2 Dues & billing

### 5.2.1 How money works in Quorum (read this first)

- Every invoice and payment has an **amount** and a **currency** (e.g. `USD`,
  `EUR`, `JPY`).
- In the interface you always type amounts the natural way — `100.00`,
  `49.50`, `1000` — and Quorum stores them exactly (internally as whole minor
  units, so there is never a rounding error). You do not need to think about
  minor units as a user; just type the amount and pick the currency.
- Currencies with no minor unit (such as `JPY`) are entered as whole numbers;
  most others use two decimals; a few use three. Quorum knows the right number
  of decimals per currency.

> **Warning — one currency per invoice.** A payment must be in the **same
> currency** as the invoice it applies to. Quorum rejects a mismatched-currency
> payment, and only same-currency payments count toward an invoice's paid status.

Every direction money can flow has a home, and each posts to the general
ledger automatically:

| Money flows… | Use | Where |
|---|---|---|
| Member → organization (dues) | Member invoice | **Dues** |
| Customer → organization (services you sold) | Contact invoice (Bill to → Outside customer) | **Dues** (5.2.2) |
| Organization → vendor (services you bought) | Vendor bill | **Payables** (5.2.8) |
| Organization → member (reimbursement) | Bill payable to the member (added as a contact), or a fund purchase | **Payables** (5.2.8) / **Funds** (6.6a) |

### 5.2.2 Create a single invoice

1. Go to **Dues** and select **+ New invoice**.
2. Under **Bill to**, keep the default **Member(s)** for dues, or switch to
   **Outside customer (contact)** to invoice someone who is not a member —
   a client you provided services to, for example. Contact invoices post to
   **Service Income** on the books instead of dues income; everything else
   (payments, statuses, waivers) works the same.
3. Pick the members from the searchable checkbox list (type to filter; **All
   shown** / **None** for bulk selection — one invoice is created per member
   selected), or pick the customer from the dropdown. Enter the **amount**
   (e.g. `100.00`), the
   **currency** (3-letter code, default `USD`), a **period label** (e.g.
   `2026 Q1`, `Annual 2026`, or an invoice reference like `INV-0042`), and a
   **due date**.
4. Optionally add notes. Select **Save**. The invoice starts as **pending**.

> **Which direction does the money flow?** Invoices (member or contact) are
> money **owed to** the organization. Money the organization **owes to
> someone else** — a vendor bill, or reimbursing a member who bought
> something out of pocket — is recorded under **Payables** (see 5.2.8).

### 5.2.3 Create invoices in bulk

To bill many members the same amount for the same period in one action, just
select them all in the member list of the same **+ New invoice** modal — filter,
use **All shown**, untick exceptions. Quorum creates one invoice per member.

### 5.2.4 Record a payment

Most online payments are recorded automatically by the payment integration
(Part 8). To record a payment **manually** (cash, check, wire, or a legacy
payment):

1. Open the invoice on **Dues**.
2. Select **+ Payment** (record transaction).
3. Enter the **amount** (same currency as the invoice), choose a **provider**
   (e.g. `manual`), and optionally a reference, method, and note.
4. Select **Save**. Quorum recomputes the invoice status.

### 5.2.5 Understanding invoice status

Quorum sets an invoice's status from its payments and due date:

| Status | Meaning |
|---|---|
| **pending** | Issued, no (qualifying) payment yet, not past due. |
| **partial** | Some payment received, but less than the full amount. |
| **paid** | Payments (in the invoice currency) total at least the amount. |
| **overdue** | Past its due date and unpaid (set automatically each night). |
| **waived** | Manually forgiven; excluded from paid/overdue calculations. |

### 5.2.6 Waive or adjust an invoice

To forgive an invoice, change its status to **waived** (a confirmation is
requested because it changes the invoice's financial state). Waived and paid
invoices are not re-aged to overdue.

**Record a refund.** Open the invoice (click its row) and use **Record refund**.
Enter the amount (in the invoice's currency — a refund must match it), the
provider/method, and an optional reason. Quorum posts a reversing entry to the
ledger and recomputes the invoice's status, so your books and the invoice both
reflect the money going back out. Refunds are just negative transactions; they
show in the invoice's transaction list.

**Offer a payment plan (installments).** In the same invoice view, under
**Payment plan**, split a large invoice into scheduled partial payments: add a
row per installment with a due date and amount. This is a *tracking aid only* —
the invoice's own paid/partial status stays authoritative and payments are still
recorded as transactions in the usual way. Saving an empty list clears the plan.

### 5.2.7 Recurring dues schedules

For dues billed on a rhythm (annual, quarterly, monthly), create a **dues
schedule** instead of remembering to bulk-invoice: choose the members, amount,
currency, and cadence, and the nightly cycle generates each period's invoices
automatically — exactly once per member per period, even across restarts.
Pause or edit the schedule any time; already-issued invoices are unaffected.

### 5.2.8 Payables: money the organization owes (vendor bills & reimbursements)

The **Payables** page (officer and up) is the mirror image of invoicing: it
records what the organization owes an outside party — a vendor's bill, or a
member who paid for something out of pocket and needs reimbursing. (For a
member reimbursement, first add the member to **Contacts** so they can be a
payee; the contact entry represents them as a counterparty, separate from
their membership record.)

1. **Record the bill** with **+ New bill**: choose the vendor (from Contacts),
   amount, currency, the **expense account** it belongs to (from your chart of
   accounts), and optionally a bill date, due date, and memo. Recording a bill
   immediately puts the liability on the books — debit the expense, credit
   **Accounts Payable** — so your balance sheet is honest even before you pay.
2. **Pay it** when the money actually moves: choose the source — **operating
   cash** (with the payment method, so the cash posts to the right
   provider-routed account) or a **fund** (refused if the fund doesn't hold
   enough). Paying posts debit A/P / credit cash and marks the bill **paid**.
3. **Void** (admin only) reverses an open bill with a mirroring journal entry
   if it was recorded in error.

Bills are permanent records: they cannot be deleted, and a paid or void bill
is frozen by the database. If you keep **cash-basis** books, unpaid bills
simply don't appear in your statements until paid — the accrual entry touches
no cash account, so both presentations stay correct with no extra work. Open
bills appear in the **Payables aging** block on the Accounting page and in the
CPA statements export.

## 5.3 Meetings

### 5.3.1 Create a meeting

1. Go to **Meetings** and select **+ New meeting**.
2. Enter a **title** and a **date & time**. Optionally add a **location**, an
   **agenda**, and **minutes/notes**.
3. Select **Save**. New meetings start with status **scheduled**.

> **Note — times.** Enter the date and time in your **local** time; Quorum stores
> it consistently and displays it back in local time. A meeting may also have an
> **end time** (used by the calendar and the calendar-file export). Text fields
> (agenda, minutes) are stored as plain text — formatting is not rendered.

> **Tip — the calendar.** The Meetings page includes a **month-grid calendar**;
> select any day to schedule a meeting on it. Every meeting offers an **.ics
> download** — the standard calendar file that Google/Apple/Outlook calendars
> import, complete with start and end times.

### 5.3.2 Record attendance

You can pick attendees right in the **Schedule meeting** form (the optional
**Attendees** block at the bottom), or afterwards: open the meeting and use
the **Attendance** panel (right-hand column). Both use the same picker. It lists
every active member with a checkbox, plus bulk-select tools so you rarely have
to click one by one:

- **All** / **None** — check or clear everyone, then fine-tune individually
  (e.g. select everyone and untick the two who were absent).
- **Officers+** — adds every member whose login holds the officer role or
  higher.
- **+ tier…** — adds everyone in a membership tier (standard, premium, …).
- **+ group…** — adds everyone in a visibility group (see 6.7).

Bulk tools only ever *add* (except **None**), so you can layer them: Officers+,
then a group, then untick individuals. Select **Save attendance** to replace
the meeting's roster with the current selection.

### 5.3.3 Record decisions and votes

Within a meeting, add a **decision** for each formal motion:

- **Summary** (required) and optional **detail**.
- Vote counts: **for**, **against**, **abstain** (a count of `0` is valid and is
  recorded as zero, not blank).
- **Outcome**: `passed`, `failed`, `tabled`, or `noted`.

Decisions can be edited or removed later by an officer.

### 5.3.4 Meeting lifecycle

Update a meeting's **status** to `completed` or `cancelled` as appropriate. The
meeting, its attendance, and its decisions remain the organization's record.

> **Warning — deletion.** Permanently deleting a meeting removes its attendance
> and decision records with it and is reserved for super-administrators (Part 7).
> Prefer marking a meeting `cancelled` over deleting it.

### 5.3.5 The recording secretary: minutes journal & motions

For organizations that run on Robert's Rules, each meeting has a **Minutes**
section that acts as a recording secretary:

- **Journal entries.** Append typed entries as the meeting proceeds — kinds:
  `call_to_order`, `previous_minutes`, `report`, `old_business`,
  `new_business`, `discussion`, `point_of_order`, `recess`, `adjournment`,
  and free-form `note`. Entries keep their chronological order.
- **Motions.** Record each motion with its title and detail, whether it is
  **new or old business**, its **second**, linked discussion entries, the
  **vote counts**, and the result. Motions appear in the generated minutes
  alongside the journal.
- **Generate the minutes document.** One action produces the formal minutes —
  attendance, proceedings, motions with votes, decisions — as both a Markdown
  document and a PDF (watermarked and integrity-sealed like every export, 5.10).
- **Finalize.** When the body approves the minutes, **finalize** them. This is
  enforced *in the database*: a finalized journal rejects all further inserts,
  edits, and deletions — even from direct SQL — and finalization itself is
  one-way. Finalize only after approval; there is no unfinalize.
- **Preview as a heat map.** The 🔥 button renders the minutes as a
  word-frequency heat map (5.7) — a fast gist of a long meeting, entirely
  in your browser. A preview is not an export: nothing is downloaded or logged.
- **Recuse yourself (conflict of interest).** On any live motion, **Recuse
  myself** records that you are stepping back from the vote for a conflict of
  interest, with an optional reason. The recusal is shown on the motion so it can
  be captured in the minutes. Recusing is advisory — it does not block you from
  voting — but it creates the auditable record good governance expects. The same
  control appears on fund **purchases** (6.6a).

## 5.4 Plans & initiatives

1. Go to **Plans** and select **+ New plan**.
2. Enter a **title**; optionally a description, an **owner** (a member), a
   **target date**, and a **status** (`draft`, `active`, `completed`,
   `archived`).
3. Within a plan, record **plan decisions** — each with a summary and optional
   rationale — to keep a decision log distinct from meeting minutes.

Prefer setting a finished plan to `completed` or `archived` rather than deleting
it.

## 5.5 Action items

Action items are tasks:

1. Go to **Action items** (or create one from context).
2. Enter a **title**; optionally a description, an **assignee** (member), a link
   to a **meeting** or **plan**, a **due date**, a **priority** (`low`,
   `normal`, `high`), and a **status** (`open`, `in_progress`, `done`,
   `cancelled`).
3. Update status as work progresses.

Open action items surface on the **Dashboard** and on each assignee's **My
Account** page.

## 5.6 Contacts directory

Maintain outside people and organizations under **Contacts**: name,
organization, email, phone, address, **category** (e.g. `vendor`, `legal`),
**tags**, and notes. Search and filter by category or tag.

## 5.7 Resource library

Maintain links and reference material under **Resources**: title, description,
**URL**, **category**, and **tags**. Only `http`/`https` links are accepted.

**Minimum role (role-based visibility).** Each resource has a *Who can see
this* section. Its **minimum role** hides the resource — and its document —
from every account below the chosen bar: *All members* (default), *Officers
and above*, or *Admins only*. Unlike groups, the bar applies to everyone:
an admins-only resource is invisible to officers too. Hidden means missing
(404), as always. Role and groups combine as AND — pass the role bar first,
then the group check.

**Visibility groups (who can see a resource).** When adding or editing a
resource, officers can tick one or more **visibility groups** (created by an
administrator, 6.7):

- **No groups ticked** → the resource is visible to all members (the default).
- **Groups ticked** → only members belonging to at least one of those groups
  can see it; to everyone else the resource simply *does not exist* — it is
  absent from lists and returns "not found" if addressed directly.
- **Officers and above always see everything**, with a 🔒 badge listing each
  restricted resource's groups.

**Uploaded documents & folders.** A resource can carry an actual file, not
just a link: in the add/edit dialog, choose a **document** (up to 25 MB) and
optionally a **folder**. Documents live inside Quorum's database, so the
nightly encrypted backups and disaster recovery cover them automatically.
Downloads respect the resource's visibility groups exactly — to a member
outside the groups, the document does not exist.

**Folders nest.** Build a tree (Legal → Contracts → 2026) via each folder's
parent in the *Folders* dialog; the library page shows a collapsible tree —
click a folder to filter, ▸/▾ to fold. Folders organize and never affect
visibility. Deleting one releases its subfolders and documents to the root;
moving a folder inside its own subtree is refused.

**The in-app viewer.** The 👁 button renders documents directly in the app:
images, SVG, PDF, sandboxed HTML, Markdown, CSV/TSV tables, plain text and
code, Mermaid diagrams, XLSX workbooks, and DOCX documents (the last three
via self-hosted libraries loaded on first use). Formats without a viewer
offer download instead. Previews serve the original bytes and are recorded
in the audit log as PREVIEW entries.

**Preview-only documents.** Tick *Preview-only* on a resource and its
document can be seen in the viewer but never downloaded — the server refuses,
for officers too (untick the box first if you legitimately need the file).
Like the screen watermark, this is deterrence with honest limits: a viewer
can still photograph a screen; what they cannot get is a clean file.

**The download ledger.** Every download is written to a permanent ledger —
who, when, from which IP — and recorded as an EXPORT in the audit log.
Text-family formats (txt, md, html, svg, mermaid, xml) are additionally
**watermarked** with a provenance trailer naming the downloader, time,
address, and ledger record. The ledger stores the SHA-256 of the *exact
bytes served*, which makes provenance answerable later (see 6.10).

**Heat-map previews.** The 🔥 button on a document renders it as a
**word-frequency heat map**: the meaningful words as tiles, colored cold
(rare) to hot (frequent) and sized by count, stopwords removed. It is a
skim-reading aid computed in your browser from data you are already authorized
to see — permissions are inherited automatically, and previewing is never
recorded as an export.

## 5.8 What officers can and cannot remove

- **Can:** edit any record; remove individual **decisions** and adjust
  attendance (these are part of editing minutes).
- **Cannot:** permanently delete meetings, plans, contacts, resources, action
  items, or user accounts (super-administrator only, Part 7); deactivate members
  (administrator only, 6.5).

## 5.9 Boards: sprint & kanban

The **Board** page tracks work two ways over the same cards:

- **Kanban** — columns as workflow lanes; drag work forward as it progresses.
- **Sprint board** — group cards into **sprints** (status `planned`,
  `active`, `completed`), each with a goal and dates, for time-boxed planning.

A closed card wears its resolution on its face: **✓ done** (green) or
**✕ cancelled** (grey, struck through) — so a Done lane distinguishes work
that shipped from work that was called off. Set it with the card's **Status**
dropdown, or drop the card into a column mapped to that status.

Every card also records its **reporter** — whoever created it — shown as
"✍ Reported by …" in the card. It is set automatically at creation and cannot
be changed. Create a card with a title, optional description, an **assignee**
(a member), optional **additional contributors** (any number of members working
on the card alongside the assignee — the card face shows 👥 +N, hover for names),
and a sprint (or none — it sits in the backlog). Officers create and move
cards; members can view the boards and take part in card conversations.

**Custom columns.** Officers shape the lanes via the **Columns** button: add
columns like *Blocked*, *Prioritized*, or *Reviewing*, rename, reorder, or
remove them. A column may carry a **status mapping** — dropping a card into a
mapped column (e.g. *Done*) also advances the card's status, which keeps
dashboards and sprint progress truthful; unmapped columns move cards without
touching status. Deleting a column never deletes cards: they fall back to the
lane matching their status.

**Card types, points & hierarchy.** Cards carry a **type** — `epic`,
`story`, `task`, `sub-task`, or `spike` — and optional **story points**
(0–100). Types nest the way you'd expect, and the *database* enforces it:
sub-tasks belong to a task, story, or spike; stories, tasks, and spikes may
belong to an epic; epics stand alone. The card dialog's *Belongs to* picker
only offers legal parents, and a type change that would strand children is
refused.

**Relationships.** Link cards as **depends on**, **blocked by**, or
**related to** (card dialog → *Relationships*). Links read correctly from
both sides ("blocked by" on one card shows as "blocks" on the other), and a
card whose blocker or dependency isn't done counts as **blocked** in
analytics.

**Accounting is configurable, not opinionated.** Admins (guided by the
org's accountant) can reshape the books without code: edit the chart of
accounts, remap every **posting rule** (Accounting → Posting rules — e.g.
route Zelle receipts to a dedicated bank account, change the default
expense account), switch statement **basis** between accrual and cash, close
and reopen periods, and post adjusting entries. Rules affect future
postings only; history is immutable.

**Sprint analytics & report.** With a sprint selected, **📊 Analytics**
shows points committed vs done, completion %, blocked and unpointed counts,
and breakdowns by type, assignee, and status. Officers can export the same
picture as a **PDF report** (also on the Reports page) with the standard
export controls: exporter watermark, embedded SHA-256 integrity seal, and
an EXPORT audit entry.

**Card conversations.** Every card has a comment thread — open the card and
write in the *Conversation* box (Ctrl+Enter sends). Each message is tagged
with its author and time; authors can delete their own messages, and admins
can moderate. Any member can read and comment — assignees are often plain
members — and cards show a 💬 count on the board.

## 5.10 Reports & PDF exports

The **Reports** page (officer and above) produces formal PDF documents: the
**member roster**, **dues & receivables**, any meeting's **minutes**, and (for
administrators) a recent **audit log** report.

Three properties make these exports evidence-grade rather than mere printouts:

1. **Watermark.** Every page carries a light diagonal stamp and footer:
   *who* exported it and *when* (UTC). A leaked paper copy names its source.
2. **Integrity seal.** Each PDF embeds its own **SHA-256 digest**. Anyone can
   later verify the file was not altered — the repository ships
   `ops/verify-pdf-export.py`, which checks a PDF offline in seconds.
3. **Audit trail.** The moment you download, an `EXPORT` entry lands in the
   tamper-evident audit log (6.8): who, what, when, and the document's digest.
   A document is authentic only if its embedded digest **matches the audit
   entry** — a forged PDF fails this cross-check even if internally consistent.

> **Note.** Browser printing of app pages is deliberately blocked (you get a
> policy notice instead) — the watermarked PDF export *is* the sanctioned way
> to put records on paper. Heat-map previews (5.7) remain available to
> everyone with access, because a preview exports nothing.

## 5.11 Governance, budgets & analytics (a quick tour)

Three more areas, each self-explanatory in the app once you know they exist:

- **Governance & voting.** Live quorum tracking during meetings; formal
  motions and votes (5.3.5); **asynchronous ballots** sent by email with
  single-use voting links, including **proxy** support — for decisions taken
  between meetings. Ballot casting is atomic: a vote counts exactly once.
- **Budgets.** Scenario planning: draft a budget, **clone** it into a what-if
  variant, adjust, and **compare** side by side before adopting one. Once a
  period is underway, **Vs actual** compares any scenario's budgeted income and
  expense against what's actually posted to the ledger over a date range, with
  the variance called out — so you can see where you're over or under plan.
- **Analytics.** Dashboard charts of membership and receivables over time.
  Multi-currency organizations: totals are per-currency — mixed currencies
  are **flagged, never silently summed** — with explicit FX conversion where
  configured.

## 5.12 Roster: office holders, committees & membership applications

The **Roster** page (in the People area; officers see all of it, members see
office holders and committees) keeps the organizational record that sits
*alongside* the permission model — none of it grants access.

- **Office holders.** Record who holds which office (Treasurer, Secretary, …)
  and since when. Ending a term keeps it in the history, so the page doubles as
  an org-chart-over-time. Admins add and end terms; a member holds a given title
  once at a time.
- **Committees.** Create named working groups with a purpose, an optional chair,
  and a roster. Admins manage them; everyone can see who's on what.
- **Membership applications.** When someone applies through the public form (see
  2.8), their request lands in the **Membership applications** queue here.
  **Approve** creates a member record from the application in one step (you can
  set a tier); **Reject** declines it. Both are officer actions.

\newpage

# Part 6 — User Story: The Administrator (People & Access)

> *"As an administrator, I want to manage who can sign in, what they can do, and
> connect logins to member records."*

Administrators do everything officers do, **plus** user management and member
deactivation. Open **Settings** to manage users.

## 6.1 Create a user account

1. Go to **Settings**.
2. Select **+ Add user**.
3. Enter the **email** and an initial **password** (minimum 10 characters),
   choose a **role**, and optionally choose a **linked member** (6.3).
4. Select **Save**. Tell the person their email and initial password; they can
   change the password after signing in (2.4).

## 6.2 Which role to grant

| Grant… | …to someone who should |
|---|---|
| `restricted` | See only their own dues/tasks (general membership self-service). |
| `member` | Read all shared records but change nothing. |
| `officer` | Do day-to-day operations (billing, meetings, plans, directories). |
| `admin` | Also manage users and deactivate members. |
| `superadmin` | Have full authority, including permanent deletion. Grant sparingly. |

> **Note.** Only a **super-administrator** may create or promote someone to
> `superadmin`. An administrator will receive "forbidden" if they try.

## 6.3 Link a login to a member (enable self-service)

This is the step that makes the `restricted` role useful — and it is easy to
forget.

1. In **Settings**, on the user's row (or in the add-user form), set the
   **Linked member** to the matching entry from the membership directory.
2. Save.

Now that user's login is connected to their member profile. A `restricted` user
can see *their own* record on **My Account**; the link is what tells Quorum which
member is "theirs."

> **Warning.** A `restricted` user with **no** linked member sees nothing at all —
> not even their own data. Always link the member when creating a self-service
> account.

## 6.4 Change a user's role

1. In **Settings**, change the **role** on the user's row and confirm.
2. Note the guardrails:
   - You **cannot change your own role** (this prevents you from accidentally
     removing your own access).
   - Only a super-administrator may grant or revoke `superadmin`.

## 6.5 Deactivate (soft-delete) a member

"Deleting" a member is a **reversible deactivation**:

1. Go to **Members**, open the member, and choose the deactivate/delete control
   (visible to administrators).
2. Confirm. The member's status becomes **inactive**; their history (invoices,
   attendance, tasks) is preserved.

To reactivate, edit the member and set **status** back to `active`.

## 6.6 Password resets & account recovery

Three layers, from least to most privileged:

1. **Self-service.** The **Forgot your password?** link on the sign-in page
   emails a single-use, quickly-expiring reset link. Requires working SMTP
   (see `EMAIL-SETUP.md`) — configure it; this should be the normal path.
2. **Admin reset.** **Settings → Users → Reset PW** sets a temporary password
   for the user (hand it over securely; they change it at next sign-in). The
   reset revokes the user's other sessions. A super-administrator's password
   can only be reset by another super-administrator.
3. **2FA lockout.** A user with a lost authenticator signs in with a
   **recovery code** (2.6). With no codes left, the *server operator* clears
   their 2FA from the command line (`quorum -unlock-2fa <email>`, see
   `RUNBOOK.md`) — shell access is deliberately the bar for bypassing a
   second factor.

> **Tip.** Keep **two** super-administrators (7.3). If your only superadmin is
> locked out, recovery requires shell access to the server.

## 6.6a Funds: restricted money with multi-person sign-off

**Funds** (sidebar) are purpose-restricted pots — each with its own general-
ledger cash account, so a fund's balance is always *derived from the books*,
never a number someone typed. Admins create funds and set the **spending
policy**: how many approvals a purchase needs (1–10), plus optionally
**named signers who must each sign**. Admins move money in or out with
Transfers (posted to the books; overdrafts refused).

Spending walks a fixed ceremony:

1. An officer **files a purchase request** (fund, amount, payee, memo).
2. Eligible people **sign** — officers or the fund's named signers, never
   the requester. Signing requires re-entering your password, and each
   signature permanently records who, when, and from which network address.
3. When the count is met AND every named signer has signed, the request is
   **approved**; an officer then **completes** it, which posts
   `DR Expenses / CR fund cash` to the general ledger in the same
   transaction — a purchase cannot complete without hitting the books, and
   cannot overdraw the fund.
4. Completed (and rejected/cancelled) requests are **frozen forever** — the
   database refuses edits — and changing a fund's policy voids in-flight
   approvals so signatures always reflect the current rules.

## 6.6b Continuity: surviving the loss of your operator

Settings → Organization settings carries the org's **bus-factor kit**:

- **Secret custody registry** — one row per critical credential (backup
  passphrase, cloud account, registrar…): where it lives and who holds it.
  Values never enter Quorum. Click **✓ attest** periodically ("I verified
  this copy exists"); editing a row resets verification, because a moved
  copy is an unverified copy. Health checks flag staleness, a lone
  superadmin, and impending TLS expiry.
- **Inactivity watchdog** — if no superadmin signs in for N days, the
  designated continuity contacts get an email pointing at the succession
  procedure. It notifies; it never grants access (that bar stays where it
  belongs: custody of the real keys).
- **Continuity pack** (superadmin) — a sealed ZIP that is your successor's
  map: a generated "you have inherited this system" README, the
  infrastructure facts you maintain, the full org configuration snapshot,
  and the custody registry. Regenerate and print after material changes;
  store beside the offline backup-passphrase copy.

## 6.6c Organization settings: automation & retention knobs

Settings → Organization settings also carries a few policy switches that let the
software run itself the way your organization prefers. All are optional and off
by default, so Quorum works as-is for any organization until you turn them on.

- **Auto-lapse overdue members** (`lapse_after_days`). Set a number of days and
  the nightly cycle flips a member from **active** to **inactive** once they have
  an invoice overdue by at least that long. Leave it at `0` (the default) to
  never lapse anyone automatically. Lapsing is reversible — you can reactivate a
  member at any time.
- **Monthly financial summary email** (`monthly_report_email`). Enter an address
  and the nightly cycle sends a monthly financial digest there — a hands-off way
  to keep a treasurer or board informed.
- **Audit legal hold** (`audit_legal_hold`, admin only). Normally the nightly
  cycle prunes old audit and bookkeeping records past their retention window.
  Turning legal hold **on** suspends that pruning entirely — nothing ages out —
  so records are preserved while litigation or an investigation is pending. Turn
  it off to resume normal retention.

## 6.7 Visibility groups (constrain what members can see)

Create groups under **Settings → Visibility groups**, ticking the members that
belong to each. Officers then tag library resources with groups (5.7); a
tagged resource is invisible to members outside its groups.

> **Warning — deleting a group widens visibility.** Resources restricted
> *only* by a deleted group become visible to **all** members. The delete
> confirmation says exactly this; read it as written.

Groups constrain the **resource library**. Meetings, minutes, and the member
directory remain readable by every `member`+ account by design — restricting
those is a governance decision, not a checkbox.

## 6.8 The audit log (who did what, when)

**Audit log** (admin) records every mutating action — creates, edits,
deletions, exports, sign-ins, denials — with the actor, the target, and the
time. Filter by action, resource, or date. Three things worth knowing:

- **It is tamper-evident.** Entries form a **hash chain** (each entry's
  digest depends on all before it), computed inside the database. The
  **Verify** button — or `quorum -verify-audit` on the server — proves the
  chain intact; a break pinpoints where history was altered.
- **Denials are recorded too.** Repeated `DENIED` entries or failed sign-ins
  against real accounts are your early warning of probing.
- **Evidence-grade export.** The **evidence CSV** download lets an outside
  party re-verify the chain without trusting your server
  (`ops/verify-audit-export.py`).

## 6.9 Verify a document's provenance

**When:** a Quorum document surfaces somewhere it shouldn't have, or you need
to prove a file is (or is not) authentic.

Hash the file in question (`sha256sum thefile`) and paste the hash into
**Resources → Verify a file** (admins), or ask the API
(`GET /api/v1/downloads/verify?sha256=<hash>`). Three possible answers:

| Status | Meaning |
|---|---|
| `original` | Byte-identical to a stored document as uploaded. |
| `download` | Matches the exact bytes of **one recorded download** — the response names when it was received and from which IP. This is what watermarked stamping buys: each download's bytes are unique. |
| `unknown` | Altered after it left Quorum, or never came from it. |

A document's full download history is available in the UI too: **Resources →
History** on any file-backed row (officer+) lists each download with its time,
IP, and hash.

## 6.10 Data portability & erasure

- **Export my data** (available to every signed-in user, including
  `restricted`): a JSON file of their account, profile, dues, and payments.
- **Erasure** ("right to be forgotten") is a super-administrator action on
  the *member record* — see 7.5.

\newpage

# Part 7 — User Story: The Super-Administrator (Elevated & Destructive)

> *"As the person ultimately responsible for this system, I need to be able to
> permanently remove records and grant top-level access — carefully, and with a
> record of what happened."*

## 7.1 The founder account

The very first account, created when the system is first stood up
("bootstrapped," see 10.6), is a **super-administrator**. On an upgraded install,
the earliest-created administrator is promoted to super-administrator
automatically so that someone always holds the top role.

## 7.2 Permanent deletion (heavily gated)

Permanently deleting a **meeting, plan, contact, resource, action item, or user
account** is a super-administrator action, and Quorum surrounds it with
deliberate friction:

1. Choose the delete control (shown only to super-administrators).
2. A confirmation dialog warns that the deletion is **permanent and cannot be
   undone**, and asks you to **type the record's exact name/title** to confirm.
3. On confirmation, Quorum:
   - Deletes the record (and its dependent rows — e.g. a meeting's attendance and
     decisions).
   - Writes an **audit-log** entry (who deleted what, when).
   - **Emails a notification** (when email is configured) to all administrators
     and super-administrators, and to the directly-affected people where they
     exist: a meeting's **attendees**, a plan's **owner**, an action item's
     **assignee**, or, for a user deletion, the **deleted user** themselves.

> **Warning.** Deletion is irreversible and cascades. For meetings, plans, and
> members, prefer **cancelling / archiving / deactivating** over deleting.
> Members are never hard-deleted; they are deactivated (6.5).

## 7.3 Grant or revoke super-administrator

Only a super-administrator can grant or revoke the `superadmin` role (via
**Settings**, changing a user's role). You cannot change your own role, which
prevents accidentally removing the last super-administrator by self-demotion.

## 7.4 Guidance: delete, or archive?

| Situation | Prefer |
|---|---|
| A meeting was cancelled | Set status **cancelled** (keep the record). |
| A plan is finished/abandoned | Set status **completed** or **archived**. |
| A member left | **Deactivate** (6.5). |
| A record was created by mistake and has no history | **Delete** (super-admin). |
| A duplicate/spam contact or resource | **Delete** (super-admin). |
| An employee/user should lose access | Change their **role** or delete the **user** (the member profile is separate). |

## 7.5 Erase a member (right to be forgotten)

Erasure strips a member's personal data **in place**: the name becomes an
anonymous placeholder; email, phone, address, and notes are removed; any
linked login is unlinked and its sessions revoked. Their invoices, payments,
attendance, and votes are **kept** — the ledger and the minutes stay
arithmetically and historically consistent, just no longer tied to a natural
person.

It is irreversible and type-to-confirm gated, and the audit log records who
performed it. In the UI it is the **Erase** button on a member row (super-
administrators only), next to Edit and Del. Prefer deactivation (6.5) unless
someone has actually exercised their right to erasure.

\newpage

# Part 8 — Payments Integration

> *Audience: the treasurer/officer who reconciles dues, and the operator who
> wires up the payment providers (10.10).* 

## 8.1 How Quorum handles money

Quorum **never processes or stores card data** — its PCI scope is zero. Members
pay through **Stripe** or **PayPal** using those providers' own hosted pages or
links, and Quorum simply **records the result** via a secure webhook. You can
also record payments **manually** for cash, checks, and wires.

## 8.2 Record a manual payment

See 5.2.4. Use provider `manual` and be sure the currency matches the invoice.

## 8.3 Connecting Stripe (overview)

1. In your Stripe dashboard, create a webhook endpoint pointing at
   `https://your-domain/api/v1/webhooks/stripe`.
2. Subscribe to `payment_intent.succeeded` (and/or `charge.succeeded`).
3. Copy the signing secret (`whsec_…`) and give it to your operator to set as
   `QUORUM_STRIPE_WEBHOOK_SECRET` (10.10).

## 8.4 Connecting PayPal (overview)

1. Point a PayPal webhook at `https://your-domain/api/v1/webhooks/paypal`.
2. Subscribe to `PAYMENT.CAPTURE.COMPLETED`.
3. Give your operator the webhook ID to set as `QUORUM_PAYPAL_WEBHOOK_ID`.

## 8.5 Linking online payments to invoices

So that an incoming payment attaches to the right invoice automatically:

- **Stripe:** include `quorum_invoice_id` (and optionally `quorum_member_id`) in
  the payment's metadata when you create the payment link/intent.
- **PayPal:** set the order's `custom_id` to `invoice:<invoice-uuid>`.

When the webhook arrives, Quorum verifies its signature, records the transaction,
links it to the invoice, and recomputes the invoice status. Duplicate deliveries
are ignored (each event is processed once).

## 8.6 Reconciliation, partial payments, refunds & corrections

- **Partial payments** move an invoice to **partial**; further payments that
  reach the full amount move it to **paid**.
- **Overpayments / different currency:** a payment in a different currency than
  the invoice does not count toward it (and manual entry of a mismatched currency
  is rejected). Keep each invoice in a single currency.
- **Refunds & corrections:** Quorum records payments but does not itself reverse
  them. Handle refunds in the payment provider; to reflect a correction in
  Quorum, record an offsetting **manual** transaction or adjust the invoice
  status (e.g. back to `pending`), and note the reason.

\newpage

# Part 9 — Email Notifications & the Nightly Cycle

## 9.1 What email Quorum sends

When SMTP is configured (10.9), Quorum sends:

1. **Overdue dues reminders** to members (9.2).
2. A nightly **admin digest** to administrators and super-administrators (9.3).
3. **Deletion notifications** when a record is permanently removed (9.4, 7.2).

If SMTP is **not** configured, no email is sent, but the rest of the nightly
housekeeping still runs (9.5).

## 9.2 Overdue reminder escalation

Each night, for every overdue invoice belonging to a member who has an email on
file, Quorum sends an escalating series — each notice at most once:

| Notice | When |
|---|---|
| **First notice** | As soon as the invoice is overdue. |
| **7-day follow-up** | About a week overdue. |
| **30-day final notice** | About a month overdue. |

A reminder only advances to the next stage after it has been **sent
successfully**, so a temporary mail-server outage results in a retry the
following night rather than a skipped notice. Partially-paid invoices that are
past due are included in reminders.

## 9.3 The admin digest

Administrators and super-administrators receive a nightly summary of the overdue
picture (counts and a list), so leadership can act without logging in.

## 9.4 Deletion notifications

When a super-administrator permanently deletes a record (7.2), Quorum emails the
governance body (all admins/super-admins) and any directly-affected member. This
provides "ample communication to those it affects" for destructive actions.

## 9.5 What runs without email

Independent of SMTP, the nightly job also:

- **Ages** past-due `pending` invoices to `overdue`.
- **Prunes** housekeeping data: expired/revoked login (refresh) tokens
  immediately, processed webhook-event records after ~90 days, and audit-log
  entries after ~1 year.

The job runs around **2 AM local time** on the server.

\newpage

# Part 10 — The System Administrator / DevOps Engineer

> *"As the operator, I need to deploy Quorum, configure it correctly and
> securely, keep it backed up, and upgrade it safely."*

This part is a practical operations guide; the repository carries deeper
companions for each concern:

| Companion | Covers |
|---|---|
| **`DEPLOY-EC2.md`** | The recommended single-server deployment, click by click |
| **`RUNBOOK.md`** | Day-2 procedures: onboarding, lockouts, upgrades, backups, forensics |
| **`ARCHITECTURE.md`** | Diagrams: components, data flow, network zones |
| **`EMAIL-SETUP.md`** | SMTP from zero (relay choice, DNS records, testing) |
| **`BACKUP.md`** | Backup/restore/verify tooling and disaster recovery |
| **`DEPLOYMENT.md`** | Kubernetes/GitOps, for multi-node scale only |

## 10.1 Architecture overview

- **One binary + one database.** Quorum compiles to a single static Go binary
  that embeds the web front-end and the database migrations. It talks to a
  **PostgreSQL 16** database.
- **Stateless app.** All state is in PostgreSQL; the binary holds none, so it is
  easy to restart or (with caveats, 10.14) scale.
- **Migrations auto-apply at startup** under a PostgreSQL advisory lock, so
  multiple instances starting together will not race.
- **Front-end** is served by the same binary at `/`. The API lives under
  `/api/v1`. Health endpoints are at the root (`/healthz`, `/readyz`).
- **TLS is terminated upstream** (a reverse proxy or ingress); the app itself
  speaks plain HTTP on its port.

## 10.2 Prerequisites

- **PostgreSQL 16** (managed or self-run).
- A container runtime (**Podman** or **Docker**) for Compose deployments, or a
  **Kubernetes** cluster with an ingress controller for cluster deployments.
- **Go 1.25+** only if you build from source; the container image needs no Go
  toolchain at runtime.
- Optionally: an **SMTP relay** (for email), and **Stripe/PayPal** accounts (for
  online payments).

## 10.3 Configuration reference (environment variables)

All configuration is via environment variables — **no config files are read at
runtime**.

| Variable | Required | Default | Purpose |
|---|:--:|---|---|
| `QUORUM_DATABASE_URL` | **yes** | — | PostgreSQL DSN, e.g. `postgres://user:pass@host:5432/quorum?sslmode=require`. |
| `QUORUM_JWT_SECRET` | **yes** | — | HS256 signing key, **>= 32 chars**; a placeholder containing `CHANGEME` is rejected at startup. Generate with `openssl rand -hex 32`. |
| `QUORUM_PORT` | no | `8080` | HTTP listen port. |
| `QUORUM_BASE_URL` | no | `http://localhost:8080` | Public URL. **Drives the cookie `Secure` flag** — see 10.7. |
| `QUORUM_JWT_ACCESS_TTL` | no | `15m` | Access-token lifetime (Go duration). |
| `QUORUM_JWT_REFRESH_TTL` | no | `168h` | Refresh-token lifetime (7 days). Use `168h`, not `7d`. |
| `QUORUM_TRUST_PROXY_HEADERS` | no | `false` | Key rate limiting on the proxy's `X-Real-IP`/`X-Forwarded-For`. Enable **only** behind a trusted proxy (10.7). |
| `QUORUM_SMTP_HOST` | no | — | SMTP host; leave empty to disable email. |
| `QUORUM_SMTP_PORT` | no | `587` | SMTP port. |
| `QUORUM_SMTP_USER` / `QUORUM_SMTP_PASS` | no | — | SMTP credentials. |
| `QUORUM_SMTP_REQUIRE_TLS` | no | `false` | Require STARTTLS; fail rather than send in cleartext. |
| `QUORUM_EMAIL_FROM` | no | `quorum@localhost` | From address. |
| `QUORUM_STRIPE_WEBHOOK_SECRET` | no | — | Stripe signing secret. **Unset => Stripe webhook returns 503** (fail-closed). |
| `QUORUM_PAYPAL_WEBHOOK_ID` | no | — | PayPal webhook ID. Unset => PayPal webhook returns 503. |
| `QUORUM_ALLOW_UNSIGNED_WEBHOOKS` | no | `false` | **Local dev only** — process webhooks without signature verification when the secret is unset. Never `true` in production. |
| `DB_PASSWORD` | Compose only | — | Postgres container password (used by `docker-compose.yml`). |

## 10.4 Deploying with Docker/Podman Compose (quickstart)

1. Copy the environment template and generate a secret:
   ```sh
   cp .env.example .env
   make secret          # prints a 64-hex-char value; paste into .env as QUORUM_JWT_SECRET
   ```
2. In `.env`, set at minimum `QUORUM_JWT_SECRET`, a strong `DB_PASSWORD`, and a
   matching `QUORUM_DATABASE_URL`.
3. Start the stack (Podman shown; `make docker-up` for Docker):
   ```sh
   make pod-up          # builds the image, starts PostgreSQL + the app
   ```
4. Create the first account (10.6), then browse to `http://localhost:8080` (put a
   TLS-terminating reverse proxy in front for anything beyond local use).

The app container binds to `127.0.0.1:8080` and runs as a non-root user; the
`pgdata` volume persists the database.

## 10.5 Deploying to Kubernetes

Three methods are provided under `deploy/`; pick one:

| Method | When to use |
|---|---|
| **Kustomize + Tekton + Argo CD** | GitOps, auto-deploy on push (recommended for production). |
| **Helm chart** (`deploy/helm/quorum`) | Imperative installs or Helm-source GitOps. |
| **Raw manifests** (`deploy/k8s`) | Simple one-off installs. |

Highlights the operator should know:

- **Migrations** run automatically at pod start; no separate migration Job is
  required.
- The container runs **rootless** (UID 1000, read-only root filesystem, dropped
  capabilities, seccomp) and includes liveness/readiness probes on
  `/healthz`/`/readyz`.
- The **Helm chart refuses to render** an image tag of `latest` or a
  `CHANGEME` secret placeholder, and supports `secrets.existingSecret` so you can
  manage credentials with Sealed Secrets or the External Secrets Operator.
- Behind the ingress, set **`QUORUM_TRUST_PROXY_HEADERS=true`** (10.7); the
  shipped ConfigMaps already do.

Example Helm install:
```sh
helm upgrade --install quorum deploy/helm/quorum \
  -f deploy/helm/quorum/values.yaml \
  -f deploy/helm/quorum/values-production.yaml \
  --namespace quorum --create-namespace \
  --set image.tag=<immutable-tag> \
  --set secrets.existingSecret=quorum-secrets
```

## 10.6 First run: bootstrapping the founder account

Immediately after the app is up and the database is migrated, create the first
account. It is minted as a **super-administrator**, and the endpoint refuses to
run once any user exists (so it cannot be abused later).

```sh
curl -s -X POST https://your-domain/api/v1/auth/bootstrap \
  -H 'Content-Type: application/json' \
  -d '{"email":"founder@your-org.example","password":"a-long-strong-password"}'
```

- The password must be **at least 10 characters**.
- A second call returns `403 Forbidden` — bootstrapping is one-time.
- With Compose you can run `make bootstrap`.

From this founder account, create the rest of your users (Part 6).

## 10.7 TLS, reverse proxies, and two gotchas

Quorum serves plain HTTP and expects TLS to terminate at a reverse proxy (nginx,
Caddy) or a cloud/ingress load balancer. Two settings are easy to get wrong:

> **Warning — the cookie `Secure` flag.** Quorum sets the `Secure` flag on the
> refresh cookie based on whether **`QUORUM_BASE_URL` begins with `https://`** —
> *not* on the connection it sees (behind a proxy it always sees plain HTTP).
> **You must set `QUORUM_BASE_URL=https://your-domain`** in any TLS deployment, or
> the refresh cookie will be sent without `Secure`.

> **Warning — rate-limit IP source.** By default Quorum keys its login/refresh
> rate limits on the raw socket address. Behind a proxy every request appears to
> come from the proxy's IP, collapsing all users into one bucket. Set
> **`QUORUM_TRUST_PROXY_HEADERS=true`** *only* when a trusted proxy sets
> `X-Real-IP`/`X-Forwarded-For` and strips client-supplied copies; then Quorum
> keys on the real client IP.

A minimal nginx location block:
```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```

## 10.8 Secrets management

- **Never commit real secrets.** The k8s `secret.yaml` and Helm defaults are
  templates/placeholders; the app rejects a `CHANGEME` JWT secret at startup and
  the Helm chart fails rendering on placeholders.
- In production, manage the `quorum-secrets` Secret with **Sealed Secrets** or
  the **External Secrets Operator**, and point the chart at it via
  `secrets.existingSecret`.
- Rotate `QUORUM_JWT_SECRET` deliberately: changing it invalidates all existing
  access tokens (users simply sign in again).

## 10.9 Configuring email (SMTP)

Set `QUORUM_SMTP_HOST` (and port/user/pass/from) to enable reminders, the admin
digest, and deletion notices. Quorum uses STARTTLS when the relay offers it; set
`QUORUM_SMTP_REQUIRE_TLS=true` to **refuse** to send if the relay does not offer
encryption. With no SMTP host set, email is silently skipped (the nightly
housekeeping still runs).

> **Tip.** New to SMTP entirely? **`EMAIL-SETUP.md`** walks the whole thing
> from zero — what a relay is, why unauthenticated mail lands in spam
> (SPF/DKIM/DMARC), and a click-by-click Amazon SES setup.

## 10.10 Configuring payment webhooks

- Set `QUORUM_STRIPE_WEBHOOK_SECRET` and/or `QUORUM_PAYPAL_WEBHOOK_ID` (Part 8).
- **Fail-closed:** if a provider's secret is unset, that webhook endpoint returns
  `503` and processes nothing — so a misconfiguration cannot silently accept
  forged payment events. For local testing only, `QUORUM_ALLOW_UNSIGNED_WEBHOOKS=true`
  bypasses verification. **Never** set it in production.
- Ensure your NetworkPolicy/egress allows outbound HTTPS to the providers (the
  PayPal path fetches a signing certificate from PayPal's API hosts).

## 10.11 Database, migrations & backups

- **Migrations** are embedded and apply automatically at startup, in order, each
  in its own transaction, under an advisory lock. To add one, drop a numbered
  `NNNN_name.up.sql` / `.down.sql` pair in `internal/db/migrations/` and restart.
- **Backups:** take regular `pg_dump` backups of the Quorum database. Example:
  ```sh
  pg_dump "$QUORUM_DATABASE_URL" --format=custom --file=quorum-$(date +%F).dump
  ```
- **Restore** into an empty database and start the app; it will confirm the
  schema is at the expected migration level.
- Use `sslmode=require` (or stronger) in `QUORUM_DATABASE_URL` for any non-local
  database.

## 10.12 Health checks & monitoring

- **`GET /healthz`** — liveness: returns `200` while the process is running.
- **`GET /readyz`** — readiness: returns `200` when the database responds, `503`
  otherwise. Use it to gate traffic.
- **Container self-check:** the binary supports `quorum -healthcheck`, which
  probes `/healthz` locally and exits `0`/`1` — used by the container
  `HEALTHCHECK` (the image is `FROM scratch` and has no shell/curl).
- **Logs** go to stdout (structured, with a **request ID** on every line —
  quote it when reporting a problem and the operator can find the exact
  request).
- **Metrics:** `GET /metrics` serves Prometheus metrics when
  `QUORUM_METRICS_TOKEN` is set (send it as a bearer token). Includes HTTP
  rates/latencies, DB pool gauges, and `quorum_audit_chain_intact` — alert if
  that gauge ever reads 0. Alert rules ship in `ops/prometheus-alerts.yml`.

## 10.13 Upgrades & rollbacks

1. Build/pull the new **immutable** image tag (never deploy `:latest`).
2. Roll it out (Helm/Kustomize/Compose). Migrations apply automatically at start.
3. **Rollback:** because migrations can add constraints, prefer rolling forward.
   If you must roll back, restore the pre-upgrade database backup and deploy the
   previous image. Down-migrations exist for each migration but a data-restore is
   the safest path for anything that changed data.

> **Note — one-way data migrations.** Some migrations transform data (for example
> the money and role migrations). Their down-migrations are provided but may be
> lossy; treat a database backup as your real rollback mechanism.

## 10.14 Scaling & multi-replica caveats

- The app is stateless and can run multiple replicas behind the ingress; startup
  migrations are advisory-locked so concurrent starts are safe.
- **Rate limiting is in-process** (per replica). With N replicas the effective
  login limit is roughly N x the per-replica limit. For strict global limiting,
  enforce it at the ingress/WAF, or run a single replica for the auth path.
- The nightly job runs in **every** replica's scheduler; its steps are written to
  be idempotent, but if you run many replicas and want exactly-once semantics,
  run the scheduler on a single designated replica (e.g. a separate Deployment
  with one pod), or accept the idempotent duplication.

## 10.15 Security hardening checklist

- [ ] `QUORUM_JWT_SECRET` is a real 32+ char random value (not a placeholder).
- [ ] `QUORUM_BASE_URL` is `https://…` in any TLS deployment (cookie `Secure`).
- [ ] `QUORUM_TRUST_PROXY_HEADERS=true` **only** behind a trusted proxy that
      strips client XFF.
- [ ] TLS terminates upstream; HSTS and HTTPS-redirect enforced at the proxy.
- [ ] Database uses `sslmode=require`+ and a strong password; not publicly
      exposed.
- [ ] Webhook secrets set (fail-closed); `QUORUM_ALLOW_UNSIGNED_WEBHOOKS` unset.
- [ ] Secrets managed via Sealed Secrets/ESO, never committed.
- [ ] Container runs rootless with the shipped `securityContext`; image pinned to
      an immutable tag.
- [ ] NetworkPolicy restricts egress to what's needed (DB, SMTP, provider APIs).
- [ ] Regular `pg_dump` backups, tested restores.
- [ ] `SMTP_REQUIRE_TLS=true` if your relay supports it.

## 10.16 Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| App exits at startup with a config error | A required var is missing, `QUORUM_JWT_SECRET` is < 32 chars or contains `CHANGEME`, or a duration like `7d` was used (use `168h`). |
| `/readyz` returns 503 | The app cannot reach PostgreSQL. Check `QUORUM_DATABASE_URL`, network policy, and the database's health. |
| Users get signed out unexpectedly / cookie not kept | `QUORUM_BASE_URL` isn't `https://` (cookie lacks `Secure` and the browser drops it), or the refresh window elapsed. |
| Everyone shares one rate-limit bucket / lockouts behind ingress | Set `QUORUM_TRUST_PROXY_HEADERS=true`. |
| Stripe/PayPal webhook returns 503 | The provider secret isn't set. Set `QUORUM_STRIPE_WEBHOOK_SECRET` / `QUORUM_PAYPAL_WEBHOOK_ID`. |
| Online payments don't attach to invoices | Add `quorum_invoice_id` (Stripe metadata) or `custom_id: invoice:<uuid>` (PayPal). |
| No emails are sent | `QUORUM_SMTP_HOST` isn't set, or the relay rejects the message; check logs. |
| A `restricted` user sees nothing | Their login isn't linked to a member (6.3). |
| Migration appears stuck on startup | Another instance may hold the advisory lock briefly; if truly stuck, inspect `pg_locks`/`pg_stat_activity` for the advisory lock and the migrating session. |

\newpage

# Part 11 — Appendices

## Appendix A — Full role -> permission matrix

Ladder: `restricted` (1) < `member` (2) < `officer` (3) < `admin` (4) <
`superadmin` (5). "own" = only the caller's linked member record.

| Area / action | restricted | member | officer | admin | superadmin |
|---|:--:|:--:|:--:|:--:|:--:|
| Sign in, change own password, sign out | Yes | Yes | Yes | Yes | Yes |
| View own profile / dues / action items | Yes (own) | Yes | Yes | Yes | Yes |
| Dashboard | | Yes | Yes | Yes | Yes |
| Members: list / view | | Yes | Yes | Yes | Yes |
| Members: create / edit | | | Yes | Yes | Yes |
| Members: deactivate (soft-delete) | | | | Yes | Yes |
| Dues: list, create invoices, record payments, update status | | | Yes | Yes | Yes |
| Meetings: view | | Yes | Yes | Yes | Yes |
| Meetings: create/edit, attendance, decisions | | | Yes | Yes | Yes |
| Meetings/plans: **permanent delete** | | | | | Yes |
| Plans / action items / contacts / resources: view | | Yes | Yes | Yes | Yes |
| Plans / action items / contacts / resources: create/edit | | | Yes | Yes | Yes |
| Action items / contacts / resources: **permanent delete** | | | | | Yes |
| Decisions (meeting/plan): create/edit/remove | | | Yes | Yes | Yes |
| Users: list / create / change role | | | | Yes | Yes |
| Users: link to a member | | | | Yes | Yes |
| Users: **permanent delete** | | | | | Yes |
| Grant / revoke `superadmin` | | | | | Yes |
| 2FA, recovery codes, sessions: manage own | Yes | Yes | Yes | Yes | Yes |
| Export own data (JSON) | Yes | Yes | Yes | Yes | Yes |
| Boards (sprint/kanban): view | | Yes | Yes | Yes | Yes |
| Boards: create/move cards, manage sprints, manage columns | | | Yes | Yes | Yes |
| Card conversations: read, write (delete: own; admin any) | | Yes | Yes | Yes | Yes |
| Resources: upload documents, manage folders | | | Yes | Yes | Yes |
| Documents: download (within visibility; audited) | | Yes | Yes | Yes | Yes |
| Minutes journal & motions: record, generate, finalize | | | Yes | Yes | Yes |
| Resources: assign visibility groups | | | Yes | Yes | Yes |
| Reports: export PDFs (audit report: admin) | | | Yes | Yes | Yes |
| Visibility groups: create/edit/delete | | | | Yes | Yes |
| Audit log: view / verify / evidence CSV | | | | Yes | Yes |
| Users: reset another user's password | | | | Yes | Yes |
| Member **erasure** (right to be forgotten) | | | | | Yes |

> Permanent deletes additionally require a **type-to-confirm** step and generate
> an audit entry and notifications (7.2).

## Appendix B — Status & field value glossary

| Field | Allowed values |
|---|---|
| User role | `restricted`, `member`, `officer`, `admin`, `superadmin` |
| Member status | `active`, `inactive`, `suspended` |
| Invoice status | `pending`, `paid`, `partial`, `overdue`, `waived` |
| Transaction provider | `stripe`, `paypal`, `manual` (and similar) |
| Meeting status | `scheduled`, `completed`, `cancelled` |
| Meeting decision outcome | `passed`, `failed`, `tabled`, `noted` |
| Action item status | `open`, `in_progress`, `done`, `cancelled` |
| Action item priority | `low`, `normal`, `high` |
| Plan status | `draft`, `active`, `completed`, `archived` |
| Minutes entry kind | `call_to_order`, `previous_minutes`, `report`, `old_business`, `new_business`, `discussion`, `point_of_order`, `recess`, `adjournment`, `note` |
| Motion business | `new`, `old` |
| Sprint status | `planned`, `active`, `completed` |
| Card type | `epic`, `story`, `task`, `sub_task`, `spike` |
| Card link kind | `depends_on`, `blocked_by`, `related_to` |

## Appendix C — Currency & money reference

- Enter amounts naturally (`100.00`, `1000`, `49.50`) and pick a 3-letter
  currency code; the default is `USD`.
- Internally, money is stored as **integer minor units** for exactness, but you
  never enter minor units directly in the UI.
- Number of decimals by currency (examples):

| Decimals | Examples |
|---|---|
| 0 (whole units) | JPY, KRW, VND, XAF, XOF, and other zero-decimal currencies |
| 2 (default) | USD, EUR, GBP, CAD, AUD, most currencies |
| 3 | BHD, KWD, OMR, TND, and other three-decimal currencies |

- A payment must be in the **same currency** as its invoice; only same-currency
  payments count toward "paid."

## Appendix D — Environment variable quick reference

See 10.3 for the full table. Minimum to start: `QUORUM_DATABASE_URL` and
`QUORUM_JWT_SECRET`. For any TLS/production deployment also set
`QUORUM_BASE_URL=https://…` and, behind an ingress,
`QUORUM_TRUST_PROXY_HEADERS=true`.

## Appendix E — Keyboard & accessibility notes

- Modal dialogs trap focus and can be dismissed with **Escape**; the confirm and
  cancel buttons are reachable by keyboard.
- Clickable rows/cards (e.g. an invoice or meeting) are focusable and activate
  with **Enter** / **Space**; buttons inside them keep their own keyboard
  behavior.
- Transient toast messages are announced to screen readers (errors assertively).
- Forms label their fields; destructive actions require typed confirmation.

## Appendix F — Glossary

- **Access token** — the short-lived credential the browser sends on each API
  call.
- **Refresh token / cookie** — the longer-lived credential that silently renews
  the access token; stored as an `HttpOnly` cookie.
- **Bootstrap** — the one-time creation of the first (super-administrator)
  account.
- **Minor units** — the smallest unit of a currency (e.g. cents); Quorum stores
  money this way for exactness.
- **Soft-delete / deactivate** — marking a member `inactive` without removing
  their history (reversible).
- **Type-to-confirm** — a deletion gate requiring you to type the record's exact
  name.
- **Webhook** — a signed HTTP callback from Stripe/PayPal that tells Quorum a
  payment succeeded.
- **Audit chain** — the hash linkage between audit entries that makes silent
  tampering detectable (verify any time: 6.8).
- **Recovery codes** — single-use codes, shown once at 2FA setup, that stand
  in for a lost authenticator.
- **Visibility group** — a named set of members; a resource tagged with groups
  is visible only inside them (5.7, 6.7).
- **Watermark** — on screen: a faint tiling of the viewer's email (2.3); on
  exported PDFs: the exporter and timestamp on every page (5.10).
- **Integrity seal** — the SHA-256 digest embedded in an exported PDF, also
  recorded in its audit entry; the pair proves the file unaltered (5.10).
- **Finalized minutes** — a meeting journal locked immutable in the database
  after approval (5.3.5).

## Appendix G — Exporting this manual to PDF

One command, from the repository root:

```sh
make manual-pdf        # writes quorum-manual.pdf
```

The script (`scripts/manual-pdf.sh`) picks the best tool it finds:

1. **pandoc + LaTeX** (xelatex or pdflatex) — a title page, clickable table
   of contents with page numbers, and each Part starting on a fresh page.
   Install on Fedora: `sudo dnf install pandoc texlive-scheme-small`.
2. **pandoc + Chrome/Chromium** — used automatically when no LaTeX exists:
   styled HTML printed headlessly to PDF.

If neither combination exists, the script says exactly what to install.
Alternatives that also work: the VS Code *Markdown PDF* extension, or opening
a rendered preview and printing to PDF from the browser.

> **Tip.** The front-matter block at the top of this file (`title`,
> `subtitle`, `date`, `geometry`) drives the title page and layout — bump the
> version line there when you edit the manual.

## Appendix H — Support, licensing & contributing

- **Documentation set.** This manual is the task/role guide. See also
  `README.md` (overview & API reference), `DESIGN.md` (architecture),
  `SECURITY.md` (security model), `DEPLOYMENT.md` (full deployment guide), and
  `CONTRIBUTING.md` (how to contribute).
- **License.** Quorum is licensed under the **GNU Affero General Public License
  v3.0 or later (AGPL-3.0-or-later)**. Self-hosting for your own organization
  carries no obligations; running a *modified* version as a network service for
  others requires offering those users the source. See `LICENSE` and `NOTICE`.
- **Contributing.** Contributions are welcome under the same license via the
  Developer Certificate of Origin — sign off commits with `git commit -s`. See
  `CONTRIBUTING.md`.

---

*End of manual.*
