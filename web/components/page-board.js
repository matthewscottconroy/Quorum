import { api, canWrite, isAdmin, getUser } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete } from '../utils.js';

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
    this._columns = [];
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
      const fetches = [api('GET', '/action-items?' + qs), api('GET', '/sprints'), api('GET', '/board/columns')];
      if (withRefs) fetches.push(api('GET', '/members?limit=200'));
      const [items, sprints, columns, members] = await Promise.all(fetches);
      this._items = items?.data ?? [];
      this._sprints = sprints ?? [];
      this._columns = columns ?? [];
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
          ${canWrite() ? '<button class="btn-secondary" id="cols-btn">Columns</button>' : ''}
          ${canWrite() ? '<button class="btn-secondary" id="sprint-new">+ New sprint</button>' : ''}
        </div>
      </div>
      <div id="sprint-bar"></div>
      <div style="overflow-x:auto"><div class="board-grid" id="board"></div></div>
    `;
    this.applyStyles();
    this.querySelector('#scope-sel').addEventListener('change', e => { this._scope = e.target.value; this.load(); });
    this.querySelector('#assignee-sel').addEventListener('change', e => { this._assignee = e.target.value; this.renderBoard(); });
    this.querySelector('#sprint-new')?.addEventListener('click', () => this.openSprintModal(null));
    this.querySelector('#cols-btn')?.addEventListener('click', () => this.openColumnsModal());
  }

  /** The lane a card renders in: its explicit column, else the column that
   *  maps its status, else null (hidden — e.g. cancelled with no lane). */
  laneOf(item) {
    if (item.column_id) {
      const c = this._columns.find(c => c.id === item.column_id);
      if (c) return c.id;
    }
    const byStatus = this._columns.find(c => c.maps_to_status === item.status);
    return byStatus ? byStatus.id : null;
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
    const quickAddCol = (this._columns.find(c => c.maps_to_status === 'open') ?? this._columns[0])?.id;
    board.style.gridTemplateColumns = `repeat(${Math.max(this._columns.length, 1)}, minmax(230px, 1fr))`;
    board.innerHTML = this._columns.map(col => {
      const cards = items.filter(i => this.laneOf(i) === col.id);
      return `
        <div class="board-col" data-col="${esc(col.id)}">
          <div class="board-col-head">${esc(col.name)} <span class="board-count">${cards.length}</span></div>
          ${officer && col.id === quickAddCol ? `
            <div class="board-quickadd">
              <input id="qa-input" placeholder="+ Add work item and press Enter">
            </div>` : ''}
          <div class="board-cards">
            ${cards.map(i => this.cardHTML(i, officer)).join('') ||
              '<div class="board-empty">Nothing here.</div>'}
          </div>
        </div>`;
    }).join('') || '<div class="board-empty">No columns yet — an officer can add some via “Columns”.</div>';

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

    // Card open: officers edit; members view details + join the conversation.
    board.querySelectorAll('.board-card').forEach(card => {
      card.addEventListener('click', () => {
        const item = this._items.find(i => i.id === card.dataset.id);
        if (item) this.openCardModal(item, officer);
      });
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
          const target = this._columns.find(c => c.id === col.dataset.col);
          const item = this._items.find(i => i.id === id);
          if (!item || !target || this.laneOf(item) === target.id) return;
          // Moving into a status-mapped lane advances the card's status too;
          // a pure workflow lane (Blocked, Reviewing…) leaves status alone.
          const patch = { column_id: target.id };
          if (target.maps_to_status) patch.status = target.maps_to_status;
          try {
            await api('PATCH', `/action-items/${id}`, patch);
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
        ${i.comment_count ? `<div class="board-card-sprint">💬 ${i.comment_count} comment${i.comment_count === 1 ? '' : 's'}</div>` : ''}
      </div>`;
  }

  /** Officer-only management of the board's lanes. */
  openColumnsModal() {
    const statusOpts = sel => ['', 'open', 'in_progress', 'done', 'cancelled']
      .map(v => `<option value="${v}" ${sel === v ? 'selected' : ''}>${v ? v.replace('_', ' ') : '— none (workflow only) —'}</option>`).join('');
    const { dialog, close } = openModal({
      title: 'Board columns',
      maxWidth: '560px',
      body: `
        <div class="modal-body">
          <p style="font-size:.8rem;color:var(--color-text-muted);margin-top:0">
            Columns are workflow lanes. A column with a <b>status mapping</b> also updates the card's
            status on drop (that keeps dashboards truthful); columns without one — Blocked,
            Prioritized, Reviewing — move cards without changing status. Deleting a column never
            deletes cards: they fall back to the lane matching their status.</p>
          <div id="col-rows"></div>
          <div style="display:flex;gap:.4rem;margin-top:.8rem;align-items:center;flex-wrap:wrap">
            <input id="nc-name" placeholder="New column name" style="flex:1;min-width:140px">
            <select id="nc-maps" style="max-width:200px">${statusOpts('')}</select>
            <button class="btn-primary" id="nc-add">Add</button>
          </div>
        </div>
        <div class="modal-footer"><button class="btn-secondary" id="close-btn">Close</button></div>`,
    });
    dialog.querySelector('#close-btn').addEventListener('click', () => { close(); this.load(); });

    const renderRows = () => {
      const rows = dialog.querySelector('#col-rows');
      rows.innerHTML = this._columns.map((c, i) => `
        <div style="display:flex;gap:.4rem;align-items:center;padding:.3rem 0;border-bottom:1px solid var(--color-border)" data-id="${esc(c.id)}">
          <button class="btn-ghost col-up" ${i === 0 ? 'disabled' : ''} title="Move left">◀</button>
          <button class="btn-ghost col-down" ${i === this._columns.length - 1 ? 'disabled' : ''} title="Move right">▶</button>
          <input class="col-name" value="${esc(c.name)}" style="flex:1">
          <span class="badge badge-none" style="font-size:.7rem">${c.maps_to_status ? '→ ' + esc(c.maps_to_status.replace('_', ' ')) : 'workflow'}</span>
          <button class="btn-ghost col-save" title="Save name">💾</button>
          <button class="btn-ghost col-del" data-name="${esc(c.name)}" style="color:var(--color-danger)">✕</button>
        </div>`).join('') || '<p style="font-size:.85rem">No columns.</p>';

      rows.querySelectorAll('[data-id]').forEach(row => {
        const id = row.dataset.id;
        row.querySelector('.col-save').addEventListener('click', async () => {
          const name = row.querySelector('.col-name').value.trim();
          if (!name) { toast('Name required', 'error'); return; }
          try {
            await api('PATCH', `/board/columns/${id}`, { name });
            toast('Renamed', 'success'); await this.reloadColumns(); renderRows();
          } catch (err) { toast(err.error ?? 'Rename failed', 'error'); }
        });
        const swap = async (dir) => {
          const i = this._columns.findIndex(c => c.id === id);
          const other = this._columns[i + dir];
          if (!other) return;
          const a = this._columns[i];
          try {
            // Swap positions; equal positions fall back to created_at order,
            // so nudge with distinct values.
            await api('PATCH', `/board/columns/${a.id}`, { position: other.position });
            await api('PATCH', `/board/columns/${other.id}`, { position: a.position });
            await this.reloadColumns(); renderRows();
          } catch (err) { toast(err.error ?? 'Reorder failed', 'error'); }
        };
        row.querySelector('.col-up').addEventListener('click', () => swap(-1));
        row.querySelector('.col-down').addEventListener('click', () => swap(1));
        row.querySelector('.col-del').addEventListener('click', () => {
          confirmDelete({
            noun: 'board column (cards fall back to their status lane)',
            name: row.querySelector('.col-del').dataset.name,
            onConfirm: async (confirmVal) => {
              try {
                await api('DELETE', `/board/columns/${id}?confirm=${encodeURIComponent(confirmVal)}`);
                toast('Column deleted', 'success'); await this.reloadColumns(); renderRows();
              } catch (err) { toast(err.error ?? 'Delete failed', 'error'); throw err; }
            },
          });
        });
      });
    };

    dialog.querySelector('#nc-add').addEventListener('click', async () => {
      const name = dialog.querySelector('#nc-name').value.trim();
      const maps = dialog.querySelector('#nc-maps').value || null;
      if (!name) { toast('Name required', 'error'); return; }
      try {
        await api('POST', '/board/columns', { name, maps_to_status: maps });
        dialog.querySelector('#nc-name').value = '';
        toast('Column added', 'success'); await this.reloadColumns(); renderRows();
      } catch (err) { toast(err.error ?? 'Add failed', 'error'); }
    });

    renderRows();
  }

  async reloadColumns() {
    try { this._columns = await api('GET', '/board/columns') ?? []; } catch { /* keep stale */ }
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

  openCardModal(item, officer = true) {
    const memberOpts = sel => `<option value="">— unassigned —</option>` +
      this._members.map(m => `<option value="${esc(m.id)}" ${sel === m.id ? 'selected' : ''}>${esc(m.display_name)}</option>`).join('');
    const sprintOpts = sel => `<option value="">— backlog —</option>` +
      this._sprints.map(s => `<option value="${esc(s.id)}" ${sel === s.id ? 'selected' : ''}>${esc(s.name)}</option>`).join('');
    if (!officer) { this.openCardViewModal(item); return; }
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
          ${this.commentsHTML()}
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cancel-btn">Cancel</button>
          <button class="btn-primary" id="save-btn">Save</button>
        </div>`,
    });
    this.wireComments(dialog, item);
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

  /** Read-only card view for members: details + the conversation. */
  openCardViewModal(item) {
    const { dialog, close } = openModal({
      title: item.title,
      body: `
        <div class="modal-body">
          ${item.description ? `<p style="white-space:pre-wrap;margin-top:0">${esc(item.description)}</p>` : ''}
          <div style="display:flex;gap:1rem;flex-wrap:wrap;font-size:.85rem;color:var(--color-text-muted)">
            <span>👤 ${esc(item.assignee_name ?? 'unassigned')}</span>
            <span>Status: ${esc(item.status.replace('_', ' '))}</span>
            ${item.due_date ? `<span>📅 ${esc(new Date(item.due_date).toLocaleDateString())}</span>` : ''}
            ${item.sprint_name ? `<span>🏁 ${esc(item.sprint_name)}</span>` : ''}
          </div>
          ${this.commentsHTML()}
        </div>
        <div class="modal-footer"><button class="btn-secondary" id="close-btn">Close</button></div>`,
    });
    dialog.querySelector('#close-btn').addEventListener('click', close);
    this.wireComments(dialog, item);
  }

  commentsHTML() {
    return `
      <div style="border-top:1px solid var(--color-border);margin-top:1rem;padding-top:.7rem">
        <div style="font-size:.8rem;font-weight:700;text-transform:uppercase;letter-spacing:.05em;color:var(--color-text-muted)">Conversation</div>
        <div id="cm-list" style="display:flex;flex-direction:column;gap:.5rem;margin:.6rem 0;max-height:260px;overflow-y:auto">
          <span class="spinner"></span>
        </div>
        <div style="display:flex;gap:.4rem;align-items:flex-start">
          <textarea id="cm-input" rows="2" placeholder="Write a comment…" style="flex:1"></textarea>
          <button class="btn-primary" id="cm-send">Comment</button>
        </div>
      </div>`;
  }

  /** Loads and wires the conversation thread inside a card dialog. */
  wireComments(dialog, item) {
    const list = dialog.querySelector('#cm-list');
    const meId = getUser()?.id;
    const renderThread = (comments) => {
      list.innerHTML = comments.length ? comments.map(c => `
        <div style="background:var(--color-bg);border:1px solid var(--color-border);border-radius:6px;padding:.45rem .6rem">
          <div style="display:flex;justify-content:space-between;gap:.6rem;font-size:.75rem;color:var(--color-text-muted)">
            <span style="font-weight:700;color:var(--color-text)">${esc(c.author_name ?? 'former user')}</span>
            <span>${esc(new Date(c.created_at).toLocaleString())}
              ${(c.author_id === meId || isAdmin()) ? `<button class="btn-ghost cm-del" data-id="${esc(c.id)}" title="Delete" style="padding:0 .3rem;color:var(--color-danger)">✕</button>` : ''}
            </span>
          </div>
          <div style="font-size:.88rem;white-space:pre-wrap;margin-top:.15rem">${esc(c.body)}</div>
        </div>`).join('')
        : '<div style="font-size:.82rem;color:var(--color-text-muted)">No comments yet — start the conversation.</div>';
      list.querySelectorAll('.cm-del').forEach(btn => btn.addEventListener('click', async () => {
        try {
          await api('DELETE', `/action-items/${item.id}/comments/${btn.dataset.id}`);
          loadThread();
        } catch (err) { toast(err.error ?? 'Delete failed', 'error'); }
      }));
      list.scrollTop = list.scrollHeight;
    };
    const loadThread = async () => {
      try { renderThread(await api('GET', `/action-items/${item.id}/comments`) ?? []); }
      catch { list.innerHTML = '<div style="font-size:.82rem;color:var(--color-text-muted)">Failed to load comments.</div>'; }
    };
    const send = async () => {
      const input = dialog.querySelector('#cm-input');
      const body = input.value.trim();
      if (!body) return;
      const sendBtn = dialog.querySelector('#cm-send');
      sendBtn.disabled = true;
      try {
        await api('POST', `/action-items/${item.id}/comments`, { body });
        input.value = '';
        await loadThread();
      } catch (err) { toast(err.error ?? 'Comment failed', 'error'); }
      finally { sendBtn.disabled = false; }
    };
    dialog.querySelector('#cm-send').addEventListener('click', send);
    dialog.querySelector('#cm-input').addEventListener('keydown', e => {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) send();
    });
    loadThread();
  }

  applyStyles() {
    if (document.getElementById('board-style')) return;
    const s = document.createElement('style');
    s.id = 'board-style';
    s.textContent = `
      .board-grid { display:grid; gap:1rem; align-items:start; min-width:min-content; }
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
