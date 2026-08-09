import { api, apiDownload, canWrite, isAuthenticated, isSuperadmin, currentMemberId } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDateTime, openModal, guardButton, toLocalInputValue, confirmDelete } from '../utils.js';
import { assembleMinutesText, openHeatmapModal } from './word-heatmap.js';
import './vote-tally.js';

/** Human labels + badge classes reused across the governance UI. */
const MOTION_BADGE = {
  draft: 'none', seconded: 'open', open: 'pending',
  carried: 'paid', failed: 'overdue', tabled: 'none', withdrawn: 'none',
};
const THRESHOLD_LABEL = { majority: 'Simple majority', two_thirds: 'Two-thirds', unanimous: 'Unanimous' };

/** Parses an integer form field, preserving a legitimate 0 and mapping blank → null. */
function intOrNull(v) {
  if (v == null || String(v).trim() === '') return null;
  const n = Number.parseInt(v, 10);
  return Number.isFinite(n) ? n : null;
}

class PageMeetings extends HTMLElement {
  constructor() { super(); this._meetings = []; this._upcoming = false; this._seq = 0; }

  connectedCallback() {
    this.render();
    this.load();
  }

  /** Renders the static page shell once; load() only touches #meeting-list. */
  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Meetings</h1>
        ${canWrite() ? '<button class="btn-primary" id="add-btn">+ Schedule meeting</button>' : ''}
      </div>
      <div class="search-bar">
        <label style="flex-direction:row;align-items:center;gap:.4rem;text-transform:none;letter-spacing:0;font-weight:400" for="upcoming-chk">
          <input type="checkbox" id="upcoming-chk" ${this._upcoming?'checked':''}> Upcoming only
        </label>
      </div>
      <div style="display:flex;flex-direction:column;gap:.75rem" id="meeting-list"></div>
    `;

    this.querySelector('#upcoming-chk')?.addEventListener('change', e => { this._upcoming = e.target.checked; this.load(); });
    this.querySelector('#add-btn')?.addEventListener('click', () => this.openCreateModal());
  }

  async load() {
    const seq = ++this._seq;
    const list = this.querySelector('#meeting-list');
    list.innerHTML = '<div style="text-align:center;padding:1rem"><span class="spinner"></span></div>';
    const params = this._upcoming ? '?upcoming=true' : '';
    try {
      const _mtPage = await api('GET', '/meetings' + params);
      if (seq !== this._seq) return; // A newer load() superseded this one.
      this._meetings = _mtPage?.data ?? _mtPage ?? [];
      this.renderList(list);
    } catch {
      if (seq !== this._seq) return;
      list.innerHTML = '<div class="empty-state"><p>Failed to load meetings.</p></div>';
      toast('Failed to load meetings', 'error');
    }
  }

  renderList(list) {
    list.innerHTML = this._meetings?.length
      ? this._meetings.map(m => `
          <div class="card meeting-card" data-id="${esc(m.id)}" style="padding:1rem 1.25rem;cursor:pointer;display:flex;align-items:center;gap:1rem" tabindex="0" role="button">
            <div style="flex:1">
              <div style="font-weight:700;font-size:1rem">${esc(m.title)}</div>
              <div style="font-size:.85rem;color:var(--color-text-muted)">${fmtDateTime(m.scheduled_at)}${m.ends_at ? ' – ' + esc(new Date(m.ends_at).toLocaleTimeString(undefined,{hour:'numeric',minute:'2-digit'})) : ''}${m.location ? ' · ' + esc(m.location) : ''}</div>
            </div>
            <span class="badge badge-${esc(m.status)}">${esc(m.status)}</span>
            ${isSuperadmin() ? `<button class="btn-ghost del-btn" data-id="${esc(m.id)}" style="color:var(--color-danger)">Del</button>` : ''}
          </div>`).join('')
      : '<div class="empty-state"><p>No meetings found.</p></div>';

    list.querySelectorAll('.meeting-card').forEach(card => {
      const open = e => {
        if (e.target.classList.contains('del-btn')) return;
        this.openEditor(card.dataset.id);
      };
      card.addEventListener('click', open);
      card.addEventListener('keydown', e => {
        // Let nested action buttons handle their own Enter/Space activation;
        // only the card itself (when directly focused) opens on keyboard.
        if (e.target !== card && e.target.closest('button, a, input, select, textarea')) return;
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(e); }
      });
    });
    list.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        const m = this._meetings.find(x => x.id === btn.dataset.id);
        confirmDelete({
          noun: 'meeting',
          name: m?.title ?? '',
          onConfirm: async (confirmVal) => {
            try {
              await api('DELETE', `/meetings/${btn.dataset.id}?confirm=${encodeURIComponent(confirmVal)}`);
              toast('Deleted','success');
              this.load();
            } catch (err) { toast(err.error ?? 'Delete failed','error'); throw err; }
          },
        });
      });
    });
  }

  openCreateModal() {
    const { dialog, close } = openModal({
      title: 'Schedule meeting',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="f-title">Title *</label><input id="f-title"></div>
          <div class="form-row">
            <div class="form-group"><label for="f-dt">Starts *</label><input id="f-dt" type="datetime-local"></div>
            <div class="form-group"><label for="f-end">Ends</label><input id="f-end" type="datetime-local"></div>
          </div>
          <div class="form-group"><label for="f-loc">Location</label><input id="f-loc" placeholder="Room or video link"></div>
          <div class="form-group"><label for="f-agenda">Agenda</label><textarea id="f-agenda" rows="4"></textarea></div>
          <div class="form-group"><label>Attendees (optional — you can also edit them later)</label>
            <div id="new-attendance"><span class="spinner"></span></div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Schedule</button>
        </div>
      `,
    });

    let newRoster = () => [];
    this.loadRosterData().then(({ members, groups }) => {
      if (!dialog.isConnected) return;
      newRoster = this.mountRosterPicker(dialog.querySelector('#new-attendance'), members, groups, new Map()).roster;
    }).catch(() => {
      dialog.querySelector('#new-attendance').innerHTML =
        '<p style="color:var(--color-text-muted);font-size:.85rem">Could not load the member list — you can set attendees after scheduling.</p>';
    });

    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const title = dialog.querySelector('#f-title').value.trim();
      const dt    = dialog.querySelector('#f-dt').value;
      if (!title || !dt) { toast('Title and date are required','error'); return; }
      try {
        const end = dialog.querySelector('#f-end').value;
        if (end && new Date(end) <= new Date(dt)) { toast('End must be after the start','error'); return; }
        const m = await api('POST', '/meetings', {
          title,
          scheduled_at: new Date(dt).toISOString(),
          ends_at: end ? new Date(end).toISOString() : null,
          location: dialog.querySelector('#f-loc').value.trim() || null,
          agenda:   dialog.querySelector('#f-agenda').value.trim() || null,
        });
        const roster = newRoster();
        if (roster.length) {
          try { await api('PUT', `/meetings/${m.id}/attendees`, { attendees: roster }); }
          catch { toast('Meeting created, but saving attendees failed — set them in the editor', 'error'); }
        }
        toast('Meeting scheduled','success');
        close();
        this.load();
        setTimeout(() => this.openEditor(m.id), 100);
      } catch (err) { toast(err.error ?? 'Failed','error'); }
    }));
  }

  async openEditor(id) {
    let mt;
    try { mt = await api('GET', `/meetings/${id}`); }
    catch { toast('Load failed','error'); return; }

    const { dialog, close } = openModal({
      title: mt.title,
      maxWidth: '880px',
      body: `
        <div class="modal-body" style="display:grid;grid-template-columns:1fr 1fr;gap:1.25rem">
          <div>
            <div class="form-group"><label for="f-title">Title</label><input id="f-title" value="${esc(mt.title)}"></div>
            <div class="form-row">
              <div class="form-group"><label for="f-dt">Starts</label><input id="f-dt" type="datetime-local" value="${esc(toLocalInputValue(mt.scheduled_at))}"></div>
              <div class="form-group"><label for="f-end">Ends</label><input id="f-end" type="datetime-local" value="${mt.ends_at ? esc(toLocalInputValue(mt.ends_at)) : ''}"></div>
              <div class="form-group">
                <label for="f-status">Status</label>
                <select id="f-status">
                  ${['scheduled','completed','cancelled'].map(s=>`<option value="${s}" ${mt.status===s?'selected':''}>${s}</option>`).join('')}
                </select>
              </div>
            </div>
            <div class="form-group"><label for="f-loc">Location</label><input id="f-loc" value="${esc(mt.location??'')}"></div>
            <div class="form-group"><label for="f-agenda">Agenda</label><textarea id="f-agenda" rows="5">${esc(mt.agenda??'')}</textarea></div>
            <div class="form-group"><label for="f-notes">Minutes / notes</label><textarea id="f-notes" rows="7">${esc(mt.notes??'')}</textarea></div>
          </div>
          <div>
            <h3 style="margin-bottom:.75rem;font-size:.95rem">Decisions</h3>
            <div id="decisions-list"></div>
            ${canWrite()?`
              <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.75rem;margin-top:.5rem">
                <div class="form-group"><label for="new-decision">Decision summary</label><input id="new-decision" placeholder="What was decided?"></div>
                <div class="form-row">
                  <div class="form-group"><label for="new-outcome">Outcome</label>
                    <select id="new-outcome">
                      <option>passed</option><option>failed</option><option>tabled</option><option>noted</option>
                    </select>
                  </div>
                  <div class="form-group" style="max-width:70px"><label for="new-vfor">For</label><input id="new-vfor" type="number" min="0" placeholder="0"></div>
                  <div class="form-group" style="max-width:70px"><label for="new-vagainst">Agn</label><input id="new-vagainst" type="number" min="0" placeholder="0"></div>
                  <div class="form-group" style="max-width:70px"><label for="new-vabs">Abs</label><input id="new-vabs" type="number" min="0" placeholder="0"></div>
                </div>
                <button class="btn-secondary" id="add-decision-btn" style="width:100%">+ Add decision</button>
              </div>`:''}
            <h3 style="margin:1rem 0 .5rem;font-size:.95rem">RSVP</h3>
            <div id="rsvp-section"><span class="spinner"></span></div>
            <h3 style="margin:1rem 0 .5rem;font-size:.95rem">Attendance</h3>
            <div id="attendance-section"><span class="spinner"></span></div>
          </div>

          <div id="gov-section" style="grid-column:1 / -1;border-top:1px solid var(--color-border);padding-top:1rem">
            <div style="text-align:center;padding:.5rem"><span class="spinner"></span></div>
          </div>

          <div id="minutes-section" style="grid-column:1 / -1;border-top:1px solid var(--color-border);padding-top:1rem">
            <div style="text-align:center;padding:.5rem"><span class="spinner"></span></div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Close</button>
          ${canWrite()?'<button class="btn-primary" id="save-btn">Save changes</button>':''}
        </div>
      `,
    });

    // Renders only the decisions region so unsaved edits in the form survive
    // adding/removing a decision.
    const renderDecisions = decisions => {
      const listEl = dialog.querySelector('#decisions-list');
      listEl.innerHTML = (decisions ?? []).map(d => `
        <div class="decision-item" style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.65rem .8rem;margin-bottom:.5rem">
          <div style="font-weight:600">${esc(d.summary)}</div>
          <div style="font-size:.8rem;color:var(--color-text-muted)">${esc(d.outcome)} ${d.vote_for!=null?`· ${esc(d.vote_for)}/${esc(d.vote_against)}/${esc(d.vote_abstain)}`:''}</div>
          ${canWrite()?`<button class="btn-ghost del-decision" data-id="${esc(d.id)}" style="font-size:.75rem;color:var(--color-danger)">Remove</button>`:''}
        </div>`).join('');

      listEl.querySelectorAll('.del-decision').forEach(btn => {
        btn.addEventListener('click', async () => {
          try {
            await api('DELETE', `/meetings/${id}/decisions/${btn.dataset.id}`);
            toast('Removed','success');
            await refreshDecisions();
          } catch { toast('Failed','error'); }
        });
      });
    };

    const refreshDecisions = async () => {
      try {
        const fresh = await api('GET', `/meetings/${id}`);
        renderDecisions(fresh.decisions);
      } catch { toast('Failed to refresh decisions','error'); }
    };

    renderDecisions(mt.decisions);
    this.renderRSVP(dialog, id);
    this.renderAttendance(dialog, id, mt);
    this.renderGovernance(dialog, id);
    this.renderMinutes(dialog, id, mt);

    // Reload the list whenever the editor closes (close button, Cancel, or Escape),
    // but only while still mounted and authenticated so a logout-triggered close
    // (openModal force-closes on auth-changed) doesn't fire a stray authed fetch.
    dialog.addEventListener('close', () => { if (this.isConnected && isAuthenticated()) this.load(); });
    dialog.querySelector('#cancel-btn').addEventListener('click', close);

    dialog.querySelector('#add-decision-btn')?.addEventListener('click', async () => {
      const summary = dialog.querySelector('#new-decision').value.trim();
      if (!summary) { toast('Summary required','error'); return; }
      try {
        await api('POST', `/meetings/${id}/decisions`, {
          summary,
          outcome:      dialog.querySelector('#new-outcome').value,
          vote_for:     intOrNull(dialog.querySelector('#new-vfor').value),
          vote_against: intOrNull(dialog.querySelector('#new-vagainst').value),
          vote_abstain: intOrNull(dialog.querySelector('#new-vabs').value),
        });
        toast('Decision added','success');
        dialog.querySelector('#new-decision').value = '';
        dialog.querySelector('#new-vfor').value = '';
        dialog.querySelector('#new-vagainst').value = '';
        dialog.querySelector('#new-vabs').value = '';
        await refreshDecisions();
      } catch { toast('Failed','error'); }
    });

    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn?.addEventListener('click', guardButton(saveBtn, async () => {
      const dt = dialog.querySelector('#f-dt').value;
      const end = dialog.querySelector('#f-end').value;
      if (dt && end && new Date(end) <= new Date(dt)) { toast('End must be after the start','error'); return; }
      try {
        await api('PATCH', `/meetings/${id}`, {
          title:        dialog.querySelector('#f-title').value.trim()||null,
          scheduled_at: dt ? new Date(dt).toISOString() : null,
          // Always present: an emptied field clears the end time (null = clear).
          ends_at:      end ? new Date(end).toISOString() : null,
          location:     dialog.querySelector('#f-loc').value.trim()||null,
          agenda:       dialog.querySelector('#f-agenda').value.trim()||null,
          notes:        dialog.querySelector('#f-notes').value.trim()||null,
          status:       dialog.querySelector('#f-status').value,
        });
        toast('Saved','success');
      } catch { toast('Save failed','error'); }
    }));
  }

  // ─── Attendance roster ─────────────────────────────────────────────────────

  /** Fetches the data every roster picker needs: a first page of members (for
   *  the initial list and tier options) and the visibility groups. */
  async loadRosterData() {
    const mp = await api('GET', '/members?limit=200&status=active');
    const members = mp?.data ?? mp ?? [];
    let groups = [];
    try { groups = await api('GET', '/groups') ?? []; } catch { /* optional */ }
    return { members, groups };
  }

  /**
   * Mounts a checkbox roster with bulk-select tools — everyone-shown, none, by
   * minimum role (linked user account), by member tier, or by visibility
   * group — into `host`. The individual list is searched server-side so anyone
   * is reachable regardless of org size; the selection lives in a Set that
   * survives searching and every bulk action. `attending` maps member_id →
   * present flag, seeding the selection and preserving present/absent. Returns
   * { roster } yielding the selection as attendee objects; callers can append
   * buttons to `.att-actions`.
   */
  mountRosterPicker(host, members, groups, attending) {
    const tiers = [...new Set(members.map(m => m.tier).filter(Boolean))].sort();
    const selected = new Set(attending.keys());
    let shown = members; // the current (searched) page of members

    host.innerHTML = `
      <div style="display:flex;gap:.35rem;flex-wrap:wrap;margin-bottom:.5rem">
        <button type="button" class="btn-secondary att-all" style="font-size:.75rem" title="Select everyone currently shown">All shown</button>
        <button type="button" class="btn-secondary att-none" style="font-size:.75rem">None</button>
        <button type="button" class="btn-secondary att-officers" style="font-size:.75rem">Officers+</button>
        <select class="att-tier" style="font-size:.75rem;max-width:9rem">
          <option value="">+ tier…</option>
          ${tiers.map(t => `<option value="${esc(t)}">${esc(t)}</option>`).join('')}
        </select>
        <select class="att-group" style="font-size:.75rem;max-width:9rem">
          <option value="">+ group…</option>
          ${groups.map(g => `<option value="${esc(g.id)}">${esc(g.name)}</option>`).join('')}
        </select>
      </div>
      <input class="att-search" placeholder="Search members by name…" autocomplete="off" style="margin-bottom:.4rem">
      <div class="att-list" style="max-height:220px;overflow-y:auto;border:1px solid var(--color-border);border-radius:var(--radius);padding:.4rem .6rem"></div>
      <div style="display:flex;justify-content:space-between;align-items:center;margin-top:.5rem">
        <span class="att-count" style="font-size:.78rem;color:var(--color-text-muted)"></span>
        <span class="att-actions"></span>
      </div>`;

    const listEl = host.querySelector('.att-list');
    const recount = () => {
      host.querySelector('.att-count').textContent = `${selected.size} attending`;
    };
    const paint = () => {
      listEl.innerHTML = shown.map(m => `
        <label style="display:flex;gap:.45rem;align-items:center;font-size:.85rem;padding:.12rem 0;cursor:pointer;text-transform:none;letter-spacing:normal;font-weight:400;color:var(--color-text);margin-bottom:0">
          <input type="checkbox" class="att-cb" style="width:auto" value="${esc(m.id)}" ${selected.has(m.id) ? 'checked' : ''}>
          <span style="flex:1">${esc(m.display_name)}</span>
          <span style="font-size:.72rem;color:var(--color-text-muted)">${esc(m.tier ?? '')}</span>
        </label>`).join('') || '<p style="color:var(--color-text-muted);font-size:.85rem">No matching members.</p>';
      listEl.querySelectorAll('.att-cb').forEach(cb => cb.addEventListener('change', () => {
        if (cb.checked) selected.add(cb.value); else selected.delete(cb.value);
        recount();
      }));
    };
    const addIDs = ids => { ids.forEach(id => selected.add(id)); paint(); recount(); };

    paint();
    recount();

    let searchTimer;
    host.querySelector('.att-search').addEventListener('input', e => {
      clearTimeout(searchTimer);
      const q = e.target.value.trim();
      searchTimer = setTimeout(async () => {
        const params = new URLSearchParams({ limit: '50', status: 'active' });
        if (q) params.set('search', q);
        try {
          const pg = await api('GET', '/members?' + params);
          shown = pg?.data ?? pg ?? [];
          paint();
        } catch { toast('Could not search members', 'error'); }
      }, 300);
    });

    host.querySelector('.att-all').addEventListener('click', () => addIDs(shown.map(m => m.id)));
    host.querySelector('.att-none').addEventListener('click', () => { selected.clear(); paint(); recount(); });
    host.querySelector('.att-officers').addEventListener('click', async () => {
      try {
        const res = await api('GET', '/members/ids?min_role=officer');
        addIDs(res.member_ids ?? []);
      } catch { toast('Could not load officers', 'error'); }
    });
    host.querySelector('.att-tier').addEventListener('change', async e => {
      const tier = e.target.value;
      if (!tier) return;
      e.target.value = '';
      try {
        const pg = await api('GET', '/members?' + new URLSearchParams({ tier, status: 'active', limit: '500' }));
        addIDs((pg?.data ?? pg ?? []).map(m => m.id));
      } catch { toast('Could not load tier members', 'error'); }
    });
    host.querySelector('.att-group').addEventListener('change', async e => {
      const gid = e.target.value;
      if (!gid) return;
      e.target.value = '';
      try {
        const res = await api('GET', `/groups/${gid}/member-ids`);
        addIDs(res.member_ids ?? []);
      } catch { toast('Could not load group members', 'error'); }
    });

    const roster = () => [...selected].map(id => ({
      member_id: id,
      // Keep an existing present/absent flag; newly added people default to present.
      present: attending.get(id) ?? true,
    }));
    return { roster, recount, check: addIDs };
  }

  /** RSVP panel: everyone sees the tally and sets their own; officers get a
   *  count. Members without a linked record can view but not respond. */
  async renderRSVP(dialog, meetingId) {
    const host = dialog.querySelector('#rsvp-section');
    if (!host) return;
    let s;
    try { s = await api('GET', `/meetings/${meetingId}/rsvp`); }
    catch { host.innerHTML = '<p style="font-size:.85rem;color:var(--color-text-muted)">RSVP unavailable.</p>'; return; }
    if (!dialog.isConnected) return;
    const canRespond = !!currentMemberId();
    const btn = (val, label) => `<button class="btn-secondary rsvp-btn" data-r="${val}"
        style="font-size:.8rem;${s.mine === val ? 'background:var(--color-primary);color:#fff' : ''}" ${canRespond ? '' : 'disabled'}>${label}</button>`;
    host.innerHTML = `
      <div style="font-size:.82rem;color:var(--color-text-muted);margin-bottom:.4rem">
        Going: <strong>${s.yes}</strong> · Maybe: <strong>${s.maybe}</strong> · No: <strong>${s.no}</strong></div>
      <div style="display:flex;gap:.35rem">${btn('yes', 'Going')}${btn('maybe', 'Maybe')}${btn('no', "Can't")}</div>
      ${canRespond ? '' : '<div style="font-size:.75rem;color:var(--color-text-muted);margin-top:.3rem">Link your login to a member record to RSVP.</div>'}`;
    host.querySelectorAll('.rsvp-btn').forEach(b => b.addEventListener('click', async () => {
      try {
        await api('PUT', `/meetings/${meetingId}/rsvp`, { response: b.dataset.r });
        this.renderRSVP(dialog, meetingId);
      } catch (err) { toast(err.error ?? 'RSVP failed', 'error'); }
    }));
  }

  /** The meeting editor's attendance panel: the picker plus its own Save. */
  async renderAttendance(dialog, meetingId, mt) {
    const host = dialog.querySelector('#attendance-section');
    if (!host) return;
    const attending = new Map((mt.attendees ?? []).map(a => [a.member_id, a.present]));

    if (!canWrite()) {
      const names = (mt.attendees ?? []).map(a => esc(a.member_name)).join(', ');
      host.innerHTML = names
        ? `<p style="font-size:.85rem">${names}</p>`
        : '<p style="font-size:.85rem;color:var(--color-text-muted)">No attendees recorded.</p>';
      return;
    }

    let data;
    try { data = await this.loadRosterData(); }
    catch { host.innerHTML = '<p class="empty-state">Failed to load members.</p>'; return; }
    if (!dialog.isConnected) return;

    const picker = this.mountRosterPicker(host, data.members, data.groups, attending);
    host.querySelector('.att-actions').innerHTML =
      '<button class="btn-secondary att-rsvp" style="font-size:.8rem" title="Check everyone who RSVP&apos;d yes">From RSVPs</button> ' +
      '<button class="btn-primary att-save" style="font-size:.8rem">Save attendance</button>';
    host.querySelector('.att-rsvp').addEventListener('click', async () => {
      try {
        const res = await api('GET', `/meetings/${meetingId}/rsvp-yes`);
        picker.check(res.member_ids ?? []);
      } catch { toast('Could not load RSVPs', 'error'); }
    });
    const saveBtn = host.querySelector('.att-save');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      try {
        const saved = await api('PUT', `/meetings/${meetingId}/attendees`, { attendees: picker.roster() });
        attending.clear();
        saved.forEach(a => attending.set(a.member_id, a.present));
        toast('Attendance saved', 'success');
        picker.recount();
      } catch (err) { toast(err.error ?? 'Save failed', 'error'); }
    }));
  }

  // ─── Governance & voting section ──────────────────────────────────────────

  /** Loads and renders the quorum meter, motions, and proxies for a meeting. */
  async renderGovernance(dialog, meetingId) {
    const host = dialog.querySelector('#gov-section');
    if (!host) return;
    const officer  = canWrite();
    const myMember = currentMemberId();

    const reload = async () => {
      let quorum, motions, proxies, members = [];
      try {
        [quorum, motions, proxies] = await Promise.all([
          api('GET', `/meetings/${meetingId}/quorum`),
          api('GET', `/meetings/${meetingId}/motions`),
          api('GET', `/meetings/${meetingId}/proxies`),
        ]);
        if (officer) { const mp = await api('GET', '/members?limit=200'); members = mp?.data ?? mp ?? []; }
      } catch {
        host.innerHTML = '<p class="empty-state">Failed to load governance data.</p>';
        return;
      }
      if (!dialog.isConnected) return;
      host.innerHTML = this.govMarkup(quorum, motions, proxies, members, officer, myMember);
    };
    // Attach the delegated click handler ONCE — reload() only swaps innerHTML,
    // so re-wiring on every reload would stack duplicate listeners.
    this.wireGovernance(host, meetingId, () => reload());
    await reload();
  }

  govMarkup(quorum, motions, proxies, members, officer, myMember) {
    const memberOpts = ph => `<option value="">${ph}</option>` +
      members.map(m => `<option value="${esc(m.id)}">${esc(m.display_name)}</option>`).join('');

    const pct = quorum.required ? Math.min(100, (quorum.effective_present / quorum.required) * 100) : 100;
    const meterColor = quorum.met ? 'var(--color-success)' : 'var(--color-warning)';
    const quorumBlock = `
      <div style="margin-bottom:1rem">
        <div style="display:flex;justify-content:space-between;align-items:baseline;font-size:.85rem;margin-bottom:.3rem">
          <span><strong>Quorum ${quorum.met ? '✓ met' : '— not yet met'}</strong></span>
          <span style="color:var(--color-text-muted);font-variant-numeric:tabular-nums">${quorum.effective_present} of ${quorum.required} needed · ${quorum.active_members} active</span>
        </div>
        <div style="height:10px;background:var(--color-border);border-radius:5px;overflow:hidden"
             role="progressbar" aria-valuemin="0" aria-valuemax="${quorum.required}" aria-valuenow="${quorum.effective_present}"
             aria-label="Quorum: ${quorum.effective_present} of ${quorum.required} needed${quorum.met ? ', met' : ', not yet met'}">
          <div style="height:100%;width:${pct}%;background:${meterColor};transition:width .3s"></div>
        </div>
        ${quorum.proxies_represented ? `<div style="font-size:.75rem;color:var(--color-text-muted);margin-top:.25rem">includes ${quorum.proxies_represented} represented by proxy</div>` : ''}
      </div>`;

    const motionCards = (motions ?? []).map(m => {
      const tally = m.tally ?? { for: 0, against: 0, abstain: 0 };
      const terminal = ['carried','failed','tabled','withdrawn'].includes(m.status);
      let controls = '';
      if (m.status === 'draft' && officer) {
        controls = `
          <div style="display:flex;gap:.4rem;flex-wrap:wrap;margin-top:.5rem">
            <select class="second-sel" style="flex:1;min-width:140px;padding:.25rem .4rem;font-size:.8rem">${memberOpts('Seconded by…')}</select>
            <button class="btn-secondary gov-act" data-act="second" style="font-size:.78rem;padding:.25rem .7rem">Record second</button>
            <button class="btn-ghost gov-act" data-act="delete" style="font-size:.78rem;color:var(--color-danger)">Delete</button>
          </div>`;
      } else if (m.status === 'seconded' && officer) {
        controls = `
          <div style="display:flex;gap:.4rem;margin-top:.5rem">
            <button class="btn-primary gov-act" data-act="open" style="font-size:.78rem;padding:.25rem .8rem">Open voting</button>
            <button class="btn-ghost gov-act" data-act="delete" style="font-size:.78rem;color:var(--color-danger)">Delete</button>
          </div>`;
      } else if (m.status === 'open') {
        const selfVote = myMember ? `
          <div style="margin-top:.5rem">
            <div style="font-size:.75rem;color:var(--color-text-muted);margin-bottom:.25rem">Your vote:</div>
            <div style="display:flex;gap:.4rem">
              <button class="btn-secondary gov-act" data-act="vote" data-choice="for" style="font-size:.78rem;flex:1">✓ For</button>
              <button class="btn-secondary gov-act" data-act="vote" data-choice="against" style="font-size:.78rem;flex:1">✗ Against</button>
              <button class="btn-secondary gov-act" data-act="vote" data-choice="abstain" style="font-size:.78rem;flex:1">– Abstain</button>
            </div>
          </div>` : '';
        const officerTally = officer ? `
          <div style="margin-top:.5rem;border-top:1px dashed var(--color-border);padding-top:.5rem">
            <div style="font-size:.75rem;color:var(--color-text-muted);margin-bottom:.25rem">Record a ballot on behalf of a member:</div>
            <div style="display:flex;gap:.4rem;flex-wrap:wrap">
              <select class="rec-sel" style="flex:1;min-width:140px;padding:.25rem .4rem;font-size:.8rem">${memberOpts('Member…')}</select>
              <label style="display:flex;align-items:center;gap:.25rem;font-size:.72rem;text-transform:none;letter-spacing:0"><input type="checkbox" class="rec-proxy"> proxy</label>
              <button class="btn-ghost gov-act" data-act="record" data-choice="for" style="font-size:.78rem;color:var(--color-success)">For</button>
              <button class="btn-ghost gov-act" data-act="record" data-choice="against" style="font-size:.78rem;color:var(--color-danger)">Against</button>
              <button class="btn-ghost gov-act" data-act="record" data-choice="abstain" style="font-size:.78rem">Abstain</button>
            </div>
            <div style="display:flex;gap:.4rem;margin-top:.5rem">
              <button class="btn-primary gov-act" data-act="close" style="font-size:.78rem">Close &amp; decide</button>
              <button class="btn-secondary gov-act" data-act="ballots" style="font-size:.78rem" title="Email a single-use ballot link to members who haven't voted">Email ballots</button>
            </div>
          </div>` : '';
        controls = selfVote + officerTally;
      } else if (terminal) {
        const label = { carried: 'Carried ✓', failed: 'Failed ✗', tabled: 'Tabled', withdrawn: 'Withdrawn' }[m.status];
        controls = `<div style="margin-top:.4rem;font-size:.82rem;font-weight:600;color:${m.status==='carried'?'var(--color-success)':m.status==='failed'?'var(--color-danger)':'var(--color-text-muted)'}">${label}</div>`;
      }
      return `
        <div class="motion-card" data-id="${esc(m.id)}" style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.75rem .9rem;margin-bottom:.6rem">
          <div style="display:flex;justify-content:space-between;gap:.5rem;align-items:flex-start">
            <div style="flex:1">
              <div style="font-weight:600">${esc(m.title)}</div>
              <div style="font-size:.76rem;color:var(--color-text-muted)">
                ${m.mover_name ? 'Moved by ' + esc(m.mover_name) : 'No mover'}${m.seconder_name ? ' · seconded by ' + esc(m.seconder_name) : ''} · ${THRESHOLD_LABEL[m.threshold] || esc(m.threshold)}
              </div>
            </div>
            ${m.business === 'old' ? '<span class="badge" style="background:var(--color-bg);color:var(--color-text-muted)">old business</span>' : ''}
            <span class="badge badge-${MOTION_BADGE[m.status] || 'none'}">${esc(m.status)}</span>
          </div>
          ${m.detail ? `<div style="font-size:.82rem;margin:.4rem 0">${esc(m.detail)}</div>` : ''}
          <div style="margin:.5rem 0"><vote-tally for="${tally.for}" against="${tally.against}" abstain="${tally.abstain}"></vote-tally></div>
          ${(m.recusals?.length) ? `<div style="font-size:.75rem;color:var(--color-text-muted);margin:.25rem 0">Recused: ${m.recusals.map(x => esc(x.member_name)).join(', ')}</div>` : ''}
          ${myMember && !terminal ? `<button class="btn-ghost gov-act" data-act="recuse" style="font-size:.75rem">Recuse myself</button>` : ''}
          ${controls}
        </div>`;
    }).join('') || '<p style="font-size:.85rem;color:var(--color-text-muted);margin:.5rem 0">No motions yet.</p>';

    const newMotion = officer ? `
      <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.75rem;margin-top:.5rem">
        <div class="form-group"><label for="nm-title">New motion</label><input id="nm-title" placeholder="e.g. Adopt the Q3 budget"></div>
        <div class="form-group"><label for="nm-detail">Detail (optional)</label><input id="nm-detail" placeholder="Context or exact wording"></div>
        <div style="display:flex;gap:.4rem;flex-wrap:wrap">
          <select id="nm-mover" style="flex:1;min-width:130px;padding:.3rem .4rem;font-size:.85rem">${memberOpts('Moved by…')}</select>
          <select id="nm-threshold" style="padding:.3rem .4rem;font-size:.85rem">
            <option value="majority">Simple majority</option>
            <option value="two_thirds">Two-thirds</option>
            <option value="unanimous">Unanimous</option>
          </select>
          <select id="nm-business" style="padding:.3rem .4rem;font-size:.85rem" title="Robert's Rules agenda class">
            <option value="new">New business</option>
            <option value="old">Old business</option>
          </select>
          <button class="btn-primary gov-act" data-act="create" style="font-size:.82rem">Add motion</button>
        </div>
      </div>` : '';

    const proxyBlock = officer ? `
      <details style="margin-top:1rem">
        <summary style="cursor:pointer;font-size:.85rem;font-weight:600">Proxies (${(proxies ?? []).length})</summary>
        <div style="margin-top:.5rem">
          ${(proxies ?? []).map(p => `
            <div style="display:flex;justify-content:space-between;align-items:center;font-size:.82rem;padding:.25rem 0">
              <span>${esc(p.grantor_name)} → <strong>${esc(p.holder_name)}</strong></span>
              <button class="btn-ghost gov-act" data-act="unproxy" data-proxy="${esc(p.id)}" style="font-size:.75rem;color:var(--color-danger)">Remove</button>
            </div>`).join('') || '<div style="font-size:.8rem;color:var(--color-text-muted)">None assigned.</div>'}
          <div style="display:flex;gap:.4rem;flex-wrap:wrap;margin-top:.5rem">
            <select id="px-grantor" style="flex:1;min-width:120px;padding:.25rem .4rem;font-size:.8rem">${memberOpts('Grantor…')}</select>
            <select id="px-holder" style="flex:1;min-width:120px;padding:.25rem .4rem;font-size:.8rem">${memberOpts('Held by…')}</select>
            <button class="btn-secondary gov-act" data-act="proxy" style="font-size:.78rem">Assign</button>
          </div>
        </div>
      </details>` : '';

    return `
      <h3 style="font-size:.95rem;margin-bottom:.75rem">Governance &amp; voting</h3>
      ${quorumBlock}
      <div>${motionCards}</div>
      ${newMotion}
      ${proxyBlock}`;
  }

  /**
   * Recording-secretary journal (Robert's Rules): chronological entries the
   * secretary types during the meeting, each optionally tied to a motion.
   * Officers add/correct/remove entries until the minutes are FINALIZED —
   * after that the journal is immutable (database-enforced) and only the
   * document export remains.
   */
  async renderMinutes(dialog, meetingId, mt) {
    const host = dialog.querySelector('#minutes-section');
    if (!host) return;
    const officer = canWrite();
    const KINDS = [
      ['call_to_order','Call to order'], ['previous_minutes','Previous minutes'],
      ['report','Report'], ['old_business','Old business'], ['new_business','New business'],
      ['discussion','Discussion'], ['point_of_order','Point of order'],
      ['recess','Recess'], ['adjournment','Adjournment'], ['note','Note'],
    ];
    const kindLabel = k => (KINDS.find(x => x[0] === k) ?? [k, k])[1];

    let entries = [], motions = [];
    try {
      [entries, motions] = await Promise.all([
        api('GET', `/meetings/${meetingId}/minutes`),
        api('GET', `/meetings/${meetingId}/motions`).catch(() => []),
      ]);
    } catch {
      host.innerHTML = '<p style="color:var(--color-text-muted)">Failed to load minutes.</p>';
      return;
    }
    entries = entries ?? []; motions = motions ?? [];
    const finalized = !!mt.minutes_finalized_at;
    const canEdit = officer && !finalized;
    const motionOpts = ['<option value="">— link a motion (optional) —</option>']
      .concat(motions.map(m => `<option value="${esc(m.id)}">${esc(m.title)}</option>`)).join('');

    host.innerHTML = `
      <div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:.5rem">
        <h3 style="font-size:.95rem;margin:0">Minutes ${finalized
          ? `<span class="badge" style="background:color-mix(in srgb, var(--color-success,#137333) 15%, transparent);color:var(--color-success,#137333)">finalized ${esc(fmtDateTime(mt.minutes_finalized_at))}</span>`
          : '<span class="badge" style="background:var(--color-bg);color:var(--color-text-muted)">draft</span>'}</h3>
        <div style="display:flex;gap:.4rem">
          <button class="btn-secondary" id="min-heatmap" style="font-size:.8rem" title="Preview the minutes as a word-frequency heat map">🔥 Heat map</button>
          <button class="btn-secondary" id="min-export" style="font-size:.8rem">Export minutes (.md)</button>
          ${canEdit ? '<button class="btn-primary" id="min-finalize" style="font-size:.8rem">Finalize minutes</button>' : ''}
        </div>
      </div>
      <div id="min-list" style="margin-top:.75rem">
        ${entries.length ? entries.map(e => `
          <div style="display:flex;gap:.6rem;align-items:flex-start;padding:.45rem 0;border-bottom:1px solid var(--color-border,#eee)" data-eid="${esc(e.id)}">
            <span class="badge" style="background:var(--color-bg);color:var(--color-text-muted);white-space:nowrap">${esc(kindLabel(e.kind))}</span>
            <div style="flex:1;font-size:.88rem">
              ${esc(e.body)}
              ${e.motion_title ? `<div style="font-size:.75rem;color:var(--color-text-muted)">re: motion “${esc(e.motion_title)}”</div>` : ''}
              <div style="font-size:.72rem;color:var(--color-text-muted)">${esc(fmtDateTime(e.recorded_at))}${e.recorded_by_name ? ' · ' + esc(e.recorded_by_name) : ''}</div>
            </div>
            ${canEdit ? `<button class="btn-ghost min-del" data-eid="${esc(e.id)}" style="font-size:.75rem;color:var(--color-danger)">Remove</button>` : ''}
          </div>`).join('')
        : '<p style="font-size:.85rem;color:var(--color-text-muted)">No journal entries yet.</p>'}
      </div>
      ${canEdit ? `
        <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.75rem;margin-top:.6rem">
          <div style="display:flex;gap:.4rem;flex-wrap:wrap;margin-bottom:.5rem">
            <select id="min-kind" style="padding:.3rem .4rem;font-size:.85rem">
              ${KINDS.map(([v,l]) => `<option value="${v}">${l}</option>`).join('')}
            </select>
            <select id="min-motion" style="flex:1;min-width:160px;padding:.3rem .4rem;font-size:.85rem">${motionOpts}</select>
          </div>
          <div class="form-group"><textarea id="min-body" rows="2" placeholder="What happened? e.g. “Meeting called to order at 6:03 PM by Chair Alvarez.”"></textarea></div>
          <button class="btn-secondary" id="min-add" style="width:100%">+ Record entry</button>
        </div>` : ''}
    `;

    const reload = async () => {
      const fresh = await api('GET', `/meetings/${meetingId}`);
      this.renderMinutes(dialog, meetingId, fresh);
    };

    host.querySelector('#min-heatmap').addEventListener('click', () => {
      openHeatmapModal(mt.title, assembleMinutesText(mt, entries, motions));
    });
    host.querySelector('#min-export').addEventListener('click', () => {
      apiDownload(`/meetings/${meetingId}/minutes.md`, 'minutes.md').catch(() => toast('Export failed', 'error'));
    });
    host.querySelector('#min-add')?.addEventListener('click', async () => {
      const body = host.querySelector('#min-body').value.trim();
      if (!body) { toast('Entry text is required', 'error'); return; }
      try {
        await api('POST', `/meetings/${meetingId}/minutes`, {
          kind: host.querySelector('#min-kind').value,
          body,
          motion_id: host.querySelector('#min-motion').value || null,
        });
        await reload();
      } catch (err) { toast(err.error ?? 'Failed to record', 'error'); }
    });
    host.querySelectorAll('.min-del').forEach(btn => {
      btn.addEventListener('click', async () => {
        try {
          await api('DELETE', `/meetings/${meetingId}/minutes/${btn.dataset.eid}`);
          await reload();
        } catch (err) { toast(err.error ?? 'Failed to remove', 'error'); }
      });
    });
    host.querySelector('#min-finalize')?.addEventListener('click', () => {
      confirmDelete({
        noun: 'minutes (finalize — this is permanent)',
        name: mt.title,
        onConfirm: async (confirmVal) => {
          try {
            await api('POST', `/meetings/${meetingId}/minutes/finalize?confirm=${encodeURIComponent(confirmVal)}`);
            toast('Minutes finalized', 'success');
            await reload();
          } catch (err) { toast(err.error ?? 'Finalize failed', 'error'); throw err; }
        },
      });
    });
  }

  /** Event delegation for all governance controls; reload() re-renders on success. */
  wireGovernance(host, meetingId, reload) {
    host.addEventListener('click', async e => {
      const btn = e.target.closest('.gov-act');
      if (!btn) return;
      const act = btn.dataset.act;
      const card = btn.closest('.motion-card');
      const motionId = card?.dataset.id;
      try {
        if (act === 'create') {
          const title = host.querySelector('#nm-title').value.trim();
          if (!title) { toast('Motion title is required','error'); return; }
          const mover = host.querySelector('#nm-mover').value;
          await api('POST', `/meetings/${meetingId}/motions`, {
            title,
            detail: host.querySelector('#nm-detail').value.trim() || null,
            mover_id: mover || null,
            threshold: host.querySelector('#nm-threshold').value,
            business: host.querySelector('#nm-business').value,
          });
          toast('Motion added','success');
        } else if (act === 'second') {
          const sec = card.querySelector('.second-sel').value;
          if (!sec) { toast('Choose who seconded','error'); return; }
          await api('POST', `/motions/${motionId}/second`, { seconder_id: sec });
          toast('Seconded','success');
        } else if (act === 'open') {
          await api('POST', `/motions/${motionId}/open`);
          toast('Voting opened','success');
        } else if (act === 'close') {
          await api('POST', `/motions/${motionId}/close`);
          toast('Motion decided','success');
        } else if (act === 'ballots') {
          const res = await api('POST', `/motions/${motionId}/ballots`);
          toast(res.queued ? `Emailing ${res.queued} ballot link${res.queued===1?'':'s'}…` : 'No eligible members to email (need an email on file, not yet voted)', res.queued ? 'success' : 'info');
          return; // no reload needed
        } else if (act === 'delete') {
          await api('DELETE', `/motions/${motionId}`);
          toast('Motion deleted','success');
        } else if (act === 'vote') {
          await api('POST', `/motions/${motionId}/vote`, { choice: btn.dataset.choice });
          toast('Your vote is recorded','success');
        } else if (act === 'record') {
          const mem = card.querySelector('.rec-sel').value;
          if (!mem) { toast('Choose a member','error'); return; }
          const isProxy = card.querySelector('.rec-proxy').checked;
          await api('POST', `/motions/${motionId}/votes`, { member_id: mem, choice: btn.dataset.choice, is_proxy: isProxy });
          toast('Ballot recorded','success');
        } else if (act === 'proxy') {
          const g = host.querySelector('#px-grantor').value, h = host.querySelector('#px-holder').value;
          if (!g || !h) { toast('Choose both members','error'); return; }
          await api('POST', `/meetings/${meetingId}/proxies`, { grantor_id: g, holder_id: h });
          toast('Proxy assigned','success');
        } else if (act === 'unproxy') {
          await api('DELETE', `/proxies/${btn.dataset.proxy}`);
          toast('Proxy removed','success');
        } else if (act === 'recuse') {
          const reason = (prompt('Reason for recusing (optional, recorded in the minutes):') ?? '').trim();
          await api('POST', `/recusals/${motionId}`, { type: 'motion', reason });
          toast('Recusal recorded','success');
        }
        await reload();
      } catch (err) {
        toast(err.error ?? 'Action failed','error');
      }
    });
  }
}
customElements.define('page-meetings', PageMeetings);
