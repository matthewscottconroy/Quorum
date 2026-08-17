import { api, apiDownload, getUser, currentMemberId } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDate, formatMoney, openModal, guardButton , moneyExponent } from '../utils.js';

/**
 * `<page-my-account>` — self-service, read-only view of the signed-in user's own
 * member profile, dues invoices, and assigned action items. Usable by any role,
 * and the sole landing view for `restricted` users. Reads are ownership-scoped:
 * GET /members/{id}, /members/{id}/dues, /members/{id}/action-items where {id}
 * is the JWT's member id.
 */
class PageMyAccount extends HTMLElement {
  connectedCallback() {
    this.render();
  }

  _renderHowToPay(text) {
    if (!text) return;
    const box = document.createElement('div');
    box.className = 'card';
    box.style.cssText = 'padding:1rem;margin-top:1rem';
    box.innerHTML = `<h3 style="margin:0 0 .4rem;font-size:.95rem">💳 How to pay</h3>
      <div style="white-space:pre-wrap;font-size:.88rem">${esc(text)}</div>`;
    this.appendChild(box);
  }

  async render() {
    const user = getUser();
    const memberId = currentMemberId();
    // Org pay info (template drives Pay links, how_to_pay is a footer card).
    // Loaded here so a single render has everything — no second pass.
    try {
      const st = await api('GET', '/settings/org');
      this._payTemplate = st?.payment_link_template || '';
      this._howToPay = st?.how_to_pay || '';
    } catch { this._payTemplate = ''; this._howToPay = ''; }

    this.innerHTML = `
      <div class="page-header">
        <h1>My Account</h1>
        <div style="display:flex;gap:.5rem;flex-wrap:wrap">
          <button class="btn-secondary" id="statement-btn">Statement (PDF)</button>
          <button class="btn-secondary" id="calendar-btn">Subscribe to calendar</button>
          <button class="btn-secondary" id="export-me-btn">Export my data</button>
        </div>
      </div>`;
    this.querySelector('#export-me-btn').addEventListener('click', async () => {
      try { await apiDownload('/auth/me/export', 'my-quorum-data.json'); }
      catch (err) { toast(err.error ?? 'Export failed','error'); }
    });
    this.querySelector('#statement-btn').addEventListener('click', async () => {
      try { await apiDownload(`/auth/me/statement.pdf?year=${new Date().getFullYear()}`, 'member-statement.pdf'); }
      catch (err) { toast(err.error ?? 'Could not build statement', 'error'); }
    });
    this.querySelector('#calendar-btn').addEventListener('click', () => this._openCalendarModal());

    if (!memberId) {
      this.innerHTML += `
        <div class="card" style="padding:1.5rem;max-width:640px">
          <p style="font-size:.9rem;margin-bottom:.5rem"><strong>Signed in as:</strong> ${esc(user?.email ?? '')}</p>
          <div class="empty-state" style="padding:1rem">
            <p>Your login isn't linked to a member record yet — contact an administrator.</p>
          </div>
        </div>`;
      return;
    }

    const body = document.createElement('div');
    body.innerHTML = `<div style="text-align:center;padding:1.5rem"><span class="spinner"></span></div>`;
    this.appendChild(body);

    const [member, dues, items] = await Promise.all([
      api('GET', `/members/${memberId}`).catch(() => null),
      api('GET', `/members/${memberId}/dues`).catch(() => null),
      api('GET', `/members/${memberId}/action-items`).catch(() => null),
    ]);

    if (!member) {
      body.innerHTML = `<div class="empty-state"><p>Failed to load your account.</p></div>`;
      toast('Failed to load your account', 'error');
      return;
    }

    // Distinguish a failed fetch (null) from a genuinely empty result so each
    // section can show an error state rather than a misleading empty state.
    const duesFailed = dues === null;
    const itemsFailed = items === null;
    const invoices = dues?.data ?? dues ?? [];
    const actionItems = items?.data ?? items ?? [];

    body.innerHTML = `
      <section class="card" style="padding:1.25rem;max-width:640px;margin-bottom:1.25rem">
        <h2 style="font-size:1rem;margin-bottom:.75rem">Profile</h2>
        <p style="font-size:.9rem;margin-bottom:.35rem"><strong>Name:</strong> ${esc(member.display_name)}</p>
        <p style="font-size:.9rem;margin-bottom:.35rem"><strong>Email:</strong> ${esc(member.email ?? user?.email ?? '—')}</p>
        <p style="font-size:.9rem;margin-bottom:.35rem"><strong>Phone:</strong> ${esc(member.phone ?? '—')}</p>
        <p style="font-size:.9rem;margin-bottom:.35rem"><strong>Tier:</strong> ${esc(member.tier)}</p>
        <p style="font-size:.9rem;margin-bottom:.35rem"><strong>Status:</strong> <span class="badge badge-${esc(member.status)}">${esc(member.status)}</span></p>
        <p style="font-size:.9rem;margin-bottom:.35rem"><strong>Dues status:</strong> <payment-status-badge status="${esc(member.dues_status ?? 'none')}"></payment-status-badge></p>
        <p style="font-size:.9rem"><strong>Member since:</strong> ${fmtDate(member.joined_at)}</p>
      </section>

      <section class="card table-scroll" style="margin-bottom:1.25rem">
        <div class="panel-header" style="padding:.75rem 1rem;border-bottom:1px solid var(--color-border)"><h2 style="font-size:1rem">My dues</h2></div>
        ${duesFailed
          ? '<p class="empty-state" style="padding:1rem;color:var(--color-danger)">Couldn\'t load your dues — try again.</p>'
          : invoices.length === 0
          ? '<p class="empty-state" style="padding:1rem">No invoices.</p>'
          : `<table>
              <thead><tr><th>Period</th><th class="num">Amount</th><th>Due date</th><th>Status</th><th></th></tr></thead>
              <tbody>
                ${invoices.map(inv => {
                  const open = inv.status !== 'paid' && inv.status !== 'waived';
                  return `
                  <tr>
                    <td>${esc(inv.period_label)}</td>
                    <td class="num">${formatMoney(inv.amount_minor, inv.currency)}</td>
                    <td>${fmtDate(inv.due_date)}</td>
                    <td><span class="badge badge-${esc(inv.status)}">${esc(inv.status)}</span></td>
                    <td style="text-align:right;white-space:nowrap">
                      ${open && this._payTemplate ? `<a class="btn-secondary" style="font-size:.75rem;padding:.2rem .5rem" href="${esc(this._payLink(inv))}" target="_blank" rel="noopener">Pay</a>` : ''}
                      ${open ? `<button class="btn-ghost report-pay" data-id="${esc(inv.id)}" data-period="${esc(inv.period_label)}" style="font-size:.75rem">I've paid</button>` : ''}
                    </td>
                  </tr>`;
                }).join('')}
              </tbody>
             </table>`}
      </section>

      <section class="card" style="overflow:hidden">
        <div class="panel-header" style="padding:.75rem 1rem;border-bottom:1px solid var(--color-border)"><h2 style="font-size:1rem">My action items</h2></div>
        ${itemsFailed
          ? '<p class="empty-state" style="padding:1rem;color:var(--color-danger)">Couldn\'t load your action items — try again.</p>'
          : actionItems.length === 0
          ? '<p class="empty-state" style="padding:1rem">No action items assigned to you.</p>'
          : `<table>
              <thead><tr><th>Item</th><th>Due date</th><th>Priority</th><th>Status</th></tr></thead>
              <tbody>
                ${actionItems.map(a => `
                  <tr>
                    <td>${esc(a.title)}</td>
                    <td>${fmtDate(a.due_date)}</td>
                    <td><span class="badge badge-${a.priority === 'high' ? 'overdue' : a.priority === 'low' ? 'none' : 'open'}">${esc(a.priority)}</span></td>
                    <td><span class="badge badge-${esc(a.status)}">${esc(a.status)}</span></td>
                  </tr>`).join('')}
              </tbody>
             </table>`}
      </section>
    `;

    body.querySelectorAll('.report-pay').forEach(btn => {
      btn.addEventListener('click', () => this._openReportModal(btn.dataset.id, btn.dataset.period));
    });
    this._renderHowToPay(this._howToPay);
  }

  /** Builds the configured pay-link for an invoice, filling placeholders.
      Exponent-aware: a hardcoded /100 told JPY members to pay 1/100th of
      their dues — this number gets typed into Venmo on trust. */
  _payLink(inv) {
    const exp = moneyExponent(inv.currency);
    const major = (Number(inv.amount_minor) || 0) / 10 ** exp;
    return (this._payTemplate || '')
      .replace('{amount_major}', encodeURIComponent(major.toFixed(exp)))
      .replace('{reference}', encodeURIComponent(inv.id));
  }

  /** "I've paid" self-report: files a pending report for officer confirmation. */
  _openReportModal(invoiceID, period) {
    const { dialog, close } = openModal({
      title: `Report a payment — ${period}`,
      maxWidth: '440px',
      body: `
        <div class="modal-body">
          <p style="font-size:.85rem;color:var(--color-text-muted);margin-top:0">
            Let the treasurer know you've sent payment (Zelle, check, etc.). They'll
            confirm it against the account; this doesn't mark the invoice paid on its own.</p>
          <div class="form-group"><label for="rp-method">How did you pay? *</label>
            <input id="rp-method" placeholder="Zelle, check, Venmo…"></div>
          <div class="form-group"><label for="rp-ref">Reference / confirmation # (optional)</label>
            <input id="rp-ref"></div>
          <div class="form-group"><label for="rp-note">Note (optional)</label>
            <textarea id="rp-note" rows="2"></textarea></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="rp-cancel">Cancel</button>
          <button class="btn-primary" id="rp-send">Send</button>
        </div>`,
    });
    dialog.querySelector('#rp-cancel').addEventListener('click', close);
    const send = dialog.querySelector('#rp-send');
    send.addEventListener('click', guardButton(send, async () => {
      const method = dialog.querySelector('#rp-method').value.trim();
      if (!method) { toast('Tell us how you paid', 'error'); return; }
      try {
        await api('POST', `/dues/${invoiceID}/report-payment`, {
          method,
          reference: dialog.querySelector('#rp-ref').value.trim(),
          note: dialog.querySelector('#rp-note').value.trim(),
        });
        toast('Thanks — the treasurer will confirm it', 'success');
        close();
      } catch (err) { toast(err.error ?? 'Could not send', 'error'); }
    }));
  }

  /** Shows the personal calendar-subscription URL (created on demand).
      Tokens are hashed server-side, so an existing feed's URL cannot be
      re-shown — only a fresh one from Reset link. */
  async _openCalendarModal() {
    let url = '';
    try { url = (await api('POST', '/calendar/subscription'))?.url ?? ''; }
    catch { toast('Could not create your calendar feed', 'error'); return; }
    const placeholder = url ? '' : 'Your feed is active. Its URL was shown when created — use “Reset link” to get a new one (the old one stops working).';
    const { dialog, close } = openModal({
      title: 'Subscribe to the meeting calendar',
      maxWidth: '520px',
      body: `
        <div class="modal-body">
          <p style="font-size:.88rem">Add this URL as a <strong>subscribed calendar</strong> in Google
            Calendar, Apple Calendar, or Outlook — meetings then appear and stay up to date automatically.</p>
          <input id="cal-url" readonly value="${esc(url)}" placeholder="${esc(placeholder)}" style="font-family:monospace;font-size:.8rem">
          <p style="font-size:.78rem;color:var(--color-text-muted)">Keep this URL private — anyone with it can see the meeting schedule.</p>
        </div>
        <div class="modal-footer">
          <button class="btn-ghost" id="cal-rotate" style="color:var(--color-danger)">Reset link</button>
          <button class="btn-secondary" id="cal-copy" ${url ? '' : 'disabled title="The URL was shown when the feed was created — use Reset link to get a new one"'}>Copy</button>
          <button class="btn-primary" id="cal-done">Done</button>
        </div>`,
    });
    dialog.querySelector('#cal-done').addEventListener('click', close);
    dialog.querySelector('#cal-copy').addEventListener('click', () => {
      if (!url) return; // never blank someone's clipboard and call it a copy
      navigator.clipboard?.writeText(url).then(() => toast('Copied', 'success')).catch(() => {});
    });
    dialog.querySelector('#cal-rotate').addEventListener('click', async () => {
      try {
        const res = await api('POST', '/calendar/rotate');
        dialog.querySelector('#cal-url').value = res.url;
        url = res.url;
        dialog.querySelector('#cal-copy').disabled = false;
        dialog.querySelector('#cal-copy').removeAttribute('title');
        toast('New link generated — the old one no longer works', 'success');
      } catch { toast('Could not reset', 'error'); }
    });
  }
}
customElements.define('page-my-account', PageMyAccount);
