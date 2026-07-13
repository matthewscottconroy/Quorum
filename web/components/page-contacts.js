import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';

function esc(s) { return String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }

class PageContacts extends HTMLElement {
  constructor() { super(); this._contacts = []; this._search = ''; this._category = ''; }

  async connectedCallback() { await this.load(); }

  async load() {
    const params = new URLSearchParams();
    if (this._search)   params.set('search', this._search);
    if (this._category) params.set('category', this._category);
    try { const _coPage = await api('GET', '/contacts?' + params);
    this._contacts = _coPage?.data ?? _coPage ?? []; }
    catch { toast('Failed to load contacts','error'); }
    this.render();
  }

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
          <tbody>
            ${this._contacts?.length
              ? this._contacts.map(c => `
                  <tr class="contact-row" data-id="${c.id}" style="cursor:pointer">
                    <td><strong>${esc(c.name)}</strong></td>
                    <td>${esc(c.organization??'—')}</td>
                    <td>${esc(c.category??'—')}</td>
                    <td>${c.email?`<a href="mailto:${esc(c.email)}">${esc(c.email)}</a>`:'—'}</td>
                    <td>${esc(c.phone??'—')}</td>
                    ${canWrite()?`<td style="text-align:right">
                      <button class="btn-ghost edit-btn" data-id="${c.id}">Edit</button>
                      <button class="btn-ghost del-btn" data-id="${c.id}" data-name="${esc(c.name)}" style="color:var(--color-danger)">Del</button>
                    </td>`:''}
                  </tr>`).join('')
              : '<tr><td colspan="6"><div class="empty-state"><p>No contacts found.</p></div></td></tr>'}
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
        const c = this._contacts.find(x => x.id === btn.dataset.id);
        if (c) this.openModal(c);
      });
    });
    this.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', async () => {
        if (!await confirm(`Delete "${btn.dataset.name}"?`,'Delete contact')) return;
        try { await api('DELETE', `/contacts/${btn.dataset.id}`); toast('Deleted','success'); this.load(); }
        catch { toast('Delete failed','error'); }
      });
    });
  }

  openModal(contact) {
    const isNew = !contact;
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal">
        <div class="modal-header"><h2>${isNew?'Add contact':'Edit contact'}</h2><button class="btn-ghost" id="close-btn">✕</button></div>
        <div class="modal-body">
          <div class="form-row">
            <div class="form-group"><label>Name *</label><input id="f-name" value="${esc(contact?.name??'')}"></div>
            <div class="form-group"><label>Organization</label><input id="f-org" value="${esc(contact?.organization??'')}"></div>
          </div>
          <div class="form-row">
            <div class="form-group"><label>Email</label><input id="f-email" type="email" value="${esc(contact?.email??'')}"></div>
            <div class="form-group"><label>Phone</label><input id="f-phone" value="${esc(contact?.phone??'')}"></div>
          </div>
          <div class="form-row">
            <div class="form-group"><label>Category</label><input id="f-category" value="${esc(contact?.category??'')}" placeholder="vendor, partner, legal…"></div>
            <div class="form-group"><label>Tags (comma-separated)</label><input id="f-tags" value="${esc((contact?.tags??[]).join(', '))}"></div>
          </div>
          <div class="form-group"><label>Address</label><input id="f-address" value="${esc(contact?.address??'')}"></div>
          <div class="form-group"><label>Notes</label><textarea id="f-notes">${esc(contact?.notes??'')}</textarea></div>
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
      const name = backdrop.querySelector('#f-name').value.trim();
      if (!name) { toast('Name required','error'); return; }
      const body = {
        name,
        organization: backdrop.querySelector('#f-org').value.trim()||null,
        email:        backdrop.querySelector('#f-email').value.trim()||null,
        phone:        backdrop.querySelector('#f-phone').value.trim()||null,
        category:     backdrop.querySelector('#f-category').value.trim()||null,
        address:      backdrop.querySelector('#f-address').value.trim()||null,
        notes:        backdrop.querySelector('#f-notes').value.trim()||null,
        tags:         backdrop.querySelector('#f-tags').value.split(',').map(t=>t.trim()).filter(Boolean),
      };
      try {
        if (isNew) await api('POST', '/contacts', body);
        else       await api('PATCH', `/contacts/${contact.id}`, body);
        toast(isNew?'Contact added':'Contact updated','success');
        close(); this.load();
      } catch (err) { toast(err.error??'Save failed','error'); }
    });
  }
}
customElements.define('page-contacts', PageContacts);
