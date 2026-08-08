import { getUser, clearAuth, api, navigate, hasRole } from '../app.js';
import { esc } from '../utils.js';
import './notification-bell.js';

// `minRole` is the lowest ladder role that may see each item.
// My Account (minRole 'restricted') is visible to everyone; for a restricted
// user it is the ONLY visible item since every other entry requires member+.
const LINKS = [
  { hash: '#/my-account', label: 'My Account', icon: '👤', minRole: 'restricted' },
  { hash: '#/dashboard',  label: 'Dashboard',  icon: '⊞',  minRole: 'member' },
  { hash: '#/members',    label: 'Members',    icon: '👥', minRole: 'member' },
  { hash: '#/dues',       label: 'Dues',       icon: '💳', minRole: 'officer' },
  { hash: '#/funds',      label: 'Funds',      icon: '🏦', minRole: 'member' },
  { hash: '#/payables',   label: 'Payables',   icon: '🧾', minRole: 'officer' },
  { hash: '#/accounting', label: 'Accounting', icon: '📒', minRole: 'officer' },
  { hash: '#/budget',     label: 'Budget',     icon: '📊', minRole: 'officer' },
  { hash: '#/analytics',  label: 'Analytics',  icon: '📈', minRole: 'officer' },
  { hash: '#/currencies', label: 'Currencies', icon: '💱', minRole: 'officer' },
  { hash: '#/meetings',   label: 'Meetings',   icon: '📅', minRole: 'member' },
  { hash: '#/calendar',   label: 'Calendar',   icon: '📆', minRole: 'member' },
  { hash: '#/board',      label: 'Board',      icon: '🗂', minRole: 'member' },
  { hash: '#/discussions', label: 'Discussions', icon: '💬', minRole: 'member' },
  { hash: '#/plans',      label: 'Plans',      icon: '📋', minRole: 'member' },
  { hash: '#/contacts',   label: 'Contacts',   icon: '📇', minRole: 'member' },
  { hash: '#/resources',  label: 'Resources',  icon: '🔗', minRole: 'member' },
  { hash: '#/reports',    label: 'Reports',    icon: '🖨', minRole: 'officer' },
  { hash: '#/audit',      label: 'Audit log',  icon: '🧾', minRole: 'admin' },
  { hash: '#/settings',   label: 'Settings',   icon: '⚙',  minRole: 'admin' },
];

class NavBar extends HTMLElement {
  connectedCallback() {
    // No document-level route-changed listener here: app-shell recreates the
    // nav-bar on every route change, so such a listener would leak per navigation.
    this.render();
  }

  render() {
    const current = location.hash || '#/dashboard';
    const user = getUser();
    const links = LINKS.filter(l => hasRole(l.minRole));
    this.innerHTML = `
      <nav class="sidebar" aria-label="Main navigation">
        <div class="sidebar-brand">Quorum</div>
        <ul class="sidebar-nav">
          ${links.map(l => `
            <li>
              <a href="${l.hash}" class="${current === l.hash ? 'active' : ''}"${current === l.hash ? ' aria-current="page"' : ''}>
                <span class="icon" aria-hidden="true">${l.icon}</span> ${l.label}
              </a>
            </li>`).join('')}
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
        padding: 1.25rem 1rem;
        font-size: 1.25rem; font-weight: 800;
        color: #fff; letter-spacing: .04em;
        border-bottom: 1px solid rgba(255,255,255,.08);
      }
      .sidebar-nav { list-style: none; padding: .5rem 0; flex: 1; }
      .sidebar-nav li a {
        display: flex; align-items: center; gap: .65rem;
        padding: .6rem 1rem;
        color: var(--color-nav-text);
        text-decoration: none;
        border-radius: 0;
        font-size: .9rem;
        transition: background .15s, color .15s;
      }
      .sidebar-nav li a .icon { font-size: 1rem; width: 1.2rem; text-align: center; }
      .sidebar-nav li a:hover { background: rgba(255,255,255,.07); color: #fff; }
      .sidebar-nav li a.active { background: rgba(255,255,255,.12); color: var(--color-nav-active); font-weight: 600; }
      .sidebar-footer {
        padding: .75rem 1rem;
        border-top: 1px solid rgba(255,255,255,.08);
        display: flex; flex-direction: column; gap: .35rem;
      }
      .user-info { font-size: .78rem; color: var(--color-nav-text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
      .sidebar-footer .btn-ghost { color: #94a3b8; font-size: .8rem; text-align: left; padding: 0; }
      .sidebar-footer .btn-ghost:hover { color: #fff; background: transparent; }
      /* On narrow screens the sidebar becomes a full-width top bar with the nav
         items and footer laid out horizontally, so it doesn't eat the viewport. */
      @media (max-width: 768px) {
        .sidebar { width: 100%; min-width: 0; height: auto; flex-direction: row; flex-wrap: wrap; align-items: center; }
        .sidebar-brand { flex: 0 0 auto; border-bottom: none; padding: .75rem 1rem; }
        .sidebar-nav { display: flex; flex-flow: row wrap; padding: .25rem; flex: 1 1 100%; order: 3; }
        .sidebar-nav li a { padding: .45rem .7rem; }
        .sidebar-footer { flex: 0 0 auto; margin-left: auto; flex-direction: row; align-items: center; gap: .6rem; border-top: none; }
      }
    `;
    document.head.appendChild(s);
  }
}
customElements.define('nav-bar', NavBar);
