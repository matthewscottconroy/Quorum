import { api, apiDownload, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, formatMoney, openModal, guardButton } from '../utils.js';

/** Fiscal-year default range from the org setting (labels/defaults only). */
function fiscalRange(startMonth) {
  const now = new Date();
  let y = now.getFullYear();
  if (now.getMonth() + 1 < startMonth) y -= 1;
  const from = new Date(Date.UTC(y, startMonth - 1, 1));
  return [from.toISOString().slice(0, 10), now.toISOString().slice(0, 10)];
}

/**
 * The books (accounting Phases A–C): statements over any date range,
 * trial balance with the reconciliation verdict, closed periods, and the
 * editable chart of accounts. Amounts display in each row's own currency.
 */
class PageAccounting extends HTMLElement {
  connectedCallback() { this.render(); }

  async render() {
    let fyStart = 1;
    try { fyStart = Number((await api('GET', '/settings/org')).fiscal_year_start_month ?? 1) || 1; } catch { /* default */ }
    const [from, to] = fiscalRange(fyStart);
    this.innerHTML = `
      <div class="page-header"><h1>Accounting</h1>
        <div style="display:flex;gap:.4rem;align-items:center;flex-wrap:wrap">
          <input id="ac-from" type="date" value="${from}">
          <span style="color:var(--color-text-muted)">→</span>
          <input id="ac-to" type="date" value="${to}">
          <select id="ac-basis" title="Accounting basis for the income statement">
            <option value="accrual">Accrual</option>
            <option value="cash">Cash basis</option>
          </select>
          <button class="btn-secondary" id="ac-run">Run</button>
          ${isAdmin() ? '<button class="btn-secondary" id="ac-entry">+ Adjusting entry</button>' : ''}
        </div></div>
      <div id="ac-body"><span class="spinner"></span></div>`;
    this.querySelector('#ac-run').addEventListener('click', () => this.load());
    this.querySelector('#ac-entry')?.addEventListener('click', () => this.openEntryModal());
    this.load();
  }

