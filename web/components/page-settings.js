import { api, getUser, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';

function esc(s) { return String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }

class PageSettings extends HTMLElement {
  async connectedCallback() {
    await this.render();
  }

  async render() {
    const me = getUser();
    let users = [];
    if (isAdmin()) {
      try { users = await api('GET', '/users'); } catch {}
    }

    this.innerHTML = `
      <div class="page-header"><h1>Settings</h1></div>

      <div style="display:grid;grid-template-columns:1fr 1fr;gap:1rem;max-width:860px">
        <section class="card" style="padding:1.25rem">
          <h2 style="font-size:1rem;margin-bottom:1rem">My account</h2>
          <p style="font-size:.9rem;margin-bottom:.25rem"><strong>Email:</strong> ${esc(me?.email ?? '')}</p>
          <p style="font-size:.9rem;margin-bottom:1rem"><strong>Role:</strong> ${esc(me?.role ?? '')}</p>
          <h3 style="font-size:.9rem;margin-bottom:.75rem">Change password</h3>
          <div class="form-group"><label>Current password</label><input id="f-pw-cur" type="password" autocomplete="current-password"></div>
          <div class="form-group"><label>New password</label><input id="f-pw" type="password" autocomplete="new-password"></div>
          <div class="form-group"><label>Confirm password</label><input id="f-pw2" type="password" autocomplete="new-password"></div>
          <button class="btn-primary" id="pw-btn">Update password</button>
        </section>

        ${isAdmin() ? `
        <section class="card" style="padding:1.25rem">
          <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:1rem">
            <h2 style="font-size:1rem">User accounts</h2>
            <button class="btn-primary" id="add-user-btn" style="font-size:.8rem;padding:.3rem .75rem">+ Add user</button>
          </div>
          <table>
            <thead><tr><th>Email</th><th>Role</th><th></th></tr></thead>
            <tbody>
              ${users.map(u => `
                <tr>
                  <td style="font-size:.9rem">${esc(u.email)}</td>
                  <td><span class="badge badge-${u.role==='admin'?'overdue':u.role==='officer'?'open':'none'}">${u.role}</span></td>
                  <td>
                    <select class="role-sel" data-id="${u.id}" style="width:auto;padding:.2rem .4rem;font-size:.8rem">
                      ${['member','officer','admin'].map(r=>`<option value="${r}" ${u.role===r?'selected':''}>${r}</option>`).join('')}
                    </select>
                  </td>
                </tr>`).join('')}
            </tbody>
          </table>
        </section>` : '<div></div>'}
      </div>
    `;

    this.querySelector('#pw-btn')?.addEventListener('click', async () => {
      const cur = this.querySelector('#f-pw-cur').value;
      const pw  = this.querySelector('#f-pw').value;
      const pw2 = this.querySelector('#f-pw2').value;
      if (!cur) { toast('Current password is required','error'); return; }
      if (!pw || pw !== pw2) { toast('Passwords do not match','error'); return; }
      if (pw.length < 10) { toast('Password must be at least 10 characters','error'); return; }
      try {
        await api('PATCH', '/auth/me/password', { current_password: cur, new_password: pw });
        toast('Password updated','success');
        this.querySelector('#f-pw-cur').value = '';
        this.querySelector('#f-pw').value = '';
        this.querySelector('#f-pw2').value = '';
      } catch (err) { toast(err.error ?? 'Update failed','error'); }
    });

    this.querySelector('#add-user-btn')?.addEventListener('click', () => this.openAddUserModal());

    this.querySelectorAll('.role-sel').forEach(sel => {
      sel.addEventListener('change', async () => {
        try {
          await api('PATCH', `/users/${sel.dataset.id}`, { role: sel.value });
          toast('Role updated','success');
        } catch { toast('Update failed','error'); }
      });
    });
  }

  openAddUserModal() {
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal" style="max-width:400px">
        <div class="modal-header"><h2>Add user</h2><button class="btn-ghost" id="close-btn">✕</button></div>
        <div class="modal-body">
          <div class="form-group"><label>Email *</label><input id="f-email" type="email"></div>
          <div class="form-group"><label>Password *</label><input id="f-pw" type="password"></div>
          <div class="form-group"><label>Role</label>
            <select id="f-role"><option value="member">member</option><option value="officer">officer</option><option value="admin">admin</option></select>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Create</button>
        </div>
      </div>
    `;
    document.body.appendChild(backdrop);
    const close = () => backdrop.remove();
    backdrop.querySelector('#close-btn').addEventListener('click', close);
    backdrop.querySelector('#cancel-btn').addEventListener('click', close);
    backdrop.querySelector('#save-btn').addEventListener('click', async () => {
      try {
        await api('POST', '/users', {
          email:    backdrop.querySelector('#f-email').value,
          password: backdrop.querySelector('#f-pw').value,
          role:     backdrop.querySelector('#f-role').value,
        });
        toast('User created','success'); close(); this.render();
      } catch (err) { toast(err.error??'Failed','error'); }
    });
  }
}
customElements.define('page-settings', PageSettings);
