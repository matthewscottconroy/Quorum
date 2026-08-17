import { api, apiDownload, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton } from '../utils.js';

const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const STATUS_COLOR = {
  scheduled: 'var(--color-primary,#2563eb)',
  completed: 'var(--color-success,#137333)',
  cancelled: 'var(--color-text-muted,#6b7280)',
};

/** Local YYYY-MM-DD key for bucketing meetings into grid days. */
function dayKey(d) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

// Month-grid calendar of meetings. Data comes from GET /meetings?from&to (the
// visible grid range); scheduling from here reuses POST /meetings, and every
// meeting can be pulled into a personal calendar via the .ics endpoints.
class PageCalendar extends HTMLElement {
  constructor() {
    super();
    const now = new Date();
    this._year = now.getFullYear();
    this._month = now.getMonth(); // 0-based
    this._byDay = new Map();
  }

  connectedCallback() {
    this.render();
    this.load();
  }

  /** First cell of the grid: the Sunday on/before the 1st of the month. */
  gridStart() {
    const first = new Date(this._year, this._month, 1);
    const start = new Date(first);
    start.setDate(first.getDate() - first.getDay());
    return start;
  }

  render() {
    const monthName = new Date(this._year, this._month, 1)
      .toLocaleDateString(undefined, { month: 'long', year: 'numeric' });
    this.innerHTML = `
      <div class="page-header">
        <h1 style="display:flex;align-items:center;gap:.75rem">
          Calendar
          <span style="font-size:1rem;font-weight:600;color:var(--color-text-muted)">${esc(monthName)}</span>
        </h1>
        <div style="display:flex;gap:.5rem;align-items:center">
          <button class="btn-secondary" id="prev-btn" aria-label="Previous month">‹</button>
          <button class="btn-secondary" id="today-btn">Today</button>
          <button class="btn-secondary" id="next-btn" aria-label="Next month">›</button>
          <button class="btn-secondary" id="ics-btn" title="Download the schedule for Google/Outlook/Apple Calendar">Download .ics</button>
          ${canWrite() ? '<button class="btn-primary" id="new-btn">+ Schedule</button>' : ''}
        </div>
      </div>
      <div class="card" style="padding:.75rem;overflow-x:auto">
        <div class="cal-grid" id="grid" style="min-width:700px">
          ${WEEKDAYS.map(d => `<div class="cal-head">${d}</div>`).join('')}
        </div>
      </div>
    `;
    this.applyStyles();
    this.querySelector('#prev-btn').addEventListener('click', () => this.shiftMonth(-1));
    this.querySelector('#next-btn').addEventListener('click', () => this.shiftMonth(1));
    this.querySelector('#today-btn').addEventListener('click', () => {
      const now = new Date();
      this._year = now.getFullYear();
      this._month = now.getMonth();
      this.render();
      this.load();
    });
    this.querySelector('#ics-btn').addEventListener('click', () => {
      apiDownload('/export/meetings.ics', 'quorum-meetings.ics').catch(() => toast('Download failed', 'error'));
    });
    this.querySelector('#new-btn')?.addEventListener('click', () => this.openSchedule(new Date()));
  }

  shiftMonth(delta) {
    const d = new Date(this._year, this._month + delta, 1);
    this._year = d.getFullYear();
    this._month = d.getMonth();
    this.render();
    this.load();
  }

  async load() {
    // Fetch the whole visible grid (6 weeks) with a day of slack on each side
    // so timezone offsets can't drop an edge meeting.
    const start = this.gridStart();
    const from = new Date(start); from.setDate(from.getDate() - 1);
    const to = new Date(start); to.setDate(to.getDate() + 43);
    let meetings = [];
    try {
      const page = await api('GET', `/meetings?from=${dayKey(from)}&to=${dayKey(to)}&limit=500`);
      meetings = page?.data ?? [];
    } catch {
      toast('Failed to load the calendar', 'error');
    }
    this._byDay = new Map();
    for (const m of meetings) {
      const key = dayKey(new Date(m.scheduled_at));
      if (!this._byDay.has(key)) this._byDay.set(key, []);
      this._byDay.get(key).push(m);
    }
    for (const list of this._byDay.values()) {
      list.sort((a, b) => new Date(a.scheduled_at) - new Date(b.scheduled_at));
    }
    this.renderGrid();
  }

