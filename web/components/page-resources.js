import { api, canWrite, isSuperadmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete } from '../utils.js';

function safeUrl(u) { try { const p = new URL(u); return (p.protocol==='https:'||p.protocol==='http:') ? u : null; } catch { return null; } }

class PageResources extends HTMLElement {
  constructor() { super(); this._resources = []; this._search = ''; this._category = ''; this._seq = 0; }

  connectedCallback() {
    this.render();
    this.load();
  }

  _cols() { return canWrite() ? 5 : 4; }

  /** Renders the static page shell once; load() only touches #tbody. */
  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Resources</h1>
        ${canWrite() ? '<button class="btn-primary" id="add-btn">+ Add resource</button>' : ''}
      </div>
      <div class="search-bar">
        <input id="search-inp" placeholder="Search resources…" value="${esc(this._search)}">
        <input id="cat-inp" placeholder="Category" value="${esc(this._category)}" style="max-width:160px">
      </div>
      <div class="card" style="overflow:hidden">
        <table>
          <thead><tr><th>Title</th><th>Category</th><th>Tags</th><th>Link</th>${canWrite()?'<th></th>':''}</tr></thead>
          <tbody id="tbody"></tbody>
        </table>
      </div>
    `;

    this.querySelector('#search-inp')?.addEventListener('input', e => {
      this._search = e.target.value;
      clearTimeout(this._timer);
      this._timer = setTimeout(() => this.load(), 350);
    });
    this.querySelector('#cat-inp')?.addEventListener('change', e => { this._category = e.target.value; this.load(); });
    this.querySelector('#add-btn')?.addEventListener('click', () => this.openModal(null));
  }

  async load() {
    const seq = ++this._seq;
    const tbody = this.querySelector('#tbody');
    tbody.innerHTML = `<tr><td colspan="${this._cols()}" style="text-align:center"><span class="spinner"></span></td></tr>`;
    const params = new URLSearchParams();
    if (this._search)   params.set('search', this._search);
    if (this._category) params.set('category', this._category);
    try {
      const _rePage = await api('GET', '/resources?' + params);
      if (seq !== this._seq) return; // A newer load() superseded this one.
      this._resources = _rePage?.data ?? _rePage ?? [];
      tbody.innerHTML = this._rows()
        || `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>No resources yet.</p></div></td></tr>`;
      this._wireRows(tbody);
    } catch {
      if (seq !== this._seq) return;
      tbody.innerHTML = `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>Failed to load resources.</p></div></td></tr>`;
      toast('Failed to load resources','error');
    }
  }

  _rows() {
    if (!this._resources?.length) return '';
    return this._resources.map(r => `
      <tr>
        <td>
          <div style="font-weight:600">${esc(r.title)}
            ${(r.group_names??[]).length ? `<span class="badge" title="Visible only to: ${esc(r.group_names.join(', '))}" style="background:color-mix(in srgb, var(--color-warning,#b45309) 14%, transparent);color:var(--color-warning,#b45309);margin-left:.3rem">🔒 ${esc(r.group_names.join(', '))}</span>` : ''}
          </div>
          ${r.description?`<div style="font-size:.8rem;color:var(--color-text-muted)">${esc(r.description.slice(0,80))}${r.description.length>80?'…':''}</div>`:''}
        </td>
        <td>${esc(r.category??'—')}</td>
        <td style="font-size:.8rem">${(r.tags??[]).map(t=>`<span class="badge badge-none" style="margin:1px">${esc(t)}</span>`).join(' ')||'—'}</td>
        <td>${safeUrl(r.url)?`<a href="${esc(r.url)}" target="_blank" rel="noopener">Open ↗</a>`:'—'}</td>
        ${canWrite()?`<td style="text-align:right">
          <button class="btn-ghost edit-btn" data-id="${esc(r.id)}">Edit</button>
          ${isSuperadmin()?`<button class="btn-ghost del-btn" data-id="${esc(r.id)}" data-title="${esc(r.title)}" style="color:var(--color-danger)">Del</button>`:''}
        </td>`:''}
      </tr>`).join('');
  }

  _wireRows(tbody) {
    tbody.querySelectorAll('.edit-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const r = this._resources.find(x => x.id === btn.dataset.id);
        if (r) this.openModal(r);
      });
    });
    tbody.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        confirmDelete({
          noun: 'resource',
          name: btn.dataset.title ?? '',
          onConfirm: async (confirmVal) => {
            try {
              await api('DELETE', `/resources/${btn.dataset.id}?confirm=${encodeURIComponent(confirmVal)}`);
              toast('Deleted','success');
              this.load();
            } catch (err) { toast(err.error ?? 'Delete failed','error'); throw err; }
          },
        });
      });
    });
  }

  async openModal(resource) {
    const isNew = !resource;
    // Visibility groups: loaded up front so the picker renders checked state.
    let groups = [];
    let checked = new Set();
    try {
      groups = await api('GET', '/groups') ?? [];
      if (!isNew && groups.length) {
        const cur = await api('GET', `/resources/${resource.id}/groups`);
        checked = new Set(cur?.group_ids ?? []);
      }
    } catch { /* groups are optional; the picker just hides */ }
    const groupPicker = groups.length ? `
      <div class="form-group">
        <label>Visible to</label>
        <div style="font-size:.78rem;color:var(--color-text-muted);margin-bottom:.35rem">
          No groups selected = visible to all members. Officers and admins always see everything.</div>
        <div id="f-groups" style="display:flex;flex-wrap:wrap;gap:.4rem">
          ${groups.map(g => `
            <label style="display:inline-flex;align-items:center;gap:.3rem;margin:0;padding:.25rem .55rem;border:1px solid var(--color-border);border-radius:999px;cursor:pointer;text-transform:none;font-weight:500;letter-spacing:normal;font-size:.82rem;color:var(--color-text)">
              <input type="checkbox" value="${esc(g.id)}" ${checked.has(g.id) ? 'checked' : ''} style="width:auto;margin:0">
              ${esc(g.name)}
            </label>`).join('')}
        </div>
      </div>` : '';
    const { dialog, close } = openModal({
      title: isNew ? 'Add resource' : 'Edit resource',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="f-title">Title *</label><input id="f-title" value="${esc(resource?.title??'')}"></div>
          <div class="form-group"><label for="f-desc">Description</label><textarea id="f-desc">${esc(resource?.description??'')}</textarea></div>
          <div class="form-group"><label for="f-url">URL</label><input id="f-url" type="url" value="${esc(resource?.url??'')}" placeholder="https://…"></div>
          <div class="form-row">
            <div class="form-group"><label for="f-category">Category</label><input id="f-category" value="${esc(resource?.category??'')}" placeholder="policy, legal, finance…"></div>
            <div class="form-group"><label for="f-tags">Tags (comma-separated)</label><input id="f-tags" value="${esc((resource?.tags??[]).join(', '))}"></div>
          </div>
          ${groupPicker}
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
      const title = dialog.querySelector('#f-title').value.trim();
      if (!title) { toast('Title required','error'); return; }
      const body = {
        title,
        description: dialog.querySelector('#f-desc').value.trim()||null,
        url:         dialog.querySelector('#f-url').value.trim()||null,
        category:    dialog.querySelector('#f-category').value.trim()||null,
        tags:        dialog.querySelector('#f-tags').value.split(',').map(t=>t.trim()).filter(Boolean),
      };
      try {
        let id = resource?.id;
        if (isNew) { const created = await api('POST', '/resources', body); id = created.id; }
        else       await api('PATCH', `/resources/${resource.id}`, body);
        // Persist the visibility selection (empty = visible to all members).
        if (groups.length) {
          const group_ids = [...dialog.querySelectorAll('#f-groups input:checked')].map(c => c.value);
          await api('PUT', `/resources/${id}/groups`, { group_ids });
        }
        toast(isNew?'Resource added':'Resource updated','success');
        close(); this.load();
      } catch (err) { toast(err.error??'Save failed','error'); }
    }));
  }
}
customElements.define('page-resources', PageResources);
