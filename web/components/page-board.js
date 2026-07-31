import { api, canWrite } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete } from '../utils.js';

const COLUMNS = [
  ['open', 'Open'],
  ['in_progress', 'In progress'],
  ['done', 'Done'],
];
const PRIORITY_COLOR = { high: 'var(--color-danger,#dc2626)', normal: 'var(--color-primary,#2563eb)', low: 'var(--color-text-muted,#9ca3af)' };

// Sprint + kanban board over action items: columns are the workflow states,
// the sprint selector scopes the board (Backlog = items with no sprint), and
// officers drag cards between columns, quick-add work, and manage sprints.
class PageBoard extends HTMLElement {
  constructor() {
    super();
    this._sprints = [];
    this._items = [];
    this._members = [];
    this._scope = ''; // '' all | 'none' backlog | sprint id
    this._assignee = '';
  }

  connectedCallback() {
    this.render();
    this.load(true);
  }

  async load(withRefs = false) {
    try {
      const qs = new URLSearchParams({ limit: '500' });
      if (this._scope) qs.set('sprint_id', this._scope);
      const fetches = [api('GET', '/action-items?' + qs), api('GET', '/sprints')];
      if (withRefs) fetches.push(api('GET', '/members?limit=200'));
      const [items, sprints, members] = await Promise.all(fetches);
      this._items = items?.data ?? [];
      this._sprints = sprints ?? [];
      if (members) this._members = members?.data ?? [];
      this.renderBoard();
    } catch {
      toast('Failed to load the board', 'error');
    }
  }

  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Board</h1>
        <div style="display:flex;gap:.5rem;align-items:center;flex-wrap:wrap">
          <select id="scope-sel" style="min-width:170px"></select>
          <select id="assignee-sel" style="min-width:150px"></select>
          ${canWrite() ? '<button class="btn-secondary" id="sprint-new">+ New sprint</button>' : ''}
        </div>
      </div>
      <div id="sprint-bar"></div>
      <div class="board-grid" id="board"></div>
    `;
    this.applyStyles();
    this.querySelector('#scope-sel').addEventListener('change', e => { this._scope = e.target.value; this.load(); });
    this.querySelector('#assignee-sel').addEventListener('change', e => { this._assignee = e.target.value; this.renderBoard(); });
    this.querySelector('#sprint-new')?.addEventListener('click', () => this.openSprintModal(null));
  }

  renderBoard() {
    // Scope + assignee selectors (preserve selection across reloads).
    const scopeSel = this.querySelector('#scope-sel');
    scopeSel.innerHTML = `
      <option value="">All work</option>
      <option value="none">Backlog (no sprint)</option>
      ${this._sprints.map(s => `<option value="${esc(s.id)}">${esc(s.name)}${s.status === 'active' ? ' · active' : ''}</option>`).join('')}`;
    scopeSel.value = this._scope;
    const aSel = this.querySelector('#assignee-sel');
    aSel.innerHTML = `<option value="">Everyone</option>` +
      this._members.map(m => `<option value="${esc(m.id)}">${esc(m.display_name)}</option>`).join('');
    aSel.value = this._assignee;

    this.renderSprintBar();

    const officer = canWrite();
    const items = this._assignee
      ? this._items.filter(i => i.assignee_id === this._assignee)
      : this._items;

    const board = this.querySelector('#board');
    board.innerHTML = COLUMNS.map(([status, label]) => {
      const cards = items.filter(i => i.status === status);
      return `
        <div class="board-col" data-status="${status}">
          <div class="board-col-head">${label} <span class="board-count">${cards.length}</span></div>
          ${officer && status === 'open' ? `
            <div class="board-quickadd">
              <input id="qa-input" placeholder="+ Add work item and press Enter">
            </div>` : ''}
          <div class="board-cards">
            ${cards.map(i => this.cardHTML(i, officer)).join('') ||
              '<div class="board-empty">Nothing here.</div>'}
          </div>
        </div>`;
    }).join('');

    // Quick add (Open column).
    board.querySelector('#qa-input')?.addEventListener('keydown', async e => {
      if (e.key !== 'Enter') return;
      const title = e.target.value.trim();
      if (!title) return;
      try {
        await api('POST', '/action-items', {
          title,
          sprint_id: this._scope && this._scope !== 'none' ? this._scope : null,
        });
        toast('Added', 'success');
        this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    });

    // Card editing.
    board.querySelectorAll('.board-card').forEach(card => {
      if (officer) {
        card.addEventListener('click', () => {
          const item = this._items.find(i => i.id === card.dataset.id);
          if (item) this.openCardModal(item);
        });
      }
    });

    // Drag & drop between columns (officers move work through the flow).
    if (officer) {
      board.querySelectorAll('.board-card').forEach(card => {
        card.setAttribute('draggable', 'true');
        card.addEventListener('dragstart', e => {
          e.dataTransfer.setData('text/plain', card.dataset.id);
          e.dataTransfer.effectAllowed = 'move';
          card.classList.add('dragging');
        });
        card.addEventListener('dragend', () => card.classList.remove('dragging'));
      });
      board.querySelectorAll('.board-col').forEach(col => {
        col.addEventListener('dragover', e => { e.preventDefault(); col.classList.add('dragover'); });
        col.addEventListener('dragleave', () => col.classList.remove('dragover'));
        col.addEventListener('drop', async e => {
          e.preventDefault();
          col.classList.remove('dragover');
          const id = e.dataTransfer.getData('text/plain');
          const status = col.dataset.status;
          const item = this._items.find(i => i.id === id);
          if (!item || item.status === status) return;
          try {
            await api('PATCH', `/action-items/${id}`, { status });
            this.load();
          } catch (err) { toast(err.error ?? 'Move failed', 'error'); }
        });
      });
    }
  }

  cardHTML(i, officer) {
    const due = i.due_date ? new Date(i.due_date) : null;
    const overdue = due && i.status !== 'done' && due < new Date();
    return `
      <div class="board-card ${officer ? 'board-card-edit' : ''}" data-id="${esc(i.id)}"
           style="border-left:3px solid ${PRIORITY_COLOR[i.priority] ?? PRIORITY_COLOR.normal}">
        <div class="board-card-title">${esc(i.title)}</div>
        <div class="board-card-meta">
          ${i.assignee_name ? `<span>👤 ${esc(i.assignee_name)}</span>` : '<span style="opacity:.6">unassigned</span>'}
          ${due ? `<span style="${overdue ? 'color:var(--color-danger,#dc2626);font-weight:700' : ''}">📅 ${esc(due.toLocaleDateString())}</span>` : ''}
        </div>
        ${i.sprint_name && !this._scope ? `<div class="board-card-sprint">🏁 ${esc(i.sprint_name)}</div>` : ''}
      </div>`;
  }

  renderSprintBar() {
    const bar = this.querySelector('#sprint-bar');
    const sp = this._sprints.find(s => s.id === this._scope);
    if (!sp) { bar.innerHTML = ''; return; }
    const done = this._items.filter(i => i.status === 'done').length;
    const total = this._items.filter(i => i.status !== 'cancelled').length;
    const pct = total ? Math.round((done / total) * 100) : 0;
    bar.innerHTML = `
      <div class="card" style="padding:.8rem 1rem;margin-bottom:1rem;display:flex;gap:1rem;align-items:center;flex-wrap:wrap">
        <div style="flex:1;min-width:220px">
          <div style="font-weight:700">${esc(sp.name)}
            <span class="badge" style="background:var(--color-bg);color:var(--color-text-muted)">${esc(sp.status)}</span></div>
          ${sp.goal ? `<div style="font-size:.85rem;color:var(--color-text-muted)">${esc(sp.goal)}</div>` : ''}
          <div style="font-size:.78rem;color:var(--color-text-muted)">${esc(sp.starts_on)} → ${esc(sp.ends_on)}</div>
        </div>
        <div style="min-width:180px;flex:1">
          <div style="display:flex;justify-content:space-between;font-size:.78rem;color:var(--color-text-muted)">
            <span>${done}/${total} done</span><span>${pct}%</span></div>
          <div style="height:8px;border-radius:999px;background:var(--color-bg);overflow:hidden">
            <div style="height:100%;width:${pct}%;background:var(--color-success,#137333)"></div></div>
        </div>
        ${canWrite() ? `
          <div style="display:flex;gap:.4rem">
            <button class="btn-secondary" id="sprint-edit" style="font-size:.8rem">Edit</button>
            <button class="btn-ghost" id="sprint-del" style="font-size:.8rem;color:var(--color-danger)">Delete</button>
          </div>` : ''}
      </div>`;
    bar.querySelector('#sprint-edit')?.addEventListener('click', () => this.openSprintModal(sp));
    bar.querySelector('#sprint-del')?.addEventListener('click', () => {
      confirmDelete({
        noun: 'sprint (items return to the backlog)',
        name: sp.name,
        onConfirm: async (confirmVal) => {
          try {
            await api('DELETE', `/sprints/${sp.id}?confirm=${encodeURIComponent(confirmVal)}`);
            toast('Sprint deleted', 'success');
            this._scope = '';
            this.load();
          } catch (err) { toast(err.error ?? 'Delete failed', 'error'); throw err; }
        },
      });
    });
  }

  openSprintModal(sp) {
    const today = new Date().toISOString().slice(0, 10);
    const { dialog, close } = openModal({
      title: sp ? 'Edit sprint' : 'New sprint',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="s-name">Name *</label><input id="s-name" value="${esc(sp?.name ?? '')}" placeholder="e.g. August iteration"></div>
          <div class="form-group"><label for="s-goal">Goal</label><input id="s-goal" value="${esc(sp?.goal ?? '')}" placeholder="What does success look like?"></div>
          <div class="form-row">
            <div class="form-group"><label for="s-start">Starts *</label><input id="s-start" type="date" value="${esc(sp?.starts_on ?? today)}"></div>
            <div class="form-group"><label for="s-end">Ends *</label><input id="s-end" type="date" value="${esc(sp?.ends_on ?? today)}"></div>
          </div>
          <div class="form-group"><label for="s-status">Status</label>
            <select id="s-status">
              ${['planned', 'active', 'completed'].map(v => `<option value="${v}" ${sp?.status === v ? 'selected' : ''}>${v}</option>`).join('')}
            </select></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">${sp ? 'Save' : 'Create'}</button>
        </div>`,
    });
    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const body = {
        name: dialog.querySelector('#s-name').value.trim(),
        goal: dialog.querySelector('#s-goal').value.trim() || null,
        starts_on: dialog.querySelector('#s-start').value,
        ends_on: dialog.querySelector('#s-end').value,
        status: dialog.querySelector('#s-status').value,
      };
      if (!body.name || !body.starts_on || !body.ends_on) { toast('Name and dates are required', 'error'); return; }
      try {
        if (sp) {
          await api('PATCH', `/sprints/${sp.id}`, body);
        } else {
          const created = await api('POST', '/sprints', body);
          this._scope = created.id; // jump straight into the new sprint
        }
        toast(sp ? 'Sprint saved' : 'Sprint created', 'success');
        close();
        this.load();
      } catch (err) { toast(err.error ?? 'Failed', 'error'); }
    }));
  }

  openCardModal(item) {
    const memberOpts = sel => `<option value="">— unassigned —</option>` +
      this._members.map(m => `<option value="${esc(m.id)}" ${sel === m.id ? 'selected' : ''}>${esc(m.display_name)}</option>`).join('');
    const sprintOpts = sel => `<option value="">— backlog —</option>` +
      this._sprints.map(s => `<option value="${esc(s.id)}" ${sel === s.id ? 'selected' : ''}>${esc(s.name)}</option>`).join('');
    const { dialog, close } = openModal({
      title: 'Work item',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="c-title">Title *</label><input id="c-title" value="${esc(item.title)}"></div>
          <div class="form-group"><label for="c-desc">Description</label><textarea id="c-desc" rows="3">${esc(item.description ?? '')}</textarea></div>
          <div class="form-row">
            <div class="form-group"><label for="c-assignee">Assignee</label><select id="c-assignee">${memberOpts(item.assignee_id)}</select></div>
            <div class="form-group"><label for="c-sprint">Sprint</label><select id="c-sprint">${sprintOpts(item.sprint_id)}</select></div>
          </div>
          <div class="form-row">
            <div class="form-group"><label for="c-priority">Priority</label>
              <select id="c-priority">${['high', 'normal', 'low'].map(v => `<option ${item.priority === v ? 'selected' : ''}>${v}</option>`).join('')}</select></div>
            <div class="form-group"><label for="c-due">Due</label><input id="c-due" type="date" value="${item.due_date ? esc(item.due_date.slice(0, 10)) : ''}"></div>
            <div class="form-group"><label for="c-status">Status</label>
              <select id="c-status">${['open', 'in_progress', 'done', 'cancelled'].map(v => `<option value="${v}" ${item.status === v ? 'selected' : ''}>${v.replace('_', ' ')}</option>`).join('')}</select></div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Save</button>
        </div>`,
    });
    dialog.querySelector('#cancel-btn').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#save-btn');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const title = dialog.querySelector('#c-title').value.trim();
      if (!title) { toast('Title is required', 'error'); return; }
      try {
        await api('PATCH', `/action-items/${item.id}`, {
          title,
          description: dialog.querySelector('#c-desc').value.trim() || null,
          assignee_id: dialog.querySelector('#c-assignee').value || null,
          sprint_id: dialog.querySelector('#c-sprint').value || null,
          priority: dialog.querySelector('#c-priority').value,
          due_date: dialog.querySelector('#c-due').value || null,
          status: dialog.querySelector('#c-status').value,
        });
        toast('Saved', 'success');
        close();
        this.load();
      } catch (err) { toast(err.error ?? 'Save failed', 'error'); }
    }));
  }

  applyStyles() {
    if (document.getElementById('board-style')) return;
    const s = document.createElement('style');
    s.id = 'board-style';
    s.textContent = `
      .board-grid { display:grid; grid-template-columns:repeat(3,1fr); gap:1rem; align-items:start; }
      @media (max-width: 900px) { .board-grid { grid-template-columns:1fr; } }
      .board-col { background:var(--color-bg,#f8fafc); border:1px solid var(--color-border,#e5e7eb);
        border-radius:var(--radius); padding:.6rem; min-height:220px; }
      .board-col.dragover { border-color:var(--color-primary,#2563eb); background:color-mix(in srgb, var(--color-primary,#2563eb) 6%, var(--color-bg,#f8fafc)); }
      .board-col-head { font-size:.8rem; font-weight:700; text-transform:uppercase; letter-spacing:.05em;
        color:var(--color-text-muted); padding:.2rem .3rem .6rem; display:flex; justify-content:space-between; }
      .board-count { background:var(--color-surface,#fff); border:1px solid var(--color-border); border-radius:999px;
        padding:0 .5rem; font-size:.75rem; }
      .board-quickadd input { width:100%; margin-bottom:.5rem; font-size:.85rem; }
      .board-cards { display:flex; flex-direction:column; gap:.5rem; }
      .board-card { background:var(--color-surface,#fff); border:1px solid var(--color-border,#e5e7eb);
        border-radius:6px; padding:.55rem .7rem; }
      .board-card-edit { cursor:pointer; }
      .board-card-edit:hover { border-color:var(--color-primary,#2563eb); }
      .board-card.dragging { opacity:.4; }
      .board-card-title { font-size:.88rem; font-weight:600; }
      .board-card-meta { display:flex; gap:.7rem; flex-wrap:wrap; font-size:.75rem; color:var(--color-text-muted); margin-top:.25rem; }
      .board-card-sprint { font-size:.72rem; color:var(--color-text-muted); margin-top:.2rem; }
      .board-empty { font-size:.8rem; color:var(--color-text-muted); text-align:center; padding:1rem 0; }`;
    document.head.appendChild(s);
  }
}
customElements.define('page-board', PageBoard);
