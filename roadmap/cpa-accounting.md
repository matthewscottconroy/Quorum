# Plan: CPA-Grade Accounting

Status: **Phase A core shipped** (migration 0031: chart of accounts,
append-only balanced journal, DB-trigger auto-posting for invoices/
payments/waivers with the same-currency rule, backfill, gl_reconcile_ar
invariant, trial-balance API). Remaining in A: nightly reconcile alert.
**Phase B core shipped** (migration 0032: funds with dedicated GL cash
accounts, N-of-M + named-signer policies, password-confirmed recorded
approvals with IP evidence, transfers and completion posting in-transaction,
overdraft guards, frozen terminal requests, policy changes voiding in-flight
approvals). Deviation from plan: overspend has NO quorum override — it hard
refuses; revisit if a real need appears.
**Phases C–D core shipped** (migration 0033): org_settings (fiscal-year
start + how-to-pay; org-agnostic — settings drive defaults/labels only,
statements take arbitrary ranges), month period-close with DB posting
guard and audited reopen, admin adjusting entries, editable chart with
code/type frozen after postings, income statement + balance sheet +
AR aging per currency, purchase expense-account categorization, and the
CPA export pack (ZIP: sealed statements PDF + trial balance / GL /
statements / funds / aging CSVs + EVIDENCE.txt with the audit-chain
head). Remaining ideas parked: nightly reconcile alert, formal year-end
closing entries (net income is computed live instead), functional-
currency translation — all CPA-conversation items. Goal: books a CPA can test, rely on, and use to prepare the org's
government filings — plus purpose-restricted accounts whose spending
requires multi-person sign-off, with every completed purchase landing on
the books automatically, verifiably, and audit-ready for tax purposes.

## 1. Where we stand vs. where a CPA needs us

Quorum today is a strong **receivables subledger** with unusual integrity
properties: append-only payment ledger, frozen invoice identity, and a
hash-chained audit log a third party can verify. A CPA, however, evaluates
**complete books**, and we are missing the other half:

| CPA needs | Today |
|---|---|
| Double-entry general ledger, debits = credits always | absent — single-sided records |
| Chart of accounts | absent |
| The expense side: purchases, disbursements, payees | absent entirely |
| Purpose/fund tracking (restricted money) | absent |
| Period close (locked months, closing entries) | absent |
| Financial statements & exports | dues/receivables only |
| Authorization evidence for spending | n/a (no spending exists) |

**Answer to the framing question: yes — double-entry is the foundation**,
not an optional extra. It is the mechanism by which "a purchase
automatically shows up on the books" becomes trustworthy: every event posts
balanced journal lines, so completeness is structural, not procedural.

## 2. Architecture

### 2.1 Chart of accounts & the journal (the core)

- `accounts`: code, name, type (`asset, liability, net_assets, income,
  expense`), optional parent, active flag. Ship a small default chart
  (Cash-Operating, Cash-per-fund, Accounts Receivable, Dues Income,
  Donations, per-category Expenses…) editable by admins until first use.
- `journal_entries` + `journal_lines`: entry (date, memo, source event
  reference, created_by) with lines (account, debit, credit, currency,
  minor units). **Database triggers enforce**: per-entry debits = credits
  per currency; lines append-only; entries immutable once posted —
  corrections are reversing entries, mirroring the existing transactions
  ledger discipline. Entries join the audit hash chain pattern (their own
  chained digest), so the GL inherits the evidence-grade story that
  COMPLIANCE.md already tells auditors.
- Accounting basis: the schema is accrual-shaped (A/R exists today);
  reports can present cash-basis views. **Open question for the CPA:
  which basis the org files under** — decide before Phase C.

### 2.2 Auto-posting bridge (existing events become entries)

Every financial event Quorum already records gains a posting rule:

| Event (exists today) | Journal entry |
|---|---|
| Invoice created | DR Accounts Receivable / CR Dues Income |
| Payment recorded (any provider) | DR Cash-{provider account} / CR Accounts Receivable |
| Invoice waived | DR Waived Dues (contra-income) / CR Accounts Receivable |
| Refund/correction entry | reversing lines of the above |

