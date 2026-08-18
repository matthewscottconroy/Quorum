import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { confirm } from './confirm-dialog.js';
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
  disconnectedCallback() { clearInterval(this._elapsedTimer); clearInterval(this._spkTimer); }

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
    // Button state keys on what the JOURNAL says, not just meeting status:
    // "Call to order" journaled once must not re-arm and invite a duplicate.
    let journal = [];
    try { journal = (await api('GET', `/meetings/${id}/minutes`)) ?? []; }
    catch { /* the buttons degrade to status-only gating */ }
    const orderEntry = journal.find(e => e.kind === 'call_to_order');
    const calledToOrder = !!orderEntry;
    const adjourned = journal.some(e => e.kind === 'adjournment');
    const inSession = calledToOrder && !adjourned && mt.status !== 'completed' && mt.status !== 'cancelled';
    clearInterval(this._elapsedTimer);
    clearInterval(this._spkTimer);
    this._spkTimer = null;

    this.innerHTML = `
      <div class="page-header" style="flex-wrap:wrap;gap:.75rem;align-items:flex-end">
        <div>
          <a href="#/meetings" style="font-size:.8rem;color:var(--color-text-muted);text-decoration:none">← All meetings</a>
          <h1 style="margin:.15rem 0 0">${esc(mt.title)}</h1>
          <div style="font-size:.85rem;color:var(--color-text-muted)">
            ${fmtDateTime(mt.scheduled_at)}${mt.location ? ' · ' + esc(mt.location) : ''}
            <span class="badge badge-${esc(mt.status)}" style="margin-left:.4rem">${esc(mt.status)}</span>
            ${inSession ? '<span id="elapsed-chip" class="badge" style="margin-left:.4rem;background:var(--color-bg);color:var(--color-text);font-variant-numeric:tabular-nums" title="Since call to order">⏱ …</span>' : ''}
          </div>
        </div>
        <div style="margin-left:auto;display:flex;gap:.5rem;flex-wrap:wrap">
          ${canWrite() && mt.status === 'scheduled' && !calledToOrder ? '<button class="btn-primary" id="order-btn" title="Journals “called to order” with the current time">▶ Call to order</button>' : ''}
          ${canWrite() && mt.status !== 'completed' && mt.status !== 'cancelled' && !adjourned ? '<button class="btn-secondary" id="adjourn-btn" title="Journals the adjournment and marks the meeting completed">■ Adjourn</button>' : ''}
          <button class="btn-secondary" id="edit-btn">Edit details</button>
        </div>
      </div>
      ${mt.agenda && canWrite() && !adjourned && mt.status !== 'cancelled' ? (() => {
        // The agenda as a WORKLIST: each line is checkable, and checking it
        // journals "Taken up: …" — the minutes write themselves as the chair
        // works down the list. Already-journaled lines render done.
        const lines = mt.agenda.split('\n').map(l => l.replace(/^\s*(?:[-*•]|\d+[.)])\s*/, '').trim()).filter(Boolean).slice(0, 40);
        return `
      <div class="card" style="padding:.75rem 1.25rem;margin-bottom:1rem">
        <strong style="font-size:.85rem">Agenda</strong>
        <div id="agenda-checklist" style="margin-top:.4rem;display:flex;flex-direction:column;gap:.15rem">
          ${lines.map((l, i) => {
            const done = journal.some(e => e.body === `Taken up: ${l}` || e.body === `Taken up: ${l}.`);
            return `<label style="display:flex;gap:.45rem;align-items:baseline;margin:0;font-weight:400;text-transform:none;letter-spacing:normal;font-size:.85rem;${done ? 'color:var(--color-text-muted);text-decoration:line-through' : ''}">
              <input type="checkbox" class="ag-item" data-line="${esc(l)}" ${done ? 'checked disabled' : ''} style="width:auto" title="${done ? 'Already journaled' : 'Journal “Taken up: …” to the minutes'}">${esc(l)}</label>`;
          }).join('')}
        </div>
      </div>`;
      })() : mt.agenda ? `
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
          ${canWrite() ? `
          <div class="card" style="padding:.75rem 1.25rem">
            <div style="display:flex;gap:.5rem;align-items:center;flex-wrap:wrap">
              <strong style="font-size:.85rem">⏱ Speaker</strong>
              <input id="spk-name" placeholder="Who has the floor?" style="flex:1;min-width:140px;font-size:.85rem">
              <span id="spk-clock" style="font-variant-numeric:tabular-nums;font-size:1rem;min-width:52px">0:00</span>
              <button class="btn-secondary" id="spk-toggle" style="font-size:.8rem">Start</button>
              <button class="btn-ghost" id="spk-log" style="font-size:.8rem" disabled title="Journals “X spoke, N min” as a discussion entry">Log</button>
            </div>
          </div>` : ''}
          <div class="card" style="padding:1rem 1.25rem"><div id="minutes-section"><span class="spinner"></span></div></div>
          <div class="card" style="padding:1rem 1.25rem">
            <h3 style="font-size:.95rem;margin-bottom:.6rem">Decision log</h3>
            <div id="run-decisions"><span class="spinner"></span></div>
            ${canWrite() && mt.status !== 'cancelled' ? `
            <div style="display:flex;gap:.4rem;margin-top:.6rem;flex-wrap:wrap">
              <input id="qd-summary" placeholder="Quick decision (no formal motion)…" style="flex:1;min-width:160px;font-size:.85rem">
              <select id="qd-outcome" style="font-size:.85rem;max-width:110px" aria-label="Outcome">
                <option>passed</option><option>failed</option><option>tabled</option><option>noted</option>
              </select>
              <button class="btn-secondary" id="qd-add" style="font-size:.8rem">Record</button>
            </div>` : ''}
          </div>
        </div>
      </div>`;

    // The last act of every meeting shouldn't cost a modal round-trip:
    // Call to order / Adjourn journal the moment with the wall-clock time,
    // and Adjourn flips the meeting to completed.
    const stamp = () => new Date().toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
    // The elapsed chip: how long the room has been in session, off the
    // call-to-order journal line's own timestamp.
    const chipEl = this.querySelector('#elapsed-chip');
    if (chipEl && orderEntry?.recorded_at) {
      const t0 = new Date(orderEntry.recorded_at).getTime();
      const paint = () => {
        const mins = Math.max(Math.floor((Date.now() - t0) / 60000), 0);
        chipEl.textContent = `⏱ ${mins >= 60 ? `${Math.floor(mins / 60)}h ${mins % 60}m` : `${mins}m`} in session`;
      };
      paint();
      this._elapsedTimer = setInterval(paint, 30000);
    }
    // Agenda checklist → journal: one click per item, minutes written live.
    this.querySelectorAll('.ag-item:not([disabled])').forEach(cb => cb.addEventListener('change', async () => {
      if (!cb.checked) return;
      cb.disabled = true;
      try {
        await api('POST', `/meetings/${mt.id}/minutes`, { kind: 'note', body: `Taken up: ${cb.dataset.line}` });
        const label = cb.closest('label');
        if (label) { label.style.color = 'var(--color-text-muted)'; label.style.textDecoration = 'line-through'; }
        this._pm.renderMinutes(this, mt.id, mt);
      } catch (err) {
        cb.checked = false; cb.disabled = false;
        toast(err.error ?? 'Failed to journal', 'error');
      }
    }));
    // Quick decisions land straight in the log — no editor-over-the-run-page.
    const qdAdd = this.querySelector('#qd-add');
    qdAdd?.addEventListener('click', guardButton(qdAdd, async () => {
      const summary = this.querySelector('#qd-summary').value.trim();
      if (!summary) { toast('What was decided?', 'error'); return; }
      try {
        await api('POST', `/meetings/${mt.id}/decisions`, { summary, outcome: this.querySelector('#qd-outcome').value });
        this.querySelector('#qd-summary').value = '';
        toast('Decision recorded', 'success');
        renderDecisions();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
    // The speaker timer: pure client state, one keypress from the journal —
    // the tool a chair actually lacks mid-meeting.
    {
      const nameEl = this.querySelector('#spk-name');
      const clockEl = this.querySelector('#spk-clock');
      const toggleEl = this.querySelector('#spk-toggle');
      const logEl = this.querySelector('#spk-log');
      if (nameEl && clockEl && toggleEl && logEl) {
        let startedAt = 0;
        // Elapsed is CAPTURED at Stop: the journal records how long they
        // spoke, not how long ago the chair got around to clicking Log.
        let capturedMs = null;
        let posting = false;
        const fmtClock = s2 => `${Math.floor(s2 / 60)}:${String(s2 % 60).padStart(2, '0')}`;
        toggleEl.addEventListener('click', () => {
          if (this._spkTimer) {
            clearInterval(this._spkTimer);
            this._spkTimer = null;
            capturedMs = Date.now() - startedAt;
            toggleEl.textContent = 'Start';
            logEl.disabled = false;
          } else {
            startedAt = Date.now();
            capturedMs = null;
            clockEl.textContent = '0:00';
            // Instance-held interval: load() re-renders this card (call to
            // order, editor close, …) and must be able to stop the old clock.
            this._spkTimer = setInterval(() => {
              clockEl.textContent = fmtClock(Math.floor((Date.now() - startedAt) / 1000));
            }, 1000);
            toggleEl.textContent = 'Stop';
            logEl.disabled = true;
          }
        });
        // Hand-rolled guard instead of guardButton: after a successful post
        // the button must STAY disabled (one Stop = one journal line), and
        // guardButton's finally would re-enable it.
        logEl.addEventListener('click', async () => {
          if (posting || capturedMs === null) return;
          posting = true;
          logEl.disabled = true;
          const who = nameEl.value.trim() || 'Speaker';
          const mins = Math.max(Math.round(capturedMs / 60000), 1);
          try {
            await api('POST', `/meetings/${mt.id}/minutes`, {
              kind: 'discussion', body: `${who} spoke, ${mins} min.`,
            });
            capturedMs = null;
            toast('Logged to the journal', 'success');
            nameEl.value = '';
            clockEl.textContent = '0:00';
            this._pm.renderMinutes(this, mt.id, mt);
          } catch (err) {
            toast(err.error ?? 'Failed', 'error');
            logEl.disabled = false; // retry allowed — nothing was journaled
          } finally { posting = false; }
        });
      }
    }

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
      // Ending the meeting deserves a beat of confirmation — and the natural
      // next step (finalizing) is offered right after, not left for later.
      if (!await confirm('Adjourn now? This journals the adjournment and marks the meeting completed.', 'Adjourn meeting')) return;
      // Two calls, deliberately ordered: if the journal line is refused
      // (finalized minutes), still mark the meeting completed — the status
      // is the part the run page can't otherwise reach.
      let journaled = true;
      try {
        await api('POST', `/meetings/${mt.id}/minutes`, { kind: 'adjournment', body: `Meeting adjourned at ${stamp()}.` });
      } catch { journaled = false; }
      try {
        await api('PATCH', `/meetings/${mt.id}`, { status: 'completed' });
        toast(journaled ? 'Adjourned' : 'Meeting marked completed (journal is finalized)', 'success');
        await this.load();
        if (!this.isConnected) return; // load() hit an error and left the page
        // The natural next step, offered in the moment: adjournment is when
        // minutes get finalized, not "later" (which becomes never).
        if (journaled && await confirm('Finalize the minutes now? Review the journal first if anything is missing — finalizing freezes it into the official record.', 'Finalize minutes?')) {
          // renderMinutes streams in after load(); wait for its button.
          for (let i = 0; i < 20 && !this.querySelector('#min-finalize'); i++) {
            await new Promise(r2 => setTimeout(r2, 150));
          }
          const fin = this.querySelector('#min-finalize');
          if (fin) { fin.scrollIntoView({ block: 'center' }); fin.click(); }
          else toast('Journal still loading — use “Finalize minutes” below', 'info');
        }
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
