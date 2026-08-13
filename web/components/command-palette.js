import { api, navigate, hasRole } from '../app.js';
import { esc, formatMoney, vocab } from '../utils.js';

// Static navigation targets, searchable by label. minRole gates each.
const NAV_TARGETS = [
  ['Dashboard', '#/dashboard', 'member'], ['Members', '#/members', 'member'],
  ['Dues', '#/dues', 'officer'], ['Payables', '#/payables', 'officer'],
  ['Funds', '#/funds', 'member'], ['Accounting', '#/accounting', 'officer'],
  ['Budget', '#/budget', 'officer'], ['Analytics', '#/analytics', 'officer'],
  ['Currencies', '#/currencies', 'officer'], ['Meetings', '#/meetings', 'member'],
  ['Calendar', '#/calendar', 'member'], ['Board', '#/board', 'member'],
  ['Discussions', '#/discussions', 'member'], ['Plans', '#/plans', 'member'],
  ['Contacts', '#/contacts', 'member'], ['Resources', '#/resources', 'member'],
  ['Reports', '#/reports', 'officer'], ['Audit log', '#/audit', 'admin'],
  ['Settings', '#/settings', 'admin'], ['My Account', '#/my-account', 'restricted'],
];

/**
 * ⌘K / Ctrl-K command palette: one input that jumps to a page or searches
 * records across members, contacts, dues, cards, meetings, plans, and
 * resources at once. Opened via the nav search button, the keyboard shortcut,
 * or the `open-command-palette` event.
 */
class CommandPalette extends HTMLElement {
  connectedCallback() {
    this._open = false;
    this._seq = 0;
    this.render();
    this._onKey = e => this.onGlobalKey(e);
    this._onOpen = () => this.open();
    document.addEventListener('keydown', this._onKey);
    document.addEventListener('open-command-palette', this._onOpen);
  }

  disconnectedCallback() {
    document.removeEventListener('keydown', this._onKey);
    document.removeEventListener('open-command-palette', this._onOpen);
  }

  onGlobalKey(e) {
    const mod = e.metaKey || e.ctrlKey;
    if (mod && (e.key === 'k' || e.key === 'K')) {
      e.preventDefault();
      this.toggle();
      return;
    }
    if (this._open && e.key === 'Escape') { this.close(); return; }
    if (this._open) return;

    // Bare-key shortcuts only when not typing in a field and no modifier
    // held. A focused CANVAS counts as typing: the arcade cabinets read
    // the keyboard (SUB-BASEMENT is literally a text game).
    const el = document.activeElement;
    const typing = el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.tagName === 'CANVAS' || el.isContentEditable);
    if (typing || mod || e.altKey) return;

    if (e.key === '/') { e.preventDefault(); this.open(); return; }
    if (e.key === '?') { e.preventDefault(); this.showHelp(); return; }

