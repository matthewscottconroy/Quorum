import { api } from '../app.js';

/**
 * Self-service "forgot password" entry point. Submits an email to
 * POST /auth/forgot-password, which always succeeds (no account enumeration),
 * so the UI shows the same confirmation regardless of whether the address is
 * registered.
 */
class ForgotPasswordPage extends HTMLElement {
  connectedCallback() { this.render(); }

  render() {
    this.innerHTML = `
      <div class="login-wrap">
        <div class="login-card card">
          <div class="login-logo">Quorum</div>
          <p class="login-sub">Reset your password</p>

          <form id="forgot-form">
            <p style="font-size:.85rem;color:var(--color-text-muted);margin-bottom:1rem">
              Enter your account email and we'll send a link to choose a new password.
            </p>
            <div class="form-group">
              <label for="email">Email</label>
              <input type="email" id="email" autocomplete="username" required>
            </div>
            <button type="submit" class="btn-primary" style="width:100%">Send reset link</button>
          </form>

          <div id="sent" style="display:none">
            <p style="font-size:.9rem;color:var(--color-text);margin-bottom:1rem">
              If that email is registered, a reset link is on its way. The link is valid for one hour.
            </p>
          </div>

          <p style="text-align:center;margin-top:1rem">
            <a href="#/login" style="font-size:.85rem;color:var(--color-primary)">Back to sign in</a>
          </p>
        </div>
      </div>
      <style>
        .login-wrap { min-height:100vh;display:flex;align-items:center;justify-content:center;background:var(--color-bg); }
        .login-card { padding:2rem;width:360px; }
        .login-logo { font-size:1.75rem;font-weight:800;margin-bottom:.25rem;color:var(--color-primary);text-align:center; }
        .login-sub  { text-align:center;color:var(--color-text-muted);font-size:.9rem;margin-bottom:1.5rem; }
      </style>
    `;

    this.querySelector('#forgot-form').addEventListener('submit', async e => {
      e.preventDefault();
      const btn = e.target.querySelector('button');
      btn.disabled = true; btn.textContent = 'Sending…';
      try {
        await api('POST', '/auth/forgot-password', { email: this.querySelector('#email').value });
      } catch {
        // Deliberately ignored: this endpoint reveals nothing, so any error
        // still resolves to the same neutral confirmation.
      }
      this.querySelector('#forgot-form').style.display = 'none';
      this.querySelector('#sent').style.display = 'block';
    });
  }
}
customElements.define('forgot-password-page', ForgotPasswordPage);
