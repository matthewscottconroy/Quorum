import { api, canWrite, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, formatMoney, parseMoney, providerOptions, knownCurrencies, currencyDatalist, renderPager, loadFilters, saveFilters, safeUrl } from '../utils.js';
import { confirm } from './confirm-dialog.js';

const PAGE = 50;

// Bill statuses ride the shared badge system (dark-safe, same as invoices)
// instead of a private hex palette.
const BILL_BADGE = { open: 'open', paid: 'paid', void: 'void' };

/**
 * Accounts payable: vendor bills accrued on the books at entry (DR expense /
 * CR A/P), settled or voided later. Cash-basis statements ignore a bill
 * until it is actually paid.
 */
class PagePayables extends HTMLElement {
  constructor() {
    super();
    this._bills = [];
    this._status = loadFilters('payables')?.status ?? 'open';
    this._offset = 0;
    this._total = 0;
  }

  connectedCallback() { this.render(); this.load(); }

  render() {
    this.innerHTML = `
      <div class="page-header"><h1>Payables</h1>
        <div style="display:flex;gap:.5rem">
          ${canWrite() ? '<button class="btn-primary" id="bl-new">+ New bill</button>' : ''}
        </div></div>
      <money-nav current="payables"></money-nav>
      <p style="color:var(--color-text-muted);font-size:.85rem;margin-bottom:1rem">
        What the organization owes vendors. Entering a bill puts the liability on the books
        immediately; paying it moves cash. Both are journal entries — nothing here bypasses
        the ledger.</p>
      <div id="bl-strip" style="font-size:.85rem;color:var(--color-text-muted);padding:.15rem .2rem .5rem"></div>
      <div class="search-bar">
        <select id="bl-status" aria-label="Filter by bill status" style="max-width:160px">
          <option value="open" ${this._status==='open'?'selected':''}>Open</option>
          <option value="paid" ${this._status==='paid'?'selected':''}>Paid</option>
          <option value="void" ${this._status==='void'?'selected':''}>Void</option>
          <option value="" ${this._status===''?'selected':''}>All</option>
        </select>
      </div>
      <div id="bl-list" style="display:flex;flex-direction:column;gap:.6rem"><span class="spinner"></span></div>
      <div id="bl-pager"></div>`;
    this.querySelector('#bl-status').addEventListener('change', e => { this._status = e.target.value; this._offset = 0; saveFilters('payables', { status: this._status }); this.load(); });
    this.querySelector('#bl-new')?.addEventListener('click', () => this.openBillModal());
  }

