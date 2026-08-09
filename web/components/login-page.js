import { api, setAuth, navigate, isRestricted } from '../app.js';
import { toast } from './toast-notification.js';

class LoginPage extends HTMLElement {
  connectedCallback() {
    this._mfaToken = null; // set once the first step reports 2FA is required
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
              <label for="email">Email</label>
              <input type="email" id="email" autocomplete="username" required>
            </div>
            <div class="form-group">
              <label for="password">Password</label>
              <input type="password" id="password" autocomplete="current-password" required>
            </div>
            <button type="submit" class="btn-primary" style="width:100%">Sign in</button>
            <p style="text-align:center;margin-top:.9rem">
              <a href="#/forgot-password" style="font-size:.85rem;color:var(--color-primary)">Forgot your password?</a>
              <span style="color:var(--color-text-muted)"> · </span>
              <a href="#/apply" style="font-size:.85rem;color:var(--color-primary)">Apply to join</a>
            </p>
          </form>

          <form id="mfa-form" style="display:none">
            <p class="login-sub" style="margin-bottom:1rem">Enter the 6-digit code from your authenticator app.</p>
            <div class="form-group">
              <label for="mfa-code">Authentication code</label>
              <input type="text" id="mfa-code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]*" maxlength="6" placeholder="123456">
            </div>
            <details style="margin-bottom:1rem">
              <summary style="font-size:.82rem;cursor:pointer;color:var(--color-text-muted)">Use a recovery code instead</summary>
              <div class="form-group" style="margin-top:.6rem">
                <label for="mfa-recovery">Recovery code</label>
                <input type="text" id="mfa-recovery" autocomplete="off" placeholder="XXXXXXXX">
              </div>
            </details>
            <button type="submit" class="btn-primary" style="width:100%">Verify</button>
            <p style="text-align:center;margin-top:.9rem">
              <a href="#" id="mfa-back" style="font-size:.85rem;color:var(--color-text-muted)">Back to sign in</a>
            </p>
          </form>

          <p id="error-msg" style="color:var(--color-danger);font-size:.85rem;margin-top:.75rem;display:none"></p>
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

    this.querySelector('#login-form').addEventListener('submit', e => this.onPasswordSubmit(e));
    this.querySelector('#mfa-form').addEventListener('submit', e => this.onMFASubmit(e));
    this.querySelector('#mfa-back').addEventListener('click', e => { e.preventDefault(); this.showStep('password'); });
  }

  showStep(step) {
    this._error(''); // clear any prior error
    this.querySelector('#login-form').style.display = step === 'password' ? '' : 'none';
    this.querySelector('#mfa-form').style.display    = step === 'mfa' ? '' : 'none';
    if (step === 'mfa') this.querySelector('#mfa-code').focus();
  }

  _error(msg) {
    const el = this.querySelector('#error-msg');
    el.textContent = msg;
    el.style.display = msg ? 'block' : 'none';
  }

  async onPasswordSubmit(e) {
    e.preventDefault();
    const btn = e.target.querySelector('button');
    btn.disabled = true; btn.textContent = 'Signing in…'; this._error('');
    try {
      const data = await api('POST', '/auth/login', {
        email:    this.querySelector('#email').value,
        password: this.querySelector('#password').value,
      });
      if (data.mfa_required) {
        // Password accepted; a second factor is still required.
        this._mfaToken = data.mfa_token;
        this.showStep('mfa');
        return;
      }
      this.finish(data);
    } catch (err) {
      this._error(err.error ?? 'Login failed');
    } finally {
      btn.disabled = false; btn.textContent = 'Sign in';
    }
  }

  async onMFASubmit(e) {
    e.preventDefault();
    const btn = e.target.querySelector('button');
    btn.disabled = true; btn.textContent = 'Verifying…'; this._error('');
    const code     = this.querySelector('#mfa-code').value.trim();
    const recovery = this.querySelector('#mfa-recovery').value.trim();
    try {
      const body = { mfa_token: this._mfaToken };
      if (recovery) body.recovery_code = recovery; else body.code = code;
      const data = await api('POST', '/auth/login/2fa', body);
      this.finish(data);
    } catch (err) {
      this._error(err.error ?? 'Verification failed');
    } finally {
      btn.disabled = false; btn.textContent = 'Verify';
    }
  }

  finish(data) {
    setAuth(data.access_token, data.user, data.expires_at);
    document.dispatchEvent(new CustomEvent('auth-changed'));
    navigate(isRestricted() ? '#/my-account' : '#/dashboard');
  }
}
customElements.define('login-page', LoginPage);
