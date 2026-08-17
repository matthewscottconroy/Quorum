import { api, apiDownload, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';
import { esc, fmtDate, openModal, guardButton, formatMoney, parseMoney, moneyExponent, renderPager, providerOptions, knownCurrencies, loadFilters, saveFilters } from '../utils.js';

const STATUSES = ['','pending','overdue','paid','partial','waived'];

class PageDues extends HTMLElement {
  constructor() {
    super();
    const f = loadFilters('dues', { status: '', period: '' });
    this._invoices = [];
    this._status   = f.status;
    this._period   = f.period;
    this._seq      = 0;
    this._offset   = 0;
    this._total    = 0;
    this._selected = new Set();
  }

  _persist() { saveFilters('dues', { status: this._status, period: this._period }); }

  connectedCallback() {
    // Deep links (the dashboard's stat cards) override the saved filter:
    // #/dues?status=overdue must land showing overdue, not last visit's view.
    const qs = new URLSearchParams((location.hash.split('?')[1] ?? ''));
    const st = qs.get('status');
    if (st !== null && STATUSES.includes(st)) {
      this._status = st;
      this._persist();
    }
    this.render();
    this.load();
  }

  _cols() { return canWrite() ? 7 : 5; }

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
      <money-nav current="dues"></money-nav>
      <div class="search-bar">
        <select id="status-sel" style="max-width:160px" aria-label="Filter by status">
          ${STATUSES.map(s => `<option value="${s}" ${this._status===s?'selected':''}>${s||'All statuses'}</option>`).join('')}
        </select>
        <input id="period-inp" placeholder="Period label (e.g. Annual 2026)" value="${esc(this._period)}" style="max-width:260px">
        <button class="btn-secondary" id="refresh-btn">Refresh</button>
      </div>
      <div id="summary-strip" style="font-size:.85rem;color:var(--color-text-muted);padding:.15rem .2rem .5rem"></div>
      <div id="bulk-bar"></div>
      <div class="card table-scroll">
        <table>
          <thead><tr>${canWrite()?'<th style="width:1.5rem"><input type="checkbox" id="sel-all" aria-label="Select all" style="width:auto"></th>':''}<th>Member</th><th>Period</th><th class="num">Amount</th><th>Due date</th><th>Status</th>${canWrite()?'<th></th>':''}</tr></thead>
          <tbody id="tbody"></tbody>
        </table>
      </div>
      <div id="pager"></div>
    `;

    // A new filter or page is a NEW working set: carrying hidden checkmarks
    // across it turns "Waive selected" into a shotgun.
    this.querySelector('#status-sel')?.addEventListener('change', e => { this._status = e.target.value; this._offset = 0; this._selected.clear(); this._persist(); this.load(); });
    this.querySelector('#period-inp')?.addEventListener('change', e => { this._period = e.target.value; this._offset = 0; this._selected.clear(); this._persist(); this.load(); });
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
          ${s.active ? '<button class="btn-secondary sched-gen" style="font-size:.78rem;padding:.25rem .6rem">Generate now</button>' : ''}
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
    const params = new URLSearchParams({ limit: '50', offset: String(this._offset) });
    if (this._status) params.set('status', this._status);
    if (this._period) params.set('period', this._period);
    try {
      const _dPage = await api('GET', '/dues?' + params);
      if (seq !== this._seq) return; // A newer load() superseded this one.
      this._invoices = _dPage?.data ?? _dPage ?? [];
      this._total = _dPage?.total ?? this._invoices.length;
      tbody.innerHTML = this._rows()
        || `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>No invoices found.</p></div></td></tr>`;
      this._wireRows(tbody);
      renderPager(this.querySelector('#pager'), { offset: this._offset, limit: 50, total: this._total, onNavigate: o => { this._offset = o; this._selected.clear(); this.load(); } });
      const strip = this.querySelector('#summary-strip');
      if (strip) {
        const parts = (_dPage?.summary ?? [])
          .map(s => `${s.count} invoice${s.count === 1 ? '' : 's'} · <strong style="color:var(--color-text)">${formatMoney(s.outstanding_minor, s.currency)} ${esc(s.currency)}</strong> outstanding`);
        strip.innerHTML = parts.length ? parts.join(' &nbsp;·&nbsp; ') : '';
      }
    } catch {
      if (seq !== this._seq) return;
      tbody.innerHTML = `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>Failed to load dues.</p></div></td></tr>`;
      const strip = this.querySelector('#summary-strip');
      if (strip) strip.innerHTML = ''; // never answer "how much?" from the PREVIOUS filter over an error
      toast('Failed to load dues', 'error');
    }
  }

  _rows() {
    if (!this._invoices?.length) return '';
    return this._invoices.map(inv => `
      <tr class="inv-row" data-id="${esc(inv.id)}" style="cursor:pointer" tabindex="0" role="button" aria-label="Open invoice ${esc(inv.member_name)} ${esc(inv.period_label)}">
        ${canWrite() ? `<td><input type="checkbox" class="sel-cb" value="${esc(inv.id)}" ${this._selected.has(inv.id) ? 'checked' : ''} style="width:auto" aria-label="Select invoice"></td>` : ''}
        <td>${esc(inv.member_name)}</td>
        <td>${esc(inv.period_label)}</td>
        <td class="num">${formatMoney(inv.amount_minor, inv.currency)}</td>
        <td>${fmtDate(inv.due_date)}</td>
        <td><span class="badge badge-${esc(inv.status)}">${esc(inv.status)}</span></td>
        ${canWrite() ? `<td>
          <button class="btn-ghost waive-btn" data-id="${esc(inv.id)}" style="font-size:.8rem">Waive</button>
          <button class="btn-ghost tx-btn" data-id="${esc(inv.id)}" style="font-size:.8rem">+ Payment</button>
        </td>` : ''}
      </tr>`).join('');
  }

  _wireRows(tbody) {
    tbody.querySelectorAll('.sel-cb').forEach(cb => cb.addEventListener('change', () => {
      cb.checked ? this._selected.add(cb.value) : this._selected.delete(cb.value);
      this._renderBulkBar();
    }));
    const selAll = this.querySelector('#sel-all');
    if (selAll && !selAll._wired) {
      // The shell renders once but _wireRows runs every load: guard the
      // listener or they stack (harmlessly, but N handlers per click).
      selAll._wired = true;
      selAll.addEventListener('change', () => {
        this._invoices.forEach(i => selAll.checked ? this._selected.add(i.id) : this._selected.delete(i.id));
        tbody.querySelectorAll('.sel-cb').forEach(cb => { cb.checked = selAll.checked; });
        this._renderBulkBar();
      });
    }
    // Filter/page changes clear the selection; the header box must follow,
    // or it sits checked over nothing and eats a dead first click.
    if (selAll) selAll.checked = this._invoices.length > 0 && this._invoices.every(i => this._selected.has(i.id));
    this._renderBulkBar();
    tbody.querySelectorAll('.inv-row').forEach(row => {
      const open = e => {
        if (e.target.classList.contains('waive-btn') || e.target.classList.contains('tx-btn') || e.target.classList.contains('sel-cb')) return;
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
                <thead><tr><th>Date</th><th>Provider</th><th>Method</th><th class="num">Amount</th><th>Status</th></tr></thead>
                <tbody>
                  ${inv.transactions.map(t => `<tr>
                    <td>${fmtDate(t.occurred_at)}</td>
                    <td>${esc(t.provider)}</td>
                    <td>${esc(t.payment_method_type ?? '—')}</td>
                    <td class="num">${formatMoney(t.amount_minor, inv.currency)}</td>
                    <td>${esc(t.provider_status ?? '—')}</td>
                  </tr>`).join('')}
                </tbody>
               </table>`}
          ${canWrite() ? `
          <h3 style="margin-top:1.25rem;margin-bottom:.5rem">Payment plan</h3>
          <div id="inst-box"><span class="spinner"></span></div>` : ''}
        </div>
        <div class="modal-footer">
          ${canWrite() && inv.status !== 'waived' ? `<button class="btn-secondary" id="refund-btn">Record refund</button>` : ''}
          ${canWrite() && inv.status !== 'waived' && inv.status !== 'paid' ? `<button class="btn-secondary" id="waive-btn2">Waive</button>` : ''}
          <button class="btn-secondary" id="close-btn2">Close</button>
          ${canWrite() && inv.status !== 'waived' ? `<button class="btn-primary" id="pay-btn2">Record payment</button>` : ''}
        </div>
      `,
    });
    dialog.querySelector('#close-btn2').addEventListener('click', close);
    dialog.querySelector('#refund-btn')?.addEventListener('click', () => {
      close();
      this.openRefundModal(inv);
    });
    // The treasurer's daily loop is review-then-record: the primary action
    // belongs where the review happens, not back in the list's row buttons.
    dialog.querySelector('#pay-btn2')?.addEventListener('click', () => {
      close();
      this.openTransactionModal(inv.id);
    });
    dialog.querySelector('#waive-btn2')?.addEventListener('click', async () => {
      if (!await confirm('Waive this invoice? The remaining balance is written off in the ledger.', 'Waive invoice')) return;
      try {
        await api('PATCH', `/dues/${inv.id}`, { status: 'waived' });
        toast('Invoice waived', 'success');
        close();
        this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    });
    if (canWrite()) this._loadInstallments(dialog, inv);
  }

  /** Renders the installment plan inside the detail modal, with an inline editor. */
  async _loadInstallments(dialog, inv) {
    const box = dialog.querySelector('#inst-box');
    if (!box) return;
    let plan;
    try { plan = await api('GET', `/dues/${inv.id}/installments`) ?? []; }
    catch { box.innerHTML = '<p style="color:var(--color-text-muted)">Failed to load.</p>'; return; }
    const rows = plan.length
      ? `<table><thead><tr><th>#</th><th>Due</th><th>Amount</th></tr></thead><tbody>
          ${plan.map((p, i) => `<tr><td>${i + 1}</td><td>${fmtDate(p.due_date)}</td><td>${formatMoney(p.amount_minor, inv.currency)}</td></tr>`).join('')}
         </tbody></table>`
      : '<p style="color:var(--color-text-muted)">No payment plan. The full amount is due on the due date.</p>';
    box.innerHTML = `${rows}<button class="btn-ghost" id="edit-inst" style="font-size:.8rem;margin-top:.4rem">${plan.length ? 'Edit plan' : 'Split into installments'}</button>`;
    box.querySelector('#edit-inst').addEventListener('click', () => this.openInstallmentModal(inv, plan));
  }

  openRefundModal(inv) {
    const { dialog, close } = openModal({
      title: `Refund — ${inv.member_name}`,
      maxWidth: '420px',
      body: `
        <div class="modal-body">
          <p style="color:var(--color-text-muted);font-size:.85rem">Records a reversing entry in ${esc(inv.currency)}. Posts to the ledger and recomputes the invoice status.</p>
          <div class="form-group"><label for="rf-amt">Amount (${esc(inv.currency)}) *</label>
            <input id="rf-amt" inputmode="decimal" placeholder="0.00"></div>
          <div class="form-group"><label for="rf-prov">Provider / method *</label>
            <select id="rf-prov">${providerOptions('manual')}</select></div>
          <div class="form-group"><label for="rf-note">Note</label>
            <input id="rf-note" placeholder="Reason for refund"></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="rf-cancel">Cancel</button>
          <button class="btn-primary" id="rf-save">Record refund</button>
        </div>`,
    });
    dialog.querySelector('#rf-cancel').addEventListener('click', close);
    const save = dialog.querySelector('#rf-save');
    save.addEventListener('click', guardButton(save, async () => {
      const minor = parseMoney(dialog.querySelector('#rf-amt').value, inv.currency);
      if (!minor || minor <= 0) { toast('Enter a positive amount', 'error'); return; }
      try {
        await api('POST', `/dues/${inv.id}/refund`, {
          amount_minor: minor,
          currency: inv.currency,
          provider: dialog.querySelector('#rf-prov').value,
          note: dialog.querySelector('#rf-note').value.trim() || null,
        });
        toast('Refund recorded', 'success'); close(); this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  openInstallmentModal(inv, plan) {
    const initial = plan.length ? plan : [{ due_date: '', amount_minor: 0 }];
    const rowHTML = (p = {}) => `
      <div class="inst-row" style="display:flex;gap:.5rem;margin-bottom:.4rem">
        <input type="date" class="inst-due" value="${p.due_date ? esc(String(p.due_date).slice(0, 10)) : ''}" style="flex:1">
        <input class="inst-amt" inputmode="decimal" placeholder="Amount" value="${p.amount_minor ? (p.amount_minor / 10 ** moneyExponent(inv.currency)).toFixed(moneyExponent(inv.currency)) : ''}" style="flex:1">
        <button class="btn-ghost inst-del" style="font-size:.8rem;color:var(--color-danger)">✕</button>
      </div>`;
    const { dialog, close } = openModal({
      title: 'Payment plan',
      maxWidth: '460px',
      body: `
        <div class="modal-body">
          <p style="color:var(--color-text-muted);font-size:.85rem">Split this ${formatMoney(inv.amount_minor, inv.currency)} invoice into scheduled installments (tracking only — payments still post through transactions). Saving an empty list clears the plan.</p>
          <div id="inst-rows">${initial.map(rowHTML).join('')}</div>
          <button class="btn-ghost" id="inst-add" style="font-size:.82rem">+ Add installment</button>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="inst-cancel">Cancel</button>
          <button class="btn-primary" id="inst-save">Save plan</button>
        </div>`,
    });
    const rowsBox = dialog.querySelector('#inst-rows');
    const wire = () => rowsBox.querySelectorAll('.inst-del').forEach(b =>
      b.onclick = () => { b.closest('.inst-row').remove(); });
    wire();
    dialog.querySelector('#inst-add').addEventListener('click', () => {
      rowsBox.insertAdjacentHTML('beforeend', rowHTML());
      wire();
    });
    dialog.querySelector('#inst-cancel').addEventListener('click', close);
    const save = dialog.querySelector('#inst-save');
    save.addEventListener('click', guardButton(save, async () => {
      const installments = [];
      for (const row of rowsBox.querySelectorAll('.inst-row')) {
        const due = row.querySelector('.inst-due').value;
        const amt = parseMoney(row.querySelector('.inst-amt').value, inv.currency);
        if (!due && !amt) continue;
        if (!due || !amt || amt <= 0) { toast('Each installment needs a date and a positive amount', 'error'); return; }
        installments.push({ due_date: due, amount_minor: amt });
      }
      try {
        const res = await api('PUT', `/dues/${inv.id}/installments`, { installments });
        // ONE message, not a success and an error stacked on top of each
        // other: either the plan sums to the invoice, or the single toast
        // says exactly how far off it is.
        if (res?.delta_minor) {
          const d = res.delta_minor;
          toast(`Plan saved, but it ${d > 0 ? 'EXCEEDS' : 'falls short of'} the invoice by ${formatMoney(Math.abs(d), inv.currency)}`, 'error');
        } else {
          toast('Payment plan saved', 'success');
        }
        close();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  /** Bulk bar for invoices: waive or re-open (mark pending) the selection. */
  _renderBulkBar() {
    const bar = this.querySelector('#bulk-bar');
    if (!bar) return;
    const n = this._selected.size;
    if (!n) { bar.innerHTML = ''; return; }
    bar.innerHTML = `
      <div class="card" style="display:flex;gap:.6rem;align-items:center;flex-wrap:wrap;padding:.6rem .9rem;margin-bottom:.6rem">
        <strong style="font-size:.9rem">${n} selected</strong>
        <button class="btn-secondary" id="bulk-waive" style="font-size:.82rem">Waive</button>
        <button class="btn-secondary" id="bulk-reopen" style="font-size:.82rem">Re-open (pending)</button>
        <button class="btn-ghost" id="bulk-clear" style="margin-left:auto">Clear</button>
      </div>`;
    const apply = async status => {
      const verb = status === 'waived' ? 'Waive' : 'Re-open';
      if (!await confirm(`${verb} ${n} invoice${n === 1 ? '' : 's'}? ${status === 'waived' ? 'Waiving writes off the remaining balance in the ledger.' : 'Recorded payments still decide paid/partial.'}`, `${verb} ${n} invoice${n === 1 ? '' : 's'}`)) return;
      try {
        const res = await api('POST', '/dues/batch', { ids: [...this._selected], status });
        toast(`Updated ${res.updated} invoice${res.updated === 1 ? '' : 's'}`, 'success');
        this._selected.clear();
        this.load();
      } catch (err) { toast(err.error ?? 'Bulk update failed', 'error'); }
    };
    bar.querySelector('#bulk-waive').addEventListener('click', () => apply('waived'));
    bar.querySelector('#bulk-reopen').addEventListener('click', () => apply('pending'));
    bar.querySelector('#bulk-clear').addEventListener('click', () => { this._selected.clear(); this.load(); });
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
            <input id="f-msearch" placeholder="Type to search all members…" autocomplete="off">
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
              <input id="f-currency" value="USD" maxlength="3" pattern="[A-Za-z]{3}" list="dues-cur-list" style="max-width:90px;text-transform:uppercase">
              <datalist id="dues-cur-list"></datalist>
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
    knownCurrencies(api).then(cs => {
      const dl = dialog.querySelector('#dues-cur-list');
      if (dl) dl.innerHTML = cs.map(c => `<option value="${esc(c)}"></option>`).join('');
    });
    api('GET', '/contacts?limit=200').then(pg => {
      dialog.querySelector('#f-contact').innerHTML = '<option value="">— choose —</option>' +
        (pg?.data ?? pg ?? []).map(c => `<option value="${esc(c.id)}">${esc(c.name)}${c.organization ? ' — ' + esc(c.organization) : ''}</option>`).join('');
    }).catch(() => {});

    // Member picker: the list is fetched from the server per search term (so an
    // org with more members than one page can still find anyone), while the
    // selection lives in a Set that survives re-querying.
    let shownMembers = [];
    const picked = new Set();
    const pickedNames = new Map(); // id -> name, so the count tooltip works after re-query
    const mlist = dialog.querySelector('#f-mlist');
    const recount = () => {
      dialog.querySelector('#f-mcount').textContent = `${picked.size} selected`;
      dialog.querySelector('#f-mcount').title = [...pickedNames.values()].join(', ');
    };
    const paint = () => {
      mlist.innerHTML = shownMembers.map(m => `
        <label style="display:flex;gap:.45rem;align-items:center;font-size:.85rem;padding:.12rem 0;cursor:pointer;text-transform:none;letter-spacing:normal;font-weight:400;color:var(--color-text);margin-bottom:0">
          <input type="checkbox" class="inv-mcb" style="width:auto" value="${esc(m.id)}" ${picked.has(m.id) ? 'checked' : ''}>
          <span style="flex:1">${esc(m.display_name)}</span>
          <span style="font-size:.72rem;color:var(--color-text-muted)">${esc(m.tier ?? '')}</span>
        </label>`).join('') || '<p style="color:var(--color-text-muted);font-size:.85rem;margin:.3rem 0">No matching members.</p>';
      mlist.querySelectorAll('.inv-mcb').forEach(cb => cb.addEventListener('change', () => {
        const m = shownMembers.find(x => x.id === cb.value);
        if (cb.checked) { picked.add(cb.value); if (m) pickedNames.set(cb.value, m.display_name); }
        else { picked.delete(cb.value); pickedNames.delete(cb.value); }
        recount();
      }));
    };
    let searchTimer;
    const runSearch = () => {
      const q = dialog.querySelector('#f-msearch').value.trim();
      const params = new URLSearchParams({ limit: '50', status: 'active' });
      if (q) params.set('search', q);
      api('GET', '/members?' + params).then(pg => {
        shownMembers = pg?.data ?? pg ?? [];
        paint();
      }).catch(() => { mlist.innerHTML = '<p style="color:var(--color-danger);font-size:.85rem">Failed to load members.</p>'; });
    };
    runSearch(); recount();
    dialog.querySelector('#f-msearch').addEventListener('input', () => {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(runSearch, 300);
    });
    dialog.querySelector('#f-mall').addEventListener('click', () => {
      shownMembers.forEach(m => { picked.add(m.id); pickedNames.set(m.id, m.display_name); });
      paint(); recount();
    });
    dialog.querySelector('#f-mnone').addEventListener('click', () => {
      picked.clear(); pickedNames.clear(); paint(); recount();
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

  async openTransactionModal(invoiceID) {
    // Fetch the invoice so the modal can say WHO and HOW MUCH IS LEFT —
    // the treasurer was typing amounts from memory into a context-free box
    // while the server (which rejects overpayments) knew the number all along.
    let inv;
    try { inv = await api('GET', `/dues/${invoiceID}`); }
    catch { toast('Could not load the invoice', 'error'); return; }
    const currency = (inv.currency || 'USD').trim().toUpperCase();
    const paid = (inv.transactions ?? [])
      .filter(t => t.provider_status !== 'failed' && (t.currency ?? currency) === currency)
      .reduce((s, t) => s + t.amount_minor, 0);
    const remaining = Math.max(inv.amount_minor - paid, 0);
    const exp = moneyExponent(currency);
    const remainingStr = (remaining / 10 ** exp).toFixed(exp);
    const { dialog, close } = openModal({
      title: `Record payment — ${inv.member_name}, ${inv.period_label}`, // openModal escapes titles itself
      maxWidth: '420px',
      body: `
        <div class="modal-body">
          <p style="font-size:.85rem;color:var(--color-text-muted);margin-bottom:.6rem">
            Invoice ${formatMoney(inv.amount_minor, currency)} ${esc(currency)} ·
            <strong style="color:var(--color-text)">Remaining ${formatMoney(remaining, currency)}</strong></p>
          <div class="form-row">
            <div class="form-group">
              <label for="f-amount">Amount *</label>
              <input id="f-amount" type="text" inputmode="decimal" pattern="[0-9]*[.]?[0-9]*" value="${remaining > 0 ? esc(remainingStr) : ''}" placeholder="${esc(remainingStr)}">
            </div>
            <div class="form-group">
              <label for="f-provider">Provider</label>
              <select id="f-provider">${providerOptions('manual')}</select>
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
      } catch (err) {
        // The server guards against a typo'd extra zero: a payment past the
        // remaining balance needs an explicit acknowledgement.
        if (err?.code === 'conflict' && (err.error ?? '').includes('allow_overpayment')) {
          // NB: this is the imported Promise-returning confirm — await it, or
          // the truthy Promise records the overpayment while the dialog is
          // still asking.
          if (await confirm('This payment is MORE than the remaining balance. Record it as an intentional overpayment?', 'Overpayment')) {
            try {
              await api('POST', `/dues/${invoiceID}/transactions`, { ...body, allow_overpayment: true });
              toast('Overpayment recorded', 'success');
              close();
              this.load();
            } catch (e2) { toast(e2.error ?? 'Failed', 'error'); }
          }
          return;
        }
        toast(err.error ?? 'Failed', 'error');
      }
    }));
  }
}
customElements.define('page-dues', PageDues);
