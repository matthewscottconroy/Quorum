import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';

function esc(s) { return String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }
function fmt$(n) { return Number(n).toLocaleString(undefined, { style: 'currency', currency: 'USD' }); }
function fmtDate(iso) { return iso ? new Date(iso).toLocaleDateString() : '—'; }

const STATUSES = ['','pending','overdue','paid','partial','waived'];

class PageDues extends HTMLElement {
  constructor() {
    super();
    this._invoices = [];
    this._status   = '';
    this._period   = '';
  }

  async connectedCallback() {
    await this.load();
  }

  async load() {
    const params = new URLSearchParams();
    if (this._status) params.set('status', this._status);
    if (this._period) params.set('period', this._period);
    try {
      const _dPage = await api('GET', '/dues?' + params);
      this._invoices = _dPage?.data ?? _dPage ?? [];
    } catch { toast('Failed to load dues', 'error'); }
    this.render();
  }

  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Dues &amp; Billing</h1>
        ${canWrite() ? '<button class="btn-primary" id="add-btn">+ Create invoice</button>' : ''}
      </div>
      <div class="search-bar">
        <select id="status-sel" style="max-width:160px">
          ${STATUSES.map(s => `<option value="${s}" ${this._status===s?'selected':''}>${s||'All statuses'}</option>`).join('')}
        </select>
        <input id="period-inp" placeholder="Period label (e.g. Annual 2026)" value="${esc(this._period)}" style="max-width:260px">
        <button class="btn-secondary" id="refresh-btn">Refresh</button>
      </div>
      <div class="card" style="overflow:hidden">
        <table>
          <thead><tr><th>Member</th><th>Period</th><th>Amount</th><th>Due date</th><th>Status</th>${canWrite()?'<th></th>':''}</tr></thead>
          <tbody>
            ${this._invoices?.length ? this._invoices.map(inv => `
              <tr class="inv-row" data-id="${inv.id}" style="cursor:pointer">
                <td>${esc(inv.member_name)}</td>
                <td>${esc(inv.period_label)}</td>
                <td>${fmt$(inv.amount)} ${esc(inv.currency)}</td>
                <td>${fmtDate(inv.due_date)}</td>
                <td><span class="badge badge-${inv.status}">${inv.status}</span></td>
                ${canWrite() ? `<td>
                  <button class="btn-ghost waive-btn" data-id="${inv.id}" style="font-size:.8rem">Waive</button>
                  <button class="btn-ghost tx-btn" data-id="${inv.id}" style="font-size:.8rem">+ Payment</button>
                </td>` : ''}
              </tr>`).join('')
            : '<tr><td colspan="6"><div class="empty-state"><p>No invoices found.</p></div></td></tr>'}
          </tbody>
        </table>
      </div>
    `;

    this.querySelector('#status-sel')?.addEventListener('change', e => { this._status = e.target.value; this.load(); });
    this.querySelector('#period-inp')?.addEventListener('change', e => { this._period = e.target.value; this.load(); });
    this.querySelector('#refresh-btn')?.addEventListener('click', () => this.load());
    this.querySelector('#add-btn')?.addEventListener('click', () => this.openCreateModal());
    this.querySelectorAll('.inv-row').forEach(row => {
      row.addEventListener('click', e => {
        if (e.target.classList.contains('waive-btn') || e.target.classList.contains('tx-btn')) return;
        this.openDetailModal(row.dataset.id);
      });
    });
    this.querySelectorAll('.waive-btn').forEach(btn => {
      btn.addEventListener('click', async () => {
        try {
          await api('PATCH', `/dues/${btn.dataset.id}`, { status: 'waived' });
          toast('Invoice waived', 'success');
          this.load();
        } catch { toast('Failed', 'error'); }
      });
    });
    this.querySelectorAll('.tx-btn').forEach(btn => {
      btn.addEventListener('click', () => this.openTransactionModal(btn.dataset.id));
    });
  }

  async openDetailModal(id) {
    let inv;
    try { inv = await api('GET', `/dues/${id}`); } catch { toast('Load failed', 'error'); return; }

    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal" style="max-width:680px">
        <div class="modal-header">
          <h2>${esc(inv.member_name)} — ${esc(inv.period_label)}</h2>
          <button class="btn-ghost" id="close-btn">✕</button>
        </div>
        <div class="modal-body">
          <p><strong>Amount:</strong> ${fmt$(inv.amount)} ${esc(inv.currency)} &nbsp; <strong>Status:</strong> <span class="badge badge-${inv.status}">${inv.status}</span></p>
          <p style="margin-top:.4rem"><strong>Due:</strong> ${fmtDate(inv.due_date)}</p>
          ${inv.notes ? `<p style="margin-top:.4rem;color:var(--color-text-muted)">${esc(inv.notes)}</p>` : ''}
          <h3 style="margin-top:1.25rem;margin-bottom:.5rem">Transactions</h3>
          ${!inv.transactions?.length ? '<p style="color:var(--color-text-muted)">No transactions recorded.</p>'
            : `<table>
                <thead><tr><th>Date</th><th>Provider</th><th>Method</th><th>Amount</th><th>Status</th></tr></thead>
                <tbody>
                  ${inv.transactions.map(t => `<tr>
                    <td>${fmtDate(t.occurred_at)}</td>
                    <td>${esc(t.provider)}</td>
                    <td>${esc(t.payment_method_type ?? '—')}</td>
                    <td>${fmt$(t.amount)}</td>
                    <td>${esc(t.provider_status ?? '—')}</td>
                  </tr>`).join('')}
                </tbody>
               </table>`}
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="close-btn2">Close</button>
        </div>
      </div>
    `;
    document.body.appendChild(backdrop);
    backdrop.querySelector('#close-btn').addEventListener('click', () => backdrop.remove());
    backdrop.querySelector('#close-btn2').addEventListener('click', () => backdrop.remove());
  }

  openCreateModal() {
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal">
        <div class="modal-header">
          <h2>Create invoice</h2>
          <button class="btn-ghost" id="close-btn">✕</button>
        </div>
        <div class="modal-body">
          <div class="form-group">
            <label>Member ID(s) — comma-separated UUIDs</label>
            <input id="f-members" placeholder="uuid1, uuid2, …">
          </div>
          <div class="form-row">
            <div class="form-group">
              <label>Amount *</label>
              <input id="f-amount" type="number" step="0.01" min="0.01" placeholder="100.00">
            </div>
            <div class="form-group">
              <label>Currency</label>
              <input id="f-currency" value="USD" style="max-width:80px">
            </div>
          </div>
          <div class="form-row">
            <div class="form-group">
              <label>Period label *</label>
              <input id="f-period" placeholder="Annual 2026">
            </div>
            <div class="form-group">
              <label>Due date *</label>
              <input id="f-due" type="date">
            </div>
          </div>
          <div class="form-group">
            <label>Notes</label>
            <textarea id="f-notes"></textarea>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Create</button>
        </div>
      </div>
    `;
    document.body.appendChild(backdrop);
    const close = () => backdrop.remove();
    backdrop.querySelector('#close-btn').addEventListener('click', close);
    backdrop.querySelector('#cancel-btn').addEventListener('click', close);
    backdrop.querySelector('#save-btn').addEventListener('click', async () => {
      const membersRaw = backdrop.querySelector('#f-members').value;
      const memberIDs  = membersRaw.split(',').map(s => s.trim()).filter(Boolean);
      const body = {
        member_ids:   memberIDs,
        amount:       parseFloat(backdrop.querySelector('#f-amount').value),
        currency:     backdrop.querySelector('#f-currency').value || 'USD',
        period_label: backdrop.querySelector('#f-period').value.trim(),
        due_date:     backdrop.querySelector('#f-due').value,
        notes:        backdrop.querySelector('#f-notes').value.trim() || null,
      };
      if (!body.period_label || !body.due_date || !(body.amount > 0)) {
        toast('Amount, period, and due date are required', 'error'); return;
      }
      if (!memberIDs.length) { toast('At least one member ID required', 'error'); return; }
      try {
        await api('POST', '/dues', body);
        toast('Invoice(s) created', 'success');
        close();
        this.load();
      } catch (err) { toast(err.error ?? 'Create failed', 'error'); }
    });
  }

  openTransactionModal(invoiceID) {
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal" style="max-width:420px">
        <div class="modal-header">
          <h2>Record payment</h2>
          <button class="btn-ghost" id="close-btn">✕</button>
        </div>
        <div class="modal-body">
          <div class="form-row">
            <div class="form-group">
              <label>Amount *</label>
              <input id="f-amount" type="number" step="0.01" min="0.01">
            </div>
            <div class="form-group">
              <label>Provider</label>
              <select id="f-provider">
                <option value="manual">manual</option>
                <option value="stripe">stripe</option>
                <option value="paypal">paypal</option>
                <option value="check">check</option>
                <option value="cash">cash</option>
              </select>
            </div>
          </div>
          <div class="form-group">
            <label>Reference ID</label>
            <input id="f-ref" placeholder="Optional provider reference">
          </div>
          <div class="form-group">
            <label>Notes</label>
            <textarea id="f-notes"></textarea>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Record</button>
        </div>
      </div>
    `;
    document.body.appendChild(backdrop);
    const close = () => backdrop.remove();
    backdrop.querySelector('#close-btn').addEventListener('click', close);
    backdrop.querySelector('#cancel-btn').addEventListener('click', close);
    backdrop.querySelector('#save-btn').addEventListener('click', async () => {
      const body = {
        amount:               parseFloat(backdrop.querySelector('#f-amount').value),
        provider:             backdrop.querySelector('#f-provider').value,
        provider_reference_id: backdrop.querySelector('#f-ref').value.trim() || null,
        notes:                backdrop.querySelector('#f-notes').value.trim() || null,
      };
      if (!(body.amount > 0)) { toast('Amount required', 'error'); return; }
      try {
        await api('POST', `/dues/${invoiceID}/transactions`, body);
        toast('Payment recorded', 'success');
        close();
        this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    });
  }
}
customElements.define('page-dues', PageDues);
