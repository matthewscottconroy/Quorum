import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';

function esc(s) { return String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }
function fmtDT(iso) { return iso ? new Date(iso).toLocaleString(undefined, { month:'short', day:'numeric', year:'numeric', hour:'2-digit', minute:'2-digit' }) : '—'; }

class PageMeetings extends HTMLElement {
  constructor() { super(); this._meetings = []; this._upcoming = false; }

  async connectedCallback() { await this.load(); }

  async load() {
    const params = this._upcoming ? '?upcoming=true' : '';
    try { const _mtPage = await api('GET', '/meetings' + params);
    this._meetings = _mtPage?.data ?? _mtPage ?? []; }
    catch { toast('Failed to load meetings', 'error'); }
    this.render();
  }

  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Meetings</h1>
        ${canWrite() ? '<button class="btn-primary" id="add-btn">+ Schedule meeting</button>' : ''}
      </div>
      <div class="search-bar">
        <label style="flex-direction:row;align-items:center;gap:.4rem;text-transform:none;letter-spacing:0;font-weight:400">
          <input type="checkbox" id="upcoming-chk" ${this._upcoming?'checked':''}> Upcoming only
        </label>
      </div>
      <div style="display:flex;flex-direction:column;gap:.75rem" id="meeting-list">
        ${this._meetings?.length
          ? this._meetings.map(m => `
              <div class="card meeting-card" data-id="${m.id}" style="padding:1rem 1.25rem;cursor:pointer;display:flex;align-items:center;gap:1rem">
                <div style="flex:1">
                  <div style="font-weight:700;font-size:1rem">${esc(m.title)}</div>
                  <div style="font-size:.85rem;color:var(--color-text-muted)">${fmtDT(m.scheduled_at)}${m.location ? ' · ' + esc(m.location) : ''}</div>
                </div>
                <span class="badge badge-${m.status}">${m.status}</span>
                ${canWrite() ? `<button class="btn-ghost del-btn" data-id="${m.id}" style="color:var(--color-danger)">Del</button>` : ''}
              </div>`).join('')
          : '<div class="empty-state"><p>No meetings found.</p></div>'}
      </div>
    `;

    this.querySelector('#upcoming-chk')?.addEventListener('change', e => { this._upcoming = e.target.checked; this.load(); });
    this.querySelector('#add-btn')?.addEventListener('click', () => this.openCreateModal());
    this.querySelectorAll('.meeting-card').forEach(card => {
      card.addEventListener('click', e => {
        if (e.target.classList.contains('del-btn')) return;
        this.openEditor(card.dataset.id);
      });
    });
    this.querySelectorAll('.del-btn').forEach(btn => {
      btn.addEventListener('click', async () => {
        const m = this._meetings.find(x => x.id === btn.dataset.id);
        if (!await confirm(`Delete meeting "${m?.title}"?`, 'Delete meeting')) return;
        try { await api('DELETE', `/meetings/${btn.dataset.id}`); toast('Deleted','success'); this.load(); }
        catch { toast('Delete failed','error'); }
      });
    });
  }

  openCreateModal() {
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal">
        <div class="modal-header"><h2>Schedule meeting</h2><button class="btn-ghost" id="close-btn">✕</button></div>
        <div class="modal-body">
          <div class="form-group"><label>Title *</label><input id="f-title"></div>
          <div class="form-row">
            <div class="form-group"><label>Date &amp; time *</label><input id="f-dt" type="datetime-local"></div>
            <div class="form-group"><label>Location</label><input id="f-loc" placeholder="Room or video link"></div>
          </div>
          <div class="form-group"><label>Agenda</label><textarea id="f-agenda" rows="4"></textarea></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Schedule</button>
        </div>
      </div>
    `;
    document.body.appendChild(backdrop);
    const close = () => backdrop.remove();
    backdrop.querySelector('#close-btn').addEventListener('click', close);
    backdrop.querySelector('#cancel-btn').addEventListener('click', close);
    backdrop.querySelector('#save-btn').addEventListener('click', async () => {
      const title = backdrop.querySelector('#f-title').value.trim();
      const dt    = backdrop.querySelector('#f-dt').value;
      if (!title || !dt) { toast('Title and date are required','error'); return; }
      try {
        const m = await api('POST', '/meetings', {
          title,
          scheduled_at: new Date(dt).toISOString(),
          location: backdrop.querySelector('#f-loc').value.trim() || null,
          agenda:   backdrop.querySelector('#f-agenda').value.trim() || null,
        });
        toast('Meeting scheduled','success');
        close();
        this.load();
        setTimeout(() => this.openEditor(m.id), 100);
      } catch (err) { toast(err.error ?? 'Failed','error'); }
    });
  }

  async openEditor(id) {
    let mt;
    try { mt = await api('GET', `/meetings/${id}`); }
    catch { toast('Load failed','error'); return; }

    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal" style="max-width:780px">
        <div class="modal-header">
          <h2>${esc(mt.title)}</h2>
          <button class="btn-ghost" id="close-btn">✕</button>
        </div>
        <div class="modal-body" style="display:grid;grid-template-columns:1fr 1fr;gap:1.25rem">
          <div>
            <div class="form-group"><label>Title</label><input id="f-title" value="${esc(mt.title)}"></div>
            <div class="form-row">
              <div class="form-group"><label>Date &amp; time</label><input id="f-dt" type="datetime-local" value="${mt.scheduled_at?.slice(0,16)??''}"></div>
              <div class="form-group">
                <label>Status</label>
                <select id="f-status">
                  ${['scheduled','completed','cancelled'].map(s=>`<option value="${s}" ${mt.status===s?'selected':''}>${s}</option>`).join('')}
                </select>
              </div>
            </div>
            <div class="form-group"><label>Location</label><input id="f-loc" value="${esc(mt.location??'')}"></div>
            <div class="form-group"><label>Agenda</label><textarea id="f-agenda" rows="5">${esc(mt.agenda??'')}</textarea></div>
            <div class="form-group"><label>Minutes / notes</label><textarea id="f-notes" rows="7">${esc(mt.notes??'')}</textarea></div>
          </div>
          <div>
            <h3 style="margin-bottom:.75rem;font-size:.95rem">Decisions</h3>
            <div id="decisions-list">
              ${(mt.decisions??[]).map(d => `
                <div class="decision-item" style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.65rem .8rem;margin-bottom:.5rem">
                  <div style="font-weight:600">${esc(d.summary)}</div>
                  <div style="font-size:.8rem;color:var(--color-text-muted)">${esc(d.outcome)} ${d.vote_for!=null?`· ${d.vote_for}/${d.vote_against}/${d.vote_abstain}`:''}</div>
                  ${canWrite()?`<button class="btn-ghost del-decision" data-id="${d.id}" style="font-size:.75rem;color:var(--color-danger)">Remove</button>`:''}
                </div>`).join('')}
            </div>
            ${canWrite()?`
              <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.75rem;margin-top:.5rem">
                <div class="form-group"><label>Decision summary</label><input id="new-decision" placeholder="What was decided?"></div>
                <div class="form-row">
                  <div class="form-group"><label>Outcome</label>
                    <select id="new-outcome">
                      <option>passed</option><option>failed</option><option>tabled</option><option>noted</option>
                    </select>
                  </div>
                  <div class="form-group" style="max-width:70px"><label>For</label><input id="new-vfor" type="number" min="0" placeholder="0"></div>
                  <div class="form-group" style="max-width:70px"><label>Agn</label><input id="new-vagainst" type="number" min="0" placeholder="0"></div>
                  <div class="form-group" style="max-width:70px"><label>Abs</label><input id="new-vabs" type="number" min="0" placeholder="0"></div>
                </div>
                <button class="btn-secondary" id="add-decision-btn" style="width:100%">+ Add decision</button>
              </div>`:''}
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Close</button>
          ${canWrite()?'<button class="btn-primary" id="save-btn">Save changes</button>':''}
        </div>
      </div>
    `;
    document.body.appendChild(backdrop);
    const close = () => { backdrop.remove(); this.load(); };
    backdrop.querySelector('#close-btn').addEventListener('click', close);
    backdrop.querySelector('#cancel-btn').addEventListener('click', close);

    backdrop.querySelectorAll('.del-decision').forEach(btn => {
      btn.addEventListener('click', async () => {
        try { await api('DELETE', `/meetings/${id}/decisions/${btn.dataset.id}`); toast('Removed','success'); backdrop.remove(); this.openEditor(id); }
        catch { toast('Failed','error'); }
      });
    });

    backdrop.querySelector('#add-decision-btn')?.addEventListener('click', async () => {
      const summary = backdrop.querySelector('#new-decision').value.trim();
      if (!summary) { toast('Summary required','error'); return; }
      try {
        await api('POST', `/meetings/${id}/decisions`, {
          summary,
          outcome:      backdrop.querySelector('#new-outcome').value,
          vote_for:     parseInt(backdrop.querySelector('#new-vfor').value)||null,
          vote_against: parseInt(backdrop.querySelector('#new-vagainst').value)||null,
          vote_abstain: parseInt(backdrop.querySelector('#new-vabs').value)||null,
        });
        toast('Decision added','success');
        backdrop.remove(); this.openEditor(id);
      } catch { toast('Failed','error'); }
    });

    backdrop.querySelector('#save-btn')?.addEventListener('click', async () => {
      const dt = backdrop.querySelector('#f-dt').value;
      try {
        await api('PATCH', `/meetings/${id}`, {
          title:        backdrop.querySelector('#f-title').value.trim()||null,
          scheduled_at: dt ? new Date(dt).toISOString() : null,
          location:     backdrop.querySelector('#f-loc').value.trim()||null,
          agenda:       backdrop.querySelector('#f-agenda').value.trim()||null,
          notes:        backdrop.querySelector('#f-notes').value.trim()||null,
          status:       backdrop.querySelector('#f-status').value,
        });
        toast('Saved','success');
      } catch { toast('Save failed','error'); }
    });
  }
}
customElements.define('page-meetings', PageMeetings);
