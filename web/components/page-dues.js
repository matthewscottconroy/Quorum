import { api, apiDownload, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';
import { esc, fmtDate, openModal, guardButton, formatMoney, parseMoney } from '../utils.js';

const STATUSES = ['','pending','overdue','paid','partial','waived'];

class PageDues extends HTMLElement {
  constructor() {
    super();
    this._invoices = [];
    this._status   = '';
    this._period   = '';
    this._seq      = 0;
  }

  connectedCallback() {
    this.render();
    this.load();
  }

  _cols() { return canWrite() ? 6 : 5; }

  /** Renders the static page shell once; load() only touches #tbody. */
  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Dues &amp; Billing</h1>
        <div style="display:flex;gap:.5rem">
          <button class="btn-secondary" id="export-dues-btn">Export dues</button>
          <button class="btn-secondary" id="export-tx-btn">Export payments</button>
          ${canWrite() ? '<button class="btn-secondary" id="schedules-btn">Recurring dues</button>' : ''}
          ${canWrite() ? '<button class="btn-primary" id="add-btn">+ Create invoice</button>' : ''}
        </div>
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
          <tbody id="tbody"></tbody>
        </table>
      </div>
    `;

    this.querySelector('#status-sel')?.addEventListener('change', e => { this._status = e.target.value; this.load(); });
    this.querySelector('#period-inp')?.addEventListener('change', e => { this._period = e.target.value; this.load(); });
    this.querySelector('#refresh-btn')?.addEventListener('click', () => this.load());
    this.querySelector('#add-btn')?.addEventListener('click', () => this.openCreateModal());
    this.querySelector('#export-dues-btn')?.addEventListener('click', async () => {
      try { await apiDownload('/export/dues.csv', 'dues.csv'); }
      catch (err) { toast(err.error ?? 'Export failed','error'); }
    });
    this.querySelector('#export-tx-btn')?.addEventListener('click', async () => {
      try { await apiDownload('/export/transactions.csv', 'transactions.csv'); }
      catch (err) { toast(err.error ?? 'Export failed','error'); }
    });
    this.querySelector('#schedules-btn')?.addEventListener('click', () => this.openSchedulesModal());
  }

  /** Manage recurring dues schedules: list, add, generate-now, delete. */
  async openSchedulesModal() {
    const CADENCE = { annual: 'Annually', quarterly: 'Quarterly', monthly: 'Monthly' };
    const { dialog, close } = openModal({
      title: 'Recurring dues schedules',
      maxWidth: '640px',
      body: `
        <div class="modal-body">
          <p style="font-size:.85rem;color:var(--color-text-muted);margin-bottom:1rem">
            Each schedule auto-generates a dues invoice for every active member of a tier, once per period.
            The nightly job fills in each new period; “Generate now” materializes the current period immediately.
          </p>
          <div id="sched-list"><div style="text-align:center;padding:1rem"><span class="spinner"></span></div></div>
          <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.75rem;margin-top:.75rem">
            <div style="font-weight:600;font-size:.85rem;margin-bottom:.5rem">Add a schedule</div>
            <div style="display:flex;gap:.4rem;flex-wrap:wrap;align-items:end">
              <div class="form-group" style="flex:1;min-width:110px;margin:0"><label for="s-tier">Tier</label><input id="s-tier" placeholder="standard"></div>
              <div class="form-group" style="max-width:110px;margin:0"><label for="s-amount">Amount</label><input id="s-amount" placeholder="50.00"></div>
              <div class="form-group" style="max-width:90px;margin:0"><label for="s-currency">Currency</label><input id="s-currency" value="USD"></div>
              <div class="form-group" style="max-width:120px;margin:0"><label for="s-cadence">Cadence</label>
                <select id="s-cadence"><option value="annual">Annually</option><option value="quarterly">Quarterly</option><option value="monthly">Monthly</option></select>
              </div>
              <div class="form-group" style="max-width:90px;margin:0"><label for="s-duedays">Due (days)</label><input id="s-duedays" type="number" value="30" min="0"></div>
              <button class="btn-primary" id="s-add" style="height:38px">Add</button>
            </div>
          </div>
        </div>
        <div class="modal-footer"><button class="btn-secondary" id="close-btn">Close</button></div>
      `,
    });
    dialog.querySelector('#close-btn').addEventListener('click', close);
    // Refresh the invoice list on close in case generation created invoices.
    dialog.addEventListener('close', () => { if (this.isConnected) this.load(); });

    const listEl = dialog.querySelector('#sched-list');
    const reload = async () => {
      let schedules = [];
      try { schedules = await api('GET', '/dues/schedules'); } catch { listEl.innerHTML = '<p class="empty-state">Failed to load.</p>'; return; }
      listEl.innerHTML = (schedules ?? []).length ? schedules.map(s => `
        <div class="sched-row" data-id="${esc(s.id)}" style="display:flex;align-items:center;gap:.6rem;border:1px solid var(--color-border);border-radius:var(--radius);padding:.55rem .75rem;margin-bottom:.5rem">
          <div style="flex:1">
            <div style="font-weight:600;font-size:.9rem">${esc(s.tier)} — ${formatMoney(s.amount_minor, s.currency)}</div>
            <div style="font-size:.78rem;color:var(--color-text-muted)">${CADENCE[s.cadence]||s.cadence} · due ${s.due_days}d after period start ${s.active?'':'· <em>inactive</em>'}</div>
          </div>
          <button class="btn-secondary sched-gen" style="font-size:.78rem;padding:.25rem .6rem">Generate now</button>
          <button class="btn-ghost sched-del" style="font-size:.78rem;color:var(--color-danger)">Delete</button>
        </div>`).join('') : '<p style="font-size:.85rem;color:var(--color-text-muted)">No schedules yet.</p>';
    };

    listEl.addEventListener('click', async e => {
      const row = e.target.closest('.sched-row'); if (!row) return;
      const id = row.dataset.id;
      try {
        if (e.target.classList.contains('sched-gen')) {
          const res = await api('POST', `/dues/schedules/${id}/generate`);
          toast(`Generated ${res.created} invoice${res.created===1?'':'s'} for ${res.period_label}`, 'success');
        } else if (e.target.classList.contains('sched-del')) {
          await api('DELETE', `/dues/schedules/${id}`);
          toast('Schedule deleted','success');
          await reload();
        }
      } catch (err) { toast(err.error ?? 'Action failed','error'); }
    });

    dialog.querySelector('#s-add').addEventListener('click', async () => {
      const tier = dialog.querySelector('#s-tier').value.trim();
      const currency = (dialog.querySelector('#s-currency').value.trim() || 'USD').toUpperCase();
      if (!/^[A-Z]{3}$/.test(currency)) { toast('Currency must be a 3-letter code (e.g. USD)','error'); return; }
      const amountStr = dialog.querySelector('#s-amount').value.trim();
      if (!tier || !amountStr) { toast('Tier and amount are required','error'); return; }
      // parseMoney returns null (never throws) on an invalid amount.
      const amount_minor = parseMoney(amountStr, currency);
      if (amount_minor === null || amount_minor <= 0) { toast('Enter a valid amount','error'); return; }
      try {
        await api('POST', '/dues/schedules', {
          tier, amount_minor, currency,
          cadence: dialog.querySelector('#s-cadence').value,
          due_days: Number.parseInt(dialog.querySelector('#s-duedays').value, 10) || 0,
        });
        toast('Schedule added','success');
        dialog.querySelector('#s-tier').value = '';
        dialog.querySelector('#s-amount').value = '';
        await reload();
      } catch (err) { toast(err.error ?? 'Failed','error'); }
    });

    await reload();
  }

  async load() {
    const seq = ++this._seq;
    const tbody = this.querySelector('#tbody');
    tbody.innerHTML = `<tr><td colspan="${this._cols()}" style="text-align:center"><span class="spinner"></span></td></tr>`;
    const params = new URLSearchParams();
    if (this._status) params.set('status', this._status);
    if (this._period) params.set('period', this._period);
    try {
      const _dPage = await api('GET', '/dues?' + params);
      if (seq !== this._seq) return; // A newer load() superseded this one.
      this._invoices = _dPage?.data ?? _dPage ?? [];
      tbody.innerHTML = this._rows()
        || `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>No invoices found.</p></div></td></tr>`;
      this._wireRows(tbody);
    } catch {
      if (seq !== this._seq) return;
      tbody.innerHTML = `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>Failed to load dues.</p></div></td></tr>`;
      toast('Failed to load dues', 'error');
    }
  }

  _rows() {
    if (!this._invoices?.length) return '';
    return this._invoices.map(inv => `
      <tr class="inv-row" data-id="${esc(inv.id)}" style="cursor:pointer" tabindex="0">
        <td>${esc(inv.member_name)}</td>
        <td>${esc(inv.period_label)}</td>
        <td>${formatMoney(inv.amount_minor, inv.currency)}</td>
        <td>${fmtDate(inv.due_date)}</td>
        <td><span class="badge badge-${esc(inv.status)}">${esc(inv.status)}</span></td>
        ${canWrite() ? `<td>
          <button class="btn-ghost waive-btn" data-id="${esc(inv.id)}" style="font-size:.8rem">Waive</button>
          <button class="btn-ghost tx-btn" data-id="${esc(inv.id)}" style="font-size:.8rem">+ Payment</button>
        </td>` : ''}
      </tr>`).join('');
  }

  _wireRows(tbody) {
    tbody.querySelectorAll('.inv-row').forEach(row => {
      const open = e => {
        if (e.target.classList.contains('waive-btn') || e.target.classList.contains('tx-btn')) return;
        this.openDetailModal(row.dataset.id);
      };
      row.addEventListener('click', open);
      row.addEventListener('keydown', e => {
        // Let nested action buttons handle their own Enter/Space activation;
        // only the row itself (when directly focused) opens on keyboard.
        if (e.target !== row && e.target.closest('button, a, input, select, textarea')) return;
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(e); }
      });
    });
    tbody.querySelectorAll('.waive-btn').forEach(btn => {
      btn.addEventListener('click', async () => {
        if (!await confirm('Waive this invoice?', 'Waive invoice')) return;
        try {
          await api('PATCH', `/dues/${btn.dataset.id}`, { status: 'waived' });
          toast('Invoice waived', 'success');
          this.load();
        } catch { toast('Failed', 'error'); }
      });
    });
    tbody.querySelectorAll('.tx-btn').forEach(btn => {
      btn.addEventListener('click', () => this.openTransactionModal(btn.dataset.id));
    });
  }

  async openDetailModal(id) {
    let inv;
    try { inv = await api('GET', `/dues/${id}`); } catch { toast('Load failed', 'error'); return; }

    const { dialog, close } = openModal({
      title: `${inv.member_name} — ${inv.period_label}`,
      maxWidth: '680px',
      body: `
        <div class="modal-body">
          <p><strong>Amount:</strong> ${formatMoney(inv.amount_minor, inv.currency)} &nbsp; <strong>Status:</strong> <span class="badge badge-${esc(inv.status)}">${esc(inv.status)}</span></p>
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
                    <td>${formatMoney(t.amount_minor, inv.currency)}</td>
                    <td>${esc(t.provider_status ?? '—')}</td>
                  </tr>`).join('')}
                </tbody>
               </table>`}
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="close-btn2">Close</button>
        </div>
      `,
    });
    dialog.querySelector('#close-btn2').addEventListener('click', close);
  }

  openCreateModal() {
    const { dialog, close } = openModal({
      title: 'Create invoice',
      body: `
        <div class="modal-body">
          <div class="form-group">
            <label for="f-ctype">Bill to</label>
            <select id="f-ctype">
              <option value="member">Member(s) — dues receivable</option>
              <option value="contact">Outside customer (contact) — service receivable</option>
            </select>
          </div>
          <div class="form-group" id="member-row">
            <label for="f-msearch">Members to bill</label>
            <input id="f-msearch" placeholder="Type to filter…" autocomplete="off">
            <div id="f-mlist" style="max-height:180px;overflow-y:auto;border:1px solid var(--color-border);border-radius:var(--radius);padding:.35rem .6rem;margin-top:.35rem">
              <span class="spinner"></span>
            </div>
            <div style="display:flex;gap:.5rem;align-items:center;margin-top:.35rem">
              <button type="button" class="btn-secondary" id="f-mall" style="font-size:.75rem">All shown</button>
              <button type="button" class="btn-secondary" id="f-mnone" style="font-size:.75rem">None</button>
              <span id="f-mcount" style="font-size:.78rem;color:var(--color-text-muted)">0 selected</span>
            </div>
          </div>
          <div class="form-group" id="contact-row" style="display:none">
            <label for="f-contact">Customer</label>
            <select id="f-contact"><option value="">Loading contacts…</option></select>
          </div>
          <div class="form-row">
            <div class="form-group">
              <label for="f-amount">Amount (e.g. 100.00) *</label>
              <input id="f-amount" type="text" inputmode="decimal" pattern="[0-9]*[.]?[0-9]*" placeholder="100.00">
            </div>
            <div class="form-group">
              <label for="f-currency">Currency</label>
              <input id="f-currency" value="USD" maxlength="3" pattern="[A-Za-z]{3}" style="max-width:80px;text-transform:uppercase">
            </div>
          </div>
          <div class="form-row">
            <div class="form-group">
              <label for="f-period">Period label *</label>
              <input id="f-period" placeholder="Annual 2026">
            </div>
            <div class="form-group">
              <label for="f-due">Due date *</label>
              <input id="f-due" type="date">
            </div>
          </div>
          <div class="form-group">
            <label for="f-notes">Notes</label>
            <textarea id="f-notes"></textarea>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Create</button>
        </div>
      `,
    });

    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const ctype = dialog.querySelector('#f-ctype');
    ctype.addEventListener('change', () => {
      const contact = ctype.value === 'contact';
      dialog.querySelector('#member-row').style.display = contact ? 'none' : '';
      dialog.querySelector('#contact-row').style.display = contact ? '' : 'none';
    });
    api('GET', '/contacts?limit=200').then(pg => {
      dialog.querySelector('#f-contact').innerHTML = '<option value="">— choose —</option>' +
        (pg?.data ?? pg ?? []).map(c => `<option value="${esc(c.id)}">${esc(c.name)}${c.organization ? ' — ' + esc(c.organization) : ''}</option>`).join('');
    }).catch(() => {});

    // Member picker: selection survives filtering because it lives in a Set,
    // not in the checkboxes themselves.
    let pickerMembers = [];
    const picked = new Set();
    const mlist = dialog.querySelector('#f-mlist');
    const recount = () => {
      dialog.querySelector('#f-mcount').textContent = `${picked.size} selected`;
    };
    const renderPicker = () => {
      const q = dialog.querySelector('#f-msearch').value.trim().toLowerCase();
      const shown = pickerMembers.filter(m => !q || m.display_name.toLowerCase().includes(q) || (m.tier ?? '').toLowerCase().includes(q));
      mlist.innerHTML = shown.map(m => `
        <label style="display:flex;gap:.45rem;align-items:center;font-size:.85rem;padding:.12rem 0;cursor:pointer;text-transform:none;letter-spacing:normal;font-weight:400;color:var(--color-text);margin-bottom:0">
          <input type="checkbox" class="inv-mcb" style="width:auto" value="${esc(m.id)}" ${picked.has(m.id) ? 'checked' : ''}>
          <span style="flex:1">${esc(m.display_name)}</span>
          <span style="font-size:.72rem;color:var(--color-text-muted)">${esc(m.tier ?? '')}</span>
        </label>`).join('') || '<p style="color:var(--color-text-muted);font-size:.85rem;margin:.3rem 0">No matching members.</p>';
      mlist.querySelectorAll('.inv-mcb').forEach(cb => cb.addEventListener('change', () => {
        if (cb.checked) picked.add(cb.value); else picked.delete(cb.value);
        recount();
      }));
    };
    api('GET', '/members?limit=200&status=active').then(pg => {
      pickerMembers = pg?.data ?? pg ?? [];
      renderPicker(); recount();
    }).catch(() => { mlist.innerHTML = '<p style="color:var(--color-danger);font-size:.85rem">Failed to load members.</p>'; });
    dialog.querySelector('#f-msearch').addEventListener('input', renderPicker);
    dialog.querySelector('#f-mall').addEventListener('click', () => {
      mlist.querySelectorAll('.inv-mcb').forEach(cb => picked.add(cb.value));
      renderPicker(); recount();
    });
    dialog.querySelector('#f-mnone').addEventListener('click', () => {
      picked.clear(); renderPicker(); recount();
    });
    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const isContact = ctype.value === 'contact';
      const memberIDs  = [...picked];
      const currency   = (dialog.querySelector('#f-currency').value || 'USD').trim().toUpperCase();
      if (!/^[A-Z]{3}$/.test(currency)) { toast('Currency must be a 3-letter code', 'error'); return; }
      const amountMinor = parseMoney(dialog.querySelector('#f-amount').value, currency);
      if (amountMinor === null || amountMinor <= 0) {
        toast('Enter a valid amount, e.g. 100.00', 'error'); return;
      }
      const body = {
        member_ids:   memberIDs,
        amount_minor: amountMinor,
        currency,
        period_label: dialog.querySelector('#f-period').value.trim(),
        due_date:     dialog.querySelector('#f-due').value,
        notes:        dialog.querySelector('#f-notes').value.trim() || null,
      };
      if (!body.period_label || !body.due_date) {
        toast('Amount, period, and due date are required', 'error'); return;
      }
      if (isContact) {
        const contact_id = dialog.querySelector('#f-contact').value;
        if (!contact_id) { toast('Choose a customer', 'error'); return; }
        delete body.member_ids;
        body.contact_id = contact_id;
      } else if (!memberIDs.length) { toast('Select at least one member', 'error'); return; }
      try {
        await api('POST', '/dues', body);
        toast('Invoice(s) created', 'success');
        close();
        this.load();
      } catch (err) { toast(err.error ?? 'Create failed', 'error'); }
    }));
  }

  openTransactionModal(invoiceID) {
    // The transaction inherits the invoice's currency; look it up from the
    // loaded list so we can convert the typed decimal to minor units.
    const currency = (this._invoices.find(i => String(i.id) === String(invoiceID))?.currency || 'USD')
      .trim().toUpperCase();
    const { dialog, close } = openModal({
      title: 'Record payment',
      maxWidth: '420px',
      body: `
        <div class="modal-body">
          <div class="form-row">
            <div class="form-group">
              <label for="f-amount">Amount (e.g. 100.00) *</label>
              <input id="f-amount" type="text" inputmode="decimal" pattern="[0-9]*[.]?[0-9]*" placeholder="100.00">
            </div>
            <div class="form-group">
              <label for="f-provider">Provider</label>
              <select id="f-provider">
                <option value="manual">manual</option>
                <option value="cash">cash</option>
                <option value="check">check</option>
                <option value="wire">wire / bank transfer</option>
                <option value="zelle">zelle</option>
                <option value="venmo">venmo</option>
                <option value="stripe">stripe (manual entry)</option>
                <option value="paypal">paypal (manual entry)</option>
                <option value="other">other</option>
              </select>
            </div>
          </div>
          <div class="form-group">
            <label for="f-ref">Reference ID</label>
            <input id="f-ref" placeholder="e.g. Zelle confirmation #, Venmo transaction ID, check number">
          </div>
          <div class="form-group">
            <label for="f-notes">Notes</label>
            <textarea id="f-notes"></textarea>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Record</button>
        </div>
      `,
    });

    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const amountMinor = parseMoney(dialog.querySelector('#f-amount').value, currency);
      if (amountMinor === null || amountMinor <= 0) {
        toast('Enter a valid amount, e.g. 100.00', 'error'); return;
      }
      const body = {
        amount_minor:         amountMinor,
        provider:             dialog.querySelector('#f-provider').value,
        provider_reference_id: dialog.querySelector('#f-ref').value.trim() || null,
        notes:                dialog.querySelector('#f-notes').value.trim() || null,
      };
      try {
        await api('POST', `/dues/${invoiceID}/transactions`, body);
        toast('Payment recorded', 'success');
        close();
        this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }
}
customElements.define('page-dues', PageDues);
