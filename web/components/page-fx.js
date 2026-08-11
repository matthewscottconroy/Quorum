import { api, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete } from '../utils.js';

// Currencies & FX rates. Analytics and budget rollups convert every amount into
// the reporting currency using the latest effective rate into it. Reads are
// officer+; the reporting currency and rate table are admin-only to change.
class PageFX extends HTMLElement {
  constructor() { super(); this._reporting = ''; this._rates = []; }

  connectedCallback() {
    this.render();
    this.load();
  }

  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Currencies</h1>
        ${isAdmin() ? '<button class="btn-primary" id="add-btn">+ Add rate</button>' : ''}
      </div>
      <money-nav current="currencies"></money-nav>

      <div class="card" style="padding:1rem;margin-bottom:1rem">
        <label for="reporting-inp" style="display:block;font-weight:600;margin-bottom:.35rem">Reporting currency</label>
        <p style="color:var(--color-text-muted);font-size:.85rem;margin:0 0 .6rem">
          Analytics and budget totals are converted into this currency. Amounts in a currency with no rate into it are left out and flagged.</p>
        <div style="display:flex;gap:.5rem;align-items:center;flex-wrap:wrap">
          <input id="reporting-inp" maxlength="8" style="max-width:120px;text-transform:uppercase"${isAdmin() ? '' : ' disabled'}>
          ${isAdmin() ? '<button class="btn-secondary" id="save-reporting">Save</button>' : ''}
        </div>
      </div>

      <div class="card" style="overflow:hidden">
        <table>
          <thead><tr><th>From</th><th>To</th><th>Rate</th><th>Effective</th>${isAdmin() ? '<th></th>' : ''}</tr></thead>
          <tbody id="tbody"></tbody>
        </table>
      </div>
    `;
    this.querySelector('#add-btn')?.addEventListener('click', () => this.openRateModal());
    this.querySelector('#save-reporting')?.addEventListener('click', e => this.saveReporting(e.target));
  }

  _cols() { return isAdmin() ? 5 : 4; }

  async load() {
    const tbody = this.querySelector('#tbody');
    tbody.innerHTML = `<tr><td colspan="${this._cols()}" style="text-align:center"><span class="spinner"></span></td></tr>`;
    try {
      const [settings, rates] = await Promise.all([
        api('GET', '/fx/settings'),
        api('GET', '/fx/rates'),
      ]);
      this._reporting = settings.reporting_currency ?? '';
      this._rates = rates ?? [];
      const inp = this.querySelector('#reporting-inp');
      if (inp) inp.value = this._reporting;
      tbody.innerHTML = this._rows()
        || `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>No exchange rates yet. ${isAdmin() ? 'Add one to convert other currencies into ' + esc(this._reporting) + '.' : ''}</p></div></td></tr>`;
      this._wireRows(tbody);
    } catch {
      tbody.innerHTML = `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>Failed to load currencies.</p></div></td></tr>`;
      toast('Failed to load currencies', 'error');
    }
  }

  _rows() {
    if (!this._rates.length) return '';
    return this._rates.map(r => `
      <tr>
        <td><strong>${esc(r.from_currency)}</strong></td>
        <td>${esc(r.to_currency)}</td>
        <td style="font-variant-numeric:tabular-nums">${esc(r.rate)}</td>
        <td>${esc(r.effective_at)}</td>
        ${isAdmin() ? `<td style="text-align:right"><button class="btn-ghost btn-sm" data-del="${esc(r.id)}" data-label="${esc(r.from_currency)}→${esc(r.to_currency)} @ ${esc(r.effective_at)}">Delete</button></td>` : ''}
      </tr>`).join('');
  }

  _wireRows(tbody) {
    tbody.querySelectorAll('[data-del]').forEach(btn => {
      btn.addEventListener('click', () => confirmDelete({
        noun: 'exchange rate', name: btn.dataset.label,
        onConfirm: async () => {
          try {
            await api('DELETE', '/fx/rates/' + btn.dataset.del);
            toast('Rate deleted', 'success');
            this.load();
          } catch { toast('Delete failed', 'error'); }
        },
      }));
    });
  }

  saveReporting(btn) {
    guardButton(btn, async () => {
      const code = (this.querySelector('#reporting-inp').value || '').trim().toUpperCase();
      try {
        await api('PUT', '/fx/settings', { reporting_currency: code });
        toast('Reporting currency updated', 'success');
        this.load();
      } catch (e) {
        toast(e?.message || 'Update failed', 'error');
      }
    })();
  }

  openRateModal() {
    const today = new Date().toISOString().slice(0, 10);
    const { dialog, close } = openModal({
      title: 'Add exchange rate',
      body: `
        <p style="color:var(--color-text-muted);font-size:.85rem;margin-top:0">
          On and after the effective date, 1 unit of <em>From</em> is worth <em>Rate</em> units of <em>To</em>.
          To feed the reporting currency (${esc(this._reporting)}), set <em>To</em> = ${esc(this._reporting)}.</p>
        <div class="form-row"><label>From currency</label><input id="f-from" maxlength="8" style="text-transform:uppercase" placeholder="EUR"></div>
        <div class="form-row"><label>To currency</label><input id="f-to" maxlength="8" style="text-transform:uppercase" value="${esc(this._reporting)}"></div>
        <div class="form-row"><label>Rate</label><input id="f-rate" inputmode="decimal" placeholder="1.0850"></div>
        <div class="form-row"><label>Effective date</label><input id="f-date" type="date" value="${today}"></div>
        <div style="display:flex;gap:.5rem;justify-content:flex-end;margin-top:1rem">
          <button class="btn-ghost" id="f-cancel">Cancel</button>
          <button class="btn-primary" id="f-save">Add rate</button>
        </div>
      `,
    });
    dialog.querySelector('#f-cancel').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#f-save');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const payload = {
        from_currency: dialog.querySelector('#f-from').value.trim().toUpperCase(),
        to_currency: dialog.querySelector('#f-to').value.trim().toUpperCase(),
        rate: dialog.querySelector('#f-rate').value.trim(),
        effective_at: dialog.querySelector('#f-date').value,
      };
      try {
        await api('POST', '/fx/rates', payload);
        toast('Rate added', 'success');
        close();
        this.load();
      } catch (err) {
        toast(err?.message || 'Could not add rate', 'error');
      }
    }));
  }
}
customElements.define('page-fx', PageFX);