Posting is synchronous and transactional with the source event — an
invoice cannot exist without its entry. A one-time **backfill migration**
derives opening entries from the historical invoices/transactions tables,
so the books start complete, and a reconciliation report proves
subledger == GL forever after (a check the nightly job re-runs and the
chain-gauge pattern can alert on).

### 2.3 Purpose-restricted accounts with multi-person sign-off (your ask)

- `funds`: name, purpose statement, linked cash account, balance derived
  from the GL (never stored). Optionally tied to a visibility group for
  who can *see* it.
- **Spending policy per fund**: `approvals_required` (N), plus an optional
  list of **named required signers** — both must be satisfied (N-of-M
  overall AND every named signer). Policy changes are themselves
  admin-gated and audited.
- **Purchase workflow** (`purchase_requests`): requester (officer+) files
  amount, fund, payee, memo, and links supporting documents from the
  resource library (quotes, invoices — reusing visibility, the download
  ledger, everything). States: `draft → pending → approved → completed`
  (or `rejected/cancelled`). Each **approval is an individually recorded
  act** — approver, timestamp, client IP — and approvers must re-enter
  their password at signing (same bar as 2FA enrollment), making each
  signature deliberate evidence. Requester cannot approve their own
  request; policy edits void in-flight approvals.
- **Completion posts automatically**: marking the purchase completed (with
  the actual paid amount and a receipt document link) writes the balanced
  entry — DR {expense account} / CR {fund cash account} — in the same
  transaction that closes the request. Nothing reaches "completed" without
  hitting the books; nothing hits the books without its authorization
  trail. Overspend guard: completion fails if it would drive the fund
  negative, unless a signer quorum explicitly overrides (also recorded).

### 2.4 Period close

`accounting_periods` (month granularity): open → closed by an admin after
a close checklist (bank reconciliation done — see the Zelle/Venmo plan's
Phase 3, no pending purchases, subledger==GL check green). A trigger
refuses postings dated into a closed period; corrections post into the
open period, the way accountants expect. Year-end close rolls income and
expense into net assets.

### 2.5 The CPA export pack

One action, per period or fiscal year, producing sealed PDFs (watermarked,
SHA-256, EXPORT-audited — the existing report machinery) **plus** machine
CSVs for the CPA's tools:

- Trial balance; General ledger detail per account; Income statement
  (statement of activities) and Balance sheet (statement of financial
  position), with per-fund columns; Fund balance & restricted-spending
  report (every purchase with its approval evidence); A/R aging; vendor
  payment totals (1099 screening); the reconciliation report; and the
  **evidence bundle** — audit chain head, `quorum -verify-audit` output,
  and the verification scripts, so the CPA can independently confirm the
  records weren't altered. COMPLIANCE.md gains a CPA engagement section.

Filing forms themselves (990/990-EZ, state charitable registrations, 1120
et al.) remain **the CPA's work product** — Quorum's job is to hand over
numbers they can trace to source in minutes. Which forms apply depends on
the org's tax status: **open question #1 for the stakeholders.**

## 3. Phasing & estimates

| Phase | Delivers | Est. |
|---|---|---|
| **A — GL core** | chart of accounts, journal w/ triggers+chain, auto-posting bridge, backfill, subledger==GL check | 2–3 wks |
| **B — Funds & purchases** | funds, spending policies, N-of-M + named-signer approvals, auto-posting completion, overspend guard | 2–3 wks |
| **C — Close & statements** | period locks, close checklist, trial balance, income statement, balance sheet, per-fund reporting | 1–2 wks |
| **D — CPA pack & engagement** | export pack, evidence bundle, COMPLIANCE.md update, dry-run with a real CPA | 1 wk + CPA feedback loop |

Each phase ships behind the usual bar (migrations with tested downs,
integration tests — the balanced-entry and period-lock triggers get the
adversarial treatment — live smoke, sealed exports).

## 4. Open questions before Phase A starts

1. Org's tax status & required filings (drives report templates).
2. Fiscal year end; cash vs accrual filing basis (ask the CPA).
3. Single functional currency for the GL? (Recommend yes — book FX
   gain/loss lines on settlement; multi-currency GL is a large complexity
   multiplier the org likely doesn't need.)
4. Who holds signing authority today, on paper? Encode reality, don't
   invent policy in software.
5. Engage the CPA **now**, at design time — their chart-of-accounts and
   basis preferences are far cheaper to honor before Phase A than after.