  async load() {
    const qs = `?limit=${PAGE}&offset=${this._offset}` + (this._status ? `&status=${this._status}` : '');
    let summary = [];
    try {
      const pg = await api('GET', '/bills' + qs);
      this._bills = pg?.data ?? [];
      this._total = pg?.total ?? this._bills.length;
      summary = pg?.summary ?? [];
    } catch { toast('Failed to load bills', 'error'); return; }
    // Same first-question treatment the dues page got: "how much do we owe?"
    // answered per currency for the SAME filtered set the list shows.
    const strip = this.querySelector('#bl-strip');
    if (strip) {
      const verb = { open: 'owed', paid: 'paid', void: 'voided' }[this._status] ?? 'total';
      strip.innerHTML = summary
        .map(s => `${s.count} ${this._status || ''} bill${s.count === 1 ? '' : 's'} · <strong style="color:var(--color-text);font-variant-numeric:tabular-nums">${formatMoney(s.outstanding_minor, s.currency)} ${esc(s.currency)}</strong> ${verb}`)
        .join(' &nbsp;·&nbsp; ');
    }
    const box = this.querySelector('#bl-list');
    box.innerHTML = this._bills.map(b => {
      const badge = BILL_BADGE[b.status] ?? 'draft';
      const due = b.due_date ? new Date(b.due_date) : null;
      // Compare as dates, not instants: "2026-08-17" parses to UTC midnight,
      // which would flag a bill overdue the morning it is due.
      const todayISO = new Date().toISOString().slice(0, 10);
      const overdue = b.due_date && b.status === 'open' && b.due_date < todayISO;
      return `
      <div class="card" style="padding:.8rem 1rem">
        <div style="display:flex;justify-content:space-between;gap:.8rem;flex-wrap:wrap;align-items:baseline">
          <div><b style="font-variant-numeric:tabular-nums">${formatMoney(b.amount_minor, b.currency)} ${esc(b.currency)}</b> → ${esc(b.contact_name)}
            <span style="font-size:.78rem;color:var(--color-text-muted)">· ${esc(b.expense_account_code)} ${esc(b.expense_account_name)}</span></div>
          <span class="badge badge-${esc(badge)}">${esc(b.status)}</span>
        </div>
        ${b.memo ? `<div style="font-size:.82rem;color:var(--color-text-muted);margin-top:.2rem">${esc(b.memo)}</div>` : ''}
        ${b.resource_id ? `<button class="btn-ghost bl-doc" data-rid="${esc(b.resource_id)}" style="font-size:.75rem;padding:.05rem .4rem">📄 vendor document</button>` : ''}
        <div style="font-size:.75rem;color:var(--color-text-muted);margin-top:.3rem">
          billed ${esc(new Date(b.bill_date).toLocaleDateString())}
          ${due ? ` · due <span style="${overdue ? 'color:var(--color-danger);font-weight:700' : ''}">${esc(due.toLocaleDateString())}</span>` : ''}
          ${b.paid_at ? ` · paid ${esc(new Date(b.paid_at).toLocaleDateString())}` : ''}
        </div>
        ${b.status === 'open' && canWrite() ? `
        <div style="display:flex;gap:.4rem;margin-top:.5rem">
          <button class="btn-primary bl-pay" data-id="${esc(b.id)}" style="font-size:.78rem">Pay (posts to books)</button>
          ${isAdmin() ? `<button class="btn-ghost bl-void" data-id="${esc(b.id)}" style="font-size:.78rem;color:var(--color-danger)">Void</button>` : ''}
        </div>` : ''}
      </div>`;
    }).join('') || `<div class="empty-state"><p>No ${this._status || ''} bills.</p>
      ${canWrite() && this._status !== 'paid' && this._status !== 'void'
        ? '<button class="btn-primary" id="bl-empty-new" style="margin-top:.5rem">+ Record your first bill</button>' : ''}</div>`;
    this.querySelector('#bl-empty-new')?.addEventListener('click', () => this.openBillModal());
    renderPager(this.querySelector('#bl-pager'), {
      offset: this._offset, limit: PAGE, total: this._total,
      onNavigate: off => { this._offset = off; this.load(); },
    });

    box.querySelectorAll('.bl-doc').forEach(b => b.addEventListener('click', async () => {
      try {
        const r0 = await api('GET', `/resources/${b.dataset.rid}`);
        if (r0.file_name) (await import('./doc-preview.js')).openDocPreview(r0);
        else if (r0.url) { const u = safeUrl(r0.url); if (u) window.open(u, '_blank', 'noopener'); }
      } catch { toast("You don't have access to this document", 'error'); }
    }));
    box.querySelectorAll('.bl-pay').forEach(btn => btn.addEventListener('click', () =>
      this.openPayModal(this._bills.find(b => b.id === btn.dataset.id))));
    box.querySelectorAll('.bl-void').forEach(btn => btn.addEventListener('click', async () => {
      if (!await confirm('Void this bill? A reversing entry is posted in the ledger.', 'Void bill')) return;
      try { await api('POST', `/bills/${btn.dataset.id}/void`); toast('Voided', 'success'); this.load(); }
      catch (err) { toast(err.error ?? 'Void failed', 'error'); }
    }));
  }

