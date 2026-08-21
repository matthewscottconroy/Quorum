import { api, getUser, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, openModal, guardButton, confirmDelete, safeUrl } from '../utils.js';
import { openDocPreview } from './doc-preview.js';

/**
 * Discussions: Slack-style channels over the org's own data. Anyone (member+)
 * creates channels and adds people; reading and posting require membership;
 * one-level threads hang off any message. Deliberately NO uploads here —
 * the 📄 button attaches a reference to a resource-library document, which
 * keeps visibility, download accountability, and audit where they belong.
 * Linked documents resolve per-viewer: if you can't see the document, the
 * chip says so and leaks nothing (not even the title).
 */
class PageChat extends HTMLElement {
  constructor() {
    super();
    this._channels = [];
    this._current = null;   // channel object incl. roster
    this._messages = [];
    this._thread = null;    // root message when a thread panel is open
    this._threadMsgs = [];
    this._resCache = new Map(); // resource_id -> {title,...} | null (hidden)
    this._timer = null;
  }

  connectedCallback() {
    this.render();
    this.loadChannels(true);
    this._timer = setInterval(() => {
      if (document.visibilityState === 'visible' && this._current) this.refreshMessages();
    }, 20000);
  }

  disconnectedCallback() { clearInterval(this._timer); }

  render() {
    this.innerHTML = `
      <div class="page-header"><h1>Discussions</h1>
        <button class="btn-primary" id="ch-new">+ New channel</button></div>
      <div class="chat-grid">
        <div class="card chat-list" id="ch-list"><span class="spinner"></span></div>
        <div class="card chat-main" id="ch-main">
          <div class="empty-state" style="padding:3rem 1rem"><p>Pick a channel — or create one and add people.</p></div>
        </div>
      </div>`;
    this.applyStyles();
    this.querySelector('#ch-new').addEventListener('click', () => this.openChannelModal(null));
  }

  async loadChannels(selectFirst = false) {
    try {
      this._channels = await api('GET', '/channels') ?? [];
      this.renderChannelList();
      if (selectFirst) {
        const mine = this._channels.find(c => c.is_member);
        if (mine) this.openChannel(mine.id);
      }
    } catch { toast('Failed to load channels', 'error'); }
  }

  renderChannelList() {
    const box = this.querySelector('#ch-list');
    const mine = this._channels.filter(c => c.is_member);
    const other = this._channels.filter(c => !c.is_member);
    const row = c => `
      <button class="ch-row ${this._current?.id === c.id ? 'ch-active' : ''}" data-id="${esc(c.id)}" ${!c.is_member && !isAdmin() ? 'disabled title="Ask any member to add you"' : ''}>
        <span># ${esc(c.name)}</span>
        <span class="ch-count">${c.message_count}</span>
      </button>`;
    box.innerHTML = `
      <div class="ch-head">Your channels</div>
      ${mine.map(row).join('') || '<div class="ch-none">None yet.</div>'}
      ${other.length ? `<div class="ch-head" style="margin-top:.7rem">Other channels</div>${other.map(row).join('')}` : ''}`;
    box.querySelectorAll('.ch-row:not([disabled])').forEach(b =>
      b.addEventListener('click', () => this.openChannel(b.dataset.id)));
  }

  async openChannel(id) {
    try {
      this._current = await api('GET', `/channels/${id}`);
      this._thread = null;
      await this.refreshMessages();
      this.renderChannelList();
    } catch (err) { toast(err.error ?? 'Cannot open channel', 'error'); }
  }

  /** Cheap change signature so the poll can tell "new content" from "tick". */
  _msgSig() {
    const sig = list => (list ?? []).map(m => m.id + ':' + (m.updated_at ?? '') + ':' + (m.reply_count ?? 0)).join('|');
    return sig(this._messages) + '§' + (this._thread?.id ?? '') + '§' + sig(this._threadMsgs);
  }

