import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDateTime, guardButton } from '../utils.js';
import './page-meetings.js';

/**
 * The RUN MEETING page (`#/meeting-run?id=…`): the meeting editor's modal is
 * fine for scheduling, but running a live meeting deserves a full screen —
 * motions and quorum on one side, the minutes journal and decision log on
 * the other, attendance in reach for the quorum count. The heavy lifting is
 * delegated to PageMeetings' section renderers, which were written against a
 * generic root, so the modal and this page can never drift apart.
 */
class PageMeetingRun extends HTMLElement {
  connectedCallback() {
    this._pm = new (customElements.get('page-meetings'))();
    this.load();
  }

  async load() {
    const id = new URLSearchParams(location.hash.split('?')[1] ?? '').get('id');
    if (!id) { location.hash = '#/meetings'; return; }
    let mt;
    try { mt = await api('GET', `/meetings/${id}`); }
    catch { toast('Meeting not found', 'error'); location.hash = '#/meetings'; return; }
    if (!this.isConnected) return;

    this.innerHTML = `
      <div class="page-header" style="flex-wrap:wrap;gap:.75rem;align-items:flex-end">
        <div>
          <a href="#/meetings" style="font-size:.8rem;color:var(--color-text-muted);text-decoration:none">← All meetings</a>
          <h1 style="margin:.15rem 0 0">${esc(mt.title)}</h1>
          <div style="font-size:.85rem;color:var(--color-text-muted)">
            ${fmtDateTime(mt.scheduled_at)}${mt.location ? ' · ' + esc(mt.location) : ''}
            <span class="badge badge-${esc(mt.status)}" style="margin-left:.4rem">${esc(mt.status)}</span>
          </div>
        </div>
        <div style="margin-left:auto;display:flex;gap:.5rem;flex-wrap:wrap">
          ${canWrite() && mt.status === 'scheduled' ? '<button class="btn-primary" id="order-btn" title="Journals “called to order” with the current time">▶ Call to order</button>' : ''}
          ${canWrite() && mt.status !== 'completed' && mt.status !== 'cancelled' ? '<button class="btn-secondary" id="adjourn-btn" title="Journals the adjournment and marks the meeting completed">■ Adjourn</button>' : ''}
          <button class="btn-secondary" id="edit-btn">Edit details</button>
        </div>
      </div>
      ${mt.agenda ? `
      <details style="margin-bottom:1rem">
        <summary style="cursor:pointer;font-size:.85rem;font-weight:600">Agenda</summary>
        <p style="white-space:pre-wrap;font-size:.85rem;color:var(--color-text-muted);margin-top:.4rem">${esc(mt.agenda)}</p>
      </details>` : ''}
      <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,380px),1fr));gap:1.25rem;align-items:start">
        <div style="display:flex;flex-direction:column;gap:1.25rem">
          <div class="card" style="padding:1rem 1.25rem"><div id="gov-section"><span class="spinner"></span></div></div>
          <div class="card" style="padding:1rem 1.25rem">
            <h3 style="font-size:.95rem;margin-bottom:.6rem">Attendance</h3>
            <div id="attendance-section"><span class="spinner"></span></div>
          </div>
        </div>
        <div style="display:flex;flex-direction:column;gap:1.25rem">
          <div class="card" style="padding:1rem 1.25rem"><div id="minutes-section"><span class="spinner"></span></div></div>
          <div class="card" style="padding:1rem 1.25rem">
            <h3 style="font-size:.95rem;margin-bottom:.6rem">Decision log</h3>
            <div id="run-decisions"><span class="spinner"></span></div>
          </div>
        </div>
      </div>`;

    // The last act of every meeting shouldn't cost a modal round-trip:
    // Call to order / Adjourn journal the moment with the wall-clock time,
    // and Adjourn flips the meeting to completed.
    const stamp = () => new Date().toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
    const orderBtn = this.querySelector('#order-btn');
    orderBtn?.addEventListener('click', guardButton(orderBtn, async () => {
      try {
        await api('POST', `/meetings/${mt.id}/minutes`, { kind: 'call_to_order', body: `Meeting called to order at ${stamp()}.` });
        toast('Called to order', 'success');
        this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
    const adjournBtn = this.querySelector('#adjourn-btn');
    adjournBtn?.addEventListener('click', guardButton(adjournBtn, async () => {
      // Two calls, deliberately ordered: if the journal line is refused
      // (finalized minutes), still mark the meeting completed — the status
      // is the part the run page can't otherwise reach.
      let journaled = true;
      try {
        await api('POST', `/meetings/${mt.id}/minutes`, { kind: 'adjournment', body: `Meeting adjourned at ${stamp()}.` });
      } catch { journaled = false; }
      try {
        await api('PATCH', `/meetings/${mt.id}`, { status: 'completed' });
        toast(journaled ? 'Adjourned — meeting marked completed' : 'Meeting marked completed (journal is finalized)', 'success');
        this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
    // Editing details still happens in the familiar modal, layered on top.
    // The dialog's own close event replaces the old 800ms poll (which also
    // mistook ANY open dialog — like a finalize confirm — for the editor).
    this.querySelector('#edit-btn').addEventListener('click', async () => {
      const ed = await this._pm.openEditor(mt.id);
      if (ed?.dialog) {
        ed.dialog.addEventListener('close', () => { if (this.isConnected) this.load(); }, { once: true });
      } else if (this.isConnected) {
        this.load();
      }
    });

    const renderDecisions = async () => {
      let fresh;
      try { fresh = await api('GET', `/meetings/${mt.id}`); } catch { return; }
      const el = this.querySelector('#run-decisions');
      if (!el) return;
      el.innerHTML = (fresh.decisions ?? []).length
        ? fresh.decisions.map(d => `
          <div style="border:1px solid var(--color-border);border-radius:var(--radius);padding:.55rem .75rem;margin-bottom:.45rem">
            <div style="font-weight:600;font-size:.88rem">${esc(d.summary)}
              ${d.motion_id ? '<span class="badge" style="background:var(--color-bg);color:var(--color-text-muted);font-size:.62rem">from motion</span>' : ''}</div>
            <div style="font-size:.78rem;color:var(--color-text-muted)">${esc(d.outcome)}${d.vote_for != null ? ` · ${esc(d.vote_for)}/${esc(d.vote_against)}/${esc(d.vote_abstain)}` : ''}</div>
          </div>`).join('')
        : '<p style="font-size:.85rem;color:var(--color-text-muted)">Nothing decided yet.</p>';
    };

    this._pm.renderGovernance(this, mt.id);
    this._pm.renderMinutes(this, mt.id, mt);
    this._pm.renderAttendance(this, mt.id, mt);
    renderDecisions();
    // Motion lifecycle actions rewrite the decision log and minutes journal
    // server-side; the sibling panels follow along here just like the modal.
    // load() re-runs after every editor close, so keep exactly ONE listener —
    // the handler reads instance state instead of closing over stale locals,
    // or each edit would stack another round of redundant refetches.
    this._govMt = mt;
    this._govDecisions = renderDecisions;
    if (!this._govWired) {
      this._govWired = true;
      this.addEventListener('gov-changed', () => {
        this._govDecisions?.();
        if (this._govMt) this._pm.renderMinutes(this, this._govMt.id, this._govMt);
      });
    }
  }
}
customElements.define('page-meeting-run', PageMeetingRun);
