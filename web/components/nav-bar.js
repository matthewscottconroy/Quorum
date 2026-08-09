import { getUser, clearAuth, api, navigate, hasRole } from '../app.js';
import { esc, vocab } from '../utils.js';
import './notification-bell.js';

// `minRole` is the lowest ladder role that may see each item.
// My Account (minRole 'restricted') is visible to everyone; for a restricted
// user it is the ONLY visible item since every other entry requires member+.
//
// Items are organized into collapsible GROUPS so the sidebar reads as 4-5
// areas instead of 20 flat entries (and the mobile top-bar stays shallow).
// A group renders only if the user can see at least one of its items.
const TOP_LINKS = [
  { hash: '#/my-account', label: 'My Account', icon: '👤', minRole: 'restricted' },
  { hash: '#/dashboard',  label: 'Dashboard',  icon: '⊞',  minRole: 'member' },
  { hash: '#/discussions', label: 'Discussions', icon: '💬', minRole: 'member' },
];

const GROUPS = [
  {
    key: 'money', label: 'Money', links: [
      { hash: '#/dues',       label: 'Dues',       icon: '💳', minRole: 'officer' },
      { hash: '#/payables',   label: 'Payables',   icon: '🧾', minRole: 'officer' },
      { hash: '#/payment-reports', label: 'Confirmations', icon: '✅', minRole: 'officer' },
      { hash: '#/funds',      label: 'Funds',      icon: '🏦', minRole: 'member' },
      { hash: '#/accounting', label: 'Accounting', icon: '📒', minRole: 'officer' },
      { hash: '#/budget',     label: 'Budget',     icon: '📊', minRole: 'officer' },
      { hash: '#/currencies', label: 'Currencies', icon: '💱', minRole: 'officer' },
    ],
  },
  {
    key: 'people', label: 'People', links: [
      { hash: '#/members',  label: 'Members',  icon: '👥', minRole: 'member' },
      { hash: '#/contacts', label: 'Contacts', icon: '📇', minRole: 'member' },
      { hash: '#/roster',   label: 'Roster',   icon: '🏛', minRole: 'member' },
    ],
  },
  {
    key: 'work', label: 'Work', links: [
      { hash: '#/board',    label: 'Board',    icon: '🗂', minRole: 'member' },
      { hash: '#/plans',    label: 'Plans',    icon: '📋', minRole: 'member' },
      { hash: '#/meetings', label: 'Meetings', icon: '📅', minRole: 'member' },
      { hash: '#/calendar', label: 'Calendar', icon: '📆', minRole: 'member' },
    ],
  },
  {
    key: 'records', label: 'Records', links: [
      { hash: '#/resources', label: 'Resources', icon: '🔗', minRole: 'member' },
      { hash: '#/reports',   label: 'Reports',   icon: '🖨', minRole: 'officer' },
      { hash: '#/analytics', label: 'Analytics', icon: '📈', minRole: 'officer' },
      { hash: '#/audit',     label: 'Audit log', icon: '🧾', minRole: 'admin' },
      { hash: '#/settings',  label: 'Settings',  icon: '⚙',  minRole: 'admin' },
    ],
  },
  {
    key: 'topsecret', label: 'Top Secret', links: [
      { hash: '#/arcade', label: 'Arcade', icon: '🕹', minRole: 'member' },
    ],
  },
];

// Collapsed-state memory. UI preference only — tokens never touch storage.
const NAV_STATE_KEY = 'quorum.nav.collapsed';
function collapsedSet() {
  try { return new Set(JSON.parse(localStorage.getItem(NAV_STATE_KEY) || '[]')); }
  catch { return new Set(); }
}
function saveCollapsed(set) {
  try { localStorage.setItem(NAV_STATE_KEY, JSON.stringify([...set])); } catch { /* private mode */ }
}

class NavBar extends HTMLElement {
  connectedCallback() {
    // No document-level route-changed listener here: app-shell recreates the
    // nav-bar on every route change, so such a listener would leak per navigation.
    this.render();
  }

  render() {
    const current = location.hash || '#/dashboard';
    const user = getUser();
    const collapsed = collapsedSet();

    const item = l => `
      <li>
        <a href="${l.hash}" class="${current === l.hash ? 'active' : ''}"${current === l.hash ? ' aria-current="page"' : ''}>
          <span class="icon" aria-hidden="true">${l.icon}</span> ${esc(vocab(l.label))}
        </a>
      </li>`;

    const groupsHTML = GROUPS.map(g => {
      const visible = g.links.filter(l => hasRole(l.minRole));
      if (!visible.length) return '';
      // A group containing the current page never renders collapsed — the
      // user must always be able to see where they are.
      const isOpen = !collapsed.has(g.key) || visible.some(l => l.hash === current);
      return `
        <li class="nav-group">
          <button class="nav-group-toggle" data-group="${g.key}" aria-expanded="${isOpen}">
            <span class="nav-group-label">${esc(vocab(g.label))}</span>
            <span class="nav-group-chev" aria-hidden="true">${isOpen ? '▾' : '▸'}</span>
          </button>
          <ul class="nav-group-items" ${isOpen ? '' : 'hidden'}>
            ${visible.map(item).join('')}
          </ul>
        </li>`;
    }).join('');

    this.innerHTML = `
      <nav class="sidebar" aria-label="Main navigation">
        <div class="sidebar-brand">Quorum</div>
        <button class="btn-ghost nav-search-btn" id="nav-search" title="Search everything (Ctrl-K)">
          🔍 Search… <kbd>⌘K</kbd>
        </button>
        <ul class="sidebar-nav">
          ${TOP_LINKS.filter(l => hasRole(l.minRole)).map(item).join('')}
          ${groupsHTML}
        </ul>
        <div class="sidebar-footer">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:.5rem">
            <span class="user-info">${esc(user?.email ?? '')}</span>
            <notification-bell></notification-bell>
          </div>
          <button id="logout-btn" class="btn-ghost">Sign out</button>
        </div>
      </nav>
    `;

    this.querySelectorAll('.nav-group-toggle').forEach(btn => {
      btn.addEventListener('click', () => {
        const key = btn.dataset.group;
        const set = collapsedSet();
        set.has(key) ? set.delete(key) : set.add(key);
        saveCollapsed(set);
        this.render();
      });
    });

    this.querySelector('#nav-search').addEventListener('click', () => {
      document.dispatchEvent(new CustomEvent('open-command-palette'));
    });

    this.querySelector('#logout-btn').addEventListener('click', async () => {
      // The refresh token lives in an HttpOnly cookie sent automatically; no body needed.
      try { await api('POST', '/auth/logout'); }
      catch {}
      clearAuth();
      document.dispatchEvent(new CustomEvent('auth-changed'));
      navigate('#/login');
    });

    this.applyStyles();
  }

