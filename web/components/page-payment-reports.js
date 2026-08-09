import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDateTime, formatMoney, guardButton } from '../utils.js';

/**
 * Officer confirmation queue for member-reported payments. A member says "I
 * paid via Zelle/check" from My Account; officers confirm (records the real
 * transaction against the invoice) or dismiss.
 */
class PagePaymentReports extends HTMLElement {
  connectedCallback() {
    this.innerHTML = `
      <div class="page-header"><h1>Payment confirmations</h1></div>
      <p style="color:var(--color-text-muted);font-size:.85rem;margin-bottom:1rem">
        Members report payments they've sent through manual rails (Zelle, check…).
        Confirming records the payment against the invoice; dismissing clears it
        without posting.</p>
      <div id="pr-list"><span class="spinner"></span></div>`;
    this.load();
  }

  async load() {
    const box = this.querySelector('#pr-list');
    let list;
    try { list = await api('GET', '/payment-reports'); }
    catch { box.innerHTML = '<p class="empty-state">Failed to load.</p>'; return; }
    if (!list?.length) {
      box.innerHTML = '<div class="empty-state"><p>No pending payment reports. 🎉</p></div>';
      return;
    }
    box.innerHTML = list.map(p => `
      <div class="card" style="padding:.85rem 1.1rem;margin-bottom:.6rem">
        <div style="display:flex;justify-content:space-between;gap:1rem;flex-wrap:wrap;align-items:baseline">
          <div>
            <b>${formatMoney(p.amount_minor, p.currency)} ${esc(p.currency)}</b>
            — ${esc(p.member_name || 'a member')}
            <span style="font-size:.8rem;color:var(--color-text-muted)">· ${esc(p.period_label)}</span>
          </div>
          <span style="font-size:.75rem;color:var(--color-text-muted)">reported ${esc(fmtDateTime(p.reported_at))}</span>
        </div>
        <div style="font-size:.85rem;margin-top:.3rem">
          Paid via <strong>${esc(p.method)}</strong>${p.reference ? ` · ref ${esc(p.reference)}` : ''}
          ${p.note ? `<div style="color:var(--color-text-muted);margin-top:.15rem">${esc(p.note)}</div>` : ''}
        </div>
        ${canWrite() ? `<div style="display:flex;gap:.4rem;margin-top:.55rem">
          <button class="btn-primary pr-confirm" data-id="${esc(p.id)}" style="font-size:.8rem">Confirm &amp; record payment</button>
          <button class="btn-ghost pr-dismiss" data-id="${esc(p.id)}" style="font-size:.8rem;color:var(--color-danger)">Dismiss</button>
        </div>` : ''}
      </div>`).join('');

    box.querySelectorAll('.pr-confirm').forEach(btn => btn.addEventListener('click', guardButton(btn, async () => {
      try {
        const res = await api('POST', `/payment-reports/${btn.dataset.id}/confirm`);
        toast(res.posted ? 'Confirmed and recorded' : 'Confirmed (not posted: ' + (res.reason || 'see invoice') + ')', 'success');
        this.load();
      } catch (err) { toast(err.error ?? 'Confirm failed', 'error'); }
    })));
    box.querySelectorAll('.pr-dismiss').forEach(btn => btn.addEventListener('click', async () => {
      if (!confirm('Dismiss this payment report without recording a payment?')) return;
      try { await api('POST', `/payment-reports/${btn.dataset.id}/dismiss`); toast('Dismissed', 'success'); this.load(); }
      catch (err) { toast(err.error ?? 'Dismiss failed', 'error'); }
    }));
  }
}
customElements.define('page-payment-reports', PagePaymentReports);
