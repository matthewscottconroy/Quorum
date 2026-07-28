import { isAuthenticated, resolveRoute, routePath, PUBLIC_ROUTES } from '../app.js';

// Bare (chrome-less) pages shown without the nav shell to logged-out visitors.
const PUBLIC_PAGE_TAGS = {
  '#/login':           'login-page',
  '#/forgot-password': 'forgot-password-page',
  '#/reset-password':  'reset-password-page',
  '#/ballot':          'ballot-page',
};

class AppShell extends HTMLElement {
  connectedCallback() {
    this.render();
    document.addEventListener('route-changed', () => this.render());
    document.addEventListener('auth-changed',  () => this.render());
  }

  render() {
    if (!isAuthenticated()) {
      // Render whichever public page the hash points at (login by default) so
      // password-recovery links work without a session.
      const path = routePath(location.hash);
      const tag = (PUBLIC_ROUTES.has(path) && PUBLIC_PAGE_TAGS[path]) || 'login-page';
      this.innerHTML = `<${tag}></${tag}>`;
      return;
    }
    const pageTag = resolveRoute();
    this.innerHTML = `
      <div class="shell">
        <nav-bar></nav-bar>
        <main class="shell-content">
          ${pageTag}
        </main>
      </div>
    `;
    this.applyStyles();
  }

  applyStyles() {
    if (document.getElementById('shell-style')) return;
    const s = document.createElement('style');
    s.id = 'shell-style';
    s.textContent = `
      .shell { display: flex; height: 100vh; overflow: hidden; }
      .shell-content {
        flex: 1; overflow-y: auto; padding: 1.75rem 2rem;
        background: var(--color-bg);
      }
    `;
    document.head.appendChild(s);
  }
}
customElements.define('app-shell', AppShell);
