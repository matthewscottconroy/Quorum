import { api, apiUpload, apiDownload, canWrite, isSuperadmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete } from '../utils.js';
import { openDocPreview } from './doc-preview.js';

function safeUrl(u) { try { const p = new URL(u); return (p.protocol==='https:'||p.protocol==='http:') ? u : null; } catch { return null; } }

function fmtBytes(n) {
  if (n == null) return '';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

class PageResources extends HTMLElement {
  constructor() {
    super();
    this._resources = []; this._folders = []; this._search = ''; this._category = '';
    this._folder = ''; this._seq = 0; this._collapsed = new Set();
  }

  /** children-of map for the folder tree; roots under the '' key. */
  _kids(parent) {
    return this._folders
      .filter(f => (f.parent_id ?? '') === parent)
      .sort((a, b) => a.name.localeCompare(b.name));
  }

  /** Depth-indented <option> list for parent/move selects. */
  _folderOptions(selected, excludeId = null, parent = '', depth = 0) {
    return this._kids(parent).flatMap(f => {
      if (f.id === excludeId) return []; // cannot move under itself
      const pad = '\u00a0\u00a0'.repeat(depth);
      return [`<option value="${esc(f.id)}" ${selected === f.id ? 'selected' : ''}>${pad}${esc(f.name)}</option>`,
        ...this._folderOptions(selected, excludeId, f.id, depth + 1)];
    }).join('');
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
        <h1>Resources</h1>
        ${canWrite() ? '<button class="btn-primary" id="add-btn">+ Add resource</button>' : ''}
      </div>
      <div class="search-bar">
        <input id="search-inp" placeholder="Search resources…" value="${esc(this._search)}">
        <input id="cat-inp" placeholder="Category" value="${esc(this._category)}" style="max-width:160px">
        ${canWrite() ? '<button class="btn-secondary" id="folders-btn">Folders</button>' : ''}
      </div>
      <div class="card" id="folder-tree" style="padding:.45rem .7rem;margin-bottom:.8rem;font-size:.86rem"></div>
      <div class="card" style="overflow:hidden">
        <table>
          <thead><tr><th>Title</th><th>Folder</th><th>Category</th><th>Tags</th><th>Content</th>${canWrite()?'<th></th>':''}</tr></thead>
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
    this.querySelector('#folders-btn')?.addEventListener('click', () => this.openFoldersModal());
    this.querySelector('#add-btn')?.addEventListener('click', () => this.openModal(null));
  }

  /** Collapsible folder tree; clicking a name filters the table. */
  _renderTree() {
    const box = this.querySelector('#folder-tree');
    if (!box) return;
    const node = (f, depth) => {
      const kids = this._kids(f.id);
      const closed = this._collapsed.has(f.id);
      return `
        <div class="ft-row" style="padding-left:${depth * 1.1}rem">
          ${kids.length
            ? `<button class="ft-toggle" data-id="${esc(f.id)}" aria-label="${closed ? 'Expand' : 'Collapse'}">${closed ? '▸' : '▾'}</button>`
            : '<span class="ft-toggle" style="visibility:hidden">▾</span>'}
          <button class="ft-name ${this._folder === f.id ? 'ft-active' : ''}" data-id="${esc(f.id)}">
            📁 ${esc(f.name)} <span class="ft-count">${f.resource_count}</span></button>
        </div>
        ${closed ? '' : kids.map(k => node(k, depth + 1)).join('')}`;
    };
    box.innerHTML = `
      <div class="ft-row">
        <span class="ft-toggle" style="visibility:hidden">▾</span>
        <button class="ft-name ${this._folder === '' ? 'ft-active' : ''}" data-id="">🗂 All documents</button>
        <button class="ft-name ${this._folder === 'none' ? 'ft-active' : ''}" data-id="none">📂 Unfiled</button>
      </div>
      ${this._kids('').map(f => node(f, 1)).join('')}`;
    box.querySelectorAll('.ft-toggle[data-id]').forEach(b => b.addEventListener('click', () => {
      const id = b.dataset.id;
      this._collapsed.has(id) ? this._collapsed.delete(id) : this._collapsed.add(id);
      this._renderTree();
    }));
    box.querySelectorAll('.ft-name').forEach(b => b.addEventListener('click', () => {
      this._folder = b.dataset.id;
      this.load();
    }));
    if (!document.getElementById('folder-tree-style')) {
      const st = document.createElement('style');
      st.id = 'folder-tree-style';
      st.textContent = `
        .ft-row { display:flex; align-items:center; gap:.15rem; }
        .ft-toggle { background:none; border:none; cursor:pointer; width:1.2rem; color:var(--color-text-muted); padding:0; }
        .ft-name { background:none; border:none; cursor:pointer; padding:.14rem .45rem; border-radius:5px; color:var(--color-text); font-size:.86rem; }
        .ft-name:hover { background:var(--color-bg); }
        .ft-active { background:color-mix(in srgb, var(--color-primary,#2563eb) 12%, transparent); font-weight:600; }
        .ft-count { color:var(--color-text-muted); font-size:.72rem; }`;
      document.head.appendChild(st);
    }
  }

  async load() {
    const seq = ++this._seq;
    const tbody = this.querySelector('#tbody');
    tbody.innerHTML = `<tr><td colspan="${this._cols()}" style="text-align:center"><span class="spinner"></span></td></tr>`;
    const params = new URLSearchParams();
    if (this._search)   params.set('search', this._search);
    if (this._category) params.set('category', this._category);
    if (this._folder)   params.set('folder', this._folder);
    try {
      const [_rePage, folders] = await Promise.all([
        api('GET', '/resources?' + params),
        api('GET', '/folders').catch(() => this._folders),
      ]);
      if (seq !== this._seq) return; // A newer load() superseded this one.
      this._resources = _rePage?.data ?? _rePage ?? [];
      this._folders = folders ?? [];
      this._renderTree();
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
        <td style="font-size:.85rem">${r.folder_name ? `📁 ${esc(r.folder_name)}` : '—'}</td>
        <td>${esc(r.category??'—')}</td>
        <td style="font-size:.8rem">${(r.tags??[]).map(t=>`<span class="badge badge-none" style="margin:1px">${esc(t)}</span>`).join(' ')||'—'}</td>
        <td style="font-size:.85rem">
          ${r.file_name ? `<button class="btn-ghost pv-btn" data-id="${esc(r.id)}" title="Preview in the app">👁 ${esc(r.file_name)}</button>` : ''}
          ${r.file_name && !r.file_preview_only ? `<button class="btn-ghost dl-btn" data-id="${esc(r.id)}" data-name="${esc(r.file_name)}" title="Download (${fmtBytes(r.file_size)}) — recorded & watermarked">⬇</button>` : ''}
          ${r.file_name && r.file_preview_only ? `<span class="badge badge-none" title="Preview-only: cannot be downloaded" style="font-size:.68rem">👁 preview-only</span>` : ''}
          ${safeUrl(r.url)?`<a href="${esc(r.url)}" target="_blank" rel="noopener">Open ↗</a>`:''}
          ${!r.file_name && !safeUrl(r.url) ? '—' : ''}
        </td>
        ${canWrite()?`<td style="text-align:right">
          <button class="btn-ghost edit-btn" data-id="${esc(r.id)}">Edit</button>
          ${isSuperadmin()?`<button class="btn-ghost del-btn" data-id="${esc(r.id)}" data-title="${esc(r.title)}" style="color:var(--color-danger)">Del</button>`:''}
        </td>`:''}
      </tr>`).join('');
  }

  _wireRows(tbody) {
    tbody.querySelectorAll('.pv-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const r = this._resources.find(x => x.id === btn.dataset.id);
        if (r) openDocPreview(r);
      });
    });
    tbody.querySelectorAll('.dl-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        apiDownload(`/resources/${btn.dataset.id}/file`, btn.dataset.name)
          .catch(err => toast(err.error ?? 'Download failed', 'error'));
      });
    });
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
          <div class="form-row">
            <div class="form-group"><label for="f-folder">Folder</label>
              <select id="f-folder">
                <option value="">— no folder —</option>
                ${this._folderOptions(resource?.folder_id ?? '')}
              </select></div>
            <div class="form-group"><label for="f-file">Document ${resource?.file_name ? `(current: ${esc(resource.file_name)})` : ''}</label>
              <input id="f-file" type="file">
              <div style="font-size:.72rem;color:var(--color-text-muted)">Optional, up to 25 MB. Uploading replaces the current document. Downloads are watermarked where the format allows and always recorded (who, when, from where).</div></div>
          </div>
          <div class="form-group" style="display:flex;align-items:center;gap:.45rem">
            <input id="f-preview-only" type="checkbox" style="width:auto;margin:0" ${resource?.file_preview_only ? 'checked' : ''}>
            <label for="f-preview-only" style="margin:0;text-transform:none;font-weight:500;letter-spacing:normal">
              Preview-only (document can be viewed in the app but never downloaded)</label>
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
        folder_id:   dialog.querySelector('#f-folder').value || null,
        file_preview_only: dialog.querySelector('#f-preview-only').checked,
      };
      const file = dialog.querySelector('#f-file').files[0] ?? null;
      if (file && file.size > 25 * 1024 * 1024) { toast('File is over the 25 MB limit', 'error'); return; }
      try {
        let id = resource?.id;
        if (isNew) { const created = await api('POST', '/resources', body); id = created.id; }
        else       await api('PATCH', `/resources/${resource.id}`, body);
        // Persist the visibility selection (empty = visible to all members).
        if (groups.length) {
          const group_ids = [...dialog.querySelectorAll('#f-groups input:checked')].map(c => c.value);
          await api('PUT', `/resources/${id}/groups`, { group_ids });
        }
        if (file) await apiUpload(`/resources/${id}/file`, file);
        toast(isNew?'Resource added':'Resource updated','success');
        close(); this.load();
      } catch (err) { toast(err.error??'Save failed','error'); }
    }));
  }

  /** Officer-only folder management: create, rename, delete. */
  openFoldersModal() {
    const { dialog, close } = openModal({
      title: 'Folders',
      maxWidth: '480px',
      body: `
        <div class="modal-body">
          <p style="font-size:.8rem;color:var(--color-text-muted);margin-top:0">
            Folders organize the library; they don't protect anything. Visibility is controlled by
            the groups on each resource. Deleting a folder returns its documents to the root.</p>
          <div id="fo-rows"></div>
          <div style="display:flex;gap:.4rem;margin-top:.8rem;flex-wrap:wrap">
            <input id="nf-name" placeholder="New folder name" style="flex:1;min-width:130px">
            <select id="nf-parent" style="max-width:160px"><option value="">(root)</option>${this._folderOptions('')}</select>
            <button class="btn-primary" id="nf-add">Add</button>
          </div>
        </div>
        <div class="modal-footer"><button class="btn-secondary" id="close-btn">Close</button></div>`,
    });
    dialog.querySelector('#close-btn').addEventListener('click', () => { close(); this.load(); });

    const renderRows = () => {
      const rows = dialog.querySelector('#fo-rows');
      rows.innerHTML = this._folders.map(f => `
        <div style="display:flex;gap:.4rem;align-items:center;padding:.3rem 0;border-bottom:1px solid var(--color-border)" data-id="${esc(f.id)}">
          <input class="fo-name" value="${esc(f.name)}" style="flex:1;min-width:110px">
          <select class="fo-parent" title="Parent folder" style="max-width:150px;font-size:.8rem">
            <option value="">(root)</option>
            ${this._folderOptions(f.parent_id ?? '', f.id)}
          </select>
          <span style="font-size:.75rem;color:var(--color-text-muted)">${f.resource_count}</span>
          <button class="btn-ghost fo-save" title="Save name/parent">💾</button>
          <button class="btn-ghost fo-del" data-name="${esc(f.name)}" style="color:var(--color-danger)">✕</button>
        </div>`).join('') || '<p style="font-size:.85rem">No folders yet.</p>';

      rows.querySelectorAll('[data-id]').forEach(row => {
        const id = row.dataset.id;
        row.querySelector('.fo-save').addEventListener('click', async () => {
          const name = row.querySelector('.fo-name').value.trim();
          if (!name) { toast('Name required', 'error'); return; }
          try {
            await api('PATCH', `/folders/${id}`, { name, parent_id: row.querySelector('.fo-parent').value || null });
            toast('Saved', 'success'); await this.reloadFolders(); renderRows();
          } catch (err) { toast(err.error ?? 'Save failed', 'error'); }
        });
        row.querySelector('.fo-del').addEventListener('click', () => {
          confirmDelete({
            noun: 'folder (its documents return to the library root)',
            name: row.querySelector('.fo-del').dataset.name,
            onConfirm: async (confirmVal) => {
              try {
                await api('DELETE', `/folders/${id}?confirm=${encodeURIComponent(confirmVal)}`);
                toast('Folder deleted', 'success'); await this.reloadFolders(); renderRows();
              } catch (err) { toast(err.error ?? 'Delete failed', 'error'); throw err; }
            },
          });
        });
      });
    };

    dialog.querySelector('#nf-add').addEventListener('click', async () => {
      const name = dialog.querySelector('#nf-name').value.trim();
      if (!name) { toast('Name required', 'error'); return; }
      try {
        await api('POST', '/folders', { name, parent_id: dialog.querySelector('#nf-parent').value || null });
        dialog.querySelector('#nf-name').value = '';
        toast('Folder added', 'success'); await this.reloadFolders(); renderRows();
      } catch (err) { toast(err.error ?? 'Add failed', 'error'); }
    });

    renderRows();
  }

  async reloadFolders() {
    try { this._folders = await api('GET', '/folders') ?? []; } catch { /* keep stale */ }
  }
}
customElements.define('page-resources', PageResources);
