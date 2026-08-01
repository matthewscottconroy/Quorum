import { api, navigate } from '../app.js';
import { toast } from './toast-notification.js';

/**
 * Completes a password reset. The single-use token arrives in the hash query
 * (`#/reset-password?token=…`) from the emailed link. On success it posts to
 * POST /auth/reset-password and returns the user to the sign-in screen.
 */
class ResetPasswordPage extends HTMLElement {
  connectedCallback() { this.render(); }

  /** Extracts the `token` param from the current hash's query string. */
  tokenFromHash() {
    const q = (location.hash.split('?')[1]) ?? '';
    return new URLSearchParams(q).get('token') ?? '';
  }

  render() {
    const token = this.tokenFromHash();
    this.innerHTML = `
      <div class="login-wrap">
        <div class="login-card card">
          <div class="login-logo">Quorum</div>
          <p class="login-sub">Choose a new password</p>

          ${token ? `
          <form id="reset-form">
            <div class="form-group">
              <label for="pw">New password</label>
              <input type="password" id="pw" autocomplete="new-password" required>
            </div>
            <div class="form-group">
              <label for="pw2">Confirm new password</label>
              <input type="password" id="pw2" autocomplete="new-password" required>
            </div>
            <p style="font-size:.78rem;color:var(--color-text-muted);margin-bottom:1rem">At least 10 characters.</p>
            <button type="submit" class="btn-primary" style="width:100%">Set new password</button>
            <p id="error-msg" style="color:var(--color-danger);font-size:.85rem;margin-top:.75rem;display:none"></p>
          </form>` : `
          <p style="font-size:.9rem;color:var(--color-danger)">This reset link is missing its token. Request a new link.</p>
          <p style="text-align:center;margin-top:1rem"><a href="#/forgot-password" style="font-size:.85rem;color:var(--color-primary)">Request a new link</a></p>
          `}
        </div>
      </div>
      <style>
        .login-wrap { min-height:100vh;display:flex;align-items:center;justify-content:center;background:var(--color-bg); }
        .login-card { padding:2rem;width:360px; }
        .login-logo { font-size:1.75rem;font-weight:800;margin-bottom:.25rem;color:var(--color-primary);text-align:center; }
        .login-sub  { text-align:center;color:var(--color-text-muted);font-size:.9rem;margin-bottom:1.5rem; }
      </style>
    `;

    this.querySelector('#reset-form')?.addEventListener('submit', async e => {
      e.preventDefault();
      const errEl = this.querySelector('#error-msg');
      const pw  = this.querySelector('#pw').value;
      const pw2 = this.querySelector('#pw2').value;
      const show = m => { errEl.textContent = m; errEl.style.display = 'block'; };
      if (pw !== pw2) { show('Passwords do not match'); return; }
      if (pw.length < 10) { show('Password must be at least 10 characters'); return; }
      const btn = e.target.querySelector('button');
      btn.disabled = true; btn.textContent = 'Saving…'; errEl.style.display = 'none';
      try {
        await api('POST', '/auth/reset-password', { token, new_password: pw });
        toast('Password updated — please sign in', 'success');
        navigate('#/login');
      } catch (err) {
        show(err.error ?? 'Reset failed. The link may have expired.');
        btn.disabled = false; btn.textContent = 'Set new password';
      }
    });
  }
}
customElements.define('reset-password-page', ResetPasswordPage);
