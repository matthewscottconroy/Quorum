import { api, setAuth, navigate } from '../app.js';
import { toast } from './toast-notification.js';

class LoginPage extends HTMLElement {
  connectedCallback() {
    this.render();
  }

  render() {
    this.innerHTML = `
      <div class="login-wrap">
        <div class="login-card card">
          <div class="login-logo">Quorum</div>
          <p class="login-sub">Sign in to your organization</p>
          <form id="login-form">
            <div class="form-group">
              <label>Email</label>
              <input type="email" id="email" autocomplete="username" required>
            </div>
            <div class="form-group">
              <label>Password</label>
              <input type="password" id="password" autocomplete="current-password" required>
            </div>
            <button type="submit" class="btn-primary" style="width:100%">Sign in</button>
            <p id="error-msg" style="color:var(--color-danger);font-size:.85rem;margin-top:.75rem;display:none"></p>
          </form>
        </div>
      </div>
      <style>
        .login-wrap {
          min-height: 100vh; display: flex;
          align-items: center; justify-content: center;
          background: var(--color-bg);
        }
        .login-card { padding: 2rem; width: 360px; }
        .login-logo { font-size: 1.75rem; font-weight: 800; margin-bottom: .25rem; color: var(--color-primary); text-align: center; }
        .login-sub  { text-align: center; color: var(--color-text-muted); font-size: .9rem; margin-bottom: 1.5rem; }
      </style>
    `;

    this.querySelector('#login-form').addEventListener('submit', async e => {
      e.preventDefault();
      const btn = e.target.querySelector('button');
      const errEl = this.querySelector('#error-msg');
      btn.disabled = true;
      btn.textContent = 'Signing in…';
      errEl.style.display = 'none';

      try {
        const data = await api('POST', '/auth/login', {
          email:    this.querySelector('#email').value,
          password: this.querySelector('#password').value,
        });
        setAuth(data.access_token, data.user);
        document.dispatchEvent(new CustomEvent('auth-changed'));
        navigate('#/dashboard');
      } catch (err) {
        errEl.textContent = err.error ?? 'Login failed';
        errEl.style.display = 'block';
        btn.disabled = false;
        btn.textContent = 'Sign in';
      }
    });
  }
}
customElements.define('login-page', LoginPage);