  applyStyles() {
    if (document.getElementById('nav-style')) return;
    const s = document.createElement('style');
    s.id = 'nav-style';
    s.textContent = `
      .sidebar {
        width: 210px; min-width: 210px;
        background: var(--color-nav-bg);
        display: flex; flex-direction: column;
        height: 100vh; overflow-y: auto;
      }
      .sidebar-brand {
        padding: 1.25rem 1rem .75rem;
        font-size: 1.25rem; font-weight: 800;
        color: #fff; letter-spacing: .04em;
      }
      .nav-search-btn {
        display: flex; align-items: center; gap: .45rem;
        margin: 0 .75rem .5rem; padding: .4rem .6rem !important;
        color: var(--color-nav-text) !important;
        background: rgba(255,255,255,.07) !important;
        border-radius: 6px; font-size: .82rem;
        border-bottom: none;
      }
      .nav-search-btn kbd {
        margin-left: auto; font-size: .68rem; opacity: .7;
        border: 1px solid rgba(255,255,255,.25); border-radius: 4px; padding: 0 .3rem;
        font-family: inherit;
      }
      .nav-search-btn:hover { color: #fff !important; }
      .sidebar-nav { list-style: none; padding: .25rem 0 .5rem; flex: 1; }
      .sidebar-nav li a {
        display: flex; align-items: center; gap: .65rem;
        padding: .55rem 1rem;
        color: var(--color-nav-text);
        text-decoration: none;
        border-radius: 0;
        font-size: .9rem;
        transition: background .15s, color .15s;
      }
      .sidebar-nav li a .icon { font-size: 1rem; width: 1.2rem; text-align: center; }
      .sidebar-nav li a:hover { background: rgba(255,255,255,.07); color: #fff; }
      .sidebar-nav li a.active { background: rgba(255,255,255,.12); color: var(--color-nav-active); font-weight: 600; }
      .nav-group { margin-top: .35rem; }
      .nav-group-toggle {
        display: flex; align-items: center; justify-content: space-between;
        width: 100%; background: none; border: none; cursor: pointer;
        padding: .35rem 1rem .2rem;
        color: rgba(255,255,255,.45);
      }
      .nav-group-toggle:hover .nav-group-label { color: rgba(255,255,255,.8); }
      .nav-group-label {
        font-size: .68rem; font-weight: 700; letter-spacing: .12em; text-transform: uppercase;
      }
      .nav-group-chev { font-size: .68rem; }
      .nav-group-items { list-style: none; padding: 0; }
      .sidebar-footer {
        padding: .75rem 1rem;
        border-top: 1px solid rgba(255,255,255,.08);
        display: flex; flex-direction: column; gap: .35rem;
      }
      .user-info { font-size: .78rem; color: var(--color-nav-text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
      .sidebar-footer .btn-ghost { color: #94a3b8; font-size: .8rem; text-align: left; padding: 0; }
      .sidebar-footer .btn-ghost:hover { color: #fff; background: transparent; }
      /* On narrow screens the sidebar becomes a full-width top bar; groups
         still collapse, which keeps the bar to one or two rows. */
      @media (max-width: 768px) {
        .sidebar { width: 100%; min-width: 0; height: auto; flex-direction: row; flex-wrap: wrap; align-items: center; }
        .sidebar-brand { flex: 0 0 auto; border-bottom: none; padding: .75rem 1rem; }
        .nav-search-btn { margin: 0; }
        .sidebar-nav { display: flex; flex-flow: row wrap; padding: .25rem; flex: 1 1 100%; order: 3; align-items: center; }
        .nav-group { display: flex; align-items: center; margin: 0; }
        .nav-group-items { display: flex; flex-flow: row wrap; }
        .nav-group-toggle { width: auto; padding: .35rem .5rem; }
        .sidebar-nav li a { padding: .45rem .7rem; }
        .sidebar-footer { flex: 0 0 auto; margin-left: auto; flex-direction: row; align-items: center; gap: .6rem; border-top: none; }
      }
    `;
    document.head.appendChild(s);
  }
}
customElements.define('nav-bar', NavBar);
