import { api } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';
import { esc, openModal, guardButton, formatMoney, parseMoney, moneyExponent } from '../utils.js';

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
    this.render();
    this.loadList();
  }

  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Budget planning</h1>
        <div style="display:flex;gap:.5rem">
          <button class="btn-secondary" id="compare-btn">Compare selected</button>
          <button class="btn-primary" id="new-btn">+ New scenario</button>
        </div>
      </div>
      <p style="font-size:.85rem;color:var(--color-text-muted);margin:-.5rem 0 1rem">
        Draft budget scenarios, model income and expenses as quantity × unit amount, and tweak the numbers to see the
        surplus or deficit update. Clone a scenario to explore a variation, or seed dues income straight from your roster.
      </p>
      <div style="display:grid;grid-template-columns:minmax(260px,340px) 1fr;gap:1.25rem;align-items:start" id="b-grid">
        <div id="b-list"><div style="text-align:center;padding:1rem"><span class="spinner"></span></div></div>
        <div id="b-detail"><div class="empty-state" style="padding:2rem"><p>Select a scenario, or create one to begin.</p></div></div>
      </div>
    `;
    this.querySelector('#new-btn').addEventListener('click', () => this.openCreate());
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
    const lineRows = kind => (s.lines ?? []).filter(l => l.kind === kind).map(l => `
      <tr class="b-line" data-id="${esc(l.id)}" data-currency="${esc(cur)}">
        <td><input class="l-label" value="${esc(l.label)}" style="width:100%;padding:.2rem .35rem;font-size:.85rem"></td>
        <td><input class="l-cat" value="${esc(l.category ?? '')}" placeholder="—" style="width:90px;padding:.2rem .35rem;font-size:.8rem"></td>
        <td><input class="l-qty" type="number" min="0" value="${l.quantity}" style="width:64px;padding:.2rem .35rem;font-size:.85rem;text-align:right"></td>
        <td><input class="l-unit" value="${plainAmount(l.unit_amount_minor, cur)}" style="width:88px;padding:.2rem .35rem;font-size:.85rem;text-align:right"></td>
        <td class="l-amount" style="text-align:right;font-variant-numeric:tabular-nums;font-weight:600;white-space:nowrap">${formatMoney(l.amount_minor, cur)}</td>
        <td><button class="btn-ghost l-del" aria-label="Delete line" title="Delete line" style="color:var(--color-danger);font-size:.75rem">✕</button></td>
      </tr>`).join('');

    const section = (kind, title, color) => `
      <div style="margin-bottom:1rem">
        <div style="font-weight:700;font-size:.85rem;color:${color};margin-bottom:.35rem">${title}</div>
        <table style="width:100%;border-collapse:collapse">
          <thead><tr style="font-size:.68rem;color:var(--color-text-muted);text-transform:uppercase;letter-spacing:.05em">
            <th style="text-align:left">Item</th><th style="text-align:left">Category</th><th style="text-align:right">Qty</th><th style="text-align:right">Unit</th><th style="text-align:right">Amount</th><th></th>
          </tr></thead>
          <tbody data-kind="${kind}">${lineRows(kind)}</tbody>
        </table>
        <button class="btn-ghost add-line" data-kind="${kind}" style="font-size:.78rem;margin-top:.25rem">+ Add ${kind} line</button>
      </div>`;

    this.querySelector('#b-detail').innerHTML = `
      <div class="card" style="padding:1.25rem">
        <div style="display:flex;justify-content:space-between;gap:1rem;align-items:start;flex-wrap:wrap;margin-bottom:1rem">
          <div style="flex:1;min-width:200px">
            <input id="s-name" value="${esc(s.name)}" style="font-size:1.1rem;font-weight:700;width:100%;padding:.3rem .4rem;border:1px solid transparent;background:transparent" title="Scenario name">
            <div style="display:flex;gap:.5rem;margin-top:.35rem;align-items:center">
              <input id="s-period" value="${esc(s.period_label ?? '')}" placeholder="Period (e.g. FY2027)" style="width:150px;padding:.2rem .4rem;font-size:.8rem">
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

        ${section('income', 'Income', 'var(--color-success)')}
        ${section('expense', 'Expenses', 'var(--color-danger)')}

        <div id="b-totals" style="border-top:2px solid var(--color-border);padding-top:.75rem"></div>
      </div>`;

    this.wireDetail(s);
    this.renderTotals(s.currency);
  }

  /** Recomputes and renders the income/expense/net footer from current inputs. */
  renderTotals(currency) {
    let income = 0, expense = 0;
    this.querySelectorAll('.b-line').forEach(row => {
      const amt = this.lineAmountMinor(row, currency);
      row.querySelector('.l-amount').textContent = formatMoney(amt, currency);
      if (row.closest('tbody').dataset.kind === 'income') income += amt; else expense += amt;
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

  /** Compares this scenario's budgeted totals to posted GL actuals over a period. */
  openVsActual(s) {
    const cur = s.currency;
    const { dialog, close } = openModal({
      title: `Budget vs. actual — ${s.name}`,
      maxWidth: '520px',
      body: `
        <div class="modal-body">
          <p style="color:var(--color-text-muted);font-size:.85rem">Compare budgeted income and expense against what's actually posted to the ledger over a date range.</p>
          <div style="display:flex;gap:.6rem;align-items:end;flex-wrap:wrap">
            <div class="form-group" style="margin-bottom:0"><label for="va-from">From</label><input id="va-from" type="date"></div>
            <div class="form-group" style="margin-bottom:0"><label for="va-to">To</label><input id="va-to" type="date"></div>
            <button class="btn-primary" id="va-run">Compare</button>
          </div>
          <div id="va-out" style="margin-top:1rem"></div>
        </div>
        <div class="modal-footer"><button class="btn-secondary" id="va-close">Close</button></div>`,
    });
    dialog.querySelector('#va-close').addEventListener('click', close);
    const run = dialog.querySelector('#va-run');
    run.addEventListener('click', guardButton(run, async () => {
      const from = dialog.querySelector('#va-from').value, to = dialog.querySelector('#va-to').value;
      if (!from || !to) { toast('Pick both dates', 'error'); return; }
      let d;
      try { d = await api('GET', `/budgets/${s.id}/vs-actual?from=${from}&to=${to}`); }
      catch (err) { toast(err.error ?? 'Failed', 'error'); return; }
      const row = (label, budget, actual, variance, favGood) => {
        const good = favGood ? variance >= 0 : variance <= 0;
        return `<tr>
          <td>${label}</td>
          <td style="text-align:right;font-variant-numeric:tabular-nums">${formatMoney(budget, cur)}</td>
          <td style="text-align:right;font-variant-numeric:tabular-nums">${formatMoney(actual, cur)}</td>
          <td style="text-align:right;font-variant-numeric:tabular-nums;color:${good ? 'var(--color-success)' : 'var(--color-danger)'}">${variance >= 0 ? '+' : ''}${formatMoney(variance, cur)}</td>
        </tr>`;
      };
      dialog.querySelector('#va-out').innerHTML = `
        <table><thead><tr><th></th><th style="text-align:right">Budget</th><th style="text-align:right">Actual</th><th style="text-align:right">Variance</th></tr></thead>
          <tbody>
            ${row('Income', d.budget_income, d.actual_income, d.income_variance, true)}
            ${row('Expense', d.budget_expense, d.actual_expense, d.expense_variance, false)}
          </tbody></table>`;
    }));
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
          });
        } catch (err) { toast(err.error ?? 'Save failed','error'); }
      };
      row.querySelectorAll('input').forEach(inp => inp.addEventListener('change', commit));
      row.querySelector('.l-del').addEventListener('click', async () => {
        try { await api('DELETE', `/budget-lines/${row.dataset.id}`); this.select(id); this.loadList(); }
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
      try {
        await api('PATCH', `/budgets/${id}`, {
          name: detail.querySelector('#s-name').value.trim() || 'Untitled',
          period_label: detail.querySelector('#s-period').value.trim() || null,
          status: detail.querySelector('#s-status').value,
        });
        this.loadList();
      } catch (err) { toast(err.error ?? 'Save failed','error'); }
    };
    ['#s-name','#s-period','#s-status'].forEach(sel => detail.querySelector(sel).addEventListener('change', saveMeta));

    detail.querySelector('#vsactual-btn').addEventListener('click', () => this.openVsActual(s));

    detail.querySelector('#seed-btn').addEventListener('click', async () => {
      try {
        const res = await api('POST', `/budgets/${id}/seed-dues`);
        toast(res.lines_added ? `Added ${res.lines_added} dues line(s) from the roster` : 'No dues schedules to seed from', res.lines_added ? 'success' : 'info');
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

    detail.querySelector('#del-btn').addEventListener('click', async () => {
      if (!await confirm(`Delete scenario “${s.name}”? This cannot be undone.`)) return;
      try {
        await api('DELETE', `/budgets/${id}`);
        toast('Scenario deleted','success');
        this._selectedId = null;
        this.querySelector('#b-detail').innerHTML = '<div class="empty-state" style="padding:2rem"><p>Select a scenario.</p></div>';
        this.loadList();
      } catch { toast('Delete failed','error'); }
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
        </div>
        <div class="modal-footer"><button class="btn-secondary" id="cancel-btn">Cancel</button><button class="btn-primary" id="save-btn">Create</button></div>`,
    });
    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    dialog.querySelector('#save-btn').addEventListener('click', async () => {
      const name = dialog.querySelector('#c-name').value.trim();
      if (!name) { toast('Name is required','error'); return; }
      try {
        const s = await api('POST', '/budgets', { name, period_label: dialog.querySelector('#c-period').value.trim() || null });
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
    const cur = data[0]?.totals.currency ?? 'USD';
    const max = Math.max(1, ...data.flatMap(s => [s.totals.income_minor, s.totals.expense_minor]));
    const { dialog, close } = openModal({
      title: 'Compare scenarios',
      maxWidth: '620px',
      body: `
        <div class="modal-body">
          ${data.map(s => `
            <div style="margin-bottom:1rem">
              <div style="display:flex;justify-content:space-between;font-size:.9rem;font-weight:600"><span>${esc(s.name)}</span><span>${netLabel(s.totals.net_minor, cur)}</span></div>
              <div style="display:flex;align-items:center;gap:.5rem;margin-top:.25rem"><span style="width:64px;font-size:.72rem;color:var(--color-success)">Income</span>
                <div style="flex:1;height:11px;background:var(--color-border);border-radius:5px;overflow:hidden"><div style="height:100%;width:${s.totals.income_minor/max*100}%;background:var(--color-success)"></div></div>
                <span style="width:90px;text-align:right;font-size:.78rem;font-variant-numeric:tabular-nums">${formatMoney(s.totals.income_minor, cur)}</span></div>
              <div style="display:flex;align-items:center;gap:.5rem;margin-top:.2rem"><span style="width:64px;font-size:.72rem;color:var(--color-danger)">Expense</span>
                <div style="flex:1;height:11px;background:var(--color-border);border-radius:5px;overflow:hidden"><div style="height:100%;width:${s.totals.expense_minor/max*100}%;background:var(--color-danger)"></div></div>
                <span style="width:90px;text-align:right;font-size:.78rem;font-variant-numeric:tabular-nums">${formatMoney(s.totals.expense_minor, cur)}</span></div>
            </div>`).join('')}
        </div>
        <div class="modal-footer"><button class="btn-primary" id="close-btn">Close</button></div>`,
    });
    dialog.querySelector('#close-btn').addEventListener('click', close);
  }
}
customElements.define('page-budget', PageBudget);