  async openBillModal() {
    const [contactsPg, accounts, currencies] = await Promise.all([
      api('GET', '/contacts?limit=200').catch(() => null),
      api('GET', '/accounting/accounts').catch(() => []),
      knownCurrencies(api),
    ]);
    const contacts = contactsPg?.data ?? contactsPg ?? [];
    if (!contacts.length) { toast('Add the vendor under Contacts first', 'error'); return; }
    const expenses = (accounts ?? []).filter(a => a.type === 'expense' && a.active);
    const { dialog, close } = openModal({
      title: 'New vendor bill',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="nb-contact">Vendor *</label>
            <select id="nb-contact">${contacts.map(c => `<option value="${esc(c.id)}">${esc(c.name)}${c.organization ? ' — ' + esc(c.organization) : ''}</option>`).join('')}</select></div>
          <div class="form-row">
            <div class="form-group"><label for="nb-amount">Amount *</label>
              <input id="nb-amount" inputmode="decimal" placeholder="450.00"></div>
            <div class="form-group"><label for="nb-cur">Currency</label>
              <input id="nb-cur" value="USD" maxlength="3" list="nb-cur-list" style="max-width:90px">
              ${currencyDatalist('nb-cur-list', currencies)}</div>
            <div class="form-group"><label for="nb-exp">Expense account *</label>
              <select id="nb-exp">${expenses.map(a => `<option value="${esc(a.id)}">${esc(a.code)} ${esc(a.name)}</option>`).join('')}</select>
              <div id="nb-budget-hint" style="font-size:.75rem;color:var(--color-text-muted);margin-top:.2rem"></div></div>
          </div>
          <div class="form-row">
            <div class="form-group"><label for="nb-billdate">Bill date</label><input id="nb-billdate" type="date"></div>
            <div class="form-group"><label for="nb-due">Due date</label><input id="nb-due" type="date"></div>
          </div>
          <div class="form-group"><label for="nb-memo">Memo</label>
            <input id="nb-memo" placeholder="What is this bill for?"></div>
          <div class="form-group"><label for="nb-doc">Vendor invoice / receipt (optional)</label>
            <select id="nb-doc"><option value="">— none —</option></select></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="nb-cancel">Cancel</button>
          <button class="btn-primary" id="nb-save">Record bill (accrues to books)</button>
        </div>`,
    });
    dialog.querySelector('#nb-cancel').addEventListener('click', close);
    // Read-only budget context for the chosen expense account.
    const budgetHint = async () => {
      const hint = dialog.querySelector('#nb-budget-hint');
      if (!hint) return;
      hint.textContent = '';
      const acct = dialog.querySelector('#nb-exp').value;
      if (!acct) return;
      try {
        const b = await api('GET', `/budgets/remaining?account_id=${acct}`);
        const over = b.remaining_minor < 0;
        hint.innerHTML = `Budget \u201C${esc(b.scenario)}\u201D: <strong style="color:${over ? 'var(--color-danger)' : 'var(--color-success)'}">${formatMoney(b.remaining_minor, b.currency)}</strong> of ${formatMoney(b.budget_minor, b.currency)} remaining`;
      } catch { /* no active budget for this account */ }
    };
    dialog.querySelector('#nb-exp').addEventListener('change', budgetHint);
    budgetHint();
    api('GET', '/resources?limit=200').then(pg => {
      const sel = dialog.querySelector('#nb-doc');
      if (sel) sel.innerHTML = '<option value="">— none —</option>' +
        (pg?.data ?? []).map(r0 => `<option value="${esc(r0.id)}">${r0.file_name ? '📄' : '🔗'} ${esc(r0.title)}</option>`).join('');
    }).catch(() => {});
    const saveBtn = dialog.querySelector('#nb-save');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const currency = dialog.querySelector('#nb-cur').value.trim().toUpperCase();
      const amount_minor = parseMoney(dialog.querySelector('#nb-amount').value.trim(), currency);
      if (!amount_minor || amount_minor <= 0) { toast('Enter a valid amount', 'error'); return; }
      try {
        await api('POST', '/bills', {
          contact_id: dialog.querySelector('#nb-contact').value,
          amount_minor, currency,
          expense_account_id: dialog.querySelector('#nb-exp').value,
          bill_date: dialog.querySelector('#nb-billdate').value || '',
          due_date: dialog.querySelector('#nb-due').value || '',
          memo: dialog.querySelector('#nb-memo').value.trim() || null,
          resource_id: dialog.querySelector('#nb-doc')?.value || null,
        });
        toast('Bill recorded and accrued', 'success');
        close(); this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  async openPayModal(bill) {
    const funds = await api('GET', '/funds').catch(() => []);
    const { dialog, close } = openModal({
      title: `Pay ${formatMoney(bill.amount_minor, bill.currency)} ${bill.currency} to ${bill.contact_name}`,
      maxWidth: '440px',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="pb-source">Pay from</label>
            <select id="pb-source">
              <option value="">Operating cash</option>
              ${(funds ?? []).map(f => `<option value="${esc(f.id)}">Fund: ${esc(f.name)}</option>`).join('')}
            </select></div>
          <div class="form-group" id="pb-provider-row"><label for="pb-provider">Paid via (routes the cash account)</label>
            <select id="pb-provider">${providerOptions('wire')}</select></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="pb-cancel">Cancel</button>
          <button class="btn-primary" id="pb-pay">Pay (posts to books)</button>
        </div>`,
    });
    dialog.querySelector('#pb-cancel').addEventListener('click', close);
    dialog.querySelector('#pb-source').addEventListener('change', e => {
      dialog.querySelector('#pb-provider-row').style.display = e.target.value ? 'none' : '';
    });
    const payBtn = dialog.querySelector('#pb-pay');
    payBtn.addEventListener('click', guardButton(payBtn, async () => {
      try {
        await api('POST', `/bills/${bill.id}/pay`, {
          fund_id: dialog.querySelector('#pb-source').value || '',
          provider: dialog.querySelector('#pb-provider').value.trim().toLowerCase(),
        });
        toast('Paid and posted', 'success');
        close(); this.load();
      } catch (err) { toast(err.error ?? 'Payment failed', 'error'); }
    }));
  }
}
customElements.define('page-payables', PagePayables);
