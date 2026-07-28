import { api, canWrite, isAuthenticated, isSuperadmin, currentMemberId } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDateTime, openModal, guardButton, toLocalInputValue, confirmDelete } from '../utils.js';
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
              <div style="font-size:.85rem;color:var(--color-text-muted)">${fmtDateTime(m.scheduled_at)}${m.location ? ' · ' + esc(m.location) : ''}</div>
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
            <div class="form-group"><label for="f-dt">Date &amp; time *</label><input id="f-dt" type="datetime-local"></div>
            <div class="form-group"><label for="f-loc">Location</label><input id="f-loc" placeholder="Room or video link"></div>
          </div>
          <div class="form-group"><label for="f-agenda">Agenda</label><textarea id="f-agenda" rows="4"></textarea></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Schedule</button>
        </div>
      `,
    });

    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const title = dialog.querySelector('#f-title').value.trim();
      const dt    = dialog.querySelector('#f-dt').value;
      if (!title || !dt) { toast('Title and date are required','error'); return; }
      try {
        const m = await api('POST', '/meetings', {
          title,
          scheduled_at: new Date(dt).toISOString(),
          location: dialog.querySelector('#f-loc').value.trim() || null,
          agenda:   dialog.querySelector('#f-agenda').value.trim() || null,
        });
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
              <div class="form-group"><label for="f-dt">Date &amp; time</label><input id="f-dt" type="datetime-local" value="${esc(toLocalInputValue(mt.scheduled_at))}"></div>
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
          </div>

          <div id="gov-section" style="grid-column:1 / -1;border-top:1px solid var(--color-border);padding-top:1rem">
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
    this.renderGovernance(dialog, id);

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
      try {
        await api('PATCH', `/meetings/${id}`, {
          title:        dialog.querySelector('#f-title').value.trim()||null,
          scheduled_at: dt ? new Date(dt).toISOString() : null,
          location:     dialog.querySelector('#f-loc').value.trim()||null,
          agenda:       dialog.querySelector('#f-agenda').value.trim()||null,
          notes:        dialog.querySelector('#f-notes').value.trim()||null,
          status:       dialog.querySelector('#f-status').value,
        });
        toast('Saved','success');
      } catch { toast('Save failed','error'); }
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
        <div style="height:10px;background:var(--color-border);border-radius:5px;overflow:hidden">
          <div style="height:100%;width:${pct}%;background:${meterColor};transition:width .3s"></div>
        </div>
        ${quorum.proxies_represented ? `<div style="font-size:.75rem;color:var(--color-text-muted);margin-top:.25rem">includes ${quorum.proxies_represented} represented by proxy</div>` : ''}
      </div>`;

    const motionCards = (motions ?? []).map(m => {
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
            <button class="btn-primary gov-act" data-act="close" style="font-size:.78rem;margin-top:.5rem">Close &amp; decide</button>
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
            <span class="badge badge-${MOTION_BADGE[m.status] || 'none'}">${esc(m.status)}</span>
          </div>
          ${m.detail ? `<div style="font-size:.82rem;margin:.4rem 0">${esc(m.detail)}</div>` : ''}
          <div style="margin:.5rem 0"><vote-tally for="${m.tally.for}" against="${m.tally.against}" abstain="${m.tally.abstain}"></vote-tally></div>
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
        }
        await reload();
      } catch (err) {
        toast(err.error ?? 'Action failed','error');
      }
    });
  }
}
customElements.define('page-meetings', PageMeetings);