  async load() {
    const from = this.querySelector('#ac-from').value, to = this.querySelector('#ac-to').value;
    const body = this.querySelector('#ac-body');
    body.innerHTML = '<span class="spinner"></span>';
    try {
      const basis = this.querySelector('#ac-basis').value;
      const [st, tb, periods, accounts, rules] = await Promise.all([
        api('GET', `/accounting/statements?from=${from}&to=${to}&basis=${basis}`),
        api('GET', '/accounting/trial-balance?limit=10'),
        api('GET', '/accounting/periods'),
        api('GET', '/accounting/accounts'),
        api('GET', '/accounting/posting-rules').catch(() => []),
      ]);
      this._accounts = accounts ?? [];
      const money = (b) => `${formatMoney(Math.abs(b.balance), b.currency)} ${esc(b.currency)}`;
      const rows = (list, flip) => (list ?? []).map(b => `
        <tr><td>${esc(b.code)}</td><td>${esc(b.name)}</td>
          <td style="text-align:right;font-variant-numeric:tabular-nums">${(flip ? -b.balance : b.balance) < 0 ? '−' : ''}${money(b)}</td></tr>`).join('')
        || '<tr><td colspan="3" style="color:var(--color-text-muted)">Nothing in range.</td></tr>';
      body.innerHTML = `
        <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:1rem">
          <div class="card" style="padding:1rem">
            <h3 style="margin:0 0 .5rem;font-size:.95rem">Income statement <span style="font-weight:400;color:var(--color-text-muted)">${esc(from)} → ${esc(to)} · ${esc(st.basis)}</span></h3>
            <table style="width:100%"><tbody>${rows(st.income_statement, true)}</tbody></table>
          </div>
          <div class="card" style="padding:1rem">
            <h3 style="margin:0 0 .5rem;font-size:.95rem">Balance sheet <span style="font-weight:400;color:var(--color-text-muted)">as of ${esc(to)}</span></h3>
            <table style="width:100%"><tbody>${rows(st.balance_sheet, false)}</tbody></table>
            <div style="font-size:.78rem;color:var(--color-text-muted);margin-top:.4rem">
              Net income to date: ${Object.entries(st.net_income_to_date ?? {}).map(([c, v]) => `${formatMoney(v, c)} ${esc(c)}`).join(' · ') || '—'}</div>
          </div>
          <div class="card" style="padding:1rem">
            <h3 style="margin:0 0 .5rem;font-size:.95rem">Receivables aging <span style="font-weight:400;color:var(--color-text-muted)">as of ${esc(to)}</span></h3>
            <table style="width:100%"><tbody>
              ${(st.ar_aging ?? []).map(a => `<tr><td>${esc(a.currency)}</td><td>${esc(a.bucket)}</td><td>${a.invoices} inv.</td>
                <td style="text-align:right">${formatMoney(a.amount, a.currency)}</td></tr>`).join('') || '<tr><td style="color:var(--color-text-muted)">No open receivables.</td></tr>'}
            </tbody></table>
            <div style="font-size:.78rem;margin-top:.4rem;${tb.reconciled ? 'color:var(--color-success,#137333)' : 'color:var(--color-danger)'}">
              ${tb.reconciled ? '✓ GL and dues subledger reconcile' : '⚠ RECONCILIATION MISMATCH — investigate'}</div>
          </div>
          <div class="card" style="padding:1rem">
            <h3 style="margin:0 0 .5rem;font-size:.95rem">Closed periods</h3>
            ${(periods ?? []).map(p => `
              <div style="display:flex;justify-content:space-between;font-size:.85rem;padding:.15rem 0">
                <span>${esc(String(p.month).slice(0, 7))}</span>
                <span style="color:var(--color-text-muted)">${esc(p.closed_by)}</span>
                ${isAdmin() ? `<button class="btn-ghost pd-reopen" data-m="${esc(String(p.month).slice(0, 10))}" style="padding:0 .3rem;font-size:.75rem">reopen</button>` : ''}
              </div>`).join('') || '<div style="font-size:.85rem;color:var(--color-text-muted)">None closed yet.</div>'}
            ${isAdmin() ? `
              <div style="display:flex;gap:.4rem;margin-top:.6rem">
                <input id="pd-month" type="month" style="flex:1">
                <button class="btn-secondary" id="pd-close">Close month</button>
              </div>
              <div style="font-size:.72rem;color:var(--color-text-muted);margin-top:.3rem">Closing locks posting dates in that month; reopening is allowed and audited.</div>` : ''}
          </div>
          <div class="card" style="padding:1rem">
            <h3 style="margin:0 0 .5rem;font-size:.95rem">Posting rules</h3>
            <p style="font-size:.74rem;color:var(--color-text-muted);margin:0 0 .5rem">
              Where automatic postings land. Your accountant reconfigures these — future postings only; history never changes.
              Add <code>cash.provider.&lt;name&gt;</code> rules to route any payment provider to its own account.</p>
            ${(rules ?? []).map(r0 => `
              <div style="display:flex;gap:.4rem;align-items:center;font-size:.82rem;padding:.1rem 0">
                <code style="flex:1">${esc(r0.key)}</code>
                ${isAdmin() ? `<select class="rule-acct" data-key="${esc(r0.key)}" style="max-width:220px;font-size:.8rem">
                  ${this._accounts.map(a => `<option value="${esc(a.code)}" ${a.code === r0.account_code ? 'selected' : ''}>${esc(a.code)} ${esc(a.name)}</option>`).join('')}
                </select>` : `<span>${esc(r0.account_code)} ${esc(r0.account_name)}</span>`}
              </div>`).join('')}
            ${isAdmin() ? `
              <div style="display:flex;gap:.3rem;margin-top:.5rem;flex-wrap:wrap">
                <input id="rule-provider" placeholder="provider (e.g. zelle)" style="flex:1;min-width:110px">
                <select id="rule-provider-acct" style="max-width:200px">${this._accounts.filter(a => a.type === 'asset').map(a => `<option value="${esc(a.code)}">${esc(a.code)} ${esc(a.name)}</option>`).join('')}</select>
                <button class="btn-secondary" id="rule-provider-add" style="font-size:.78rem">Add provider rule</button>
              </div>
              <button class="btn-primary" id="rules-save" style="margin-top:.5rem;font-size:.8rem">Save posting rules</button>` : ''}
          </div>
          <div class="card" style="padding:1rem">
            <h3 style="margin:0 0 .5rem;font-size:.95rem">Chart of accounts</h3>
            <div style="max-height:260px;overflow-y:auto">
              ${this._accounts.map(a => `
                <div style="display:flex;gap:.5rem;font-size:.84rem;padding:.12rem 0;${a.active ? '' : 'opacity:.5'}">
                  <span style="font-variant-numeric:tabular-nums">${esc(a.code)}</span>
                  <span style="flex:1">${esc(a.name)}</span>
                  <span class="badge badge-none" style="font-size:.66rem">${esc(a.type)}</span>
                  ${a.has_postings ? '<span title="Has postings: code and type frozen">🔒</span>' : ''}
                </div>`).join('')}
            </div>
            ${isAdmin() ? `
              <div style="display:flex;gap:.3rem;margin-top:.6rem;flex-wrap:wrap">
                <input id="na-code" placeholder="4100" maxlength="4" style="width:70px">
                <input id="na-name" placeholder="Account name" style="flex:1;min-width:120px">
                <select id="na-type">${['income', 'expense', 'asset', 'liability', 'net_assets'].map(t => `<option>${t}</option>`).join('')}</select>
                <button class="btn-secondary" id="na-add">Add</button>
              </div>` : ''}
          </div>
        </div>`;
      this.wire();
    } catch (err) { body.innerHTML = `<div class="empty-state"><p>${esc(err?.error ?? 'Failed to load')}</p></div>`; }
  }