  async refreshMessages() {
    if (!this._current) return;
    try {
      const before = this._msgSig();
      this._messages = await api('GET', `/channels/${this._current.id}/messages`) ?? [];
      if (this._thread) {
        this._threadMsgs = await api('GET', `/channels/${this._current.id}/messages?thread=${this._thread.id}`) ?? [];
      }
      // A quiet 20s tick must not rebuild the DOM: rebuilding wipes the
      // half-typed draft, drops the attachment chip, and yanks a reader
      // scrolled up in history back to the bottom.
      if (this._msgSig() === before) return;
      const drafts = {};
      for (const p of ['ch', 'th']) {
        const inp = this.querySelector(`#${p}-input`);
        if (inp) drafts[p] = { text: inp.value, focused: document.activeElement === inp };
      }
      const msgsEl = this.querySelector('#msgs');
      const atBottom = !msgsEl || msgsEl.scrollTop + msgsEl.clientHeight >= msgsEl.scrollHeight - 40;
      const scrollTop = msgsEl?.scrollTop ?? 0;
      this.renderMain();
      for (const p of ['ch', 'th']) {
        const inp = this.querySelector(`#${p}-input`);
        if (inp && drafts[p]?.text) {
          inp.value = drafts[p].text;
          if (drafts[p].focused) {
            inp.focus();
            inp.setSelectionRange(inp.value.length, inp.value.length);
          }
        }
      }
      const msgs2 = this.querySelector('#msgs');
      if (msgs2 && !atBottom) msgs2.scrollTop = scrollTop; // stay where the reader was
    } catch { /* transient */ }
  }

  msgHTML(m, inThread) {
    const me = getUser()?.id;
    const canDel = m.author_id === me || isAdmin();
    return `
      <div class="msg">
        <div class="msg-head">
          <b>${esc(m.author_name ?? 'former user')}</b>
          <span>${esc(new Date(m.created_at).toLocaleString())}</span>
          ${canDel ? `<button class="btn-ghost msg-del" data-id="${esc(m.id)}" aria-label="Delete message" title="Delete message" style="padding:0 .3rem;color:var(--color-danger)">✕</button>` : ''}
        </div>
        <div class="msg-body">${esc(m.body)}</div>
        ${m.resource_id ? `<div class="msg-doc" data-rid="${esc(m.resource_id)}"><span class="spinner" style="width:12px;height:12px"></span></div>` : ''}
        ${!inThread ? `<button class="btn-ghost msg-thread" data-id="${esc(m.id)}" style="font-size:.75rem;padding:.05rem .4rem">
          ${m.reply_count ? `💬 ${m.reply_count} repl${m.reply_count === 1 ? 'y' : 'ies'}` : 'Reply in thread'}</button>` : ''}
      </div>`;
  }

  renderMain() {
    const c = this._current;
    const main = this.querySelector('#ch-main');
    const roster = (c.members ?? []).map(m => esc(m.name)).join(', ');
    main.innerHTML = `
      <div class="chat-top">
        <div>
          <b># ${esc(c.name)}</b> ${c.topic ? `<span style="color:var(--color-text-muted);font-size:.85rem">— ${esc(c.topic)}</span>` : ''}
          <div class="chat-roster" title="${roster}">👥 ${c.member_count} member${c.member_count === 1 ? '' : 's'}: ${roster}</div>
        </div>
        <div style="display:flex;gap:.3rem;flex-wrap:wrap">
          <button class="btn-secondary" id="ch-add" style="font-size:.78rem">+ Add people</button>
          <button class="btn-ghost" id="ch-leave" style="font-size:.78rem">Leave</button>
          ${(c.created_by === getUser()?.id || isAdmin()) ? `
            <button class="btn-ghost" id="ch-edit" style="font-size:.78rem">Edit</button>
            <button class="btn-ghost" id="ch-del" style="font-size:.78rem;color:var(--color-danger)">Delete</button>` : ''}
        </div>
      </div>
      <div class="chat-body">
        <div class="chat-msgs" id="msgs">
          ${this._messages.map(m => this.msgHTML(m, false)).join('') ||
            '<div class="empty-state" style="padding:2rem"><p>No messages yet — say something.</p></div>'}
        </div>
        ${this._thread ? `
        <div class="chat-thread" id="thread">
          <div class="chat-top" style="padding:.4rem .6rem">
            <b style="font-size:.85rem">Thread</b>
            <button class="btn-ghost" id="th-close" aria-label="Close thread" title="Close thread" style="padding:0 .4rem">✕</button>
          </div>
          <div class="chat-msgs">
            ${this.msgHTML(this._thread, true)}
            <div style="border-top:1px solid var(--color-border);margin:.4rem 0"></div>
            ${this._threadMsgs.map(m => this.msgHTML(m, true)).join('') || '<div style="font-size:.8rem;color:var(--color-text-muted);padding:.4rem">No replies yet.</div>'}
          </div>
          ${this.composerHTML('th')}
        </div>` : ''}
      </div>
      ${this._thread ? '' : this.composerHTML('ch')}
    `;
    this.wireMain();
    const msgs = this.querySelector('#msgs');
    if (msgs) msgs.scrollTop = msgs.scrollHeight;
    this.resolveDocChips(main);
  }

