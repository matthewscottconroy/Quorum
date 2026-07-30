import { api, navigate } from '../app.js';
import { esc, fmtDateTime } from '../utils.js';

// A bell in the sidebar footer showing the unread count, with a dropdown of
// recent notices. It polls the unread count every 60s and cleans up its timer on
// disconnect (the nav-bar — and thus this bell — is recreated on each route).
class NotificationBell extends HTMLElement {
  constructor() { super(); this._open = false; this._unread = 0; }

  connectedCallback() {
    this.render();
    this.refreshCount();
    this._timer = setInterval(() => this.refreshCount(), 60000);
    // Close the dropdown on an outside click.
    this._onDocClick = e => { if (this._open && !this.contains(e.target)) this.close(); };
    document.addEventListener('click', this._onDocClick);
  }

  disconnectedCallback() {
    clearInterval(this._timer);
    document.removeEventListener('click', this._onDocClick);
  }

  render() {
    this.innerHTML = `
      <div class="nbell">
        <button id="nbell-btn" class="nbell-btn" aria-label="Notifications" aria-haspopup="true">
          <span aria-hidden="true">🔔</span>
          <span id="nbell-badge" class="nbell-badge" hidden></span>
        </button>
        <div id="nbell-panel" class="nbell-panel" role="menu" hidden></div>
      </div>`;
    this.querySelector('#nbell-btn').addEventListener('click', e => { e.stopPropagation(); this.toggle(); });
    this.applyStyles();
  }

  async refreshCount() {
    try {
      const { unread } = await api('GET', '/notifications/unread-count');
      this._unread = unread ?? 0;
      const badge = this.querySelector('#nbell-badge');
      if (!badge) return;
      badge.textContent = this._unread > 99 ? '99+' : String(this._unread);
      badge.hidden = this._unread === 0;
    } catch { /* transient; try again next tick */ }
  }

  toggle() { this._open ? this.close() : this.open(); }

  close() {
    this._open = false;
    const p = this.querySelector('#nbell-panel');
    if (p) p.hidden = true;
  }

  async open() {
    this._open = true;
    const panel = this.querySelector('#nbell-panel');
    panel.hidden = false;
    panel.innerHTML = `<div class="nbell-loading">Loading…</div>`;
    let items = [];
    try {
      items = await api('GET', '/notifications?limit=10') ?? [];
    } catch {
      panel.innerHTML = `<div class="nbell-empty">Couldn't load notifications.</div>`;
      return;
    }
    const header = `
      <div class="nbell-head">
        <strong>Notifications</strong>
        ${this._unread ? '<button class="nbell-link" id="nbell-readall">Mark all read</button>' : ''}
      </div>`;
    const list = items.length
      ? items.map(n => `
          <button class="nbell-item ${n.read_at ? '' : 'unread'}" data-id="${esc(n.id)}" data-link="${esc(n.link ?? '')}" role="menuitem">
            <span class="nbell-item-title">${esc(n.title)}</span>
            ${n.body ? `<span class="nbell-item-body">${esc(n.body)}</span>` : ''}
            <span class="nbell-item-time">${esc(fmtDateTime(n.created_at))}</span>
          </button>`).join('')
      : `<div class="nbell-empty">You're all caught up.</div>`;
    const footer = `<button class="nbell-seeall" id="nbell-seeall">See all notifications</button>`;
    panel.innerHTML = header + `<div class="nbell-list">${list}</div>` + footer;

    panel.querySelector('#nbell-readall')?.addEventListener('click', async e => {
      e.stopPropagation();
      try { await api('POST', '/notifications/read-all'); } catch {}
      this.refreshCount();
      this.open(); // re-render the panel
    });
    panel.querySelector('#nbell-seeall').addEventListener('click', () => { this.close(); navigate('#/notifications'); });
    panel.querySelectorAll('.nbell-item').forEach(btn => {
      btn.addEventListener('click', async () => {
        const id = btn.dataset.id, link = btn.dataset.link;
        try { await api('POST', `/notifications/${id}/read`); } catch {}
        this.refreshCount();
        this.close();
        if (link && link.startsWith('#/')) navigate(link);
      });
    });
  }

  applyStyles() {
    if (document.getElementById('nbell-style')) return;
    const s = document.createElement('style');
    s.id = 'nbell-style';
    s.textContent = `
      .nbell { position: relative; }
      .nbell-btn { position: relative; background: none; border: 0; cursor: pointer; font-size: 1.15rem; padding: .25rem .4rem; border-radius: var(--radius); }
      .nbell-btn:hover { background: rgba(255,255,255,.08); }
      .nbell-badge { position: absolute; top: -2px; right: -2px; min-width: 1.1rem; height: 1.1rem; padding: 0 .28rem;
        background: var(--color-danger,#dc2626); color: #fff; border-radius: 999px; font-size: .68rem; line-height: 1.1rem;
        text-align: center; font-weight: 700; }
      /* Fixed, not absolute: the sidebar's overflow:auto would otherwise clip a
         dropdown anchored inside it. Anchored to the bottom-left of the viewport,
         just above the footer where the bell lives. */
      .nbell-panel { position: fixed; bottom: 3.5rem; left: .5rem; width: 300px; max-width: calc(100vw - 1rem);
        max-height: 60vh; overflow-y: auto;
        background: var(--color-surface,#fff); color: var(--color-text,#111); border: 1px solid var(--color-border,#e5e7eb);
        border-radius: var(--radius); box-shadow: 0 8px 24px rgba(0,0,0,.18); z-index: 200; }
      .nbell-head { display: flex; justify-content: space-between; align-items: center; padding: .6rem .75rem; border-bottom: 1px solid var(--color-border,#e5e7eb); }
      .nbell-link { background: none; border: 0; color: var(--color-primary,#2563eb); cursor: pointer; font-size: .78rem; }
      .nbell-list { display: flex; flex-direction: column; }
      .nbell-item { display: flex; flex-direction: column; gap: .15rem; text-align: left; background: none; border: 0;
        border-bottom: 1px solid var(--color-border,#f1f1f1); padding: .55rem .75rem; cursor: pointer; }
      .nbell-item:hover { background: var(--color-bg,#f8fafc); }
      .nbell-item.unread { background: color-mix(in srgb, var(--color-primary,#2563eb) 8%, transparent); }
      .nbell-item-title { font-size: .84rem; font-weight: 600; }
      .nbell-item-body { font-size: .78rem; color: var(--color-text-muted,#6b7280); }
      .nbell-item-time { font-size: .7rem; color: var(--color-text-muted,#9ca3af); }
      .nbell-empty, .nbell-loading { padding: 1rem .75rem; font-size: .82rem; color: var(--color-text-muted,#6b7280); text-align: center; }
      .nbell-seeall { width: 100%; background: none; border: 0; border-top: 1px solid var(--color-border,#e5e7eb);
        padding: .55rem; color: var(--color-primary,#2563eb); cursor: pointer; font-size: .8rem; }
      .nbell-seeall:hover { background: var(--color-bg,#f8fafc); }`;
    document.head.appendChild(s);
  }
}
customElements.define('notification-bell', NotificationBell);
