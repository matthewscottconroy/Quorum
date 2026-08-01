# Evidence & Compliance

This document explains why records produced by Quorum can be trusted by a third
party — an accountant, a lawyer, an external auditor — and gives the exact
procedure for verifying them. It is written to be handed, as-is, to that third
party.

## Summary for auditors

1. Every change made through the application is recorded in an **append-only,
   hash-chained audit log**: who, what, which record, when. Each entry contains
   a SHA-256 digest of its own content *and* the digest of the previous entry.
   Editing, deleting, or inserting anything into retained history breaks the
   chain from that point forward, detectably.
2. The **payments ledger is append-only at the database level**: a recorded
   transaction can never be modified or deleted, only offset by a correcting
   entry — the same discipline as a paper ledger. An invoice's identity (payer,
   amount, currency, period) is frozen at creation; only its lifecycle status
   may change, and every such change is audited.
3. **Attribution is permanent.** An account that has recorded history cannot be
   deleted (the database refuses); it can only be retired. "Who did this" can
   never become "nobody".
4. **Verification is mechanical**, not testimonial: you re-run the hash
   computation yourself and compare. You do not have to trust the operator, the
   developer, or this document.

## What is recorded

Every non-read API request that succeeds is logged with the acting account, the
HTTP action, the resource type, the affected record id, and a server-assigned
timestamp (the database clock — entries cannot be backdated). Additionally:

- security events: logins, failed logins against real accounts, failed
  two-factor attempts, 2FA enrollment/disable/recovery-code regeneration,
  session revocations, password resets, and use of the break-glass CLI
  (`auth.2fa_unlocked_breakglass`);
- **change content for sensitive mutations** (the `detail` column, inside the
  chain): edits to recorded meeting/plan decisions and motions record the
  values that were set; invoice status transitions record the new status; role
  and member-link changes record old → new; quorum-settings changes record the
  new rules. Detail never contains personal profile data, so it coexists with
  erasure;
- **denied attempts**: a 401/403 on a mutating request by an authenticated
  user is recorded as `DENIED(<status>) <method> <path>` — the primary
  insider-threat signal (an account probing above its privileges).

Anonymous failures (e.g. password guesses against unknown emails) are
deliberately *not* recorded, so outsiders cannot spray noise into the log.

## The hash chain, precisely

Each audit row carries a monotonically increasing `seq`, `prev_hash`, and
`entry_hash`, where:

```
entry_hash = SHA-256( seq || '|' || user_id || '|' || action || '|' ||
                      entity_type || '|' || entity_id || '|' ||
                      detail(jsonb::text, '' when null) || '|' ||
                      created_at(UTC, microseconds, 'YYYY-MM-DD HH24:MI:SS.US') || '|' ||
                      prev_hash )
```

This is formula **v2** (migration 0018), which brought the `detail` column —
what a sensitive change actually set — under the chain. The migration
recomputed the retained chain, so a head hash recorded before it will not
match afterwards; the migration file itself is the auditable record of that
event, and engagements re-anchor on the post-migration head.

(nullable fields render as empty strings; `prev_hash` of the first retained row
is empty). The digest is computed by a database trigger on **every** insert —
including inserts made directly in SQL — under a lock that keeps the chain
linear. The same SQL function that computes it is the one verification uses, so
the two can never drift.

## Verifying — three independent ways

1. **In the application** (admin): *Audit log* page → the chain badge, or
   `GET /api/v1/audit/verify` → `{ok, entries, head_seq, head_hash}`.
2. **At the command line** (operator): `quorum -verify-audit` — exits 0 if
   intact, 1 with the first broken sequence number if not. Suitable for cron.
   The server also re-verifies every 6 hours and exposes the result as the
   `quorum_audit_chain_intact` metric (alert rule shipped in
   `ops/prometheus-alerts.yml`).
3. **Independently, from an export** (you): take *Export evidence (CSV)* (or
   `GET /api/v1/audit/export.csv`). The first line stamps the chain status and
   head hash at export time. For each row, recompute the formula above with any
   SHA-256 tool and confirm (a) it equals `entry_hash`, and (b) each row's
   `prev_hash` equals the previous row's `entry_hash`. A dozen lines of Python
   suffice; no Quorum software is required.

**At each engagement, record the head hash and head seq.** At the next
engagement, verify that the previous head is still in the chain at the same
position with the same hash. That proves no retained history between your two
visits was rewritten.

