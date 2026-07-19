import { api, canWrite, isAuthenticated, isSuperadmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDateTime, openModal, guardButton, toLocalInputValue, confirmDelete } from '../utils.js';

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
      maxWidth: '780px',
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
}
customElements.define('page-meetings', PageMeetings);