  composerHTML(prefix) {
    if (!this._current?.is_member) {
      return `
      <div class="chat-compose" style="display:flex;gap:.6rem;align-items:center">
        <span style="font-size:.82rem;color:var(--color-text-muted);flex:1">
          You're viewing this channel but haven't joined it — join to post.</span>
        <button class="btn-secondary" id="${prefix}-join" style="font-size:.8rem">Join channel</button>
      </div>`;
    }
    return `
      <div class="chat-compose">
        <div id="${prefix}-doc-chip" style="display:none"></div>
        <div style="display:flex;gap:.4rem;align-items:flex-end">
          <textarea id="${prefix}-input" rows="2" placeholder="Write a message… (Enter sends, Shift+Enter for a new line)" style="flex:1"></textarea>
          <button class="btn-ghost" id="${prefix}-attach" title="Link a document from Resources">📄</button>
          <button class="btn-primary" id="${prefix}-send">Send</button>
        </div>
      </div>`;
  }

  /** Resolve 📄 chips per-viewer through the normal visibility check. */
  async resolveDocChips(scope) {
    for (const el of scope.querySelectorAll('.msg-doc[data-rid]')) {
      const rid = el.dataset.rid;
      let res = this._resCache.has(rid) ? this._resCache.get(rid) : undefined;
      if (res === undefined) {
        try { res = await api('GET', `/resources/${rid}`); }
        catch { res = null; }
        this._resCache.set(rid, res);
      }
      if (res) {
        el.innerHTML = `<button class="btn-ghost doc-open" style="font-size:.78rem;border:1px solid var(--color-border);border-radius:6px;padding:.15rem .5rem">📄 ${esc(res.title)}${res.file_name ? ` · ${esc(res.file_name)}` : ''}</button>`;
        el.querySelector('.doc-open').addEventListener('click', () => {
          if (res.file_name) openDocPreview(res);
          else if (res.url) { const u = safeUrl(res.url); if (u) window.open(u, '_blank', 'noopener'); }
          else location.hash = '#/resources';
        });
      } else {
        el.innerHTML = `<span style="font-size:.75rem;color:var(--color-text-muted)">📄 a document you don't have access to</span>`;
      }
    }
  }

  wireComposer(prefix, parentID) {
    this._attached ??= {};
    const chip = this.querySelector(`#${prefix}-doc-chip`);
    const input = this.querySelector(`#${prefix}-input`);
    const renderChip = () => {
      const attached = this._attached[prefix];
      chip.style.display = attached ? 'block' : 'none';
      chip.innerHTML = attached
        ? `<span class="badge badge-none">📄 ${esc(attached.title)} <button class="btn-ghost" id="${prefix}-unattach" aria-label="Remove attachment" title="Remove attachment" style="padding:0 .2rem">✕</button></span>` : '';
      chip.querySelector(`#${prefix}-unattach`)?.addEventListener('click', () => { this._attached[prefix] = null; renderChip(); });
    };
    renderChip(); // restore a chip that survived a poll re-render
    this.querySelector(`#${prefix}-attach`)?.addEventListener('click', async () => {
      try {
        const page = await api('GET', '/resources?limit=200');
        const list = page?.data ?? [];
        if (!list.length) { toast('No resources you can see — add some in Resources first', 'error'); return; }
        const { dialog, close } = openModal({
          title: 'Link a document',
          body: `
            <div class="modal-body">
              <input id="pick-filter" placeholder="Filter…" style="width:100%;margin-bottom:.5rem">
              <div id="pick-list" style="max-height:300px;overflow-y:auto;display:flex;flex-direction:column;gap:.2rem"></div>
            </div>
            <div class="modal-footer"><button class="btn-secondary" id="pick-cancel">Cancel</button></div>`,
        });
        const listEl = dialog.querySelector('#pick-list');
        const renderPick = (f = '') => {
          listEl.innerHTML = list
            .filter(r => r.title.toLowerCase().includes(f.toLowerCase()))
            .map(r => `<button class="btn-ghost pick-row" data-id="${esc(r.id)}" style="text-align:left">
                ${r.file_name ? '📄' : '🔗'} ${esc(r.title)}
                ${(r.group_names ?? []).length ? '🔒' : ''}${r.visible_min_role ? '🛡' : ''}</button>`).join('')
            || '<div style="font-size:.8rem;color:var(--color-text-muted)">No match.</div>';
          listEl.querySelectorAll('.pick-row').forEach(b => b.addEventListener('click', () => {
            this._attached[prefix] = list.find(r => r.id === b.dataset.id);
            renderChip(); close();
          }));
        };
        dialog.querySelector('#pick-filter').addEventListener('input', e => renderPick(e.target.value));
        dialog.querySelector('#pick-cancel').addEventListener('click', close);
        renderPick();
      } catch { toast('Failed to load resources', 'error'); }
    });
    const send = async () => {
      const body = input.value.trim();
      if (!body) return;
      try {
        await api('POST', `/channels/${this._current.id}/messages`, {
          body, parent_id: parentID ?? '', resource_id: this._attached[prefix]?.id ?? '',
        });
        input.value = ''; this._attached[prefix] = null; renderChip();
        await this.refreshMessages();
        this.loadChannels(); // counts changed
      } catch (err) { toast(err.error ?? 'Send failed', 'error'); }
    };
    this.querySelector(`#${prefix}-send`)?.addEventListener('click', send);
    input?.addEventListener('keydown', e => {
      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
    });
  }

