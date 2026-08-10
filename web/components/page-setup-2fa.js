import { api } from '../app.js';
import { toast } from './toast-notification.js';
import { esc } from '../utils.js';

/**
 * The walled garden for an org-wide 2FA mandate: when the server answers
 * mfa_required, the app routes here. The only way out is enrolling (or
 * signing out). After enabling, a full reload refreshes the token with the
 * mfa claim and the gate lifts.
 */
class PageSetup2FA extends HTMLElement {
  connectedCallback() {
    this.step1();
  }

  step1() {
    this.innerHTML = `
      <div class="card" style="max-width:480px;margin:3rem auto;padding:1.5rem">
        <h1 style="font-size:1.2rem;margin:0 0 .4rem">🔐 Two-factor setup required</h1>
        <p style="font-size:.9rem;color:var(--color-text-muted)">
          Your organization requires two-factor authentication for your account.
          It takes about two minutes: confirm your password, scan a key into an
          authenticator app (Aegis, Google Authenticator, 1Password…), and save
          your recovery codes.</p>
        <div class="form-group"><label for="s2-pw">Your password</label>
          <input id="s2-pw" type="password" autocomplete="current-password"></div>
        <div style="display:flex;gap:.5rem;justify-content:space-between">
          <button class="btn-ghost" id="s2-logout">Sign out</button>
          <button class="btn-primary" id="s2-start">Start setup</button>
        </div>
      </div>`;
    this.querySelector('#s2-logout').addEventListener('click', async () => {
      try { await api('POST', '/auth/logout'); } catch { /* ignore */ }
      location.hash = '#/login'; location.reload();
    });
    this.querySelector('#s2-start').addEventListener('click', async () => {
      const password = this.querySelector('#s2-pw').value;
      if (!password) { toast('Password required', 'error'); return; }
      try {
        const setup = await api('POST', '/auth/2fa/setup', { password });
        this.step2(setup);
      } catch (err) { toast(err.error ?? 'Could not start setup', 'error'); }
    });
    this.querySelector('#s2-pw').focus();
  }

  step2(setup) {
    this.innerHTML = `
      <div class="card" style="max-width:480px;margin:3rem auto;padding:1.5rem">
        <h1 style="font-size:1.2rem;margin:0 0 .6rem">Scan with your authenticator</h1>
        ${setup.qr_png_base64 ? `
        <div style="text-align:center;margin-bottom:.8rem">
          <img src="data:image/png;base64,${setup.qr_png_base64}" alt="QR code — scan with your authenticator app"
               width="200" height="200" style="border-radius:8px;background:#fff;padding:6px">
        </div>
        <p style="font-size:.82rem;color:var(--color-text-muted);text-align:center;margin-bottom:.8rem">
          Point your authenticator app's camera at the code, or enter the key manually:</p>` : ''}
        <div style="font-family:monospace;font-size:1rem;letter-spacing:.06em;background:var(--color-bg);border:1px solid var(--color-border);border-radius:6px;padding:.6rem;text-align:center;word-break:break-all;margin-bottom:.8rem">${esc(setup.secret)}</div>
        <p style="font-size:.78rem;color:var(--color-text-muted);word-break:break-all;margin-bottom:1rem">
          Or paste the setup URI: <span style="font-family:monospace">${esc(setup.provisioning_uri)}</span></p>
        <div class="form-group"><label for="s2-code">Enter the 6-digit code shown in the app</label>
          <input id="s2-code" inputmode="numeric" maxlength="6" placeholder="123456"></div>
        <button class="btn-primary" id="s2-enable" style="width:100%">Confirm &amp; enable</button>
      </div>`;
    this.querySelector('#s2-enable').addEventListener('click', async () => {
      const code = this.querySelector('#s2-code').value.trim();
      if (!/^\d{6}$/.test(code)) { toast('Enter the 6-digit code', 'error'); return; }
      try {
        const res = await api('POST', '/auth/2fa/enable', { code });
        this.step3(res.recovery_codes ?? []);
      } catch (err) { toast(err.error ?? 'Invalid code', 'error'); }
    });
    this.querySelector('#s2-code').focus();
  }

  step3(codes) {
    this.innerHTML = `
      <div class="card" style="max-width:480px;margin:3rem auto;padding:1.5rem">
        <h1 style="font-size:1.2rem;margin:0 0 .4rem">✅ Two-factor enabled</h1>
        <p style="font-size:.88rem">Save these one-time <b>recovery codes</b> somewhere safe
          (password manager or printed). Each works once if you lose your authenticator:</p>
        <pre style="background:var(--color-bg);border:1px solid var(--color-border);border-radius:6px;padding:.6rem;font-size:.9rem;white-space:pre-wrap">${esc(codes.join('\n'))}</pre>
        <button class="btn-primary" id="s2-done" style="width:100%">I saved them — continue to the app</button>
      </div>`;
    this.querySelector('#s2-done').addEventListener('click', () => {
      // Full reload: boot() refreshes the session, the new token carries the
      // mfa claim, and the gate lifts.
      location.hash = '#/dashboard';
      location.reload();
    });
  }
}
customElements.define('page-setup-2fa', PageSetup2FA);
