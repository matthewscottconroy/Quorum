import { api } from '../app.js';
import { esc } from '../utils.js';

/**
 * Public membership-application form (`#/apply`). Anyone can submit their
 * details; the request lands in the officer queue on the Roster page. No
 * authentication — mirrors the public ballot page's standalone shell.
 */
class PageApply extends HTMLElement {
  connectedCallback() { this.render(); }

  shell(inner) {
    return `
      <div class="login-wrap">
        <div class="login-card card" style="width:420px">
          <div class="login-logo">Quorum</div>
          ${inner}
        </div>
      </div>
      <style>
        .login-wrap { min-height:100vh;display:flex;align-items:center;justify-content:center;background:var(--color-bg); }
        .login-card { padding:2rem; }
        .login-logo { font-size:1.75rem;font-weight:800;margin-bottom:1rem;color:var(--color-primary);text-align:center; }
      </style>`;
  }

  render() {
    this.innerHTML = this.shell(`
      <h1 style="font-size:1.1rem;text-align:center;margin-bottom:.25rem">Apply to join</h1>
      <p style="font-size:.85rem;color:var(--color-text-muted);text-align:center;margin-bottom:1.25rem">
        Send your details and an officer will review your application.</p>
      <form id="apply-form">
        <div class="form-group"><label for="ap-name">Your name *</label>
          <input id="ap-name" autocomplete="name" required maxlength="200"></div>
        <div class="form-group"><label for="ap-email">Email *</label>
          <input id="ap-email" type="email" autocomplete="email" required maxlength="200"></div>
        <div class="form-group"><label for="ap-msg">Anything you'd like us to know?</label>
          <textarea id="ap-msg" rows="3" maxlength="1000"></textarea></div>
        <button class="btn-primary" id="ap-submit" type="submit" style="width:100%">Submit application</button>
      </form>
      <p id="ap-msgbox" role="status" style="font-size:.85rem;margin-top:1rem;text-align:center"></p>
      <p style="text-align:center;margin-top:1rem"><a href="#/login" style="font-size:.85rem;color:var(--color-primary)">Back to sign in</a></p>`);

    const form = this.querySelector('#apply-form');
    const btn = this.querySelector('#ap-submit');
    const msg = this.querySelector('#ap-msgbox');
    form.addEventListener('submit', async e => {
      e.preventDefault();
      const name = this.querySelector('#ap-name').value.trim();
      const email = this.querySelector('#ap-email').value.trim();
      if (!name || !email) { msg.style.color = 'var(--color-danger)'; msg.textContent = 'Name and email are required.'; return; }
      btn.disabled = true;
      try {
        await api('POST', '/public/join-request', {
          name, email,
          message: this.querySelector('#ap-msg').value.trim() || null,
        });
        this.innerHTML = this.shell(`
          <h1 style="font-size:1.1rem;text-align:center">Thank you!</h1>
          <p style="font-size:.9rem;color:var(--color-text-muted);text-align:center;margin-top:.5rem">
            Your application has been received. An officer will be in touch.</p>
          <p style="text-align:center;margin-top:1.25rem"><a href="#/login" style="font-size:.85rem;color:var(--color-primary)">Back to sign in</a></p>`);
      } catch (err) {
        btn.disabled = false;
        msg.style.color = 'var(--color-danger)';
        msg.textContent = err.error ?? 'Could not submit your application. Please try again.';
      }
    });
  }
}
customElements.define('page-apply', PageApply);