    // "g" then a letter jumps to a section (Gmail-style). A short window after g.
    if (e.key === 'g') { this._gPending = Date.now(); return; }
    if (this._gPending && Date.now() - this._gPending < 1200) {
      const dest = { d: '#/dashboard', b: '#/board', m: '#/members', u: '#/dues', e: '#/meetings', p: '#/plans', r: '#/resources' }[e.key];
      this._gPending = 0;
      if (dest && hasRole(dest === '#/dues' ? 'officer' : 'member')) { e.preventDefault(); navigate(dest); }
    }
  }

  showHelp() {
    this._open = true;
    this.render();
    const box = this.querySelector('#cmdk-results');
    if (box) {
      box.innerHTML = `
        <div class="cmdk-group-title">Keyboard shortcuts</div>
        <div style="padding:.3rem .6rem;font-size:.88rem;line-height:1.9">
          <div><kbd>⌘K</kbd> / <kbd>Ctrl-K</kbd> — search everything</div>
          <div><kbd>/</kbd> — open search · <kbd>?</kbd> — this help</div>
          <div><kbd>g</kbd> then <kbd>d/b/m/u/e/p/r</kbd> — go to Dashboard/Board/Members/Dues/Meetings/Plans/Resources</div>
        </div>`;
      const inp = this.querySelector('#cmdk-input');
      if (inp) inp.placeholder = 'Type to search, or Esc to close…';
    }
  }

  toggle() { this._open ? this.close() : this.open(); }

  open() {
    this._open = true;
    this.render();
    const inp = this.querySelector('#cmdk-input');
    if (inp) inp.focus();
  }

  close() {
    this._open = false;
    this.render();
  }

  render() {
    if (!this._open) { this.innerHTML = ''; return; }
    this.innerHTML = `
      <div class="cmdk-backdrop" role="dialog" aria-modal="true" aria-label="Search">
        <div class="cmdk-panel">
          <input id="cmdk-input" type="text" autocomplete="off" spellcheck="false"
                 placeholder="Search pages, members, invoices, cards, meetings…" aria-label="Search everything">
          <div id="cmdk-results" class="cmdk-results"></div>
          <div class="cmdk-hint">↑↓ to move · Enter to open · Esc to close</div>
        </div>
      </div>`;
    this.applyStyles();

    const backdrop = this.querySelector('.cmdk-backdrop');
    backdrop.addEventListener('mousedown', e => { if (e.target === backdrop) this.close(); });
    const inp = this.querySelector('#cmdk-input');
    inp.addEventListener('input', () => this.search(inp.value));
    inp.addEventListener('keydown', e => this.onInputKey(e));
    this._active = 0;
    this.search('');
  }

  onInputKey(e) {
    const items = [...this.querySelectorAll('.cmdk-item')];
    if (e.key === 'ArrowDown') { e.preventDefault(); this._active = Math.min(this._active + 1, items.length - 1); this.highlight(items); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); this._active = Math.max(this._active - 1, 0); this.highlight(items); }
    else if (e.key === 'Enter') { e.preventDefault(); items[this._active]?.click(); }
  }

  highlight(items) {
    items.forEach((el, i) => el.classList.toggle('active', i === this._active));
    items[this._active]?.scrollIntoView({ block: 'nearest' });
  }

  // Renders navigation matches immediately, then merges in server record hits.
  async search(q) {
    const box = this.querySelector('#cmdk-results');
    if (!box) return;
    const query = q.trim();
    const lower = query.toLowerCase();

    const navHits = NAV_TARGETS
      .filter(([label, , role]) => hasRole(role) && (!lower || vocab(label).toLowerCase().includes(lower)))
      .slice(0, 6)
      .map(([label, hash]) => ({ label: vocab(label), sub: 'Page', hash }));

    let recordHits = [];
    if (query.length >= 2) {
      const seq = ++this._seq;
      recordHits = await this.fetchRecords(query);
      if (seq !== this._seq) return; // superseded
    }

    const groups = [
      ['Go to', navHits],
      ['Records', recordHits],
    ].filter(([, hits]) => hits.length);

    if (!groups.length) {
      box.innerHTML = `<div class="cmdk-empty">${query ? 'No matches' : 'Type to search…'}</div>`;
      return;
    }
    let idx = 0;
    box.innerHTML = groups.map(([title, hits]) => `
      <div class="cmdk-group-title">${esc(title)}</div>
      ${hits.map(h => {
        const i = idx++;
        return `<button class="cmdk-item${i === 0 ? ' active' : ''}" data-hash="${esc(h.hash)}" data-i="${i}">
          <span class="cmdk-item-label">${esc(h.label)}</span>
          <span class="cmdk-item-sub">${esc(h.sub)}</span>
        </button>`;
      }).join('')}`).join('');
    this._active = 0;

    box.querySelectorAll('.cmdk-item').forEach(el => {
      el.addEventListener('click', () => { navigate(el.dataset.hash); this.close(); });
      el.addEventListener('mousemove', () => {
        this._active = Number(el.dataset.i);
        this.highlight([...box.querySelectorAll('.cmdk-item')]);
      });
    });
  }

  // Queries the record types the caller can see, in parallel, and flattens the
  // first few of each into palette rows with a deep link.
  async fetchRecords(q) {
    const enc = encodeURIComponent(q);
    const jobs = [];
    const add = (role, fn) => { if (hasRole(role)) jobs.push(fn()); };

    add('member', async () => {
      const p = await api('GET', `/members?search=${enc}&limit=4`).catch(() => null);
      return (p?.data ?? []).map(m => ({ label: m.display_name, sub: 'Member', hash: '#/members' }));
    });
    add('member', async () => {
      const p = await api('GET', `/contacts?search=${enc}&limit=4`).catch(() => null);
      return (p?.data ?? []).map(c => ({ label: c.name, sub: 'Contact', hash: '#/contacts' }));
    });
    add('officer', async () => {
      const p = await api('GET', `/dues?period=${enc}&limit=4`).catch(() => null);
      return (p?.data ?? []).map(i => ({
        label: `${i.member_name} — ${formatMoney(i.amount_minor, i.currency)}`,
        sub: `Invoice · ${i.period_label}`, hash: '#/dues',
      }));
    });
    add('member', async () => {
      const items = await api('GET', `/action-items?limit=40`).catch(() => null);
      const list = items?.data ?? items ?? [];
      return list.filter(c => c.title.toLowerCase().includes(q.toLowerCase()))
        .slice(0, 4).map(c => ({ label: c.title, sub: 'Card', hash: '#/board' }));
    });
    add('member', async () => {
      const p = await api('GET', `/resources?search=${enc}&limit=4`).catch(() => null);
      return (p?.data ?? []).map(r => ({ label: r.title, sub: 'Resource', hash: '#/resources' }));
    });

    const results = await Promise.all(jobs);
    return results.flat().slice(0, 16);
  }

  applyStyles() {
    if (document.getElementById('cmdk-style')) return;
    const s = document.createElement('style');
    s.id = 'cmdk-style';
    s.textContent = `
      .cmdk-backdrop {
        position: fixed; inset: 0; background: rgba(0,0,0,.4);
        display: flex; align-items: flex-start; justify-content: center;
        padding-top: 12vh; z-index: 1000;
      }
      .cmdk-panel {
        width: min(560px, 92vw); background: var(--color-surface);
        border: 1px solid var(--color-border); border-radius: 12px;
        box-shadow: 0 24px 60px rgba(0,0,0,.35); overflow: hidden;
      }
      #cmdk-input {
        width: 100%; border: none; border-bottom: 1px solid var(--color-border);
        padding: 1rem 1.1rem; font-size: 1.05rem; background: transparent; color: var(--color-text);
        outline: none;
      }
      .cmdk-results { max-height: 52vh; overflow-y: auto; padding: .35rem; }
      .cmdk-group-title {
        font-size: .68rem; text-transform: uppercase; letter-spacing: .1em;
        color: var(--color-text-muted); padding: .5rem .6rem .25rem;
      }
      .cmdk-item {
        display: flex; align-items: baseline; justify-content: space-between; gap: 1rem;
        width: 100%; text-align: left; background: none; border: none; cursor: pointer;
        padding: .5rem .6rem; border-radius: 7px; color: var(--color-text); font-size: .92rem;
      }
      .cmdk-item.active { background: color-mix(in srgb, var(--color-primary) 14%, transparent); }
      .cmdk-item-sub { font-size: .74rem; color: var(--color-text-muted); white-space: nowrap; }
      .cmdk-empty { padding: 1.2rem; text-align: center; color: var(--color-text-muted); font-size: .9rem; }
      .cmdk-hint { padding: .5rem .8rem; border-top: 1px solid var(--color-border); font-size: .72rem; color: var(--color-text-muted); }
    `;
    document.head.appendChild(s);
  }
}
customElements.define('command-palette', CommandPalette);
