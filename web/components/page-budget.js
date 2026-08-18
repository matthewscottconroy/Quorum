import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';
import { esc, openModal, guardButton, formatMoney, parseMoney, moneyExponent, confirmDelete } from '../utils.js';

/** A quiet "that stuck" pulse on the element a commit just saved. */
function flashSaved(el) {
  if (!el) return;
  el.style.transition = 'background-color .15s';
  el.style.backgroundColor = 'color-mix(in srgb, var(--color-success, #137333) 18%, transparent)';
  setTimeout(() => { el.style.backgroundColor = ''; }, 650);
}

const STATUSES = ['draft', 'active', 'archived'];

/**
 * Renders minor units as a plain editable major-unit string (no currency symbol
 * or grouping), so it round-trips through parseMoney. formatMoney is for display
 * only — its Intl output ("$1,200.00") is NOT parseable back.
 */
function plainAmount(minor, code) {
  const exp = moneyExponent(code);
  return ((Number(minor) || 0) / 10 ** exp).toFixed(exp);
}

/** Formats a signed net figure with a surplus/deficit label and color. */
function netLabel(minor, currency) {
  const color = minor > 0 ? 'var(--color-success)' : minor < 0 ? 'var(--color-danger)' : 'var(--color-text-muted)';
  const word = minor > 0 ? 'surplus' : minor < 0 ? 'deficit' : 'balanced';
  return `<span style="color:${color};font-weight:700">${formatMoney(Math.abs(minor), currency)} ${word}</span>`;
}

class PageBudget extends HTMLElement {
  connectedCallback() {
    this._scenarios = [];
    this._selectedId = null;
    this._compare = new Set();
    this._seq = 0; // guards against out-of-order scenario-detail responses
    this._accounts = []; // income/expense accounts for line linking
    this._fyStart = 1;
    this.render();
    this.loadList();
    this.loadAccounts();
  }

  async loadAccounts() {
    try {
      const all = await api('GET', '/accounting/accounts') ?? [];
      this._accounts = all.filter(a => a.type === 'income' || a.type === 'expense');
    } catch { /* linking stays optional */ }
    try {
      this._fyStart = Number((await api('GET', '/settings/org')).fiscal_year_start_month ?? 1) || 1;
    } catch { /* calendar year default */ }
  }

