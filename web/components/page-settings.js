import { api, apiDownload, getUser, isAdmin, isSuperadmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete, getTheme, applyTheme } from '../utils.js';

/**
 * Builds <option> markup for the role ladder. `superadmin` is offered only to a
 * superadmin (the backend 403s otherwise); an already-assigned role outside the
 * assignable set is still included so the current value renders correctly.
 */
function roleOptions(selected) {
  const roles = ['restricted','member','officer','admin'];
  if (isSuperadmin()) roles.push('superadmin');
  if (selected && !roles.includes(selected)) roles.push(selected);
  return roles.map(r => `<option value="${esc(r)}" ${selected===r?'selected':''}>${esc(r)}</option>`).join('');
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
          <h3 style="font-size:.9rem;margin-bottom:.5rem">Appearance</h3>
          <div class="form-group" style="max-width:220px">
            <label for="theme-sel">Theme</label>
            <select id="theme-sel">
              <option value="system">System (auto)</option>
              <option value="light">Light</option>
              <option value="dark">Dark</option>
            </select>
          </div>

          <hr style="border:none;border-top:1px solid var(--color-border);margin:1.25rem 0">
          <h3 style="font-size:.9rem;margin-bottom:.5rem">Emailed report digests</h3>
          <p style="font-size:.8rem;color:var(--color-text-muted);margin-bottom:.5rem">Get financial reports on a schedule. Weekly digests send Monday; monthly on the 1st.</p>
          <div id="report-subs"></div>

          <hr style="border:none;border-top:1px solid var(--color-border);margin:1.25rem 0">
          <h3 style="font-size:.9rem;margin-bottom:.5rem">Export my data</h3>
          <p style="font-size:.85rem;color:var(--color-text-muted);margin-bottom:.75rem">Download your account, profile, dues and payments as a JSON file.</p>
          <button class="btn-secondary" id="export-me-btn">Download my data (JSON)</button>
        </section>

        ${isAdmin() ? `
        <section class="card" style="padding:1.25rem;grid-column:1 / -1">
          <h2 style="font-size:1rem;margin:0 0 .35rem">Organization settings</h2>
          <p style="font-size:.83rem;color:var(--color-text-muted);margin:0 0 .75rem">
            Org-agnostic knobs: the fiscal-year start drives accounting defaults and labels only;
            "How to pay" appears on every member's My Account page (Zelle address, Venmo handle, checks payable to…).</p>
          <div class="form-row">
            <div class="form-group" style="max-width:220px"><label for="og-fy">Fiscal year starts (month 1–12)</label>
              <input id="og-fy" type="number" min="1" max="12" value="1"></div>
            <div class="form-group" style="max-width:200px"><label for="og-latefee">Late fee (minor units)</label>
              <input id="og-latefee" type="number" min="0" placeholder="0 = off">
              <div style="font-size:.72rem;color:var(--color-text-muted)">Posted nightly as a separate fee invoice once dues are past due + grace — including invoices already overdue when you enable it. The number is charged at face value in each invoice's own currency (500 = $5.00 or ¥500). Waive the fee invoice to forgive it.</div></div>
            <div class="form-group" style="max-width:160px"><label for="og-lategrace">Late-fee grace (days)</label>
              <input id="og-lategrace" type="number" min="0" max="365" placeholder="0"></div>
            <div class="form-group" style="max-width:260px"><label for="og-tz">Timezone (IANA name)</label>
              <input id="og-tz" placeholder="America/New_York">
              <div style="font-size:.72rem;color:var(--color-text-muted)">Used wherever the server renders times: minutes documents and meeting emails. Blank = the server's own zone.</div></div>
            <div class="form-group" style="max-width:300px"><label for="og-2fa">Require two-factor auth</label>
              <select id="og-2fa">
                <option value="off">Off (optional for everyone)</option>
                <option value="admin">Admins and above</option>
                <option value="officer">Officers and above</option>
                <option value="member">Everyone (members and above)</option>
              </select>
              <div style="font-size:.72rem;color:var(--color-text-muted)">Server-enforced. Un-enrolled accounts at/above the chosen role are walked through setup at next use (takes effect within ~30s).</div></div>
          </div>
          <div class="form-group"><label for="og-agendatpl">Agenda templates (JSON)</label>
            <textarea id="og-agendatpl" rows="3" placeholder='[{"name":"Board meeting","agenda":"1. Call to order\n2. Minutes\n3. Treasurer\u2019s report\n4. Old business\n5. New business\n6. Adjourn"}]'></textarea>
            <div style="font-size:.72rem;color:var(--color-text-muted)">Offered as a picker when scheduling a meeting.</div></div>
          <div class="form-group"><label for="og-pay">How to pay (shown to members)</label>
            <textarea id="og-pay" rows="3" placeholder="Zelle: treasurer@…  ·  Venmo: @org-handle  ·  Checks payable to …"></textarea></div>
          <div class="form-group"><label for="og-paylink">Pay-now link template (optional)</label>
            <input id="og-paylink" placeholder="https://pay.example.com/?amt={amount_major}&ref={reference}">
            <div style="font-size:.72rem;color:var(--color-text-muted)">An https URL with optional {amount_major} and {reference} placeholders; a "Pay" button appears on each member's open invoice.</div></div>
          <div class="form-group"><label for="og-vocab">Vocabulary overrides (optional JSON)</label>
            <input id="og-vocab" placeholder='{"Dues":"Assessments","Members":"Residents"}'>
            <div style="font-size:.72rem;color:var(--color-text-muted)">Rename UI labels to your org's terms. Takes effect on next load.</div></div>
          <div class="form-row">
            <div class="form-group" style="max-width:240px"><label for="og-lapse">Auto-lapse overdue members (days, 0=off)</label>
              <input id="og-lapse" type="number" min="0" max="3650" value="0">
              <div style="font-size:.72rem;color:var(--color-text-muted)">Members overdue beyond this move to inactive nightly.</div></div>
            <div class="form-group"><label for="og-monthly">Monthly financial summary email (optional)</label>
              <input id="og-monthly" placeholder="board@example.com"></div>
          </div>
          <div class="form-group" style="max-width:280px"><label for="og-arcade">Top Secret arcade</label>
            <select id="og-arcade"><option value="on">On — members see the arcade</option><option value="off">Off — hidden and disabled</option></select>
            <div style="font-size:.72rem;color:var(--color-text-muted)">When off, the sidebar section disappears and the arcade API and game rooms refuse (server-enforced, takes effect within ~30s). Plays, scores and saved levels are kept.</div></div>
          <div class="form-group" style="max-width:240px"><label for="og-hold">Audit legal hold</label>
            <select id="og-hold"><option value="off">Off — prune per retention</option><option value="on">On — preserve everything</option></select>
            <div style="font-size:.72rem;color:var(--color-text-muted)">When on, the nightly job never prunes the audit log (litigation/investigation hold).</div></div>
          <div class="form-group"><label for="og-infra">Infrastructure facts (admin-only; feeds the continuity pack)</label>
            <textarea id="og-infra" rows="4" placeholder="Registrar: … · DNS: Route 53 · Cloud: AWS acct owner … · Server: … · Backup bucket: … · Password vault: …"></textarea></div>
          <div class="form-row">
            <div class="form-group" style="max-width:220px"><label for="og-watch">Inactivity watchdog (days, 0=off)</label>
              <input id="og-watch" type="number" min="0" max="365" value="0"></div>
            <div class="form-group"><label for="og-contacts">Continuity contacts (emails, comma-separated)</label>
              <input id="og-contacts" placeholder="president@…, secretary@…"></div>
          </div>
          <button class="btn-primary" id="og-save" style="font-size:.85rem">Save org settings</button>

          <div id="cont-section" style="border-top:1px solid var(--color-border);margin-top:1rem;padding-top:.8rem">
            <h3 style="font-size:.95rem;margin:0 0 .3rem">Continuity: secret custody & health</h3>
            <div id="cont-checks" style="font-size:.82rem;margin-bottom:.6rem"></div>
            <div id="cont-rows"></div>
            <div style="display:flex;gap:.3rem;margin-top:.5rem;flex-wrap:wrap">
              <input id="cu-name" placeholder="Secret (e.g. backup passphrase)" style="flex:1;min-width:150px">
              <input id="cu-loc" placeholder="Where it lives" style="flex:1;min-width:150px">
              <input id="cu-holder" placeholder="Who holds it" style="min-width:120px">
              <button class="btn-secondary" id="cu-add" style="font-size:.8rem">Add</button>
            </div>
            <div style="display:flex;gap:.4rem;margin-top:.7rem;align-items:center">
              <button class="btn-primary" id="cont-pack" style="font-size:.8rem">⬇ Continuity pack (superadmin)</button>
              <span style="font-size:.72rem;color:var(--color-text-muted)">The successor's map — sealed & audited. Print after material changes.</span>
            </div>
          </div>
        </section>` : ''}

        ${isAdmin() ? `
        <section class="card" style="padding:1.25rem;grid-column:1 / -1" id="groups-section">
          <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:.35rem">
            <h2 style="font-size:1rem;margin:0">Visibility groups</h2>
            <button class="btn-secondary" id="group-add-btn" style="font-size:.8rem">+ New group</button>
          </div>
          <p style="font-size:.83rem;color:var(--color-text-muted);margin:0 0 .75rem">
            Groups constrain who can see a resource in the library. A resource with no groups is visible to
            all members; officers and admins always see everything.</p>
          <div id="groups-list"><span class="spinner"></span></div>
        </section>
        <section class="card" style="padding:1.25rem">
          <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:1rem">
            <h2 style="font-size:1rem">User accounts</h2>
            <button class="btn-primary" id="add-user-btn" style="font-size:.8rem;padding:.3rem .75rem">+ Add user</button>
          </div>
          <div style="overflow-x:auto"><table>
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
          </table></div>
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

    const themeSel = this.querySelector('#theme-sel');
    if (themeSel) {
      themeSel.value = getTheme();
      themeSel.addEventListener('change', e => applyTheme(e.target.value));
    }
    this._renderReportSubs();

    this.querySelector('#twofa-setup-btn')?.addEventListener('click', () => this.open2FASetup());
    this.querySelector('#twofa-disable-btn')?.addEventListener('click', () => this.open2FADisable());
    this.querySelector('#twofa-codes-btn')?.addEventListener('click', () => this.openRegenerateCodes());
    this.loadSessionCount();
    this.loadGroups();
    this.querySelector('#group-add-btn')?.addEventListener('click', () => this.openGroupModal(null));
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

  /** Admin: visibility groups — list, create/edit (with member picker), delete. */
  async loadGroups() {
    const host = this.querySelector('#groups-list');
    if (!host) return;
    try {
      const groups = await api('GET', '/groups') ?? [];
      host.innerHTML = groups.length ? groups.map(g => `
        <div style="display:flex;justify-content:space-between;align-items:center;padding:.45rem 0;border-bottom:1px solid var(--color-border,#eee)">
          <div>
            <strong>${esc(g.name)}</strong>
            <span style="font-size:.8rem;color:var(--color-text-muted)"> · ${g.member_count} member${g.member_count === 1 ? '' : 's'}</span>
            ${g.description ? `<div style="font-size:.8rem;color:var(--color-text-muted)">${esc(g.description)}</div>` : ''}
          </div>
          <div style="display:flex;gap:.4rem">
            <button class="btn-ghost group-edit" data-id="${esc(g.id)}" style="font-size:.78rem">Edit</button>
            <button class="btn-ghost group-del" data-id="${esc(g.id)}" data-name="${esc(g.name)}" style="font-size:.78rem;color:var(--color-danger)">Delete</button>
          </div>
        </div>`).join('')
        : '<p style="font-size:.85rem;color:var(--color-text-muted)">No groups yet — every resource is visible to all members.</p>';
      host.querySelectorAll('.group-edit').forEach(b => b.addEventListener('click', async () => {
        try { this.openGroupModal(await api('GET', `/groups/${b.dataset.id}`)); }
        catch { toast('Failed to load group', 'error'); }
      }));
      host.querySelectorAll('.group-del').forEach(b => b.addEventListener('click', () => {
        confirmDelete({
          noun: 'group (resources restricted only by it become visible to ALL members)',
          name: b.dataset.name,
          onConfirm: async (confirmVal) => {
            try {
              await api('DELETE', `/groups/${b.dataset.id}?confirm=${encodeURIComponent(confirmVal)}`);
              toast('Group deleted', 'success');
              this.loadGroups();
            } catch (err) { toast(err.error ?? 'Delete failed', 'error'); throw err; }
          },
        });
      }));
    } catch {
      host.innerHTML = '<p style="color:var(--color-text-muted)">Failed to load groups.</p>';
    }
  }

  async openGroupModal(group) {
    const isNew = !group;
    let members = [];
    try { members = (await api('GET', '/members?limit=200'))?.data ?? []; }
    catch { toast('Failed to load members', 'error'); return; }
    const inGroup = new Set(group?.member_ids ?? []);
    const { dialog, close } = openModal({
      title: isNew ? 'New visibility group' : `Edit group — ${group.name}`,
      maxWidth: '520px',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="g-name">Name *</label><input id="g-name" value="${esc(group?.name ?? '')}" placeholder="e.g. Board, Finance committee"></div>
          <div class="form-group"><label for="g-desc">Description</label><input id="g-desc" value="${esc(group?.description ?? '')}"></div>
          <div class="form-group"><label>Members (${members.length})</label>
            <div style="max-height:220px;overflow-y:auto;border:1px solid var(--color-border);border-radius:var(--radius);padding:.5rem" id="g-members">
              ${members.map(m => `
                <label style="display:flex;align-items:center;gap:.5rem;margin:0;padding:.2rem 0;cursor:pointer;text-transform:none;font-weight:400;letter-spacing:normal;color:var(--color-text);font-size:.88rem">
                  <input type="checkbox" value="${esc(m.id)}" ${inGroup.has(m.id) ? 'checked' : ''} style="width:auto;margin:0">
                  ${esc(m.display_name)}${m.email ? ` <span style="color:var(--color-text-muted);font-size:.78rem">${esc(m.email)}</span>` : ''}
                </label>`).join('')}
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">${isNew ? 'Create' : 'Save'}</button>
        </div>`,
    });
    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const name = dialog.querySelector('#g-name').value.trim();
      if (!name) { toast('Name is required', 'error'); return; }
      const member_ids = [...dialog.querySelectorAll('#g-members input:checked')].map(c => c.value);
      try {
        let id = group?.id;
        if (isNew) {
          const created = await api('POST', '/groups', { name, description: dialog.querySelector('#g-desc').value.trim() || null });
          id = created.id;
        } else {
          await api('PATCH', `/groups/${id}`, { name, description: dialog.querySelector('#g-desc').value.trim() || null });
        }
        await api('PUT', `/groups/${id}/members`, { member_ids });
        toast(isNew ? 'Group created' : 'Group saved', 'success');
        close();
        this.loadGroups();
      } catch (err) { toast(err.error ?? 'Save failed', 'error'); }
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
          ${setup.qr_png_base64 ? `
          <div style="text-align:center;margin-bottom:.6rem">
            <img src="data:image/png;base64,${setup.qr_png_base64}" alt="QR code — scan with your authenticator app"
                 width="190" height="190" style="border-radius:8px;background:#fff;padding:6px">
          </div>
          <p style="font-size:.85rem;text-align:center;margin-bottom:.8rem">Scan the code with your authenticator app — or add the key manually:</p>` : `
          <p style="font-size:.88rem;margin:0 0 .6rem">In your authenticator app, add an account and enter this key:</p>`}
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

  /** Emailed report-digest subscriptions (officer+). Each report → cadence. */
  async _renderReportSubs() {
    const host = this.querySelector('#report-subs');
    if (!host) return;
    let subs = [];
    try { subs = await api('GET', '/me/report-subscriptions') ?? []; }
    catch { host.innerHTML = '<p style="font-size:.82rem;color:var(--color-text-muted)">Not available.</p>'; return; }
    const current = {};
    subs.forEach(s => { current[s.report] = s.cadence; });
    const reports = [
      ['ar_aging', 'Receivables aging'],
      ['ap_aging', 'Payables aging'],
      ['income_statement', 'Income statement'],
    ];
    host.innerHTML = reports.map(([key, label]) => `
      <div style="display:flex;align-items:center;gap:.6rem;margin-bottom:.4rem">
        <span style="flex:1;font-size:.88rem">${esc(label)}</span>
        <select data-report="${key}" style="max-width:9rem">
          <option value="off" ${!current[key] ? 'selected' : ''}>Off</option>
          <option value="weekly" ${current[key] === 'weekly' ? 'selected' : ''}>Weekly</option>
          <option value="monthly" ${current[key] === 'monthly' ? 'selected' : ''}>Monthly</option>
        </select>
      </div>`).join('');
    host.querySelectorAll('select[data-report]').forEach(sel => sel.addEventListener('change', async () => {
      try {
        await api('PUT', '/me/report-subscriptions', { report: sel.dataset.report, cadence: sel.value });
        toast('Saved', 'success');
      } catch (err) { toast(err.error ?? 'Save failed', 'error'); }
    }));
  }
}
customElements.define('page-settings', PageSettings);


