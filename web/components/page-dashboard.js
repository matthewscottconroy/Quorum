import { api, isSuperadmin, canWrite } from '../app.js';
import { esc, fmtDate, confirmDelete } from '../utils.js';
import { toast } from './toast-notification.js';

class PageDashboard extends HTMLElement {
  async connectedCallback() {
    this.innerHTML = '<div class="spinner"></div>';
    try {
      const data = await api('GET', '/dashboard');
      this.render(data);
      this.wire();
    } catch {
      this.innerHTML = '<p class="empty-state">Failed to load dashboard.</p>';
    }
  }

  // Wire the superadmin-only action-item delete controls (type-to-confirm).
  wire() {
    this.querySelectorAll('.del-ai').forEach(btn => {
      btn.addEventListener('click', () => {
        confirmDelete({
          noun: 'action item',
          name: btn.dataset.title,
          onConfirm: async (typed) => {
            try {
              await api('DELETE', `/action-items/${btn.dataset.id}?confirm=${encodeURIComponent(typed)}`);
              toast('Action item deleted', 'success');
              this.connectedCallback();
            } catch (err) {
              toast(err?.error ?? 'Delete failed', 'error');
              throw err; // keep the confirm dialog open
            }
          },
        });
      });
    });
  }

  render(d) {
    const canDelete = isSuperadmin();
    this.innerHTML = `
      <div class="page-header"><h1>Dashboard</h1></div>

      <div class="stat-row">
        <div class="stat-card card">
          <div class="stat-value ${d.overdue_dues_count > 0 ? 'danger' : ''}">${d.overdue_dues_count}</div>
          <div class="stat-label">Overdue invoices</div>
        </div>
        <div class="stat-card card">
          <div class="stat-value">${d.pending_dues_count}</div>
          <div class="stat-label">Pending invoices</div>
        </div>
        <div class="stat-card card">
          <div class="stat-value">${d.active_member_count}</div>
          <div class="stat-label">Active members</div>
        </div>
        <div class="stat-card card">
          <div class="stat-value">${d.open_action_items.length}</div>
          <div class="stat-label">Open action items</div>
        </div>
        ${canWrite() ? `<div class="stat-card card">
          <div class="stat-value ${d.open_bills_count > 0 ? 'danger' : ''}">${d.open_bills_count ?? 0}</div>
          <div class="stat-label"><a href="#/payables" style="color:inherit">Open bills</a></div>
        </div>` : ''}
      </div>

      <div class="dash-grid">
        <section class="card dash-panel">
          <div class="panel-header"><h2>Upcoming meetings</h2></div>
          ${d.upcoming_meetings.length === 0
            ? '<p class="empty-state" style="padding:1rem">No upcoming meetings</p>'
            : `<table>
                <thead><tr><th>Meeting</th><th>Date</th><th>Status</th></tr></thead>
                <tbody>
                  ${d.upcoming_meetings.map(m => `
                    <tr>
                      <td><a href="#/meetings">${esc(m.title)}</a></td>
                      <td>${fmtDate(m.scheduled_at, { month: 'short', day: 'numeric', year: 'numeric' })}</td>
                      <td><span class="badge badge-${esc(m.status)}">${esc(m.status)}</span></td>
                    </tr>`).join('')}
                </tbody>
               </table>`}
        </section>

        <section class="card dash-panel">
          <div class="panel-header"><h2>Open action items</h2></div>
          ${d.open_action_items.length === 0
            ? '<p class="empty-state" style="padding:1rem">No open items</p>'
            : `<table>
                <thead><tr><th>Item</th><th>Assignee</th><th>Priority</th>${canDelete ? '<th></th>' : ''}</tr></thead>
                <tbody>
                  ${d.open_action_items.map(a => `
                    <tr>
                      <td>${esc(a.title)}</td>
                      <td>${esc(a.assignee_name ?? '—')}</td>
                      <td><span class="badge badge-${a.priority === 'high' ? 'overdue' : a.priority === 'low' ? 'none' : 'open'}">${esc(a.priority)}</span></td>
                      ${canDelete ? `<td class="ai-actions"><button type="button" class="del-ai" data-id="${esc(a.id)}" data-title="${esc(a.title)}" aria-label="Delete action item">Delete</button></td>` : ''}
                    </tr>`).join('')}
                </tbody>
               </table>`}
        </section>
      </div>

      <style>
        .stat-row { display: flex; gap: 1rem; margin-bottom: 1.5rem; flex-wrap: wrap; }
        .stat-card { flex: 1; min-width: 140px; padding: 1.1rem 1.25rem; }
        .stat-value { font-size: 2rem; font-weight: 800; color: var(--color-primary); }
        .stat-value.danger { color: var(--color-danger); }
        .stat-label { font-size: .8rem; color: var(--color-text-muted); text-transform: uppercase; letter-spacing: .04em; margin-top: .25rem; }
        .dash-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; }
        .panel-header { padding: .75rem 1rem; border-bottom: 1px solid var(--color-border); }
        .panel-header h2 { font-size: 1rem; font-weight: 700; }
        .ai-actions { text-align: right; }
        .del-ai { background: none; border: none; color: var(--color-danger); cursor: pointer; font-size: .8rem; padding: .1rem .3rem; }
        .del-ai:hover { text-decoration: underline; }
        @media (max-width: 700px) { .dash-grid { grid-template-columns: 1fr; } }
      </style>
    `;
  }
}
customElements.define('page-dashboard', PageDashboard);
