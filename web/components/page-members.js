import { api, apiDownload, apiUpload, canWrite, isAdmin, isSuperadmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete, renderPager, loadFilters, saveFilters } from '../utils.js';

const TIERS = ['standard','associate','honorary','lifetime','other'];
const STATUSES = ['active','inactive','suspended'];

class PageMembers extends HTMLElement {
  constructor() {
    super();
    const f = loadFilters('members', { search: '', status: '' });
    this._members  = [];
    this._search   = f.search;
    this._status   = f.status;
    this._seq      = 0;
    this._offset   = 0;
    this._total    = 0;
    this._selected = new Set();
  }

  _persist() { saveFilters('members', { search: this._search, status: this._status }); }

  connectedCallback() {
    this.render();
    this.load();
  }

  _cols() { return canWrite() ? 7 : 5; }

  /** Renders the static page shell once; load() only touches #tbody. */
  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Members</h1>
        <div style="display:flex;gap:.5rem">
          <button class="btn-secondary" id="export-btn">Export CSV</button>
          ${isAdmin() ? '<button class="btn-secondary" id="import-btn">Import CSV</button>' : ''}
          ${canWrite() ? '<button class="btn-primary" id="add-btn">+ Add member</button>' : ''}
        </div>
      </div>
      <div class="search-bar">
        <input id="search-inp" aria-label="Search members by name or email" placeholder="Search by name or email…" value="${esc(this._search)}">
        <select id="status-sel" aria-label="Filter by status">
          <option value="">All statuses</option>
          ${STATUSES.map(s => `<option value="${s}" ${this._status===s?'selected':''}>${s}</option>`).join('')}
        </select>
      </div>
      <div id="bulk-bar"></div>
      <div class="card" style="overflow-x:auto">
        <table>
          <thead><tr>${canWrite()?'<th style="width:1.5rem"><input type="checkbox" id="sel-all" aria-label="Select all" style="width:auto"></th>':''}<th>Name</th><th>Email</th><th>Tier</th><th>Status</th><th>Dues</th>${canWrite()?'<th></th>':''}</tr></thead>
          <tbody id="tbody"></tbody>
        </table>
      </div>
      <div id="pager"></div>
    `;

    this.querySelector('#search-inp')?.addEventListener('input', e => {
      this._search = e.target.value;
      clearTimeout(this._searchTimer);
      this._searchTimer = setTimeout(() => { this._offset = 0; this._persist(); this.load(); }, 350);
    });
    this.querySelector('#status-sel')?.addEventListener('change', e => {
      this._status = e.target.value;
      this._offset = 0;
      this._persist();
      this.load();
    });
    this.querySelector('#add-btn')?.addEventListener('click', () => this.openModal(null));
    this.querySelector('#export-btn')?.addEventListener('click', async () => {
      try { await apiDownload('/export/members.csv', 'members.csv'); }
      catch (err) { toast(err.error ?? 'Export failed','error'); }
    });
    this.querySelector('#import-btn')?.addEventListener('click', () => this.openImportModal());
  }

  /** CSV import: upload → dry-run report → commit. */
  openImportModal() {
    const { dialog, close } = openModal({
      title: 'Import members from CSV',
      maxWidth: '620px',
      body: `
        <div class="modal-body">
          <p style="font-size:.85rem;color:var(--color-text-muted);margin-top:0">
            Columns (case-insensitive): <code>name</code> (required), email, phone,
            address, tier, status, joined_at (YYYY-MM-DD). We'll preview before importing.</p>
          <input type="file" id="imp-file" accept=".csv,text/csv">
          <div id="imp-report" style="margin-top:.8rem"></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="imp-cancel">Cancel</button>
          <button class="btn-secondary" id="imp-dry" disabled>Preview</button>
          <button class="btn-primary" id="imp-commit" disabled>Import</button>
        </div>`,
    });
    dialog.querySelector('#imp-cancel').addEventListener('click', close);
    const fileInput = dialog.querySelector('#imp-file');
    const dryBtn = dialog.querySelector('#imp-dry');
    const commitBtn = dialog.querySelector('#imp-commit');
    const report = dialog.querySelector('#imp-report');
    fileInput.addEventListener('change', () => { dryBtn.disabled = !fileInput.files.length; commitBtn.disabled = true; });

    const run = async commit => {
      const file = fileInput.files[0];
      if (!file) return;
      try {
        const res = await apiUpload('/members/import' + (commit ? '?commit=true' : ''), file);
        report.innerHTML = `
          <div class="card" style="padding:.7rem .9rem">
            <div style="font-size:.9rem"><strong>${res.new}</strong> new · <strong>${res.duplicate}</strong> duplicate · <strong>${res.invalid}</strong> invalid</div>
            ${res.committed ? `<div style="color:var(--color-success,#137333);margin-top:.3rem">✓ Imported ${res.imported}</div>` : ''}
            ${(res.rows || []).filter(r => r.problem || r.duplicate).slice(0, 8).map(r =>
              `<div style="font-size:.78rem;color:var(--color-text-muted)">line ${r.line}: ${esc(r.name || '(no name)')} — ${r.problem ? esc(r.problem) : 'duplicate email'}</div>`).join('')}
          </div>`;
        commitBtn.disabled = res.committed || res.new === 0;
        if (res.committed) { toast(`Imported ${res.imported} members`, 'success'); this.load(); setTimeout(close, 800); }
      } catch (err) { toast(err.error ?? 'Import failed', 'error'); }
    };
    dryBtn.addEventListener('click', () => run(false));
    commitBtn.addEventListener('click', guardButton(commitBtn, () => run(true)));
  }

  async load() {
    const seq = ++this._seq;
    const tbody = this.querySelector('#tbody');
    tbody.innerHTML = `<tr><td colspan="${this._cols()}" style="text-align:center"><span class="spinner"></span></td></tr>`;
    try {
      const params = new URLSearchParams({ limit: '50', offset: String(this._offset) });
      if (this._search) params.set('search', this._search);
      if (this._status) params.set('status', this._status);
      const _mPage = await api('GET', '/members?' + params);
      if (seq !== this._seq) return; // A newer load() superseded this one.
      this._members = _mPage?.data ?? _mPage ?? [];
      this._total = _mPage?.total ?? this._members.length;
      tbody.innerHTML = this._rows()
        || `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>No members found.</p></div></td></tr>`;
      this._wireRows(tbody);
      renderPager(this.querySelector('#pager'), { offset: this._offset, limit: 50, total: this._total, onNavigate: o => { this._offset = o; this.load(); } });
    } catch {
      if (seq !== this._seq) return;
      tbody.innerHTML = `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>Failed to load members.</p></div></td></tr>`;
      toast('Failed to load members', 'error');
    }
  }

  _rows() {
    if (!this._members?.length) return '';
    return this._members.map(m => `
      <tr>
        ${canWrite() ? `<td><input type="checkbox" class="sel-cb" value="${esc(m.id)}" ${this._selected.has(m.id) ? 'checked' : ''} style="width:auto" aria-label="Select ${esc(m.display_name)}"></td>` : ''}
        <td><strong>${esc(m.display_name)}</strong></td>
        <td>${esc(m.email ?? '—')}</td>
        <td>${esc(m.tier)}</td>
        <td><span class="badge badge-${esc(m.status)}">${esc(m.status)}</span></td>
        <td><payment-status-badge status="${esc(m.dues_status || 'none')}"></payment-status-badge></td>
        ${canWrite() ? `<td style="text-align:right;white-space:nowrap">
          <button class="btn-ghost edit-btn" data-id="${esc(m.id)}">Edit</button>
          ${isAdmin() ? `<button class="btn-ghost del-btn" data-id="${esc(m.id)}" data-name="${esc(m.display_name)}" style="color:var(--color-danger)">Del</button>` : ''}
          ${isSuperadmin() ? `<button class="btn-ghost erase-btn" data-id="${esc(m.id)}" data-name="${esc(m.display_name)}" style="color:var(--color-danger)" title="Erase all personal data (GDPR)">Erase</button>` : ''}
        </td>` : ''}
      </tr>`).join('');
  }

  _wireRows(tbody) {
    tbody.querySelectorAll('.sel-cb').forEach(cb => cb.addEventListener('change', () => {
      cb.checked ? this._selected.add(cb.value) : this._selected.delete(cb.value);
      this._renderBulkBar();
    }));
    const selAll = this.querySelector('#sel-all');
    if (selAll) selAll.addEventListener('change', () => {
      this._members.forEach(m => selAll.checked ? this._selected.add(m.id) : this._selected.delete(m.id));
      tbody.querySelectorAll('.sel-cb').forEach(cb => { cb.checked = selAll.checked; });
      this._renderBulkBar();
    });
    this._renderBulkBar();
    tbody.querySelectorAll('.edit-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const m = this._members.find(x => x.id === btn.dataset.id);
        if (m) this.openModal(m);
      });
    });
    tbody.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', () => this.deleteMember(btn.dataset.id, btn.dataset.name));
    });
    tbody.querySelectorAll('.erase-btn').forEach(btn => {
      btn.addEventListener('click', () => this.eraseMember(btn.dataset.id, btn.dataset.name));
    });
  }

  /** Bulk action bar: appears when rows are selected; sets status/tier for all. */
  _renderBulkBar() {
    const bar = this.querySelector('#bulk-bar');
    if (!bar) return;
    const n = this._selected.size;
    if (!n) { bar.innerHTML = ''; return; }
    bar.innerHTML = `
      <div class="card" style="display:flex;gap:.6rem;align-items:center;flex-wrap:wrap;padding:.6rem .9rem;margin-bottom:.6rem">
        <strong style="font-size:.9rem">${n} selected</strong>
        <select id="bulk-status" style="max-width:11rem"><option value="">Set status…</option>
          ${STATUSES.map(x => `<option value="${x}">${x}</option>`).join('')}</select>
        <select id="bulk-tier" style="max-width:11rem"><option value="">Set tier…</option>
          ${TIERS.map(x => `<option value="${x}">${x}</option>`).join('')}</select>
        <button class="btn-ghost" id="bulk-clear" style="margin-left:auto">Clear</button>
      </div>`;
    const apply = async fields => {
      try {
        const res = await api('POST', '/members/batch', { ids: [...this._selected], ...fields });
        toast(`Updated ${res.updated} member${res.updated === 1 ? '' : 's'}`, 'success');
        this._selected.clear();
        this.load();
      } catch (err) { toast(err.error ?? 'Bulk update failed', 'error'); }
    };
    bar.querySelector('#bulk-status').addEventListener('change', e => { if (e.target.value) apply({ status: e.target.value }); });
    bar.querySelector('#bulk-tier').addEventListener('change', e => { if (e.target.value) apply({ tier: e.target.value }); });
    bar.querySelector('#bulk-clear').addEventListener('click', () => { this._selected.clear(); this.load(); });
  }

  // eraseMember fulfils a right-to-be-forgotten request: unlike Delete (which
  // soft-deletes to inactive, keeping the record), this scrubs personal data
  // irreversibly. Superadmin only; the backend refuses if the member is linked
  // to an admin login.
  eraseMember(id, name) {
    confirmDelete({
      noun: `member's personal data — "${name}" (irreversible GDPR erasure, not a soft-delete)`,
      name: name ?? '',
      onConfirm: async (confirmVal) => {
        try {
          await api('POST', `/members/${id}/erase?confirm=${encodeURIComponent(confirmVal)}`);
          toast('Personal data erased', 'success');
          this.load();
        } catch (err) { toast(err.error ?? 'Erase failed', 'error'); throw err; }
      },
    });
  }

  deleteMember(id, name) {
    // Type-to-confirm, matching every other destructive action; the backend
    // requires the echoed name via ?confirm=.
    confirmDelete({
      noun: 'member',
      name: name ?? '',
      onConfirm: async (confirmVal) => {
        try {
          await api('DELETE', `/members/${id}?confirm=${encodeURIComponent(confirmVal)}`);
          toast('Member removed', 'success');
          this.load();
        } catch (err) { toast(err.error ?? 'Delete failed', 'error'); throw err; }
      },
    });
  }

  openModal(member) {
    const isNew = !member;
    const { dialog, close } = openModal({
      title: isNew ? 'Add member' : 'Edit member',
      body: `
        <div class="modal-body">
          <div class="form-row">
            <div class="form-group">
              <label for="f-name">Display name *</label>
              <input id="f-name" value="${esc(member?.display_name ?? '')}">
            </div>
            <div class="form-group">
              <label for="f-tier">Tier</label>
              <select id="f-tier">
                ${TIERS.map(t => `<option value="${t}" ${(member?.tier??'standard')===t?'selected':''}>${t}</option>`).join('')}
              </select>
            </div>
          </div>
          <div class="form-row">
            <div class="form-group">
              <label for="f-email">Email</label>
              <input id="f-email" type="email" value="${esc(member?.email ?? '')}">
            </div>
            <div class="form-group">
              <label for="f-phone">Phone</label>
              <input id="f-phone" value="${esc(member?.phone ?? '')}">
            </div>
          </div>
          <div class="form-group">
            <label for="f-address">Address</label>
            <input id="f-address" value="${esc(member?.address ?? '')}">
          </div>
          <div class="form-row">
            <div class="form-group">
              <label for="f-status">Status</label>
              <select id="f-status">
                ${STATUSES.map(s => `<option value="${s}" ${(member?.status??'active')===s?'selected':''}>${s}</option>`).join('')}
              </select>
            </div>
            <div class="form-group">
              <label for="f-joined">Joined date</label>
              <input id="f-joined" type="date" value="${member?.joined_at ? esc(member.joined_at.slice(0,10)) : new Date().toISOString().slice(0,10)}">
            </div>
          </div>
          <div class="form-group">
            <label for="f-notes">Notes</label>
            <textarea id="f-notes">${esc(member?.notes ?? '')}</textarea>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Save</button>
        </div>
      `,
    });

    dialog.querySelector('#cancel-btn').addEventListener('click', close);

    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const body = {
        display_name: dialog.querySelector('#f-name').value.trim(),
        email:        dialog.querySelector('#f-email').value.trim() || null,
        phone:        dialog.querySelector('#f-phone').value.trim() || null,
        address:      dialog.querySelector('#f-address').value.trim() || null,
        tier:         dialog.querySelector('#f-tier').value,
        status:       dialog.querySelector('#f-status').value,
        joined_at:    dialog.querySelector('#f-joined').value || null,
        notes:        dialog.querySelector('#f-notes').value.trim() || null,
      };
      if (!body.display_name) { toast('Name is required', 'error'); return; }
      try {
        if (isNew) await api('POST', '/members', body);
        else       await api('PATCH', `/members/${member.id}`, body);
        toast(isNew ? 'Member added' : 'Member updated', 'success');
        close();
        this.load();
      } catch (err) { toast(err.error ?? 'Save failed', 'error'); }
    }));
  }
}
customElements.define('page-members', PageMembers);
