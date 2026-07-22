import { api, canWrite, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';
import { esc, openModal, guardButton } from '../utils.js';

const TIERS = ['standard','associate','honorary','lifetime','other'];
const STATUSES = ['active','inactive','suspended'];

class PageMembers extends HTMLElement {
  constructor() {
    super();
    this._members = [];
    this._search  = '';
    this._status  = '';
    this._seq     = 0;
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
        <h1>Members</h1>
        ${canWrite() ? '<button class="btn-primary" id="add-btn">+ Add member</button>' : ''}
      </div>
      <div class="search-bar">
        <input id="search-inp" placeholder="Search by name or email…" value="${esc(this._search)}">
        <select id="status-sel">
          <option value="">All statuses</option>
          ${STATUSES.map(s => `<option value="${s}" ${this._status===s?'selected':''}>${s}</option>`).join('')}
        </select>
      </div>
      <div class="card" style="overflow:hidden">
        <table>
          <thead><tr><th>Name</th><th>Email</th><th>Tier</th><th>Status</th><th>Dues</th>${canWrite()?'<th></th>':''}</tr></thead>
          <tbody id="tbody"></tbody>
        </table>
      </div>
    `;

    this.querySelector('#search-inp')?.addEventListener('input', e => {
      this._search = e.target.value;
      clearTimeout(this._searchTimer);
      this._searchTimer = setTimeout(() => this.load(), 350);
    });
    this.querySelector('#status-sel')?.addEventListener('change', e => {
      this._status = e.target.value;
      this.load();
    });
    this.querySelector('#add-btn')?.addEventListener('click', () => this.openModal(null));
  }

  async load() {
    const seq = ++this._seq;
    const tbody = this.querySelector('#tbody');
    tbody.innerHTML = `<tr><td colspan="${this._cols()}" style="text-align:center"><span class="spinner"></span></td></tr>`;
    try {
      const params = new URLSearchParams();
      if (this._search) params.set('search', this._search);
      if (this._status) params.set('status', this._status);
      const _mPage = await api('GET', '/members?' + params);
      if (seq !== this._seq) return; // A newer load() superseded this one.
      this._members = _mPage?.data ?? _mPage ?? [];
      tbody.innerHTML = this._rows()
        || `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>No members found.</p></div></td></tr>`;
      this._wireRows(tbody);
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
        <td><strong>${esc(m.display_name)}</strong></td>
        <td>${esc(m.email ?? '—')}</td>
        <td>${esc(m.tier)}</td>
        <td><span class="badge badge-${esc(m.status)}">${esc(m.status)}</span></td>
        <td><payment-status-badge status="${esc(m.dues_status || 'none')}"></payment-status-badge></td>
        ${canWrite() ? `<td style="text-align:right">
          <button class="btn-ghost edit-btn" data-id="${esc(m.id)}">Edit</button>
          ${isAdmin() ? `<button class="btn-ghost del-btn" data-id="${esc(m.id)}" data-name="${esc(m.display_name)}" style="color:var(--color-danger)">Del</button>` : ''}
        </td>` : ''}
      </tr>`).join('');
  }

  _wireRows(tbody) {
    tbody.querySelectorAll('.edit-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const m = this._members.find(x => x.id === btn.dataset.id);
        if (m) this.openModal(m);
      });
    });
    tbody.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', () => this.deleteMember(btn.dataset.id, btn.dataset.name));
    });
  }

  async deleteMember(id, name) {
    if (!await confirm(`Remove "${name}" from the member list?`, 'Remove Member')) return;
    try {
      await api('DELETE', `/members/${id}`);
      toast('Member removed', 'success');
      this.load();
    } catch { toast('Delete failed', 'error'); }
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
