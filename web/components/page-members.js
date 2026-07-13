import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';

const TIERS = ['standard','associate','honorary','lifetime','other'];
const STATUSES = ['active','inactive','suspended'];

function esc(s) { return String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }
function fmtDate(iso) { return iso ? new Date(iso).toLocaleDateString() : '—'; }

class PageMembers extends HTMLElement {
  constructor() {
    super();
    this._members = [];
    this._search  = '';
    this._status  = '';
  }

  async connectedCallback() {
    await this.load();
  }

  async load() {
    this.renderList('<tr><td colspan="5"><span class="spinner"></span></td></tr>');
    try {
      const params = new URLSearchParams();
      if (this._search) params.set('search', this._search);
      if (this._status) params.set('status', this._status);
      const _mPage = await api('GET', '/members?' + params);
      this._members = _mPage?.data ?? _mPage ?? [];
      this.renderList();
    } catch {
      toast('Failed to load members', 'error');
    }
  }

  renderList(loadingRows) {
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
          <tbody id="tbody">
            ${loadingRows ?? this._rows()}
          </tbody>
        </table>
        ${this._members.length === 0 && !loadingRows ? '<div class="empty-state"><p>No members found.</p></div>' : ''}
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
    this.querySelectorAll('.edit-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const m = this._members.find(x => x.id === btn.dataset.id);
        if (m) this.openModal(m);
      });
    });
    this.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', () => this.deleteMember(btn.dataset.id, btn.dataset.name));
    });
  }

  _rows() {
    if (!this._members?.length) return '';
    return this._members.map(m => `
      <tr>
        <td><strong>${esc(m.display_name)}</strong></td>
        <td>${esc(m.email ?? '—')}</td>
        <td>${esc(m.tier)}</td>
        <td><span class="badge badge-${m.status}">${m.status}</span></td>
        <td><payment-status-badge status="${esc(m.dues_status)}"></payment-status-badge></td>
        ${canWrite() ? `<td style="text-align:right">
          <button class="btn-ghost edit-btn" data-id="${m.id}">Edit</button>
          <button class="btn-ghost del-btn" data-id="${m.id}" data-name="${esc(m.display_name)}" style="color:var(--color-danger)">Del</button>
        </td>` : ''}
      </tr>`).join('');
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
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal">
        <div class="modal-header">
          <h2>${isNew ? 'Add member' : 'Edit member'}</h2>
          <button class="btn-ghost" id="close-btn">✕</button>
        </div>
        <div class="modal-body">
          <div class="form-row">
            <div class="form-group">
              <label>Display name *</label>
              <input id="f-name" value="${esc(member?.display_name ?? '')}">
            </div>
            <div class="form-group">
              <label>Tier</label>
              <select id="f-tier">
                ${TIERS.map(t => `<option value="${t}" ${(member?.tier??'standard')===t?'selected':''}>${t}</option>`).join('')}
              </select>
            </div>
          </div>
          <div class="form-row">
            <div class="form-group">
              <label>Email</label>
              <input id="f-email" type="email" value="${esc(member?.email ?? '')}">
            </div>
            <div class="form-group">
              <label>Phone</label>
              <input id="f-phone" value="${esc(member?.phone ?? '')}">
            </div>
          </div>
          <div class="form-group">
            <label>Address</label>
            <input id="f-address" value="${esc(member?.address ?? '')}">
          </div>
          <div class="form-row">
            <div class="form-group">
              <label>Status</label>
              <select id="f-status">
                ${STATUSES.map(s => `<option value="${s}" ${(member?.status??'active')===s?'selected':''}>${s}</option>`).join('')}
              </select>
            </div>
            <div class="form-group">
              <label>Joined date</label>
              <input id="f-joined" type="date" value="${member?.joined_at ? member.joined_at.slice(0,10) : new Date().toISOString().slice(0,10)}">
            </div>
          </div>
          <div class="form-group">
            <label>Notes</label>
            <textarea id="f-notes">${esc(member?.notes ?? '')}</textarea>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Save</button>
        </div>
      </div>
    `;
    document.body.appendChild(backdrop);

    const close = () => backdrop.remove();
    backdrop.querySelector('#close-btn').addEventListener('click', close);
    backdrop.querySelector('#cancel-btn').addEventListener('click', close);

    backdrop.querySelector('#save-btn').addEventListener('click', async () => {
      const body = {
        display_name: backdrop.querySelector('#f-name').value.trim(),
        email:        backdrop.querySelector('#f-email').value.trim() || null,
        phone:        backdrop.querySelector('#f-phone').value.trim() || null,
        address:      backdrop.querySelector('#f-address').value.trim() || null,
        tier:         backdrop.querySelector('#f-tier').value,
        status:       backdrop.querySelector('#f-status').value,
        joined_at:    backdrop.querySelector('#f-joined').value || null,
        notes:        backdrop.querySelector('#f-notes').value.trim() || null,
      };
      if (!body.display_name) { toast('Name is required', 'error'); return; }
      try {
        if (isNew) await api('POST', '/members', body);
        else       await api('PATCH', `/members/${member.id}`, body);
        toast(isNew ? 'Member added' : 'Member updated', 'success');
        close();
        this.load();
      } catch (err) { toast(err.error ?? 'Save failed', 'error'); }
    });
  }
}
customElements.define('page-members', PageMembers);
