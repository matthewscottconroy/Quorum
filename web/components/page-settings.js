import { api, apiDownload, getUser, isAdmin, isSuperadmin } from '../app.js';
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

/**
 * Builds <option> markup for linking a user to a member record. The empty value
 * means "no linkage"; `selected` is a member id (UUID string) or null/undefined.
 */
function memberOptions(members, selected) {
  const opts = ['<option value="">— none —</option>'];
  for (const m of (members ?? [])) {
    opts.push(`<option value="${esc(m.id)}" ${selected===m.id?'selected':''}>${esc(m.display_name)}</option>`);
  }
  return opts.join('');
}

class PageSettings extends HTMLElement {
  async connectedCallback() {
    await this.render();
  }

  async render() {
    // Re-fetch so the two-factor status reflects any change made this session.
    let me = getUser();
    try { me = await api('GET', '/auth/me') ?? me; } catch {}
    let users = [];
    let members = [];
    if (isAdmin()) {
      try {
        const page = await api('GET', '/users');
        users = page?.data ?? page ?? [];
      } catch {}
      try {
        const mPage = await api('GET', '/members?limit=200');
        members = mPage?.data ?? mPage ?? [];
      } catch {}
    }
    this._members = members;

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

          <hr style="border:none;border-top:1px solid var(--color-border);margin:1.25rem 0">
          <h3 style="font-size:.9rem;margin-bottom:.5rem">Two-factor authentication</h3>
          ${me?.totp_enabled ? `
            <p style="font-size:.85rem;color:var(--color-success,#137333);margin-bottom:.75rem">✓ Two-factor is <strong>on</strong>. You'll be asked for a code at sign-in.</p>
            <div style="display:flex;gap:.5rem;flex-wrap:wrap">
              <button class="btn-secondary" id="twofa-codes-btn">Regenerate recovery codes</button>
              <button class="btn-ghost" id="twofa-disable-btn" style="color:var(--color-danger)">Turn off two-factor</button>
            </div>
          ` : `
            <p style="font-size:.85rem;color:var(--color-text-muted);margin-bottom:.75rem">Add a second step at sign-in using an authenticator app (Google Authenticator, 1Password, Authy…).</p>
            <button class="btn-primary" id="twofa-setup-btn">Set up two-factor</button>
          `}

          <hr style="border:none;border-top:1px solid var(--color-border);margin:1.25rem 0">
          <h3 style="font-size:.9rem;margin-bottom:.5rem">Active sessions</h3>
          <p style="font-size:.85rem;color:var(--color-text-muted);margin-bottom:.75rem">
            <span id="session-count">Checking…</span> If you've signed in somewhere you no longer trust, sign those devices out.</p>
          <button class="btn-secondary" id="revoke-sessions-btn">Sign out other devices</button>

          <hr style="border:none;border-top:1px solid var(--color-border);margin:1.25rem 0">
          <h3 style="font-size:.9rem;margin-bottom:.5rem">Export my data</h3>
          <p style="font-size:.85rem;color:var(--color-text-muted);margin-bottom:.75rem">Download your account, profile, dues and payments as a JSON file.</p>
          <button class="btn-secondary" id="export-me-btn">Download my data (JSON)</button>
        </section>

        ${isAdmin() ? `
        <section class="card" style="padding:1.25rem">
          <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:1rem">
            <h2 style="font-size:1rem">User accounts</h2>
            <button class="btn-primary" id="add-user-btn" style="font-size:.8rem;padding:.3rem .75rem">+ Add user</button>
          </div>
          <table>
            <thead><tr><th>Email</th><th>Role</th><th>Linked member</th><th></th></tr></thead>
            <tbody>
              ${users.map(u => `
                <tr>
                  <td style="font-size:.9rem">${esc(u.email)}</td>
                  <td>
                    <select class="role-sel" data-id="${esc(u.id)}" style="width:auto;padding:.2rem .4rem;font-size:.8rem">
                      ${roleOptions(u.role)}
                    </select>
                  </td>
                  <td>
                    <select class="member-sel" data-id="${esc(u.id)}" style="width:auto;padding:.2rem .4rem;font-size:.8rem">
                      ${memberOptions(members, u.member_id)}
                    </select>
                  </td>
                  <td style="text-align:right;white-space:nowrap">
                    <button class="btn-ghost reset-pw-btn" data-id="${esc(u.id)}" data-email="${esc(u.email)}" style="font-size:.8rem">Reset PW</button>
                    ${isSuperadmin() && u.id !== me?.id ? `<button class="btn-ghost del-user-btn" data-id="${esc(u.id)}" data-email="${esc(u.email)}" style="color:var(--color-danger);font-size:.8rem">Del</button>` : ''}
                  </td>
                </tr>`).join('')}
            </tbody>
          </table>
          <p style="font-size:.8rem;color:var(--color-text-muted);margin-top:.75rem">Linking a user to a member record is what lets a <strong>restricted</strong> user see their own profile, dues, and action items. Choose “— none —” to unlink.</p>
        </section>` : '<div></div>'}
      </div>

      ${isAdmin() ? `
      <section class="card" style="padding:1.25rem;max-width:860px;margin-top:1rem">
        <h2 style="font-size:1rem;margin-bottom:.35rem">Governance &amp; quorum rules</h2>
        <p style="font-size:.82rem;color:var(--color-text-muted);margin-bottom:1rem">How quorum is calculated at meetings and the default passing bar for new motions.</p>
        <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:1rem;align-items:end">
          <div class="form-group">
            <label for="g-mode">Quorum rule</label>
            <select id="g-mode">
              <option value="majority">Majority of active members</option>
              <option value="percent">Percent of active members</option>
              <option value="fixed">Fixed number of members</option>
            </select>
          </div>
          <div class="form-group" id="g-value-wrap">
            <label for="g-value"><span id="g-value-label">Value</span></label>
            <input id="g-value" type="number" min="0" value="0">
          </div>
          <div class="form-group">
            <label for="g-threshold">Default motion threshold</label>
            <select id="g-threshold">
              <option value="majority">Simple majority</option>
              <option value="two_thirds">Two-thirds</option>
              <option value="unanimous">Unanimous</option>
            </select>
          </div>
          <label style="flex-direction:row;align-items:center;gap:.45rem;text-transform:none;letter-spacing:0;font-weight:400;font-size:.85rem;padding-bottom:.5rem">
            <input type="checkbox" id="g-proxies"> Proxies count toward quorum
          </label>
        </div>
        <button class="btn-primary" id="g-save" style="margin-top:.5rem">Save governance rules</button>
      </section>` : ''}
    `;

    const pwBtn = this.querySelector('#pw-btn');
    pwBtn?.addEventListener('click', guardButton(pwBtn, async () => {
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
    }));

    this.querySelector('#export-me-btn')?.addEventListener('click', async () => {
      try { await apiDownload('/auth/me/export', 'my-quorum-data.json'); }
      catch (err) { toast(err.error ?? 'Export failed','error'); }
    });

    this.querySelector('#twofa-setup-btn')?.addEventListener('click', () => this.open2FASetup());
    this.querySelector('#twofa-disable-btn')?.addEventListener('click', () => this.open2FADisable());
    this.querySelector('#twofa-codes-btn')?.addEventListener('click', () => this.openRegenerateCodes());
    this.loadSessionCount();
    this.querySelector('#revoke-sessions-btn')?.addEventListener('click', e => this.revokeOtherSessions(e.target));

    this.querySelector('#add-user-btn')?.addEventListener('click', () => this.openAddUserModal());

    this.querySelectorAll('.reset-pw-btn').forEach(btn => {
      btn.addEventListener('click', () => this.openAdminReset(btn.dataset.id, btn.dataset.email));
    });

    this.wireGovernanceSettings();
  }

  /** Loads and saves the org-wide governance/quorum rules (admin only). */
  async wireGovernanceSettings() {
    const modeSel = this.querySelector('#g-mode');
    if (!modeSel) return; // not an admin
    const valueWrap  = this.querySelector('#g-value-wrap');
    const valueLabel = this.querySelector('#g-value-label');
    const valueInp   = this.querySelector('#g-value');

    // The value field only applies to percent (1–100) and fixed (a count);
    // majority derives its own threshold, so hide it there.
    const syncValueField = () => {
      const mode = modeSel.value;
      valueWrap.style.display = mode === 'majority' ? 'none' : '';
      valueLabel.textContent  = mode === 'percent' ? 'Percent (1–100)' : 'Member count';
    };
    modeSel.addEventListener('change', syncValueField);

    try {
      const s = await api('GET', '/governance/settings');
      modeSel.value = s.quorum_mode;
      valueInp.value = s.quorum_value;
      this.querySelector('#g-threshold').value = s.default_threshold;
      this.querySelector('#g-proxies').checked = s.proxies_count_toward_quorum;
    } catch { /* leave defaults */ }
    syncValueField();

    const gSave = this.querySelector('#g-save');
    gSave.addEventListener('click', guardButton(gSave, async () => {
      const mode = modeSel.value;
      const body = {
        quorum_mode: mode,
        quorum_value: mode === 'majority' ? 0 : (Number.parseInt(valueInp.value, 10) || 0),
        default_threshold: this.querySelector('#g-threshold').value,
        proxies_count_toward_quorum: this.querySelector('#g-proxies').checked,
      };
      try {
        await api('PUT', '/governance/settings', body);
        toast('Governance rules saved','success');
      } catch (err) { toast(err.error ?? 'Save failed','error'); }
    }));

    this.querySelectorAll('.role-sel').forEach(sel => {
      let original = sel.value;
      sel.addEventListener('change', async () => {
        try {
          await api('PATCH', `/users/${sel.dataset.id}`, { role: sel.value });
          original = sel.value; // Adopt the new value as the baseline so a later failed change reverts correctly.
          toast('Role updated','success');
        } catch (err) {
          sel.value = original; // Revert the dropdown when the backend rejects the change.
          toast(err.error ?? 'Update failed','error');
        }
      });
    });

    this.querySelectorAll('.member-sel').forEach(sel => {
      let original = sel.value;
      sel.addEventListener('change', async () => {
        try {
          await api('PATCH', `/users/${sel.dataset.id}`, { member_id: sel.value || null });
          original = sel.value; // Adopt the new value as the baseline for future reverts.
          toast('Linked member updated','success');
        } catch (err) {
          sel.value = original; // Revert when the backend rejects the change (e.g. 403).
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
          <div class="form-group"><label for="f-member">Linked member (optional)</label>
            <select id="f-member">${memberOptions(this._members, '')}</select>
            <p style="font-size:.75rem;color:var(--color-text-muted);margin-top:.35rem">Linking a member lets a <strong>restricted</strong> user see their own record.</p>
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
      const body = {
        email:    dialog.querySelector('#f-email').value,
        password: dialog.querySelector('#f-user-pw').value,
        role:     dialog.querySelector('#f-role').value,
      };
      const memberId = dialog.querySelector('#f-member').value;
      if (memberId) body.member_id = memberId; // Omit when none is selected.
      try {
        await api('POST', '/users', body);
        toast('User created','success'); close(); this.render();
      } catch (err) { toast(err.error??'Failed','error'); }
    }));
  }

  /** Shows how many devices currently hold a live session. */
  async loadSessionCount() {
    const el = this.querySelector('#session-count');
    if (!el) return;
    try {
      const { active_sessions: n } = await api('GET', '/auth/me/sessions');
      el.textContent = n === 1
        ? 'This is your only active session.'
        : `You have ${n} active sessions.`;
    } catch {
      el.textContent = 'Could not check your active sessions.';
    }
  }

  /** Revoke every session except this one. */
  revokeOtherSessions(btn) {
    guardButton(btn, async () => {
      try {
        const { revoked } = await api('POST', '/auth/me/sessions/revoke-others');
        toast(revoked ? `Signed out ${revoked} other session${revoked === 1 ? '' : 's'}` : 'No other sessions to sign out', 'success');
        this.loadSessionCount();
      } catch (err) {
        toast(err.error ?? 'Could not sign out other devices', 'error');
      }
    })();
  }

  /**
   * Asks for the account password in a small modal. Resolves to the password,
   * or null if the user cancelled. Used to gate changes to the second factor —
   * a hijacked session alone must not be able to add, remove, or re-issue it.
   */
  promptPassword({ title, message, confirmLabel = 'Continue' }) {
    return new Promise(resolve => {
      let settled = false;
      const done = v => { if (!settled) { settled = true; resolve(v); } };
      const { dialog, close } = openModal({
        title,
        maxWidth: '400px',
        body: `
          <div class="modal-body">
            <p style="font-size:.88rem;margin-bottom:1rem">${esc(message)}</p>
            <div class="form-group"><label for="f-pw-confirm">Password</label>
              <input id="f-pw-confirm" type="password" autocomplete="current-password"></div>
          </div>
          <div class="modal-footer">
            <button class="btn-secondary" id="cancel-btn">Cancel</button>
            <button class="btn-primary" id="ok-btn">${esc(confirmLabel)}</button>
          </div>
        `,
      });
      // Any close path (button, Escape, backdrop) resolves null, so an awaiting
      // caller is never left hanging.
      dialog.addEventListener('close', () => done(null));
      dialog.querySelector('#cancel-btn').addEventListener('click', close);
      dialog.querySelector('#f-pw-confirm').focus();
      const submit = () => {
        const pw = dialog.querySelector('#f-pw-confirm').value;
        if (!pw) { toast('Password is required', 'error'); return; }
        done(pw);
        close();
      };
      dialog.querySelector('#ok-btn').addEventListener('click', submit);
      dialog.querySelector('#f-pw-confirm').addEventListener('keydown', e => { if (e.key === 'Enter') submit(); });
    });
  }

  /** Enroll in TOTP: confirm the password, show the secret + otpauth URI, then confirm with a code. */
  async open2FASetup() {
    const password = await this.promptPassword({
      title: 'Confirm your password',
      message: 'Enrolling a new authenticator requires your account password.',
    });
    if (!password) return;
    let setup;
    try {
      setup = await api('POST', '/auth/2fa/setup', { password });
    } catch (err) { toast(err.error ?? 'Could not start setup','error'); return; }

    const { dialog, close } = openModal({
      title: 'Set up two-factor authentication',
      maxWidth: '440px',
      body: `
        <div class="modal-body">
          <ol style="font-size:.88rem;padding-left:1.1rem;margin:0 0 1rem">
            <li style="margin-bottom:.5rem">In your authenticator app, add an account and enter this key:</li>
          </ol>
          <div style="font-family:monospace;font-size:1rem;letter-spacing:.06em;background:var(--color-bg);border:1px solid var(--color-border);border-radius:6px;padding:.6rem;text-align:center;word-break:break-all;margin-bottom:1rem">${esc(setup.secret)}</div>
          <p style="font-size:.82rem;color:var(--color-text-muted);margin-bottom:1rem;word-break:break-all">Or use the setup URI:<br><span style="font-family:monospace">${esc(setup.provisioning_uri)}</span></p>
          <div class="form-group"><label for="f-2fa-code">Enter the 6-digit code to confirm</label>
            <input id="f-2fa-code" inputmode="numeric" maxlength="6" placeholder="123456"></div>
          <div id="recovery-block" style="display:none">
            <p style="font-size:.85rem;margin:.5rem 0">Save these one-time <strong>recovery codes</strong> somewhere safe. Each works once if you lose your authenticator:</p>
            <pre id="recovery-codes" style="background:var(--color-bg);border:1px solid var(--color-border);border-radius:6px;padding:.6rem;font-size:.85rem;white-space:pre-wrap"></pre>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="confirm-btn">Confirm &amp; enable</button>
        </div>
      `,
    });

    // Refresh the panel on ANY close (button, Escape, backdrop) so the 2FA
    // status can't be left stale after enabling.
    dialog.addEventListener('close', () => { if (this.isConnected) this.render(); });
    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const confirmBtn = dialog.querySelector('#confirm-btn');
    confirmBtn.addEventListener('click', guardButton(confirmBtn, async () => {
      const code = dialog.querySelector('#f-2fa-code').value.trim();
      if (!/^\d{6}$/.test(code)) { toast('Enter the 6-digit code','error'); return; }
      let res;
      try {
        res = await api('POST', '/auth/2fa/enable', { code });
      } catch (err) { toast(err.error ?? 'Invalid code','error'); return; }
      // Reveal the recovery codes and switch the dialog to a "done" state.
      dialog.querySelector('#recovery-codes').textContent = (res.recovery_codes ?? []).join('\n');
      dialog.querySelector('#recovery-block').style.display = 'block';
      dialog.querySelector('#f-2fa-code').closest('.form-group').style.display = 'none';
      confirmBtn.style.display = 'none';
      const cancel = dialog.querySelector('#cancel-btn');
      cancel.textContent = 'Done';
      cancel.className = 'btn-primary';
      toast('Two-factor enabled','success');
    }));
  }

  /**
   * Issue a fresh set of recovery codes, invalidating the previous ones. For
   * when codes are lost or have been spent.
   */
  async openRegenerateCodes() {
    const password = await this.promptPassword({
      title: 'Regenerate recovery codes',
      message: 'Your existing recovery codes will stop working immediately. Confirm your password to continue.',
      confirmLabel: 'Regenerate',
    });
    if (!password) return;
    let res;
    try {
      res = await api('POST', '/auth/2fa/recovery-codes', { password });
    } catch (err) { toast(err.error ?? 'Could not regenerate codes', 'error'); return; }

    const { dialog, close } = openModal({
      title: 'Your new recovery codes',
      maxWidth: '440px',
      body: `
        <div class="modal-body">
          <p style="font-size:.85rem;margin:0 0 .75rem">Save these somewhere safe — each works <strong>once</strong> if you lose your authenticator. Your previous codes no longer work, and these are shown only now.</p>
          <pre style="background:var(--color-bg);border:1px solid var(--color-border);border-radius:6px;padding:.6rem;font-size:.85rem;white-space:pre-wrap">${esc((res.recovery_codes ?? []).join('\n'))}</pre>
        </div>
        <div class="modal-footer"><button class="btn-primary" id="done-btn">Done</button></div>
      `,
    });
    dialog.querySelector('#done-btn').addEventListener('click', close);
    toast('Recovery codes regenerated', 'success');
  }

  /** Turn off TOTP after re-confirming the account password. */
  open2FADisable() {
    const { dialog, close } = openModal({
      title: 'Turn off two-factor',
      maxWidth: '400px',
      body: `
        <div class="modal-body">
          <p style="font-size:.88rem;margin-bottom:1rem">Confirm your password to disable two-factor authentication.</p>
          <div class="form-group"><label for="f-2fa-pw">Password</label><input id="f-2fa-pw" type="password" autocomplete="current-password"></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="disable-btn" style="background:var(--color-danger)">Turn off</button>
        </div>
      `,
    });
    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const btn = dialog.querySelector('#disable-btn');
    btn.addEventListener('click', guardButton(btn, async () => {
      const password = dialog.querySelector('#f-2fa-pw').value;
      if (!password) { toast('Password is required','error'); return; }
      try {
        await api('POST', '/auth/2fa/disable', { password });
        toast('Two-factor disabled','success'); close(); this.render();
      } catch (err) { toast(err.error ?? 'Could not disable','error'); }
    }));
  }

  /** Admin: reset another user's password (optionally to a generated one). */
  openAdminReset(userId, email) {
    const { dialog, close } = openModal({
      title: 'Reset password',
      maxWidth: '420px',
      body: `
        <div class="modal-body">
          <p style="font-size:.88rem;margin-bottom:1rem">Reset the password for <strong>${esc(email)}</strong>. Leave the field blank to generate a strong temporary password to relay to them. All their active sessions will be signed out.</p>
          <div class="form-group"><label for="f-newpw">New password (optional)</label><input id="f-newpw" type="text" autocomplete="off" placeholder="leave blank to auto-generate"></div>
          <div id="temp-block" style="display:none">
            <p style="font-size:.85rem;margin:.5rem 0">Temporary password (share securely, shown once):</p>
            <pre id="temp-pw" style="background:var(--color-bg);border:1px solid var(--color-border);border-radius:6px;padding:.6rem;font-size:.95rem;white-space:pre-wrap"></pre>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="reset-btn">Reset password</button>
        </div>
      `,
    });
    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const btn = dialog.querySelector('#reset-btn');
    btn.addEventListener('click', guardButton(btn, async () => {
      const pw = dialog.querySelector('#f-newpw').value.trim();
      const body = pw ? { new_password: pw } : {};
      let res;
      try {
        res = await api('POST', `/users/${userId}/reset-password`, body);
      } catch (err) { toast(err.error ?? 'Reset failed','error'); return; }
      if (res.temporary_password) {
        dialog.querySelector('#temp-pw').textContent = res.temporary_password;
        dialog.querySelector('#temp-block').style.display = 'block';
        dialog.querySelector('#f-newpw').closest('.form-group').style.display = 'none';
        btn.style.display = 'none';
        const cancel = dialog.querySelector('#cancel-btn');
        cancel.textContent = 'Done'; cancel.className = 'btn-primary';
        toast('Password reset','success');
      } else {
        toast('Password reset','success'); close();
      }
    }));
  }
}
customElements.define('page-settings', PageSettings);
