import { api, canWrite, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDate, openModal, guardButton } from '../utils.js';

/**
 * Org roster: current office holders (with history), committees, and the
 * membership-application queue. None of these grant access — they're the
 * organizational record that sits alongside the permission model.
 */
class PageRoster extends HTMLElement {
  connectedCallback() {
    this.innerHTML = `
      <div class="page-header"><h1>Roster &amp; committees</h1></div>
      <section class="card" style="padding:1rem 1.25rem;margin-bottom:1rem">
        <div style="display:flex;justify-content:space-between;align-items:baseline">
          <h2 style="font-size:1rem;margin:0">Office holders</h2>
          ${isAdmin() ? '<button class="btn-secondary" id="add-office" style="font-size:.8rem">+ Record office</button>' : ''}
        </div>
        <div id="offices" style="margin-top:.6rem"><span class="spinner"></span></div>
      </section>
      <section class="card" style="padding:1rem 1.25rem;margin-bottom:1rem">
        <div style="display:flex;justify-content:space-between;align-items:baseline">
          <h2 style="font-size:1rem;margin:0">Committees</h2>
          ${isAdmin() ? '<button class="btn-secondary" id="add-committee" style="font-size:.8rem">+ New committee</button>' : ''}
        </div>
        <div id="committees" style="margin-top:.6rem"><span class="spinner"></span></div>
      </section>
      ${canWrite() ? `
      <section class="card" style="padding:1rem 1.25rem">
        <h2 style="font-size:1rem;margin:0 0 .6rem">Membership applications</h2>
        <div id="applications"><span class="spinner"></span></div>
      </section>` : ''}`;
    this.querySelector('#add-office')?.addEventListener('click', () => this.openOfficeModal());
    this.querySelector('#add-committee')?.addEventListener('click', () => this.openCommitteeModal(null));
    this.loadOffices();
    this.loadCommittees();
    if (canWrite()) this.loadApplications();
  }

  async _members() {
    if (this._m) return this._m;
    const p = await api('GET', '/members?limit=200&status=active').catch(() => null);
    this._m = p?.data ?? [];
    return this._m;
  }