// Org settings card (admin): populate + save.
(function () {
  const proto = customElements.get('page-settings')?.prototype;
  if (!proto) return;
  // Hook render(), not connectedCallback: the base class re-renders on
  // several events, and each re-render replaces the DOM (and any listeners
  // wired from outside). Re-wire after every render instead.
  const origRender = proto.render;
  proto.render = async function (...args) {
    await origRender.apply(this, args);
    (async () => {
      if (!this.querySelector('#og-save')) return; // non-admin: card absent
      const { api } = await import('../app.js');
      const { toast } = await import('./toast-notification.js');
      const { esc, setOrgFlags } = await import('../utils.js');
      try {
        const st = await api('GET', '/settings/org');
        const set = (id, v) => { const el = this.querySelector(id); if (el && v != null) el.value = v; };
        set('#og-fy', st.fiscal_year_start_month); set('#og-pay', st.how_to_pay);
        set('#og-tz', st.timezone);
        set('#og-latefee', st.late_fee_minor); set('#og-lategrace', st.late_fee_grace_days);
        set('#og-agendatpl', st.agenda_templates);
        set('#og-2fa', st.require_2fa ?? 'off');
        set('#og-infra', st.infrastructure_facts); set('#og-watch', st.continuity_watch_days);
        set('#og-paylink', st.payment_link_template); set('#og-vocab', st.vocab_overrides);
        set('#og-lapse', st.lapse_after_days); set('#og-monthly', st.monthly_report_email);
        if (this.querySelector('#og-hold')) this.querySelector('#og-hold').value = st.audit_legal_hold || 'off';
        if (this.querySelector('#og-arcade')) this.querySelector('#og-arcade').value = st.arcade_visible || 'on';
        set('#og-contacts', st.continuity_contacts);
      } catch { /* defaults are fine */ }
      this.querySelector('#og-save')?.addEventListener('click', async () => {
        try {
          const saved = await api('PUT', '/settings/org', {
            fiscal_year_start_month: String(this.querySelector('#og-fy').value || '1'),
            timezone: this.querySelector('#og-tz').value.trim(),
            late_fee_minor: this.querySelector('#og-latefee').value.trim(),
            late_fee_grace_days: this.querySelector('#og-lategrace').value.trim(),
            agenda_templates: this.querySelector('#og-agendatpl').value.trim(),
            require_2fa: this.querySelector('#og-2fa').value,
            how_to_pay: this.querySelector('#og-pay').value,
            payment_link_template: this.querySelector('#og-paylink').value,
            vocab_overrides: this.querySelector('#og-vocab').value || '{}',
            lapse_after_days: String(this.querySelector('#og-lapse').value || '0'),
            monthly_report_email: this.querySelector('#og-monthly').value,
            audit_legal_hold: this.querySelector('#og-hold').value,
            arcade_visible: this.querySelector('#og-arcade').value,
            infrastructure_facts: this.querySelector('#og-infra').value,
            continuity_watch_days: String(this.querySelector('#og-watch').value || '0'),
            continuity_contacts: this.querySelector('#og-contacts').value,
          });
          // The PUT echoes the fresh settings map; feed it to the shell so
          // flag-driven chrome (the Top Secret nav section) updates now, not
          // at the next full page load.
          setOrgFlags(saved);
          document.dispatchEvent(new CustomEvent('route-changed', { detail: location.hash }));
          toast('Org settings saved', 'success');
        } catch (err) { toast(err.error ?? 'Save failed', 'error'); }
      });

      // Continuity: custody registry + checks.
      const loadCont = async () => {
        const box = this.querySelector('#cont-rows');
        if (!box) return;
        try {
          const [rows, checks] = await Promise.all([
            api('GET', '/continuity/custody'), api('GET', '/continuity/checks'),
          ]);
          const c = this.querySelector('#cont-checks');
          const bad = [];
          if (!checks.superadmins_ok) bad.push(`⚠ only ${checks.superadmins} superadmin — bus risk`);
          if (checks.custody_rows === 0) bad.push('⚠ custody registry empty');
          if (checks.custody_stale > 0) bad.push(`⚠ ${checks.custody_stale} custody item(s) unverified in ${checks.attest_days}d`);
          if (!checks.watch_configured) bad.push('· inactivity watchdog off');
          if (checks.tls_days_left != null && checks.tls_days_left < 21) bad.push(`⚠ TLS cert expires in ${checks.tls_days_left}d`);
          c.innerHTML = bad.length
            ? `<span style="color:var(--color-warning,#b45309)">${bad.map(esc).join(' &nbsp; ')}</span>`
            : '<span style="color:var(--color-success,#137333)">✓ continuity checks green</span>';
          box.innerHTML = (rows ?? []).map(r0 => `
            <div style="display:flex;gap:.5rem;align-items:center;font-size:.82rem;padding:.15rem 0;border-bottom:1px solid var(--color-border)">
              <b style="min-width:140px">${esc(r0.name)}</b>
              <span style="flex:1;color:var(--color-text-muted)">${esc(r0.location)} — ${esc(r0.holder)}</span>
              <span style="font-size:.72rem;color:var(--color-text-muted)">${r0.last_verified_at ? 'verified ' + esc(new Date(r0.last_verified_at).toLocaleDateString()) + ' by ' + esc(r0.last_verified_by) : 'never verified'}</span>
              <button class="btn-ghost cu-attest" data-id="${esc(r0.id)}" title="I verified this copy exists today" style="font-size:.75rem">✓ attest</button>
              <button class="btn-ghost cu-del" data-id="${esc(r0.id)}" aria-label="Delete custody record" title="Delete custody record" style="color:var(--color-danger);padding:0 .3rem">✕</button>
            </div>`).join('') || '<div style="font-size:.8rem;color:var(--color-text-muted)">Register where each critical secret lives (values never enter Quorum).</div>';
          box.querySelectorAll('.cu-attest').forEach(b => b.addEventListener('click', async () => {
            try { await api('POST', `/continuity/custody/${b.dataset.id}/attest`); loadCont(); }
            catch (err) { toast(err.error ?? 'Attest failed', 'error'); }
          }));
          box.querySelectorAll('.cu-del').forEach(b => b.addEventListener('click', async () => {
            try { await api('DELETE', `/continuity/custody/${b.dataset.id}`); loadCont(); }
            catch (err) { toast(err.error ?? 'Delete failed', 'error'); }
          }));
        } catch { /* non-admin or transient */ }
      };
      this.querySelector('#cu-add')?.addEventListener('click', async () => {
        try {
          await api('POST', '/continuity/custody', {
            Name: this.querySelector('#cu-name').value,
            Location: this.querySelector('#cu-loc').value,
            Holder: this.querySelector('#cu-holder').value,
          });
          ['#cu-name', '#cu-loc', '#cu-holder'].forEach(i => this.querySelector(i).value = '');
          loadCont();
        } catch (err) { toast(err.error ?? 'Add failed', 'error'); }
      });
      this.querySelector('#cont-pack')?.addEventListener('click', async () => {
        const { apiDownload } = await import('../app.js');
        apiDownload('/reports/continuity-pack.zip', 'quorum-continuity-pack.zip')
          .catch(err => toast(err.error ?? 'Superadmin only', 'error'));
      });
      loadCont();
    })();
  };
})();
