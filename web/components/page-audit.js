import { api } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDateTime } from '../utils.js';

const PAGE_SIZE = 50;

// Admin-only audit-log viewer. The log is append-only: this page reads and
// filters it, and offers no way to edit or delete an entry.
class PageAudit extends HTMLElement {
  constructor() { super(); this._offset = 0; this._total = 0; this._seq = 0; }

  connectedCallback() {
    this.render();
    this.load();
  }

  render() {
    this.innerHTML = `
      <div class="page-header"><h1>Audit log</h1></div>
      <div class="search-bar" style="flex-wrap:wrap;gap:.5rem">
        <input id="f-action" placeholder="Action contains… (e.g. DELETE, auth.)" style="max-width:260px">
        <input id="f-entity" placeholder="Resource (e.g. members)" style="max-width:170px">
        <label style="text-transform:none;font-weight:400;letter-spacing:normal;margin:0;display:flex;align-items:center;gap:.35rem;font-size:.82rem">
          From <input id="f-since" type="date" style="width:auto"></label>
        <label style="text-transform:none;font-weight:400;letter-spacing:normal;margin:0;display:flex;align-items:center;gap:.35rem;font-size:.82rem">
          To <input id="f-until" type="date" style="width:auto"></label>
        <button class="btn-secondary" id="clear-btn">Clear</button>
      </div>
      <div class="card" style="overflow:hidden">
        <table>
          <thead><tr><th>When</th><th>Who</th><th>Action</th><th>Resource</th><th>Record</th></tr></thead>
          <tbody id="tbody"></tbody>
        </table>
      </div>
      <div id="pager" style="display:flex;justify-content:space-between;align-items:center;margin-top:.75rem;font-size:.85rem"></div>
    `;
    const debounce = fn => { clearTimeout(this._t); this._t = setTimeout(fn, 350); };
    this.querySelector('#f-action').addEventListener('input', () => debounce(() => { this._offset = 0; this.load(); }));
    this.querySelector('#f-entity').addEventListener('input', () => debounce(() => { this._offset = 0; this.load(); }));
    this.querySelector('#f-since').addEventListener('change', () => { this._offset = 0; this.load(); });
    this.querySelector('#f-until').addEventListener('change', () => { this._offset = 0; this.load(); });
    this.querySelector('#clear-btn').addEventListener('click', () => {
      ['#f-action', '#f-entity', '#f-since', '#f-until'].forEach(s => { this.querySelector(s).value = ''; });
      this._offset = 0;
      this.load();
    });
  }

  async load() {
    const seq = ++this._seq;
    const tbody = this.querySelector('#tbody');
    tbody.innerHTML = `<tr><td colspan="5" style="text-align:center"><span class="spinner"></span></td></tr>`;
    const p = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(this._offset) });
    const action = this.querySelector('#f-action').value.trim();
    const entity = this.querySelector('#f-entity').value.trim();
    const since = this.querySelector('#f-since').value;
    const until = this.querySelector('#f-until').value;
    if (action) p.set('action', action);
    if (entity) p.set('entity_type', entity);
    if (since) p.set('since', since);
    if (until) p.set('until', until);

    try {
      const page = await api('GET', '/audit?' + p);
      if (seq !== this._seq) return; // a newer load() superseded this one
      this._total = page?.total ?? 0;
      const rows = page?.data ?? [];
      tbody.innerHTML = rows.length
        ? rows.map(e => `
            <tr>
              <td style="white-space:nowrap">${esc(fmtDateTime(e.created_at))}</td>
              <td>${esc(e.user_email ?? '—')}</td>
              <td style="font-family:monospace;font-size:.82rem">${esc(e.action)}</td>
              <td>${esc(e.entity_type ?? '—')}</td>
              <td style="font-family:monospace;font-size:.75rem;color:var(--color-text-muted)">${esc(e.entity_id ?? '—')}</td>
            </tr>`).join('')
        : `<tr><td colspan="5"><div class="empty-state"><p>No matching audit entries.</p></div></td></tr>`;
      this.renderPager();
    } catch {
      if (seq !== this._seq) return;
      tbody.innerHTML = `<tr><td colspan="5"><div class="empty-state"><p>Failed to load the audit log.</p></div></td></tr>`;
      toast('Failed to load the audit log', 'error');
    }
  }

  renderPager() {
    const from = this._total === 0 ? 0 : this._offset + 1;
    const to = Math.min(this._offset + PAGE_SIZE, this._total);
    this.querySelector('#pager').innerHTML = `
      <span style="color:var(--color-text-muted)">${from}–${to} of ${this._total}</span>
      <span style="display:flex;gap:.5rem">
        <button class="btn-secondary" id="prev-btn" ${this._offset === 0 ? 'disabled' : ''}>Previous</button>
        <button class="btn-secondary" id="next-btn" ${to >= this._total ? 'disabled' : ''}>Next</button>
      </span>`;
    this.querySelector('#prev-btn').addEventListener('click', () => {
      this._offset = Math.max(0, this._offset - PAGE_SIZE);
      this.load();
    });
    this.querySelector('#next-btn').addEventListener('click', () => {
      this._offset += PAGE_SIZE;
      this.load();
    });
  }
}
customElements.define('page-audit', PageAudit);
