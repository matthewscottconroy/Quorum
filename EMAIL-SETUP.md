# Email Setup, From Zero

Quorum sends email for two things: **password-reset links** and
**notifications** (ballots, reminders). Until SMTP is configured, everything
else works — but "Forgot password" quietly can't deliver, so set this up
soon after launch. This guide assumes no prior email knowledge.

## 1. What you are actually setting up (and what you are not)

**You are NOT running an email server.** Nobody sane does anymore. Quorum is
an email *client* — like the Mail app on a phone. It composes a message and
hands it to a **relay** (a company whose entire job is delivering mail), the
same way your phone hands mail to Gmail's servers.

SMTP is just the protocol for that hand-off — "the fax line for email." The
four `.env` values you'll fill in are nothing more than the relay's address
and your account on it:

```
QUORUM_SMTP_HOST=  which relay to talk to
QUORUM_SMTP_PORT=  587 (the standard client hand-off port)
QUORUM_SMTP_USER=  your account on the relay
QUORUM_SMTP_PASS=  its password
QUORUM_EMAIL_FROM= the From: address recipients will see
```

## 2. Why your last attempt got spam-blocked (this matters)

When a mail server receives a message claiming to be from
`quorum@your-domain.com`, it does not trust the claim. It looks up DNS
records on **your-domain.com** asking: *did the domain's owner authorize
this sending server?* Those records are:

- **SPF** — "these servers may send for this domain" (a TXT record)
- **DKIM** — a cryptographic signature on each message, verified against a
  public key published in your DNS
- **DMARC** — "here's what to do with mail that fails the above"

Mail sent from a random machine with none of these — which is what almost
certainly happened to your earlier project — fails every check and dies in
spam. **The fix is not a better server; it's publishing the right DNS
records and sending through a relay with a good reputation.** The setup
below makes the relay generate those records and you click them into DNS.

## 3. Choosing the relay

| Option | Good | Bad | Verdict |
|---|---|---|---|
| **Amazon SES** | ~$0.10 per 1,000 mails; same AWS account; with Route 53 the DNS records are added by a single button | one-time "production access" request form | **Use this** |
| Gmail + app password | zero new accounts | ~500 mails/day cap, ties the org's mail to a personal Gmail, deliverability is "fine-ish" | acceptable stopgap |
| Postmark / Mailgun / Brevo | excellent deliverability | another vendor account | fine if you prefer |

## 4. The walkthrough: Amazon SES (~20 minutes + one wait)

AWS reshuffles this console regularly — currently it's a **setup wizard**
that asks everything in one flow. So instead of click-by-click, here is the
*shape* of what you're answering; every wizard layout asks these same five
questions, whatever order it puts them in. Keep the region shown top-right
the same as your server's, and stay in it.

### Part A — the wizard, question by question

**Q1 — "Email address" or "Sending domain"?** Choose **domain**, and enter
yours (e.g. `chuposkamountain.com`). Verifying a whole domain lets you send
from any address at it and is what makes DKIM possible.

**Q2 — MAIL FROM domain (it may suggest one, or ask).** Say **yes**, and
accept the suggested subdomain (`mail.chuposkamountain.com`) or type that
yourself. What this is: every email has *two* senders — the one humans see
(`quorum@chuposkamountain.com`, the "From:" header) and a hidden
envelope/bounce address that mail servers actually talk to (also called
Return-Path). Without a custom MAIL FROM, the envelope says
`...amazonses.com` — technically fine, but the two senders don't match your
domain, which weakens your DMARC alignment and looks less legitimate.
Setting it means both senders are yours. It requires two extra DNS records
(an MX and an SPF TXT on the subdomain) — the wizard creates them for you
in Route 53.

**Q3 — "Behavior on MX failure."** Choose **Use default MAIL FROM domain**
(the fallback option; the alternative is *Reject message*). Translation: if
the DNS records for your MAIL FROM subdomain ever break or go missing,
should SES (a) fall back to sending with its own `amazonses.com` envelope —
mail keeps flowing, alignment temporarily degrades — or (b) refuse to send
anything at all? For an org whose email is password resets and meeting
notices, silent non-delivery is the worse failure. Take the fallback.

**Q4 — DKIM (usually pre-checked as "Easy DKIM").** Leave it **enabled**,
RSA 2048. This is the cryptographic signature that receivers verify against
your DNS; it's the single biggest spam-folder antidote. The wizard produces
three CNAME records.

**Q5 — publish the DNS records.** Because your DNS lives in Route 53, the
wizard offers a **publish/create records in Route 53** button covering all
of it (DKIM CNAMEs + the MAIL FROM MX and SPF). Click it — no copy-paste.
If you ever *don't* see the button, each record is listed with name/type/
value and goes into Route 53 → your hosted zone → Create record.

Then wait: the identity's status flips from *Pending* to **Verified**,
usually within minutes (up to an hour). Coffee break.

### Part B — get out of the sandbox

New SES accounts start in a **sandbox**: you can only send *to* addresses
you've verified. Fine for testing, useless for real members.

