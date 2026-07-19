import { api, canWrite, isSuperadmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete } from '../utils.js';

class PageContacts extends HTMLElement {
  constructor() { super(); this._contacts = []; this._search = ''; this._category = ''; this._seq = 0; }

  connectedCallback() {
    this.render();
    this.load();
  }

  _cols() { return canWrite() ? 6 : 5; }

  /** Renders the static page shell once; load() only touches #tbody. */
  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Contacts</h1>
        ${canWrite() ? '<button class="btn-primary" id="add-btn">+ Add contact</button>' : ''}
      </div>
      <div class="search-bar">
        <input id="search-inp" placeholder="Search contacts…" value="${esc(this._search)}">
        <input id="cat-inp" placeholder="Category" value="${esc(this._category)}" style="max-width:160px">
      </div>
      <div class="card" style="overflow:hidden">
        <table>
          <thead><tr><th>Name</th><th>Organization</th><th>Category</th><th>Email</th><th>Phone</th>${canWrite()?'<th></th>':''}</tr></thead>
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
      const _coPage = await api('GET', '/contacts?' + params);
      if (seq !== this._seq) return; // A newer load() superseded this one.
      this._contacts = _coPage?.data ?? _coPage ?? [];
      tbody.innerHTML = this._rows()
        || `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>No contacts found.</p></div></td></tr>`;
      this._wireRows(tbody);
    } catch {
      if (seq !== this._seq) return;
      tbody.innerHTML = `<tr><td colspan="${this._cols()}"><div class="empty-state"><p>Failed to load contacts.</p></div></td></tr>`;
      toast('Failed to load contacts','error');
    }
  }

  _rows() {
    if (!this._contacts?.length) return '';
    return this._contacts.map(c => `
      <tr class="contact-row" data-id="${esc(c.id)}" style="cursor:pointer">
        <td><strong>${esc(c.name)}</strong></td>
        <td>${esc(c.organization??'—')}</td>
        <td>${esc(c.category??'—')}</td>
        <td>${c.email?`<a href="mailto:${esc(c.email)}">${esc(c.email)}</a>`:'—'}</td>
        <td>${esc(c.phone??'—')}</td>
        ${canWrite()?`<td style="text-align:right">
          <button class="btn-ghost edit-btn" data-id="${esc(c.id)}">Edit</button>
          ${isSuperadmin()?`<button class="btn-ghost del-btn" data-id="${esc(c.id)}" data-name="${esc(c.name)}" style="color:var(--color-danger)">Del</button>`:''}
        </td>`:''}
      </tr>`).join('');
  }

  _wireRows(tbody) {
    tbody.querySelectorAll('.edit-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const c = this._contacts.find(x => x.id === btn.dataset.id);
        if (c) this.openModal(c);
      });
    });
    tbody.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        confirmDelete({
          noun: 'contact',
          name: btn.dataset.name ?? '',
          onConfirm: async (confirmVal) => {
            try {
              await api('DELETE', `/contacts/${btn.dataset.id}?confirm=${encodeURIComponent(confirmVal)}`);
              toast('Deleted','success');
              this.load();
            } catch (err) { toast(err.error ?? 'Delete failed','error'); throw err; }
          },
        });
      });
    });
  }

  openModal(contact) {
    const isNew = !contact;
    const { dialog, close } = openModal({
      title: isNew ? 'Add contact' : 'Edit contact',
      body: `
        <div class="modal-body">
          <div class="form-row">
            <div class="form-group"><label for="f-name">Name *</label><input id="f-name" value="${esc(contact?.name??'')}"></div>
            <div class="form-group"><label for="f-org">Organization</label><input id="f-org" value="${esc(contact?.organization??'')}"></div>
          </div>
          <div class="form-row">
            <div class="form-group"><label for="f-email">Email</label><input id="f-email" type="email" value="${esc(contact?.email??'')}"></div>
            <div class="form-group"><label for="f-phone">Phone</label><input id="f-phone" value="${esc(contact?.phone??'')}"></div>
          </div>
          <div class="form-row">
            <div class="form-group"><label for="f-category">Category</label><input id="f-category" value="${esc(contact?.category??'')}" placeholder="vendor, partner, legal…"></div>
            <div class="form-group"><label for="f-tags">Tags (comma-separated)</label><input id="f-tags" value="${esc((contact?.tags??[]).join(', '))}"></div>
          </div>
          <div class="form-group"><label for="f-address">Address</label><input id="f-address" value="${esc(contact?.address??'')}"></div>
          <div class="form-group"><label for="f-notes">Notes</label><textarea id="f-notes">${esc(contact?.notes??'')}</textarea></div>
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
      const name = dialog.querySelector('#f-name').value.trim();
      if (!name) { toast('Name required','error'); return; }
      const body = {
        name,
        organization: dialog.querySelector('#f-org').value.trim()||null,
        email:        dialog.querySelector('#f-email').value.trim()||null,
        phone:        dialog.querySelector('#f-phone').value.trim()||null,
        category:     dialog.querySelector('#f-category').value.trim()||null,
        address:      dialog.querySelector('#f-address').value.trim()||null,
        notes:        dialog.querySelector('#f-notes').value.trim()||null,
        tags:         dialog.querySelector('#f-tags').value.split(',').map(t=>t.trim()).filter(Boolean),
      };
      try {
        if (isNew) await api('POST', '/contacts', body);
        else       await api('PATCH', `/contacts/${contact.id}`, body);
        toast(isNew?'Contact added':'Contact updated','success');
        close(); this.load();
      } catch (err) { toast(err.error??'Save failed','error'); }
    }));
  }
}
customElements.define('page-contacts', PageContacts);