## Tamper resistance vs. tamper evidence (threat model)

| Actor | What they can do | What stops / catches it |
|---|---|---|
| Application user (any role) | Only what their role allows; every mutation and every denied attempt is logged | RBAC + audit log |
| Insider with app-level SQL (e.g. injection, leaked app credentials) | Cannot `UPDATE` audit rows, cannot delete audit rows younger than 90 days, cannot alter or delete ledger transactions, cannot change invoice identity, cannot backdate entries | Database triggers refuse, in the database itself |
| Database owner/superuser | Can disable triggers and edit anything | **Cannot do so undetectably**: any edit breaks the hash chain and is flagged by verification; recomputing the whole chain from the tampered row forward changes the head hash, which no longer matches the head recorded off-box |
| Superuser who also controls all backups and every previously recorded head hash | Out of scope | This is why head hashes belong in auditors' engagement notes and why backups go off-box (BACKUP.md) |

The retention floor (audit rows younger than **90 days** can never be deleted,
enforced in the database) guarantees a minimum evidence window even against a
misconfigured retention policy; the configured retention
(`QUORUM_AUDIT_RETENTION_DAYS`, default 365, minimum 90) governs routine
pruning beyond that. Pruning removes the chain's *prefix* only, which does not
break verification of what remains.

## Exported PDF documents

Every PDF report is watermarked with the exporting account and UTC time (as a
light diagonal stamp on each page and in each footer) and carries an embedded
integrity stamp: "Integrity (SHA-256): <digest>", where the digest is computed
over the document's exact bytes with the digest field zeroed. Two checks:

1. **Integrity** (offline, stdlib Python): `ops/verify-pdf-export.py file.pdf`
   re-zeros the field, hashes, and compares. Any post-export edit fails.
2. **Authenticity**: the same digest is recorded in the audit log's
   `EXPORT <what>` entry (`detail.sha256`), inside the hash chain — so a
   fabricated document with a self-consistent stamp still fails, because no
   chained export entry carries its digest.

## Financial records

- `transactions` (payments received): append-only, database-enforced.
  Corrections are new entries. Sum of the ledger = money received, always.
- `dues_invoices` (receivables): amount, currency, member, and period are
  frozen at creation; status transitions (pending → paid/overdue/cancelled…)
  are the mutable lifecycle, each transition audited. Invoices cannot be
  deleted — a cancelled invoice remains visible.
- Multi-currency analytics convert at explicitly recorded, effective-dated
  exchange rates (`fx_rates`, admin-entered, themselves audited); amounts with
  no recorded rate are excluded and flagged, never silently converted.
- Money is stored in integer minor units end to end; no floating point touches
  a stored amount.

## Governance records

- A motion that reached a decision (carried/failed/tabled/withdrawn) can no
  longer be edited **or deleted**; its votes are preserved with it.
- Recorded meeting decisions (the minutes) can be corrected by officers — every
  correction is audited — but removed only by a superadmin.
- Meetings with recorded decisions or decided motions cannot be deleted at all;
  they can only be marked cancelled.
- Emailed ballots are single-use, hashed at rest, expiring, and consumed
  atomically with the vote they cast.

## Privacy interplay (GDPR-style erasure)

Right-to-erasure anonymizes a member in place: personal fields are cleared, the
linked login is retired (placeholder email, unusable password, second factor
removed), and unused ballot tokens are destroyed — while invoices, payments,
attendance, and votes remain, attributed to the anonymized row. The audit log
stores only identifiers and action names, never profile data, so erasure and
the immutable log do not conflict. The erasure itself is an audited action.

## Known limits — read these

- For sensitive records (decisions, motions, invoice status, roles, quorum
  settings) the log now records the changed values in chain-protected detail.
  For other mutable records (contacts, plans, meeting notes) it proves
  actor/time/target only — by design, since their bodies may contain personal
  data that must not outlive erasure.
- In-app timestamps come from the database clock. Run the database on NTP; for
  disputes where absolute time is critical, corroborate with off-box backups.
- A verification performed on the live system trusts the live database to run
  the query honestly. For adversarial settings, verify from the CSV export on
  your own machine (method 3 above) — that computation involves no Quorum code.
- Integrity mechanisms are tested in CI against real PostgreSQL
  (`internal/repo/audit_chain_integration_test.go`), including the
  triggers-disabled tamper scenario.