6. To test right now: **Identities → Create identity → Email address**,
   enter your own Gmail, click the link SES mails you.
7. For real use: left sidebar → **Account dashboard** → **Request
   production access**. Fill the form honestly: transactional mail for a
   member-management app — password resets and meeting notifications to
   your organization's own members, low volume. Approval typically arrives
   within a day.

### Part C — credentials and configuration

8. Left sidebar → **SMTP settings**. Note the endpoint shown, e.g.
   `email-smtp.us-east-1.amazonaws.com`. Click **Create SMTP credentials**
   → accept the defaults → **download the credentials** (a username and a
   password — this is the only time the password is shown; put both in your
   password manager next to the other secrets).
9. On the server:
   ```
   sudo nano /opt/quorum/.env
   ```
   Set:
   ```
   QUORUM_SMTP_HOST=email-smtp.us-east-1.amazonaws.com
   QUORUM_SMTP_PORT=587
   QUORUM_SMTP_USER=paste-the-smtp-username
   QUORUM_SMTP_PASS=paste-the-smtp-password
   QUORUM_EMAIL_FROM=quorum@chuposkamountain.com
   QUORUM_SMTP_REQUIRE_TLS=true
   ```
   The From address must be at your verified domain; the mailbox itself
   doesn't need to exist unless you want replies. Ctrl+O, Enter, Ctrl+X.
   ```
   sudo systemctl restart quorum
   ```

### Part D — prove it works

10. In a private browser window: your login page → **Forgot password** →
    your own email → submit. The mail should arrive within a minute —
    check spam the first time. If it's in spam, mark "Not spam" once;
    reputation warms up quickly on a verified domain.
11. If nothing arrives: `sudo journalctl -u quorum | grep -i -m5 mail`
    shows the send attempt and the relay's exact complaint.

### Part E — one optional polish record

Add a DMARC record so big providers treat you as a serious sender. Route 53
→ your hosted zone → **Create record**: name `_dmarc`, type **TXT**, value:

```
"v=DMARC1; p=none; rua=mailto:you@your-real-email.com"
```

`p=none` means "just report, don't punish" — the right starting policy.

## 5. The Gmail stopgap (if you want mail today, properly labeled as a shortcut)

Requires 2-Step Verification on the Google account. Google Account →
Security → 2-Step Verification → **App passwords** → create one named
`quorum`. Then:

```
QUORUM_SMTP_HOST=smtp.gmail.com
QUORUM_SMTP_PORT=587
QUORUM_SMTP_USER=youraddress@gmail.com
QUORUM_SMTP_PASS=the-16-character-app-password
QUORUM_EMAIL_FROM=youraddress@gmail.com
QUORUM_SMTP_REQUIRE_TLS=true
```

Restart, test as in Part D. Caveats: mail comes "from" your personal Gmail,
daily caps apply, and Google occasionally blocks "suspicious" server logins.
Migrate to SES when it matters.

## 6. Decoder ring: the terms on AWS's forms

| Term | Plain meaning |
|---|---|
| **Identity** | A domain or address you've proven you control and may send as. |
| **MAIL FROM domain** | The hidden *envelope* sender (a.k.a. Return-Path / bounce address) — where bounces go and what SPF actually checks. Custom = a subdomain of yours (`mail.…`) instead of amazonses.com. |
| **Behavior on MX failure** | What SES does if your MAIL FROM subdomain's DNS breaks: fall back to amazonses.com (mail still flows — pick this) or reject (nothing sends). |
| **MX record** | DNS entry saying "mail for this (sub)domain is handled by that server." The MAIL FROM subdomain needs one so bounces have somewhere to land. |
| **SPF** | DNS TXT record listing servers allowed to send for a domain. Receivers check it against the *envelope* sender — which is why the custom MAIL FROM matters. |
| **DKIM** | Per-message cryptographic signature, verified via your DNS. "Easy DKIM" = SES manages the keys, you host three CNAMEs. |
| **DMARC** | Your published policy on what receivers should do when SPF/DKIM fail, plus where to send reports. `p=none` = observe only. |
| **Alignment** | DMARC's core test: do the visible From domain and the SPF/DKIM domains *match*? Custom MAIL FROM + DKIM on your domain = aligned. |
| **Sandbox / production access** | New SES accounts can only mail verified addresses until you request production access (a short form, ~a day). |
| **Configuration set** | Optional per-stream settings/metrics bundle. You can leave it empty/default — Quorum doesn't need one. |
| **Bounce / complaint rate** | The metrics AWS watches; keep them low by mailing only real members (Quorum only mails your member list, so this takes care of itself). |

## 6a. FAQ

- **Is this receiving email too?** No. Quorum only sends. Nothing here
  creates inboxes; replies to a nonexistent From address just bounce.
- **Port 25?** Blocked by EC2 and irrelevant — client hand-off uses 587.
- **Does the server's IP reputation matter?** No — that's the relay's
  problem, which is the whole point of using one.
