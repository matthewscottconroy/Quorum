# Privacy Policy — TEMPLATE

> **⚠ This is a starting-point template, not legal advice.** Data-protection
> obligations vary by jurisdiction (GDPR, UK GDPR, CCPA, PIPEDA, …). Have
> counsel review and complete this before publishing it. Bracketed fields are
> placeholders; the technical statements about Quorum's behavior are accurate
> as shipped and cross-referenced to COMPLIANCE.md.

**Data controller:** [ORGANIZATION NAME], [address], [contact email]

## What we collect

- **Membership records:** name, email, phone, postal address, membership tier
  and status, join date, and any notes officers record about your membership.
- **Account data:** login email, a salted password hash (bcrypt — we never
  store your password), optional two-factor secrets, session tokens.
- **Financial records:** dues invoices and payments (amounts, dates, payment
  provider references). We do not store card numbers; payments are processed by
  [Stripe/PayPal/…], whose privacy policies apply.
- **Governance records:** meeting attendance, proxies, votes on motions.
- **Operational records:** an audit log of actions taken in the system (who did
  what, when — identifiers only, no profile data) and email delivery of notices
  you have not opted out of.

## Why (legal bases — counsel to confirm per jurisdiction)

- Administering your membership and collecting dues ([contract]).
- Maintaining accurate governance and financial records ([legitimate
  interest / legal obligation] — e.g. corporate and tax record-keeping).
- Security of the service ([legitimate interest]).

## Retention

- Membership, financial, and governance records: retained for
  [N years / statutory period].
- Audit log: [365] days (minimum 90), then pruned automatically.
- Backups: [N] days, stored [where], encrypted [how].

## Your rights

- **Access / portability:** download your data anytime (Settings → Export my
  data, JSON).
- **Correction:** ask [contact] or any officer.
- **Erasure:** on request we anonymize your personal data in place. Financial
  and governance records (invoices, payments, attendance, votes) are retained
  in anonymized form because [statutory record-keeping / legitimate interest];
  they can no longer be linked to you. Your login is disabled as part of
  erasure.
- **Notification preferences:** per-category email opt-outs in the app; in-app
  notices remain visible when you log in.
- **Complaints:** [supervisory authority / contact].

## Security measures (technical summary)

Passwords hashed with bcrypt; optional TOTP two-factor with single-use recovery
codes; role-based access control; TLS in transit [confirm deployment]; an
append-only, hash-chained audit log (see COMPLIANCE.md); automated, verified
backups; encryption at rest [confirm deployment].

## Sharing

We do not sell personal data. Processors: [SMTP provider], [payment
provider(s)], [hosting provider] — each under [DPA reference].

_Last updated: [DATE]. Changes will be announced via [channel]._
