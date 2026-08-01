import { api } from '../app.js';
import { esc } from '../utils.js';

/**
 * Public async-ballot page reached from an emailed link
 * (`#/ballot?token=…`). It shows the motion and lets the recipient cast a
 * single vote without an app session — so rank-and-file / restricted members
 * can vote remotely. No authentication.
 */
class BallotPage extends HTMLElement {
  connectedCallback() { this.load(); }

  tokenFromHash() {
    const q = (location.hash.split('?')[1]) ?? '';
    return new URLSearchParams(q).get('token') ?? '';
  }

  shell(inner) {
    return `
      <div class="login-wrap">
        <div class="login-card card" style="width:400px">
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

  async load() {
    const token = this.tokenFromHash();
    if (!token) { this.innerHTML = this.shell('<p style="color:var(--color-danger)">This ballot link is missing its token.</p>'); return; }
    this.innerHTML = this.shell('<div style="text-align:center;padding:1rem"><span class="spinner"></span></div>');
    let ctx;
    try {
      ctx = await api('GET', `/public/ballot?token=${encodeURIComponent(token)}`);
    } catch (err) {
      this.innerHTML = this.shell(`<p style="color:var(--color-danger);font-size:.9rem">${esc(err.error ?? 'This ballot link is invalid or has expired.')}</p>
        <p style="text-align:center;margin-top:1rem"><a href="#/login" style="font-size:.85rem;color:var(--color-primary)">Go to sign in</a></p>`);
      return;
    }
    this.renderBallot(token, ctx);
  }

  renderBallot(token, ctx) {
    this.innerHTML = this.shell(`
      <div style="font-size:.78rem;text-transform:uppercase;letter-spacing:.05em;color:var(--color-text-muted);text-align:center;margin-bottom:.25rem">${esc(ctx.meeting_title)}</div>
      <h2 style="font-size:1.1rem;text-align:center;margin-bottom:.5rem">${esc(ctx.title)}</h2>
      ${ctx.detail ? `<p style="font-size:.88rem;color:var(--color-text);margin-bottom:1rem">${esc(ctx.detail)}</p>` : ''}
      <p style="font-size:.82rem;color:var(--color-text-muted);margin-bottom:1rem">Voting as <strong>${esc(ctx.member_name)}</strong>. Your vote is final.</p>
      <div id="ballot-choices" style="display:flex;flex-direction:column;gap:.5rem">
        <button class="btn-secondary bchoice" data-choice="for" style="width:100%">✓ For</button>
        <button class="btn-secondary bchoice" data-choice="against" style="width:100%">✗ Against</button>
        <button class="btn-secondary bchoice" data-choice="abstain" style="width:100%">– Abstain</button>
      </div>
      <p id="ballot-msg" style="font-size:.85rem;margin-top:1rem;display:none"></p>`);

    this.querySelectorAll('.bchoice').forEach(btn => btn.addEventListener('click', async () => {
      this.querySelectorAll('.bchoice').forEach(b => (b.disabled = true));
      const msg = this.querySelector('#ballot-msg');
      try {
        await api('POST', '/public/ballot', { token, choice: btn.dataset.choice });
        this.querySelector('#ballot-choices').style.display = 'none';
        msg.style.display = 'block';
        msg.style.color = 'var(--color-success)';
        msg.textContent = `Your vote (${btn.dataset.choice}) has been recorded. Thank you!`;
      } catch (err) {
        this.querySelectorAll('.bchoice').forEach(b => (b.disabled = false));
        msg.style.display = 'block';
        msg.style.color = 'var(--color-danger)';
        msg.textContent = err.error ?? 'Could not record your vote.';
      }
    }));
  }
}
customElements.define('ballot-page', BallotPage);