  wire() {
    this.querySelector('#pd-close')?.addEventListener('click', async () => {
      const m = this.querySelector('#pd-month').value;
      if (!m) { toast('Pick a month', 'error'); return; }
      try { await api('POST', '/accounting/periods/close', { month: m + '-01' }); toast('Period closed', 'success'); this.load(); }
      catch (err) { toast(err.error ?? 'Close failed', 'error'); }
    });
    this.querySelectorAll('.pd-reopen').forEach(b => b.addEventListener('click', async () => {
      try { await api('POST', '/accounting/periods/reopen', { month: b.dataset.m }); toast('Reopened (audited)', 'success'); this.load(); }
      catch (err) { toast(err.error ?? 'Reopen failed', 'error'); }
    }));
    this.querySelector('#rules-save')?.addEventListener('click', async () => {
      const body = {};
      this.querySelectorAll('.rule-acct').forEach(s2 => { body[s2.dataset.key] = s2.value; });
      try { await api('PUT', '/accounting/posting-rules', body); toast('Posting rules saved — applies to future postings', 'success'); this.load(); }
      catch (err) { toast(err.error ?? 'Save failed', 'error'); }
    });
    this.querySelector('#rule-provider-add')?.addEventListener('click', async () => {
      const name = this.querySelector('#rule-provider').value.trim().toLowerCase();
      if (!/^[a-z0-9_]{1,32}$/.test(name)) { toast('Provider must be letters/digits/underscore', 'error'); return; }
      try {
        await api('PUT', '/accounting/posting-rules', { ['cash.provider.' + name]: this.querySelector('#rule-provider-acct').value });
        toast('Provider rule added', 'success'); this.load();
      } catch (err) { toast(err.error ?? 'Add failed', 'error'); }
    });
    this.querySelector('#na-add')?.addEventListener('click', async () => {
      try {
        await api('POST', '/accounting/accounts', {
          Code: this.querySelector('#na-code').value.trim(),
          Name: this.querySelector('#na-name').value.trim(),
          Type: this.querySelector('#na-type').value,
        });
        toast('Account added', 'success'); this.load();
      } catch (err) { toast(err.error ?? 'Add failed', 'error'); }
    });
  }

  openEntryModal() {
    const codes = (this._accounts ?? []).filter(a => a.active);
    const lineRow = () => `
      <div class="en-line" style="display:flex;gap:.3rem;margin-bottom:.3rem">
        <select class="en-code" style="flex:1">${codes.map(a => `<option value="${esc(a.code)}">${esc(a.code)} ${esc(a.name)}</option>`).join('')}</select>
        <input class="en-cur" value="USD" maxlength="3" style="width:60px">
        <input class="en-debit" placeholder="debit" inputmode="numeric" style="width:90px">
        <input class="en-credit" placeholder="credit" inputmode="numeric" style="width:90px">
      </div>`;
    const { dialog, close } = openModal({
      title: 'Adjusting journal entry',
      maxWidth: '560px',
      body: `
        <div class="modal-body">
          <p style="font-size:.78rem;color:var(--color-text-muted);margin-top:0">
            Amounts are integer minor units (cents). Debits must equal credits per currency —
            the database refuses anything else, and closed periods refuse postings.</p>
          <div class="form-row">
            <div class="form-group"><label for="en-date">Entry date</label><input id="en-date" type="date" value="${new Date().toISOString().slice(0, 10)}"></div>
            <div class="form-group"><label for="en-memo">Memo *</label><input id="en-memo" placeholder="Why this adjustment?"></div>
          </div>
          <div id="en-lines">${lineRow()}${lineRow()}</div>
          <button class="btn-ghost" id="en-more" style="font-size:.78rem">+ line</button>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="en-cancel">Cancel</button>
          <button class="btn-primary" id="en-post">Post entry</button>
        </div>`,
    });
    dialog.querySelector('#en-cancel').addEventListener('click', close);
    dialog.querySelector('#en-more').addEventListener('click', () =>
      dialog.querySelector('#en-lines').insertAdjacentHTML('beforeend', lineRow()));
    const postBtn = dialog.querySelector('#en-post');
    postBtn.addEventListener('click', guardButton(postBtn, async () => {
      const memo = dialog.querySelector('#en-memo').value.trim();
      if (!memo) { toast('Memo required', 'error'); return; }
      const lines = [...dialog.querySelectorAll('.en-line')].map(row => ({
        account_code: row.querySelector('.en-code').value,
        currency: row.querySelector('.en-cur').value.trim().toUpperCase(),
        debit: Number(row.querySelector('.en-debit').value || 0),
        credit: Number(row.querySelector('.en-credit').value || 0),
      })).filter(l => l.debit > 0 || l.credit > 0);
      try {
        await api('POST', '/accounting/entries', {
          entry_date: dialog.querySelector('#en-date').value, memo, lines,
        });
        toast('Entry posted', 'success'); close(); this.load();
      } catch (err) { toast(err.error ?? 'Post failed', 'error'); }
    }));
  }
}
customElements.define('page-accounting', PageAccounting);
