import { isAuthenticated, resolveRoute, navigate } from '../app.js';

class AppShell extends HTMLElement {
  connectedCallback() {
    this.render();
    document.addEventListener('route-changed', () => this.render());
    document.addEventListener('auth-changed',  () => this.render());
  }

  render() {
    if (!isAuthenticated()) {
      this.innerHTML = '<login-page></login-page>';
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