  /** [from, to] ISO dates for the fiscal year containing/preceding today. */
  fiscalYear(offset = 0) {
    const now = new Date();
    let year = now.getFullYear() + offset;
    if (now.getMonth() + 1 < this._fyStart) year -= 1;
    const from = new Date(Date.UTC(year, this._fyStart - 1, 1));
    const to = new Date(Date.UTC(year + 1, this._fyStart - 1, 0)); // day 0 = last of prior month
    return [from.toISOString().slice(0, 10), to.toISOString().slice(0, 10)];
  }

  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Budget planning</h1>
        <div style="display:flex;gap:.5rem">
          <button class="btn-secondary" id="compare-btn">Compare selected</button>
          ${canWrite() ? '<button class="btn-primary" id="new-btn">+ New scenario</button>' : ''}
        </div>
      </div>
      <money-nav current="budget"></money-nav>
      <p style="font-size:.85rem;color:var(--color-text-muted);margin:-.5rem 0 1rem">
        Draft budget scenarios, model income and expenses as quantity × unit amount, and tweak the numbers to see the
        surplus or deficit update. Clone a scenario to explore a variation, or seed dues income straight from your roster.
      </p>
      <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,300px),1fr));gap:1.25rem;align-items:start" id="b-grid">
        <div id="b-list"><div style="text-align:center;padding:1rem"><span class="spinner"></span></div></div>
        <div id="b-detail"><div class="empty-state" style="padding:2rem"><p>Select a scenario, or create one to begin.</p></div></div>
      </div>
    `;
    this.querySelector('#new-btn')?.addEventListener('click', () => this.openCreate());
    this.querySelector('#compare-btn').addEventListener('click', () => this.openCompare());
  }

  async loadList() {
    try {
      this._scenarios = await api('GET', '/budgets') ?? [];
    } catch {
      this.querySelector('#b-list').innerHTML = '<div class="empty-state"><p>Failed to load.</p></div>';
      return;
    }
    this.renderList();
  }

  renderList() {
    const el = this.querySelector('#b-list');
    el.innerHTML = this._scenarios.length ? this._scenarios.map(s => `
      <div class="card b-card" data-id="${esc(s.id)}" role="button" tabindex="0" aria-label="Open scenario ${esc(s.name)}" style="padding:.85rem 1rem;margin-bottom:.6rem;cursor:pointer;border-left:3px solid ${s.id===this._selectedId?'var(--color-primary)':'transparent'}">
        <div style="display:flex;align-items:center;gap:.5rem">
          <input type="checkbox" class="cmp-chk" data-id="${esc(s.id)}" ${this._compare.has(s.id)?'checked':''} title="Add to comparison" style="width:auto">
          <div style="flex:1">
            <div style="font-weight:700;font-size:.95rem">${esc(s.name)}</div>
            <div style="font-size:.75rem;color:var(--color-text-muted)">${esc(s.period_label ?? '')} <span class="badge badge-${s.status==='active'?'paid':'none'}" style="font-size:.65rem">${esc(s.status)}</span></div>
          </div>
        </div>
        <div style="font-size:.8rem;margin-top:.4rem;font-variant-numeric:tabular-nums">
          <span style="color:var(--color-success)">+${formatMoney(s.totals.income_minor, s.totals.currency)}</span>
          <span style="color:var(--color-danger);margin-left:.5rem">−${formatMoney(s.totals.expense_minor, s.totals.currency)}</span>
          <span style="margin-left:.5rem">= ${netLabel(s.totals.net_minor, s.totals.currency)}</span>
        </div>
      </div>`).join('') : '<div class="empty-state"><p>No scenarios yet.</p></div>';

    el.querySelectorAll('.b-card').forEach(card => {
      const open = e => {
        if (e.target.classList.contains('cmp-chk')) return;
        this.select(card.dataset.id);
      };
      card.addEventListener('click', open);
      card.addEventListener('keydown', e => {
        if (e.target.closest('input, button, a, select')) return;
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(e); }
      });
    });
    el.querySelectorAll('.cmp-chk').forEach(chk => {
      chk.addEventListener('change', () => {
        if (chk.checked) this._compare.add(chk.dataset.id); else this._compare.delete(chk.dataset.id);
      });
    });
  }

  async select(id) {
    const seq = ++this._seq;
    this._selectedId = id;
    this.renderList();
    const detail = this.querySelector('#b-detail');
    detail.innerHTML = '<div style="text-align:center;padding:2rem"><span class="spinner"></span></div>';
    let s;
    try { s = await api('GET', `/budgets/${id}`); }
    catch { if (seq === this._seq) detail.innerHTML = '<div class="empty-state"><p>Failed to load scenario.</p></div>'; return; }
    if (seq !== this._seq) return; // a newer select() superseded this one
    this.renderDetail(s);
  }

  renderDetail(s) {
    const cur = s.currency;
    const accountOpts = (kind, current) => ['<option value="">— GL account —</option>']
      .concat(this._accounts.filter(a => a.type === kind).map(a =>
        `<option value="${esc(a.id)}" ${a.id === current ? 'selected' : ''}>${esc(a.code)} ${esc(a.name)}</option>`))
      .join('');
    const lineRows = kind => (s.lines ?? []).filter(l => l.kind === kind).map(l => `
      <tr class="b-line" data-id="${esc(l.id)}" data-currency="${esc(cur)}">
        <td style="white-space:nowrap">
          <button class="btn-ghost l-up" title="Move up" aria-label="Move line up" style="font-size:.7rem;padding:0 .2rem">▲</button><button class="btn-ghost l-down" title="Move down" aria-label="Move line down" style="font-size:.7rem;padding:0 .2rem">▼</button>
        </td>
        <td><input class="l-label" value="${esc(l.label)}" style="width:100%;padding:.2rem .35rem;font-size:.85rem"></td>
        <td><input class="l-cat" list="cat-options" value="${esc(l.category ?? '')}" placeholder="—" style="width:80px;padding:.2rem .35rem;font-size:.8rem"></td>
        <td><select class="l-acct" title="Link to a GL account for budget-vs-actual" style="width:120px;padding:.2rem;font-size:.75rem">${accountOpts(kind, l.account_id)}</select></td>
        <td><input class="l-qty" type="number" min="0" value="${l.quantity}" style="width:60px;padding:.2rem .35rem;font-size:.85rem;text-align:right"></td>
        <td><input class="l-unit" value="${plainAmount(l.unit_amount_minor, cur)}" style="width:84px;padding:.2rem .35rem;font-size:.85rem;text-align:right"></td>
        <td class="l-amount" style="text-align:right;font-variant-numeric:tabular-nums;font-weight:600;white-space:nowrap">${formatMoney(l.amount_minor, cur)}</td>
        <td><button class="btn-ghost l-del" aria-label="Delete line" title="Delete line" style="color:var(--color-danger);font-size:.75rem">✕</button></td>
      </tr>`).join('');

    const section = (kind, title, color) => `
      <div style="margin-bottom:1rem">
        <div style="font-weight:700;font-size:.85rem;color:${color};margin-bottom:.35rem">${title}</div>
        <table style="width:100%;border-collapse:collapse">
          <thead><tr style="font-size:.68rem;color:var(--color-text-muted);text-transform:uppercase;letter-spacing:.05em">
            <th></th><th style="text-align:left">Item</th><th style="text-align:left">Category</th><th style="text-align:left">Account</th><th style="text-align:right">Qty</th><th style="text-align:right">Unit</th><th style="text-align:right">Amount</th><th></th>
          </tr></thead>
          <tbody data-kind="${kind}">${lineRows(kind)}</tbody>
        </table>
        <div class="cat-subtotals" data-kind="${kind}" style="font-size:.72rem;color:var(--color-text-muted);margin-top:.25rem"></div>
        <button class="btn-ghost add-line" data-kind="${kind}" style="font-size:.78rem;margin-top:.25rem">+ Add ${kind} line</button>
      </div>`;

    this.querySelector('#b-detail').innerHTML = `
      <div class="card" style="padding:1.25rem">
        <div style="display:flex;justify-content:space-between;gap:1rem;align-items:start;flex-wrap:wrap;margin-bottom:1rem">
          <div style="flex:1;min-width:200px">
            <input id="s-name" value="${esc(s.name)}" style="font-size:1.1rem;font-weight:700;width:100%;padding:.3rem .4rem;border:1px solid transparent;background:transparent" title="Scenario name">
            <div style="display:flex;gap:.5rem;margin-top:.35rem;align-items:center;flex-wrap:wrap">
              <input id="s-period" value="${esc(s.period_label ?? '')}" placeholder="Period (e.g. FY2027)" style="width:130px;padding:.2rem .4rem;font-size:.8rem">
              <input id="s-starts" type="date" value="${s.starts_on ? esc(String(s.starts_on).slice(0,10)) : ''}" title="Period start (enables proration and date defaults)" style="padding:.2rem .3rem;font-size:.78rem">
              <span style="font-size:.75rem;color:var(--color-text-muted)">→</span>
              <input id="s-ends" type="date" value="${s.ends_on ? esc(String(s.ends_on).slice(0,10)) : ''}" title="Period end" style="padding:.2rem .3rem;font-size:.78rem">
              <select id="s-status" style="width:auto;padding:.2rem .4rem;font-size:.8rem">${STATUSES.map(x=>`<option ${s.status===x?'selected':''}>${x}</option>`).join('')}</select>
            </div>
          </div>
          <div style="display:flex;gap:.4rem;flex-wrap:wrap">
            <button class="btn-secondary" id="vsactual-btn" style="font-size:.78rem">Vs actual</button>
            <button class="btn-secondary" id="seed-btn" style="font-size:.78rem">Seed dues</button>
            <button class="btn-secondary" id="clone-btn" style="font-size:.78rem">Clone</button>
            <button class="btn-ghost" id="del-btn" style="font-size:.78rem;color:var(--color-danger)">Delete</button>
          </div>
        </div>

        ${!(s.lines ?? []).length && canWrite() ? `
        <div style="border:1px dashed var(--color-border);border-radius:var(--radius);padding:.75rem 1rem;margin-bottom:1rem;display:flex;justify-content:space-between;align-items:center;gap:.75rem;flex-wrap:wrap">
          <span style="font-size:.85rem;color:var(--color-text-muted)">This scenario is empty. The fastest start: project dues income straight from your roster.</span>
          <button class="btn-primary" id="empty-seed-btn" style="font-size:.8rem">Seed dues from roster</button>
        </div>` : ''}

        ${section('income', 'Income', 'var(--color-success)')}
        ${section('expense', 'Expenses', 'var(--color-danger)')}
        <datalist id="cat-options">
          ${[...new Set((s.lines ?? []).map(l => l.category).filter(Boolean))].map(c => `<option value="${esc(c)}"></option>`).join('')}
        </datalist>

        <div id="b-totals" style="border-top:2px solid var(--color-border);padding-top:.75rem"></div>
      </div>`;

    this.wireDetail(s);
    this.renderTotals(s.currency);
    // Read-only members see the numbers, not the levers: the server would
    // refuse the writes anyway, so don't render working-looking controls.
    if (!canWrite()) {
      const detail = this.querySelector('#b-detail');
      detail.querySelectorAll('input, select').forEach(i => { i.disabled = true; });
      detail.querySelectorAll('.add-line, .l-del, .l-up, .l-down, #seed-btn, #clone-btn, #del-btn').forEach(b => b.remove());
    }
  }

  /** Recomputes and renders the income/expense/net footer from current inputs. */
  renderTotals(currency) {
    let income = 0, expense = 0;
    const cats = { income: new Map(), expense: new Map() };
    this.querySelectorAll('.b-line').forEach(row => {
      const amt = this.lineAmountMinor(row, currency);
      row.querySelector('.l-amount').textContent = formatMoney(amt, currency);
      const kind = row.closest('tbody').dataset.kind;
      if (kind === 'income') income += amt; else expense += amt;
      const cat = row.querySelector('.l-cat').value.trim();
      if (cat) cats[kind].set(cat, (cats[kind].get(cat) ?? 0) + amt);
    });
    // Category subtotals appear once a section actually uses categories.
    this.querySelectorAll('.cat-subtotals').forEach(el => {
      const m = cats[el.dataset.kind];
      el.innerHTML = m.size >= 2
        ? 'By category: ' + [...m.entries()].sort((a, b) => b[1] - a[1])
            .map(([c, v]) => `<strong>${esc(c)}</strong> ${formatMoney(v, currency)}`).join(' · ')
        : '';
    });
    const net = income - expense;
    // Proportional income-vs-expense bar (share of the larger of the two).
    const max = Math.max(income, expense, 1);
    this.querySelector('#b-totals').innerHTML = `
      <div style="display:flex;flex-direction:column;gap:.35rem;font-variant-numeric:tabular-nums">
        <div style="display:flex;align-items:center;gap:.5rem"><span style="width:70px;font-size:.8rem;color:var(--color-success)">Income</span>
          <div style="flex:1;height:12px;background:var(--color-border);border-radius:6px;overflow:hidden"><div style="height:100%;width:${income/max*100}%;background:var(--color-success)"></div></div>
          <span style="width:100px;text-align:right;font-weight:600">${formatMoney(income, currency)}</span></div>
        <div style="display:flex;align-items:center;gap:.5rem"><span style="width:70px;font-size:.8rem;color:var(--color-danger)">Expenses</span>
          <div style="flex:1;height:12px;background:var(--color-border);border-radius:6px;overflow:hidden"><div style="height:100%;width:${expense/max*100}%;background:var(--color-danger)"></div></div>
          <span style="width:100px;text-align:right;font-weight:600">${formatMoney(expense, currency)}</span></div>
        <div style="display:flex;justify-content:flex-end;gap:.5rem;margin-top:.35rem;font-size:1rem">Net: ${netLabel(net, currency)}</div>
      </div>`;
  }

  /** Budget vs. actual: per-category variance, basis choice, FY presets,
      time-elapsed proration, and honest multi-currency handling. */
  openVsActual(s) {
    const cur = s.currency;
    const defFrom = s.starts_on ? String(s.starts_on).slice(0, 10) : this.fiscalYear()[0];
    const defTo = s.ends_on ? String(s.ends_on).slice(0, 10) : this.fiscalYear()[1];
    const { dialog, close } = openModal({
      title: `Budget vs. actual — ${s.name}`,
      maxWidth: '660px',
      body: `
        <div class="modal-body">
          <p style="color:var(--color-text-muted);font-size:.85rem">Planned vs. posted, in ${esc(cur)} only. Link lines to GL accounts for the per-category rows.</p>
          <div style="display:flex;gap:.6rem;align-items:end;flex-wrap:wrap">
            <div class="form-group" style="margin-bottom:0"><label for="va-from">From</label><input id="va-from" type="date" value="${esc(defFrom)}"></div>
            <div class="form-group" style="margin-bottom:0"><label for="va-to">To</label><input id="va-to" type="date" value="${esc(defTo)}"></div>
            <div class="form-group" style="margin-bottom:0"><label for="va-basis">Basis</label>
              <select id="va-basis"><option value="accrual">Accrual</option><option value="cash">Cash</option></select></div>
            <button class="btn-primary" id="va-run">Compare</button>
          </div>
          <div style="display:flex;gap:.4rem;margin-top:.4rem">
            <button class="btn-ghost" id="va-thisfy" style="font-size:.75rem">This fiscal year</button>
            <button class="btn-ghost" id="va-lastfy" style="font-size:.75rem">Last fiscal year</button>
          </div>
          <div id="va-out" style="margin-top:1rem"></div>
        </div>
        <div class="modal-footer"><button class="btn-secondary" id="va-close">Close</button></div>`,
    });
    dialog.querySelector('#va-close').addEventListener('click', close);
    const setRange = ([f, t]) => {
      dialog.querySelector('#va-from').value = f;
      dialog.querySelector('#va-to').value = t;
    };
    dialog.querySelector('#va-thisfy').addEventListener('click', () => setRange(this.fiscalYear()));
    dialog.querySelector('#va-lastfy').addEventListener('click', () => setRange(this.fiscalYear(-1)));
    const run = dialog.querySelector('#va-run');
    const doRun = guardButton(run, async () => {
      const from = dialog.querySelector('#va-from').value, to = dialog.querySelector('#va-to').value;
      const basis = dialog.querySelector('#va-basis').value;
      if (!from || !to) { toast('Pick both dates', 'error'); return; }
      let d;
      try { d = await api('GET', `/budgets/${s.id}/vs-actual?from=${from}&to=${to}&basis=${basis}`); }
      catch (err) { toast(err.error ?? 'Failed', 'error'); return; }
      const money = m => formatMoney(m, cur);
      const row = (label, budget, actual, variance, favGood, dim) => {
        const good = favGood ? variance >= 0 : variance <= 0;
        return `<tr${dim ? ' style="color:var(--color-text-muted)"' : ''}>
          <td>${label}</td>
          <td style="text-align:right;font-variant-numeric:tabular-nums">${money(budget)}</td>
          <td style="text-align:right;font-variant-numeric:tabular-nums">${money(actual)}</td>
          <td style="text-align:right;font-variant-numeric:tabular-nums;color:${dim ? 'inherit' : good ? 'var(--color-success)' : 'var(--color-danger)'}">${variance >= 0 ? '+' : ''}${money(variance)}</td>
        </tr>`;
      };
      let html = '';
      if (d.period_elapsed_pct != null) {
        html += `<p style="font-size:.78rem;color:var(--color-text-muted);margin-bottom:.5rem">
          ${d.period_elapsed_pct}% of the budget period has elapsed — the prorated rows show where you \u201Cshould\u201D be by now (straight-line).</p>`;
      }
      html += `
        <table><thead><tr><th></th><th style="text-align:right">Budget</th><th style="text-align:right">Actual</th><th style="text-align:right">Variance</th></tr></thead>
          <tbody>
            ${row('Income', d.budget_income, d.actual_income, d.income_variance, true)}
            ${d.prorated_budget_income != null ? row('&nbsp;&nbsp;vs prorated', d.prorated_budget_income, d.actual_income, d.actual_income - d.prorated_budget_income, true, true) : ''}
            ${row('Expense', d.budget_expense, d.actual_expense, d.expense_variance, true)}
            ${d.prorated_budget_expense != null ? row('&nbsp;&nbsp;vs prorated', d.prorated_budget_expense, d.actual_expense, d.actual_expense - d.prorated_budget_expense, false, true) : ''}
          </tbody></table>`;
      const cats = d.categories ?? [];
      if (cats.length) {
        html += `
          <div style="font-weight:700;font-size:.8rem;margin:1rem 0 .3rem">By account</div>
          <table><thead><tr><th>Account</th><th></th><th style="text-align:right">Budget</th><th style="text-align:right">Actual</th><th style="text-align:right">Variance</th></tr></thead>
            <tbody>${cats.map(c => `
              <tr${c.label === '(unlinked)' ? ' style="color:var(--color-text-muted)"' : ''}>
                <td>${c.account_code ? esc(c.account_code) + ' ' : ''}${esc(c.label)}</td>
                <td style="font-size:.7rem;color:var(--color-text-muted)">${esc(c.kind)}</td>
                <td style="text-align:right;font-variant-numeric:tabular-nums">${money(c.budget_minor)}</td>
                <td style="text-align:right;font-variant-numeric:tabular-nums">${c.label === '(unlinked)' ? '—' : money(c.actual_minor)}</td>
                <td style="text-align:right;font-variant-numeric:tabular-nums">${c.label === '(unlinked)' ? '—' : (c.variance_minor >= 0 ? '+' : '') + money(c.variance_minor)}</td>
              </tr>`).join('')}</tbody></table>`;
      }
      if ((d.excluded_currencies ?? []).length) {
        html += `<p style="font-size:.78rem;color:var(--color-warning,#b45309);margin-top:.6rem">
          ⚠ Ledger activity in ${d.excluded_currencies.map(esc).join(', ')} was excluded — this view never mixes currencies.</p>`;
      }
      dialog.querySelector('#va-out').innerHTML = html;
    });
    run.addEventListener('click', doRun);
    doRun(); // dates are prefilled: show the comparison immediately
  }

  /** Leniently computes a line's amount in minor units from its live inputs. */
  lineAmountMinor(row, currency) {
    const qty = Number.parseInt(row.querySelector('.l-qty').value, 10) || 0;
    // parseMoney returns null on an unparseable/partial value — treat as 0 for
    // the live preview (the commit path validates before persisting).
    const unit = parseMoney(row.querySelector('.l-unit').value.trim() || '0', currency) ?? 0;
    return qty * unit;
  }

  wireDetail(s) {
    const id = s.id;
    const detail = this.querySelector('#b-detail');

    // Live totals as the planner types (the what-if feel).
    detail.querySelectorAll('.l-qty, .l-unit').forEach(inp =>
      inp.addEventListener('input', () => this.renderTotals(s.currency)));

    // Commit a line edit on change (blur/enter).
    detail.querySelectorAll('.b-line').forEach(row => {
      const commit = async () => {
        const unitMinor = parseMoney(row.querySelector('.l-unit').value.trim() || '0', s.currency);
        if (unitMinor == null) { toast('Enter a valid unit amount','error'); return; }
        try {
          await api('PATCH', `/budget-lines/${row.dataset.id}`, {
            label: row.querySelector('.l-label').value.trim() || 'Untitled',
            category: row.querySelector('.l-cat').value.trim() || null,
            quantity: Number.parseInt(row.querySelector('.l-qty').value, 10) || 0,
            unit_amount_minor: unitMinor,
            account_id: row.querySelector('.l-acct').value, // '' unlinks
          });
          flashSaved(row); // edits commit silently otherwise — show they stuck
        } catch (err) { toast(err.error ?? 'Save failed','error'); }
      };
      row.querySelectorAll('input, select').forEach(inp => inp.addEventListener('change', commit));
      // Reorder: rewrite sort_order for the whole section from DOM order
      // with this row shifted one slot.
      const move = async dir => {
        const tbody = row.closest('tbody');
        const rows = [...tbody.querySelectorAll('.b-line')];
        const i = rows.indexOf(row);
        const j = i + dir;
        if (j < 0 || j >= rows.length) return;
        [rows[i], rows[j]] = [rows[j], rows[i]];
        try {
          await api('PUT', `/budgets/${id}/lines/order`, { ids: rows.map(r => r.dataset.id) });
          this.select(id);
        } catch (err) { toast(err.error ?? 'Reorder failed','error'); }
      };
      row.querySelector('.l-up').addEventListener('click', () => move(-1));
      row.querySelector('.l-down').addEventListener('click', () => move(1));
      row.querySelector('.l-del').addEventListener('click', async () => {
        const label = row.querySelector('.l-label').value.trim() || 'this line';
        if (!await confirm(`Delete “${label}” from the budget?`, 'Delete line')) return;
        // Capture enough to resurrect it — delete is the only destructive
        // click in the app that used to have neither confirm nor undo.
        const snapshot = {
          kind: row.closest('tbody')?.dataset.kind ?? 'expense',
          label,
          category: row.querySelector('.l-cat').value.trim() || null,
          account_id: row.querySelector('.l-acct').value || null,
          quantity: Number.parseInt(row.querySelector('.l-qty').value, 10) || 0,
          unit_amount_minor: parseMoney(row.querySelector('.l-unit').value.trim() || '0', row.dataset.currency) ?? 0,
        };
        try {
          await api('DELETE', `/budget-lines/${row.dataset.id}`);
          toast('Line deleted', 'success', {
            action: 'Undo',
            onAction: async () => {
              try { await api('POST', `/budgets/${id}/lines`, snapshot); this.select(id); this.loadList(); }
              catch { toast('Undo failed', 'error'); }
            },
          });
          this.select(id); this.loadList();
        }
        catch { toast('Delete failed','error'); }
      });
    });

    detail.querySelectorAll('.add-line').forEach(btn => btn.addEventListener('click', async () => {
      try {
        await api('POST', `/budgets/${id}/lines`, { kind: btn.dataset.kind, label: 'New line', quantity: 1, unit_amount_minor: 0 });
        this.select(id); this.loadList();
      } catch { toast('Failed to add line','error'); }
    }));

    // Scenario metadata commits on change.
    const saveMeta = async () => {
      const body = {
        name: detail.querySelector('#s-name').value.trim() || 'Untitled',
        period_label: detail.querySelector('#s-period').value.trim() || null,
        status: detail.querySelector('#s-status').value,
      };
      const starts = detail.querySelector('#s-starts').value;
      const ends = detail.querySelector('#s-ends').value;
      if (starts) body.starts_on = starts;
      if (ends) body.ends_on = ends;
      try {
        await api('PATCH', `/budgets/${id}`, body);
        flashSaved(detail.querySelector('#s-name'));
        this.loadList();
      } catch (err) { toast(err.error ?? 'Save failed','error'); }
    };
    ['#s-name','#s-period','#s-status','#s-starts','#s-ends'].forEach(sel => detail.querySelector(sel).addEventListener('change', saveMeta));

    detail.querySelector('#vsactual-btn').addEventListener('click', () => this.openVsActual(s));
    detail.querySelector('#empty-seed-btn')?.addEventListener('click', () => detail.querySelector('#seed-btn')?.click());

    detail.querySelector('#seed-btn').addEventListener('click', async () => {
      if (!await confirm('Seeding replaces every line in the “Dues” category (including hand-edited ones) with a fresh projection from the roster. Continue?', 'Seed dues income')) return;
      try {
        const res = await api('POST', `/budgets/${id}/seed-dues`);
        toast(res.lines_added ? `Added ${res.lines_added} dues line(s) from the roster` : 'No dues schedules to seed from', res.lines_added ? 'success' : 'info');
        // Every skipped tier is a hole in the projection — name each one.
        for (const sk of res.skipped ?? []) {
          const why = sk.reason === 'no_fx_rate'
            ? 'no FX rate into this scenario\u2019s currency'
            : 'no active dues schedule';
          toast(`Not projected: ${sk.members} member(s) in tier \u201C${sk.tier}\u201D — ${why}`, 'info');
        }
        this.select(id); this.loadList();
      } catch (err) { toast(err.error ?? 'Seed failed','error'); }
    });

    detail.querySelector('#clone-btn').addEventListener('click', async () => {
      try {
        const clone = await api('POST', `/budgets/${id}/clone`, { name: `${s.name} (copy)` });
        toast('Scenario cloned','success');
        await this.loadList();
        this.select(clone.id);
      } catch { toast('Clone failed','error'); }
    });

    detail.querySelector('#del-btn')?.addEventListener('click', () => {
      confirmDelete({
        noun: 'budget scenario',
        name: s.name,
        message: `This will <strong>permanently delete</strong> the scenario and all its lines. This <strong>cannot be undone</strong>. (No one is notified — budgets are internal planning documents.)`,
        onConfirm: async (confirmVal) => {
          try {
            await api('DELETE', `/budgets/${id}?confirm=${encodeURIComponent(confirmVal)}`);
            toast('Scenario deleted','success');
            this._selectedId = null;
            this.querySelector('#b-detail').innerHTML = '<div class="empty-state" style="padding:2rem"><p>Select a scenario.</p></div>';
            this.loadList();
          } catch (err) { toast(err.error ?? 'Delete failed','error'); throw err; }
        },
      });
    });
  }

  openCreate() {
    const { dialog, close } = openModal({
      title: 'New budget scenario',
      maxWidth: '420px',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="c-name">Name *</label><input id="c-name" placeholder="e.g. FY2027 Base Case"></div>
          <div class="form-group"><label for="c-period">Period label</label><input id="c-period" placeholder="FY2027"></div>
          <div style="display:flex;gap:.6rem">
            <div class="form-group" style="flex:1"><label for="c-starts">Starts</label><input id="c-starts" type="date"></div>
            <div class="form-group" style="flex:1"><label for="c-ends">Ends</label><input id="c-ends" type="date"></div>
          </div>
          <p style="font-size:.75rem;color:var(--color-text-muted);margin-top:-.4rem">Dates enable fiscal-year defaults and time-elapsed proration in “Vs actual”.</p>
        </div>
        <div class="modal-footer"><button class="btn-secondary" id="cancel-btn">Cancel</button><button class="btn-primary" id="save-btn">Create</button></div>`,
    });
    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    dialog.querySelector('#save-btn').addEventListener('click', async () => {
      const name = dialog.querySelector('#c-name').value.trim();
      if (!name) { toast('Name is required','error'); return; }
      try {
        const body = { name, period_label: dialog.querySelector('#c-period').value.trim() || null };
        const cs = dialog.querySelector('#c-starts').value, ce = dialog.querySelector('#c-ends').value;
        if (cs) body.starts_on = cs;
        if (ce) body.ends_on = ce;
        const s = await api('POST', '/budgets', body);
        close(); await this.loadList(); this.select(s.id);
      } catch (err) { toast(err.error ?? 'Failed','error'); }
    });
  }

  async openCompare() {
    const ids = [...this._compare];
    if (ids.length < 2) { toast('Tick at least two scenarios to compare','info'); return; }
    let data;
    try { data = await api('GET', `/budgets/compare?ids=${ids.join(',')}`); }
    catch { toast('Compare failed','error'); return; }
    const currencies = [...new Set(data.map(s => s.totals.currency))];
    const mixedWarning = currencies.length > 1
      ? `<p style="font-size:.78rem;color:var(--color-warning,#b45309);margin-bottom:.8rem">⚠ These scenarios use different currencies (${currencies.map(esc).join(', ')}) — the bars are not directly comparable.</p>`
      : '';
    const max = Math.max(1, ...data.flatMap(s => [s.totals.income_minor, s.totals.expense_minor]));
    const { dialog, close } = openModal({
      title: 'Compare scenarios',
      maxWidth: '620px',
      body: `
        <div class="modal-body">
          ${mixedWarning}
          ${data.map(s => `
            <div style="margin-bottom:1rem">
              <div style="display:flex;justify-content:space-between;font-size:.9rem;font-weight:600"><span>${esc(s.name)}</span><span>${netLabel(s.totals.net_minor, s.totals.currency)}</span></div>
              <div style="display:flex;align-items:center;gap:.5rem;margin-top:.25rem"><span style="width:64px;font-size:.72rem;color:var(--color-success)">Income</span>
                <div style="flex:1;height:11px;background:var(--color-border);border-radius:5px;overflow:hidden"><div style="height:100%;width:${s.totals.income_minor/max*100}%;background:var(--color-success)"></div></div>
                <span style="width:90px;text-align:right;font-size:.78rem;font-variant-numeric:tabular-nums">${formatMoney(s.totals.income_minor, s.totals.currency)}</span></div>
              <div style="display:flex;align-items:center;gap:.5rem;margin-top:.2rem"><span style="width:64px;font-size:.72rem;color:var(--color-danger)">Expense</span>
                <div style="flex:1;height:11px;background:var(--color-border);border-radius:5px;overflow:hidden"><div style="height:100%;width:${s.totals.expense_minor/max*100}%;background:var(--color-danger)"></div></div>
                <span style="width:90px;text-align:right;font-size:.78rem;font-variant-numeric:tabular-nums">${formatMoney(s.totals.expense_minor, s.totals.currency)}</span></div>
            </div>`).join('')}
          <div id="cmp-delta"></div>
        </div>
        <div class="modal-footer"><button class="btn-primary" id="close-btn">Close</button></div>`,
    });
    dialog.querySelector('#close-btn').addEventListener('click', close);

    // Two same-currency scenarios get the question actually being asked —
    // "what changed?" — as a per-category delta table.
    if (ids.length === 2 && currencies.length === 1) {
      try {
        const [a, b] = await Promise.all(ids.map(x => api('GET', `/budgets/${x}`)));
        if (!dialog.isConnected) return;
        const cur = currencies[0];
        const sums = s => {
          const m = new Map();
          (s.lines ?? []).forEach(l => {
            const key = `${l.kind}:${l.category?.trim() || '(uncategorized)'}`;
            const sign = l.kind === 'income' ? 1 : -1;
            m.set(key, (m.get(key) ?? 0) + sign * l.amount_minor);
          });
          return m;
        };
        const ma = sums(a), mb = sums(b);
        const keys = [...new Set([...ma.keys(), ...mb.keys()])].sort();
        const rows = keys.map(k => {
          const [kind, cat] = [k.slice(0, k.indexOf(':')), k.slice(k.indexOf(':') + 1)];
          const va = ma.get(k) ?? 0, vb = mb.get(k) ?? 0, d = vb - va;
          const fmt = v => formatMoney(Math.abs(v), cur);
          return `<tr${d === 0 ? ' style="color:var(--color-text-muted)"' : ''}>
            <td>${esc(cat)}</td><td style="font-size:.7rem;color:var(--color-text-muted)">${esc(kind)}</td>
            <td style="text-align:right;font-variant-numeric:tabular-nums">${fmt(va)}</td>
            <td style="text-align:right;font-variant-numeric:tabular-nums">${fmt(vb)}</td>
            <td style="text-align:right;font-variant-numeric:tabular-nums;color:${d > 0 ? 'var(--color-success)' : d < 0 ? 'var(--color-danger)' : 'inherit'}">${d === 0 ? '—' : (d > 0 ? '+' : '−') + fmt(d)}</td>
          </tr>`;
        }).join('');
        if (rows) {
          dialog.querySelector('#cmp-delta').innerHTML = `
            <div style="font-weight:700;font-size:.8rem;margin:.5rem 0 .3rem">What changed (net effect by category)</div>
            <table><thead><tr><th>Category</th><th></th>
              <th style="text-align:right">${esc(a.name)}</th><th style="text-align:right">${esc(b.name)}</th>
              <th style="text-align:right">Δ</th></tr></thead><tbody>${rows}</tbody></table>`;
        }
      } catch { /* the bars above still answer the broad question */ }
    }
  }
}
customElements.define('page-budget', PageBudget);
