import { api, canWrite, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, formatMoney, parseMoney, providerOptions, knownCurrencies, currencyDatalist } from '../utils.js';

const BILL_BADGE = {
  open: ['⏳ open', '#b45309'], paid: ['✓ paid', '#137333'], void: ['— void', '#6b7280'],
};

/**
 * Accounts payable: vendor bills accrued on the books at entry (DR expense /
 * CR A/P), settled or voided later. Cash-basis statements ignore a bill
 * until it is actually paid.
 */
class PagePayables extends HTMLElement {
  constructor() { super(); this._bills = []; this._status = 'open'; }

  connectedCallback() { this.render(); this.load(); }

  render() {
    this.innerHTML = `
      <div class="page-header"><h1>Payables</h1>
        <div style="display:flex;gap:.5rem">
          <select id="bl-status">
            <option value="open">Open</option><option value="paid">Paid</option>
            <option value="void">Void</option><option value="">All</option>
          </select>
          ${canWrite() ? '<button class="btn-primary" id="bl-new">+ New bill</button>' : ''}
        </div></div>
      <money-nav current="payables"></money-nav>
      <p style="color:var(--color-text-muted);font-size:.85rem;margin-bottom:1rem">
        What the organization owes vendors. Entering a bill puts the liability on the books
        immediately; paying it moves cash. Both are journal entries — nothing here bypasses
        the ledger.</p>
      <div id="bl-list" style="display:flex;flex-direction:column;gap:.6rem"><span class="spinner"></span></div>`;
    this.querySelector('#bl-status').addEventListener('change', e => { this._status = e.target.value; this.load(); });
    this.querySelector('#bl-new')?.addEventListener('click', () => this.openBillModal());
  }

  async load() {
    const qs = this._status ? `?status=${this._status}` : '';
    try { this._bills = await api('GET', '/bills' + qs) ?? []; }
    catch { toast('Failed to load bills', 'error'); return; }
    const box = this.querySelector('#bl-list');
    box.innerHTML = this._bills.map(b => {
      const [label, color] = BILL_BADGE[b.status] ?? [b.status, '#6b7280'];
      const due = b.due_date ? new Date(b.due_date) : null;
      const overdue = due && b.status === 'open' && due < new Date();
      return `
      <div class="card" style="padding:.8rem 1rem">
        <div style="display:flex;justify-content:space-between;gap:.8rem;flex-wrap:wrap;align-items:baseline">
          <div><b>${formatMoney(b.amount_minor, b.currency)} ${esc(b.currency)}</b> → ${esc(b.contact_name)}
            <span style="font-size:.78rem;color:var(--color-text-muted)">· ${esc(b.expense_account_code)} ${esc(b.expense_account_name)}</span></div>
          <span class="badge" style="background:color-mix(in srgb, ${color} 14%, transparent);color:${color}">${label}</span>
        </div>
        ${b.memo ? `<div style="font-size:.82rem;color:var(--color-text-muted);margin-top:.2rem">${esc(b.memo)}</div>` : ''}
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
    }).join('') || `<div class="empty-state"><p>No ${this._status || ''} bills.</p></div>`;

    box.querySelectorAll('.bl-pay').forEach(btn => btn.addEventListener('click', () =>
      this.openPayModal(this._bills.find(b => b.id === btn.dataset.id))));
    box.querySelectorAll('.bl-void').forEach(btn => btn.addEventListener('click', async () => {
      if (!confirm('Void this bill? A reversing entry is posted.')) return;
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