  async loadOffices() {
    const box = this.querySelector('#offices');
    let terms;
    try { terms = await api('GET', '/office-terms') ?? []; } catch { box.innerHTML = '<p class="empty-state">Failed to load.</p>'; return; }
    if (!terms.length) { box.innerHTML = '<p style="color:var(--color-text-muted);font-size:.88rem">No offices recorded.</p>'; return; }
    box.innerHTML = terms.map(t => `
      <div style="display:flex;justify-content:space-between;align-items:baseline;padding:.3rem 0;border-bottom:1px solid var(--color-border);font-size:.9rem">
        <span><strong>${esc(t.title)}</strong> — ${esc(t.member_name)}
          <span style="color:var(--color-text-muted);font-size:.8rem">
            ${esc(fmtDate(t.started_on))}${t.ended_on ? ' – ' + esc(fmtDate(t.ended_on)) : ' – present'}</span></span>
        ${isAdmin() && !t.ended_on ? `<button class="btn-ghost end-office" data-id="${esc(t.id)}" style="font-size:.75rem">End term</button>` : ''}
      </div>`).join('');
    box.querySelectorAll('.end-office').forEach(b => b.addEventListener('click', async () => {
      if (!confirm('End this office term as of today?')) return;
      try { await api('POST', `/office-terms/${b.dataset.id}/end`); toast('Term ended', 'success'); this.loadOffices(); }
      catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  async openOfficeModal() {
    const members = await this._members();
    const { dialog, close } = openModal({
      title: 'Record an office',
      maxWidth: '440px',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="of-title">Office title *</label>
            <input id="of-title" placeholder="Treasurer"></div>
          <div class="form-group"><label for="of-member">Member *</label>
            <select id="of-member">${members.map(m => `<option value="${esc(m.id)}">${esc(m.display_name)}</option>`).join('')}</select></div>
          <div class="form-group"><label for="of-start">Start date</label><input id="of-start" type="date"></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="of-cancel">Cancel</button>
          <button class="btn-primary" id="of-save">Save</button>
        </div>`,
    });
    dialog.querySelector('#of-cancel').addEventListener('click', close);
    const save = dialog.querySelector('#of-save');
    save.addEventListener('click', guardButton(save, async () => {
      const title = dialog.querySelector('#of-title').value.trim();
      if (!title) { toast('Title required', 'error'); return; }
      try {
        await api('POST', '/office-terms', {
          title, member_id: dialog.querySelector('#of-member').value,
          started_on: dialog.querySelector('#of-start').value || '',
        });
        toast('Office recorded', 'success'); close(); this.loadOffices();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  async loadCommittees() {
    const box = this.querySelector('#committees');
    let list;
    try { list = await api('GET', '/committees') ?? []; } catch { box.innerHTML = '<p class="empty-state">Failed to load.</p>'; return; }
    if (!list.length) { box.innerHTML = '<p style="color:var(--color-text-muted);font-size:.88rem">No committees yet.</p>'; return; }
    box.innerHTML = list.map(c => `
      <div style="padding:.4rem 0;border-bottom:1px solid var(--color-border);font-size:.9rem">
        <div style="display:flex;justify-content:space-between;align-items:baseline">
          <span><strong>${esc(c.name)}</strong>
            ${c.chair_name ? `<span style="color:var(--color-text-muted);font-size:.8rem"> · chair: ${esc(c.chair_name)}</span>` : ''}
            <span style="color:var(--color-text-muted);font-size:.8rem"> · ${c.member_count} member${c.member_count === 1 ? '' : 's'}</span></span>
          ${isAdmin() ? `<span style="white-space:nowrap">
            <button class="btn-ghost edit-committee" data-id="${esc(c.id)}" style="font-size:.75rem">Edit</button>
            <button class="btn-ghost del-committee" data-id="${esc(c.id)}" style="font-size:.75rem;color:var(--color-danger)">Delete</button></span>` : ''}
        </div>
        ${c.purpose ? `<div style="color:var(--color-text-muted);font-size:.82rem">${esc(c.purpose)}</div>` : ''}
      </div>`).join('');
    box.querySelectorAll('.edit-committee').forEach(b => b.addEventListener('click', async () => {
      const c = await api('GET', `/committees/${b.dataset.id}`).catch(() => null);
      if (c) this.openCommitteeModal(c);
    }));
    box.querySelectorAll('.del-committee').forEach(b => b.addEventListener('click', async () => {
      if (!confirm('Delete this committee?')) return;
      try { await api('DELETE', `/committees/${b.dataset.id}`); toast('Deleted', 'success'); this.loadCommittees(); }
      catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  async openCommitteeModal(committee) {
    const members = await this._members();
    const chosen = new Set((committee?.members ?? []).map(m => m.member_id));
    const { dialog, close } = openModal({
      title: committee ? 'Edit committee' : 'New committee',
      maxWidth: '520px',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="cm-name">Name *</label>
            <input id="cm-name" value="${esc(committee?.name ?? '')}"></div>
          <div class="form-group"><label for="cm-purpose">Purpose</label>
            <textarea id="cm-purpose" rows="2">${esc(committee?.purpose ?? '')}</textarea></div>
          <div class="form-group"><label for="cm-chair">Chair</label>
            <select id="cm-chair"><option value="">— none —</option>
              ${members.map(m => `<option value="${esc(m.id)}" ${committee?.chair_id === m.id ? 'selected' : ''}>${esc(m.display_name)}</option>`).join('')}</select></div>
          <div class="form-group"><label>Members</label>
            <div id="cm-members" style="max-height:160px;overflow-y:auto;border:1px solid var(--color-border);border-radius:var(--radius);padding:.4rem .6rem">
              ${members.map(m => `<label style="display:flex;gap:.45rem;align-items:center;font-size:.85rem;padding:.1rem 0;text-transform:none;letter-spacing:normal;font-weight:400;margin-bottom:0">
                <input type="checkbox" class="cm-cb" value="${esc(m.id)}" ${chosen.has(m.id) ? 'checked' : ''} style="width:auto"><span>${esc(m.display_name)}</span></label>`).join('')}
            </div></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cm-cancel">Cancel</button>
          <button class="btn-primary" id="cm-save">Save</button>
        </div>`,
    });
    dialog.querySelector('#cm-cancel').addEventListener('click', close);
    const save = dialog.querySelector('#cm-save');
    save.addEventListener('click', guardButton(save, async () => {
      const name = dialog.querySelector('#cm-name').value.trim();
      if (!name) { toast('Name required', 'error'); return; }
      const payload = {
        name,
        purpose: dialog.querySelector('#cm-purpose').value.trim() || null,
        chair_id: dialog.querySelector('#cm-chair').value || null,
      };
      const memberIDs = [...dialog.querySelectorAll('.cm-cb:checked')].map(cb => cb.value);
      try {
        const c = committee
          ? await api('PATCH', `/committees/${committee.id}`, payload)
          : await api('POST', '/committees', payload);
        await api('PUT', `/committees/${c.id}/members`, { member_ids: memberIDs });
        toast('Committee saved', 'success'); close(); this.loadCommittees();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  async loadApplications() {
    const box = this.querySelector('#applications');
    if (!box) return;
    let list;
    try { list = await api('GET', '/join-requests') ?? []; } catch { box.innerHTML = '<p class="empty-state">Failed to load.</p>'; return; }
    if (!list.length) { box.innerHTML = '<p style="color:var(--color-text-muted);font-size:.88rem">No pending applications.</p>'; return; }
    box.innerHTML = list.map(j => `
      <div style="display:flex;justify-content:space-between;gap:1rem;flex-wrap:wrap;align-items:baseline;padding:.4rem 0;border-bottom:1px solid var(--color-border);font-size:.9rem">
        <div><strong>${esc(j.name)}</strong> · ${esc(j.email)}
          ${j.message ? `<div style="color:var(--color-text-muted);font-size:.82rem">${esc(j.message)}</div>` : ''}
          <span style="color:var(--color-text-muted);font-size:.75rem">applied ${esc(fmtDate(j.created_at))}</span></div>
        <span style="white-space:nowrap">
          <button class="btn-primary jr-approve" data-id="${esc(j.id)}" style="font-size:.78rem">Approve</button>
          <button class="btn-ghost jr-reject" data-id="${esc(j.id)}" style="font-size:.78rem;color:var(--color-danger)">Reject</button></span>
      </div>`).join('');
    box.querySelectorAll('.jr-approve').forEach(b => b.addEventListener('click', async () => {
      try { await api('POST', `/join-requests/${b.dataset.id}/approve`, {}); toast('Approved — member created', 'success'); this.loadApplications(); }
      catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
    box.querySelectorAll('.jr-reject').forEach(b => b.addEventListener('click', async () => {
      if (!confirm('Reject this application?')) return;
      try { await api('POST', `/join-requests/${b.dataset.id}/reject`); toast('Rejected', 'success'); this.loadApplications(); }
      catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }
}
customElements.define('page-roster', PageRoster);
