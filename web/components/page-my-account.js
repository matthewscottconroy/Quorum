import { api, getUser, currentMemberId } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDate, formatMoney } from '../utils.js';

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

  async render() {
    const user = getUser();
    const memberId = currentMemberId();

    this.innerHTML = `<div class="page-header"><h1>My Account</h1></div>`;

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

      <section class="card" style="overflow:hidden;margin-bottom:1.25rem">
        <div class="panel-header" style="padding:.75rem 1rem;border-bottom:1px solid var(--color-border)"><h2 style="font-size:1rem">My dues</h2></div>
        ${duesFailed
          ? '<p class="empty-state" style="padding:1rem;color:var(--color-danger)">Couldn\'t load your dues — try again.</p>'
          : invoices.length === 0
          ? '<p class="empty-state" style="padding:1rem">No invoices.</p>'
          : `<table>
              <thead><tr><th>Period</th><th>Amount</th><th>Due date</th><th>Status</th></tr></thead>
              <tbody>
                ${invoices.map(inv => `
                  <tr>
                    <td>${esc(inv.period_label)}</td>
                    <td>${formatMoney(inv.amount_minor, inv.currency)}</td>
                    <td>${fmtDate(inv.due_date)}</td>
                    <td><span class="badge badge-${esc(inv.status)}">${esc(inv.status)}</span></td>
                  </tr>`).join('')}
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
  }
}
customElements.define('page-my-account', PageMyAccount);