  wireMain() {
    const c = this._current;
    this.querySelectorAll('.msg-thread').forEach(b => b.addEventListener('click', async () => {
      this._thread = this._messages.find(m => m.id === b.dataset.id) ?? null;
      await this.refreshMessages();
    }));
    this.querySelector('#th-close')?.addEventListener('click', () => { this._thread = null; this.renderMain(); });
    this.querySelectorAll('.msg-del').forEach(b => b.addEventListener('click', async () => {
      if (!confirm('Delete this message?')) return;
      try {
        await api('DELETE', `/channels/${c.id}/messages/${b.dataset.id}`);
        if (this._thread?.id === b.dataset.id) this._thread = null;
        await this.refreshMessages(); this.loadChannels();
      } catch (err) { toast(err.error ?? 'Delete failed', 'error'); }
    }));
    this.querySelector('#ch-add')?.addEventListener('click', () => this.openAddPeople());
    this.querySelector('#ch-leave')?.addEventListener('click', async () => {
      try {
        await api('DELETE', `/channels/${c.id}/members/${getUser().id}`);
        this._current = null; this._thread = null;
        this.loadChannels(true);
        this.querySelector('#ch-main').innerHTML = '<div class="empty-state" style="padding:3rem 1rem"><p>You left the channel.</p></div>';
      } catch (err) { toast(err.error ?? 'Leave failed', 'error'); }
    });
    this.querySelector('#ch-edit')?.addEventListener('click', () => this.openChannelModal(c));
    this.querySelector('#ch-del')?.addEventListener('click', () => {
      confirmDelete({
        noun: 'channel and its entire history',
        name: c.name,
        onConfirm: async (confirmVal) => {
          try {
            await api('DELETE', `/channels/${c.id}?confirm=${encodeURIComponent(confirmVal)}`);
            toast('Channel deleted', 'success');
            this._current = null; this.render(); this.loadChannels(true);
          } catch (err) { toast(err.error ?? 'Delete failed', 'error'); throw err; }
        },
      });
    });
    const joinBtn = this.querySelector('#ch-join') ?? this.querySelector('#th-join');
    if (joinBtn) {
      joinBtn.addEventListener('click', async () => {
        try {
          await api('POST', `/channels/${c.id}/members`, { user_id: getUser().id });
          await this.openChannel(c.id);
          this.loadChannels();
        } catch (err) { toast(err.error ?? 'Join failed', 'error'); }
      });
    } else if (this._thread) this.wireComposer('th', this._thread.id);
    else this.wireComposer('ch', null);
  }

  async openAddPeople() {
    const c = this._current;
    let users = [];
    try { users = await api('GET', '/channels/users') ?? []; }
    catch { toast('Failed to load users', 'error'); return; }
    const memberIDs = new Set((c.members ?? []).map(m => m.user_id));
    const candidates = users.filter(u => !memberIDs.has(u.user_id));
    const { dialog, close } = openModal({
      title: `Add people to #${c.name}`,
      body: `
        <div class="modal-body">
          ${candidates.length ? `
          <div style="max-height:300px;overflow-y:auto;display:flex;flex-direction:column;gap:.2rem">
            ${candidates.map(u => `<button class="btn-ghost add-row" data-id="${esc(u.user_id)}" style="text-align:left">➕ ${esc(u.name)}</button>`).join('')}
          </div>` : '<p style="font-size:.85rem">Everyone is already here.</p>'}
        </div>
        <div class="modal-footer"><button class="btn-secondary" id="ap-close">Done</button></div>`,
    });
    dialog.querySelectorAll('.add-row').forEach(b => b.addEventListener('click', guardButton(b, async () => {
      try {
        await api('POST', `/channels/${c.id}/members`, { user_id: b.dataset.id });
        b.disabled = true; b.textContent = '✓ added';
      } catch (err) { toast(err.error ?? 'Add failed', 'error'); }
    })));
    dialog.querySelector('#ap-close').addEventListener('click', async () => {
      close();
      this._current = await api('GET', `/channels/${c.id}`);
      this.renderMain(); this.loadChannels();
    });
  }