  renderGrid() {
    const grid = this.querySelector('#grid');
    if (!grid) return;
    const todayKey = dayKey(new Date());
    const start = this.gridStart();
    let cells = '';
    for (let i = 0; i < 42; i++) {
      const d = new Date(start);
      d.setDate(start.getDate() + i);
      const key = dayKey(d);
      const inMonth = d.getMonth() === this._month;
      const dayMeetings = this._byDay.get(key) ?? [];
      const shown = dayMeetings.slice(0, 3);
      const extra = dayMeetings.length - shown.length;
      cells += `
        <div class="cal-cell ${inMonth ? '' : 'cal-out'} ${key === todayKey ? 'cal-today' : ''}"
             data-day="${key}" role="button" tabindex="0"
             aria-label="${esc(d.toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric' }))}, ${dayMeetings.length} meeting(s)">
          <div class="cal-daynum">${d.getDate()}</div>
          ${shown.map(m => `
            <div class="cal-chip ${m.status === 'cancelled' ? 'cal-chip-cancelled' : ''}"
                 style="border-left-color:${STATUS_COLOR[m.status] ?? STATUS_COLOR.scheduled}">
              <span class="cal-chip-time">${esc(new Date(m.scheduled_at).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' }))}</span>
              ${esc(m.title)}
            </div>`).join('')}
          ${extra > 0 ? `<div class="cal-more">+${extra} more</div>` : ''}
        </div>`;
    }
    grid.innerHTML = WEEKDAYS.map(d => `<div class="cal-head">${d}</div>`).join('') + cells;
    grid.querySelectorAll('.cal-cell').forEach(cell => {
      const open = () => this.openDay(cell.dataset.day);
      cell.addEventListener('click', open);
      cell.addEventListener('keydown', e => {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(); }
      });
    });
  }

  /** Day drill-in: the day's meetings with details, .ics per meeting, and an
   *  officer shortcut to schedule on that date. */
  openDay(key) {
    const meetings = this._byDay.get(key) ?? [];
    const [y, mo, da] = key.split('-').map(Number);
    const date = new Date(y, mo - 1, da);
    const title = date.toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric' });
    const { dialog, close } = openModal({
      title,
      maxWidth: '480px',
      body: `
        <div class="modal-body">
          ${meetings.length ? meetings.map(m => `
            <div style="display:flex;gap:.75rem;align-items:flex-start;padding:.6rem 0;border-bottom:1px solid var(--color-border,#eee)">
              <div style="min-width:70px;font-variant-numeric:tabular-nums;font-weight:600">
                ${esc(new Date(m.scheduled_at).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' }))}
                ${m.ends_at ? `<div style="font-weight:400;font-size:.75rem;color:var(--color-text-muted)">– ${esc(new Date(m.ends_at).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' }))}</div>` : ''}
              </div>
              <div style="flex:1">
                <div style="font-weight:600"><a href="#/meetings?open=${esc(m.id)}" style="color:inherit">${esc(m.title)}</a></div>
                ${m.location ? `<div style="font-size:.82rem;color:var(--color-text-muted)">${esc(m.location)}</div>` : ''}
                <span class="badge badge-${esc(m.status)}">${esc(m.status)}</span>
              </div>
              <button class="btn-ghost btn-sm" data-ics="${esc(m.id)}" title="Add to your calendar">📆 .ics</button>
            </div>`).join('')
            : '<p style="color:var(--color-text-muted)">No meetings this day.</p>'}
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="close-btn">Close</button>
          ${canWrite() ? '<button class="btn-primary" id="schedule-btn">+ Schedule this day</button>' : ''}
        </div>
      `,
    });
    dialog.querySelector('#close-btn').addEventListener('click', close);
    dialog.querySelectorAll('[data-ics]').forEach(btn => {
      btn.addEventListener('click', () => {
        apiDownload(`/meetings/${btn.dataset.ics}/ics`, 'meeting.ics').catch(() => toast('Download failed', 'error'));
      });
    });
    dialog.querySelector('#schedule-btn')?.addEventListener('click', () => {
      close();
      // Default new meetings on this day to 18:00 local — adjust in the form.
      this.openSchedule(new Date(y, mo - 1, da, 18, 0));
    });
  }

  /** Quick-create prefilled with the chosen date/time (officer+). */
  openSchedule(when) {
    const pad = n => String(n).padStart(2, '0');
    const toLocal = d => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
    const localValue = toLocal(when);
    const endValue = toLocal(new Date(when.getTime() + 60 * 60 * 1000)); // default 1h block
    const { dialog, close } = openModal({
      title: 'Schedule meeting',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="f-title">Title *</label><input id="f-title"></div>
          <div class="form-row">
            <div class="form-group"><label for="f-dt">Starts *</label><input id="f-dt" type="datetime-local" value="${localValue}"></div>
            <div class="form-group"><label for="f-end">Ends</label><input id="f-end" type="datetime-local" value="${endValue}"></div>
          </div>
          <div class="form-group"><label for="f-loc">Location</label><input id="f-loc" placeholder="Room or video link"></div>
          <div class="form-group"><label for="f-agenda">Agenda</label><textarea id="f-agenda" rows="3"></textarea></div>
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
      const dt = dialog.querySelector('#f-dt').value;
      if (!title || !dt) { toast('Title and date are required', 'error'); return; }
      try {
        const end = dialog.querySelector('#f-end').value;
        if (end && new Date(end) <= new Date(dt)) { toast('End must be after the start', 'error'); return; }
        await api('POST', '/meetings', {
          title,
          scheduled_at: new Date(dt).toISOString(),
          ends_at: end ? new Date(end).toISOString() : null,
          location: dialog.querySelector('#f-loc').value.trim() || null,
          agenda: dialog.querySelector('#f-agenda').value.trim() || null,
        });
        toast('Meeting scheduled', 'success');
        close();
        this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  applyStyles() {
    if (document.getElementById('cal-style')) return;
    const s = document.createElement('style');
    s.id = 'cal-style';
    s.textContent = `
      .cal-grid { display:grid; grid-template-columns:repeat(7,1fr); gap:4px; }
      .cal-head { text-align:center; font-size:.72rem; font-weight:700; letter-spacing:.05em;
        text-transform:uppercase; color:var(--color-text-muted); padding:.35rem 0; }
      .cal-cell { min-height:96px; border:1px solid var(--color-border,#e5e7eb); border-radius:6px;
        padding:.3rem; cursor:pointer; background:var(--color-surface,#fff);
        display:flex; flex-direction:column; gap:2px; overflow:hidden; }
      .cal-cell:hover { border-color:var(--color-primary,#2563eb); }
      .cal-cell:focus-visible { outline:2px solid var(--color-primary,#2563eb); outline-offset:1px; }
      .cal-out { opacity:.45; background:var(--color-bg,#f8fafc); }
      .cal-today { box-shadow: inset 0 0 0 2px var(--color-primary,#2563eb); }
      .cal-today .cal-daynum { color:var(--color-primary,#2563eb); font-weight:800; }
      .cal-daynum { font-size:.78rem; font-weight:600; color:var(--color-text-muted); }
      .cal-chip { font-size:.7rem; line-height:1.25; padding:1px 4px 1px 5px; border-left:3px solid;
        border-radius:3px; background:var(--color-bg,#f1f5f9);
        white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
      .cal-chip-cancelled { text-decoration:line-through; opacity:.6; }
      .cal-chip-time { font-weight:700; margin-right:2px; }
      .cal-more { font-size:.68rem; color:var(--color-text-muted); }`;
    document.head.appendChild(s);
  }
}
customElements.define('page-calendar', PageCalendar);
