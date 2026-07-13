import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';

function esc(s) { return String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }
function safeUrl(u) { try { const p = new URL(u); return (p.protocol==='https:'||p.protocol==='http:') ? u : null; } catch { return null; } }

class PageResources extends HTMLElement {
  constructor() { super(); this._resources = []; this._search = ''; this._category = ''; }

  async connectedCallback() { await this.load(); }

  async load() {
    const params = new URLSearchParams();
    if (this._search)   params.set('search', this._search);
    if (this._category) params.set('category', this._category);
    try { const _rePage = await api('GET', '/resources?' + params);
    this._resources = _rePage?.data ?? _rePage ?? []; }
    catch { toast('Failed to load resources','error'); }
    this.render();
  }

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
          <tbody>
            ${this._resources?.length
              ? this._resources.map(r => `
                  <tr>
                    <td>
                      <div style="font-weight:600">${esc(r.title)}</div>
                      ${r.description?`<div style="font-size:.8rem;color:var(--color-text-muted)">${esc(r.description.slice(0,80))}${r.description.length>80?'…':''}</div>`:''}
                    </td>
                    <td>${esc(r.category??'—')}</td>
                    <td style="font-size:.8rem">${(r.tags??[]).map(t=>`<span class="badge badge-none" style="margin:1px">${esc(t)}</span>`).join(' ')||'—'}</td>
                    <td>${safeUrl(r.url)?`<a href="${esc(r.url)}" target="_blank" rel="noopener">Open ↗</a>`:'—'}</td>
                    ${canWrite()?`<td style="text-align:right">
                      <button class="btn-ghost edit-btn" data-id="${r.id}">Edit</button>
                      <button class="btn-ghost del-btn" data-id="${r.id}" data-title="${esc(r.title)}" style="color:var(--color-danger)">Del</button>
                    </td>`:''}
                  </tr>`).join('')
              : '<tr><td colspan="5"><div class="empty-state"><p>No resources yet.</p></div></td></tr>'}
          </tbody>
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
    this.querySelectorAll('.edit-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const r = this._resources.find(x => x.id === btn.dataset.id);
        if (r) this.openModal(r);
      });
    });
    this.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', async () => {
        if (!await confirm(`Delete "${btn.dataset.title}"?`,'Delete resource')) return;
        try { await api('DELETE', `/resources/${btn.dataset.id}`); toast('Deleted','success'); this.load(); }
        catch { toast('Delete failed','error'); }
      });
    });
  }

  openModal(resource) {
    const isNew = !resource;
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal">
        <div class="modal-header"><h2>${isNew?'Add resource':'Edit resource'}</h2><button class="btn-ghost" id="close-btn">✕</button></div>
        <div class="modal-body">
          <div class="form-group"><label>Title *</label><input id="f-title" value="${esc(resource?.title??'')}"></div>
          <div class="form-group"><label>Description</label><textarea id="f-desc">${esc(resource?.description??'')}</textarea></div>
          <div class="form-group"><label>URL</label><input id="f-url" type="url" value="${esc(resource?.url??'')}" placeholder="https://…"></div>
          <div class="form-row">
            <div class="form-group"><label>Category</label><input id="f-category" value="${esc(resource?.category??'')}" placeholder="policy, legal, finance…"></div>
            <div class="form-group"><label>Tags (comma-separated)</label><input id="f-tags" value="${esc((resource?.tags??[]).join(', '))}"></div>
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
      const title = backdrop.querySelector('#f-title').value.trim();
      if (!title) { toast('Title required','error'); return; }
      const body = {
        title,
        description: backdrop.querySelector('#f-desc').value.trim()||null,
        url:         backdrop.querySelector('#f-url').value.trim()||null,
        category:    backdrop.querySelector('#f-category').value.trim()||null,
        tags:        backdrop.querySelector('#f-tags').value.split(',').map(t=>t.trim()).filter(Boolean),
      };
      try {
        if (isNew) await api('POST', '/resources', body);
        else       await api('PATCH', `/resources/${resource.id}`, body);
        toast(isNew?'Resource added':'Resource updated','success');
        close(); this.load();
      } catch (err) { toast(err.error??'Save failed','error'); }
    });
  }
}
customElements.define('page-resources', PageResources);