  openChannelModal(existing) {
    const { dialog, close } = openModal({
      title: existing ? 'Edit channel' : 'New channel',
      body: `
        <div class="modal-body">
          <div class="form-group"><label for="cn-name">Name *</label>
            <input id="cn-name" value="${esc(existing?.name ?? '')}" placeholder="e.g. fundraising-ideas"></div>
          <div class="form-group"><label for="cn-topic">Topic</label>
            <input id="cn-topic" value="${esc(existing?.topic ?? '')}" placeholder="What is this channel for?"></div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" id="cn-cancel">Cancel</button>
          <button class="btn-primary" id="cn-save">${existing ? 'Save' : 'Create'}</button>
        </div>`,
    });
    dialog.querySelector('#cn-cancel').addEventListener('click', close);
    const saveBtn = dialog.querySelector('#cn-save');
    saveBtn.addEventListener('click', guardButton(saveBtn, async () => {
      const name = dialog.querySelector('#cn-name').value.trim();
      if (!name) { toast('Name required', 'error'); return; }
      const topic = dialog.querySelector('#cn-topic').value.trim() || null;
      try {
        if (existing) {
          await api('PATCH', `/channels/${existing.id}`, { name, topic });
          this._current = await api('GET', `/channels/${existing.id}`);
          this.renderMain();
        } else {
          const created = await api('POST', '/channels', { name, topic });
          await this.loadChannels();
          this.openChannel(created.id);
        }
        toast(existing ? 'Saved' : 'Channel created — now add people', 'success');
        close();
        this.loadChannels();
      } catch (err) { toast(err.error ?? 'Save failed', 'error'); }
    }));
  }

  applyStyles() {
    if (document.getElementById('chat-style')) return;
    const s = document.createElement('style');
    s.id = 'chat-style';
    s.textContent = `
      .chat-grid { display:grid; grid-template-columns:230px 1fr; gap:1rem; align-items:start; }
      @media (max-width: 800px) { .chat-grid { grid-template-columns:1fr; } }
      .chat-list { padding:.6rem; }
      .ch-head { font-size:.72rem; font-weight:700; text-transform:uppercase; letter-spacing:.05em; color:var(--color-text-muted); padding:.2rem .3rem; }
      .ch-none { font-size:.8rem; color:var(--color-text-muted); padding:.2rem .3rem; }
      .ch-row { display:flex; justify-content:space-between; width:100%; background:none; border:none; cursor:pointer;
        padding:.3rem .45rem; border-radius:6px; color:var(--color-text); font-size:.88rem; }
      .ch-row:hover:not([disabled]) { background:var(--color-bg); }
      .ch-row[disabled] { opacity:.55; cursor:default; }
      .ch-active { background:color-mix(in srgb, var(--color-primary,#2563eb) 12%, transparent); font-weight:600; }
      .ch-count { color:var(--color-text-muted); font-size:.72rem; }
      .chat-main { padding:0; display:flex; flex-direction:column; min-height:420px; }
      .chat-top { display:flex; justify-content:space-between; align-items:flex-start; gap:.6rem;
        padding:.7rem .9rem; border-bottom:1px solid var(--color-border); flex-wrap:wrap; }
      .chat-roster { font-size:.75rem; color:var(--color-text-muted); max-width:520px;
        overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
      .chat-body { display:flex; flex:1; min-height:0; }
      .chat-msgs { flex:1; overflow-y:auto; max-height:52vh; padding:.7rem .9rem; display:flex; flex-direction:column; gap:.6rem; }
      .chat-thread { width:44%; border-left:1px solid var(--color-border); display:flex; flex-direction:column; }
      @media (max-width: 900px) { .chat-thread { width:100%; } .chat-body { flex-direction:column; } }
      .msg-head { display:flex; gap:.6rem; align-items:baseline; font-size:.75rem; color:var(--color-text-muted); }
      .msg-head b { color:var(--color-text); font-size:.85rem; }
      .msg-body { font-size:.9rem; white-space:pre-wrap; }
      .chat-compose { border-top:1px solid var(--color-border); padding:.6rem .9rem; }`;
    document.head.appendChild(s);
  }
}
customElements.define('page-chat', PageChat);
