import { api, getUser, isAdmin, isSuperadmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete } from '../utils.js';

/**
 * Builds <option> markup for the role ladder. `superadmin` is offered only to a
 * superadmin (the backend 403s otherwise); an already-assigned role outside the
 * assignable set is still included so the current value renders correctly.
 */
function roleOptions(selected) {
  const roles = ['restricted','member','officer','admin'];
  if (isSuperadmin()) roles.push('superadmin');
  if (selected && !roles.includes(selected)) roles.push(selected);
  return roles.map(r => `<option value="${r}" ${selected===r?'selected':''}>${r}</option>`).join('');
}

class PageSettings extends HTMLElement {
  async connectedCallback() {
    await this.render();
  }

  async render() {
    const me = getUser();
    let users = [];
    if (isAdmin()) {
      try {
        const page = await api('GET', '/users');
        users = page?.data ?? page ?? [];
      } catch {}
    }

    this.innerHTML = `
      <div class="page-header"><h1>Settings</h1></div>

      <div style="display:grid;grid-template-columns:1fr 1fr;gap:1rem;max-width:860px">
        <section class="card" style="padding:1.25rem">
          <h2 style="font-size:1rem;margin-bottom:1rem">My account</h2>
          <p style="font-size:.9rem;margin-bottom:.25rem"><strong>Email:</strong> ${esc(me?.email ?? '')}</p>
          <p style="font-size:.9rem;margin-bottom:1rem"><strong>Role:</strong> ${esc(me?.role ?? '')}</p>
          <h3 style="font-size:.9rem;margin-bottom:.75rem">Change password</h3>
          <div class="form-group"><label for="f-pw-cur">Current password</label><input id="f-pw-cur" type="password" autocomplete="current-password"></div>
          <div class="form-group"><label for="f-pw">New password</label><input id="f-pw" type="password" autocomplete="new-password"></div>
          <div class="form-group"><label for="f-pw2">Confirm password</label><input id="f-pw2" type="password" autocomplete="new-password"></div>
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
                  <td><span class="badge badge-${u.role==='admin'||u.role==='superadmin'?'overdue':u.role==='officer'?'open':'none'}">${esc(u.role)}</span></td>
                  <td style="text-align:right;white-space:nowrap">
                    <select class="role-sel" data-id="${esc(u.id)}" style="width:auto;padding:.2rem .4rem;font-size:.8rem">
                      ${roleOptions(u.role)}
                    </select>
                    ${isSuperadmin() && u.id !== me?.id ? `<button class="btn-ghost del-user-btn" data-id="${esc(u.id)}" data-email="${esc(u.email)}" style="color:var(--color-danger);font-size:.8rem">Del</button>` : ''}
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
      const original = sel.value;
      sel.addEventListener('change', async () => {
        try {
          await api('PATCH', `/users/${sel.dataset.id}`, { role: sel.value });
          toast('Role updated','success');
        } catch (err) {
          sel.value = original; // Revert the dropdown when the backend rejects the change.
          toast(err.error ?? 'Update failed','error');
        }
      });
    });

    this.querySelectorAll('.del-user-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        confirmDelete({
          noun: 'user account',
          name: btn.dataset.email ?? '',
          onConfirm: async (confirmVal) => {
            try {
              await api('DELETE', `/users/${btn.dataset.id}?confirm=${encodeURIComponent(confirmVal)}`);
              toast('User deleted','success');
              this.render();
            } catch (err) { toast(err.error ?? 'Delete failed','error'); throw err; }
          },
        });
      });
    });
  }

  openAddUserModal() {
    const { dialog, close } = openModal({
      title: 'Add user',
      maxWidth: '400px',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="f-email">Email *</label><input id="f-email" type="email"></div>
          <div class="form-group"><label for="f-user-pw">Password *</label><input id="f-user-pw" type="password"></div>
          <div class="form-group"><label for="f-role">Role</label>
            <select id="f-role">${roleOptions('member')}</select>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Create</button>
        </div>
      `,
    });

    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      try {
        await api('POST', '/users', {
          email:    dialog.querySelector('#f-email').value,
          password: dialog.querySelector('#f-user-pw').value,
          role:     dialog.querySelector('#f-role').value,
        });
        toast('User created','success'); close(); this.render();
      } catch (err) { toast(err.error??'Failed','error'); }
    }));
  }
}
customElements.define('page-settings', PageSettings);
