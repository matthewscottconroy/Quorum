import { api } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDateTime, guardButton } from '../utils.js';

const PREF_FIELDS = [
  { key: 'governance_email', label: 'Governance', hint: 'Motions opened for voting and decided' },
  { key: 'meetings_email', label: 'Meetings', hint: 'New meetings scheduled' },
  { key: 'dues_email', label: 'Dues', hint: 'Invoices and overdue reminders' },
  { key: 'assignments_email', label: 'Assignments', hint: 'Action items assigned to you' },
];

// Full notifications feed plus per-category email preferences. In-app notices
// always appear here; the checkboxes control email delivery only.
class PageNotifications extends HTMLElement {
  connectedCallback() {
    this.render();
    this.load();
  }

  render() {
    this.innerHTML = `
      <div class="page-header">
        <h1>Notifications</h1>
        <button class="btn-secondary" id="readall-btn">Mark all read</button>
      </div>

      <div class="card" style="padding:1rem;margin-bottom:1rem">
        <h2 style="margin:0 0 .25rem;font-size:1rem">Email preferences</h2>
        <p style="color:var(--color-text-muted);font-size:.85rem;margin:0 0 .75rem">
          You always see notifications here. Choose which also send you an email.</p>
        <div id="prefs"></div>
      </div>

      <div class="card" style="overflow:hidden">
        <div id="feed"></div>
      </div>
    `;
    this.querySelector('#readall-btn').addEventListener('click', async e => {
      await guardButton(e.target, async () => {
        try { await api('POST', '/notifications/read-all'); toast('All marked read', 'success'); this.load(); }
        catch { toast('Failed', 'error'); }
      })();
    });
  }

  async load() {
    const feed = this.querySelector('#feed');
    feed.innerHTML = `<div style="padding:1rem;text-align:center"><span class="spinner"></span></div>`;
    try {
      const [items, prefs] = await Promise.all([
        api('GET', '/notifications?limit=50'),
        api('GET', '/notifications/preferences'),
      ]);
      this.renderPrefs(prefs ?? {});
      this.renderFeed(items ?? []);
    } catch {
      feed.innerHTML = `<div class="empty-state"><p>Failed to load notifications.</p></div>`;
      toast('Failed to load notifications', 'error');
    }
  }

  renderPrefs(prefs) {
    const host = this.querySelector('#prefs');
    // Reset the global uppercase/block label styling for these inline rows.
    host.innerHTML = PREF_FIELDS.map(f => `
      <label style="display:flex;justify-content:flex-start;align-items:center;gap:.6rem;padding:.35rem 0;margin:0;cursor:pointer;text-transform:none;font-weight:400;letter-spacing:normal;color:var(--color-text)">
        <input type="checkbox" data-pref="${f.key}" ${prefs[f.key] !== false ? 'checked' : ''} style="width:auto;margin:0;flex:0 0 auto">
        <span style="font-size:.9rem"><strong>${esc(f.label)}</strong> <span style="color:var(--color-text-muted);font-size:.82rem">— ${esc(f.hint)}</span></span>
      </label>`).join('');
    host.querySelectorAll('input[data-pref]').forEach(cb => {
      cb.addEventListener('change', () => this.savePrefs());
    });
  }

  async savePrefs() {
    const payload = {};
    this.querySelectorAll('#prefs input[data-pref]').forEach(cb => { payload[cb.dataset.pref] = cb.checked; });
    try { await api('PUT', '/notifications/preferences', payload); toast('Preferences saved', 'success'); }
    catch { toast('Failed to save preferences', 'error'); }
  }

  renderFeed(items) {
    const feed = this.querySelector('#feed');
    if (!items.length) {
      feed.innerHTML = `<div class="empty-state"><p>No notifications yet.</p></div>`;
      return;
    }
    feed.innerHTML = `<div class="nfeed">${items.map(n => `
      <div class="nfeed-row ${n.read_at ? '' : 'unread'}">
        <div class="nfeed-main">
          <div class="nfeed-title">${esc(n.title)}</div>
          ${n.body ? `<div class="nfeed-body">${esc(n.body)}</div>` : ''}
          <div class="nfeed-time">${esc(fmtDateTime(n.created_at))}</div>
        </div>
        <div class="nfeed-actions">
          ${n.link ? `<a class="btn-ghost btn-sm" href="${esc(n.link)}" data-read="${esc(n.id)}">Open</a>` : ''}
          ${n.read_at ? '' : `<button class="btn-ghost btn-sm" data-read="${esc(n.id)}">Mark read</button>`}
        </div>
      </div>`).join('')}</div>`;
    feed.querySelectorAll('[data-read]').forEach(el => {
      el.addEventListener('click', async () => {
        try { await api('POST', `/notifications/${el.dataset.read}/read`); } catch {}
        // Let an <a> follow its href after marking read; buttons just refresh.
        if (el.tagName !== 'A') this.load();
      });
    });
    this.applyStyles();
  }

  applyStyles() {
    if (document.getElementById('nfeed-style')) return;
    const s = document.createElement('style');
    s.id = 'nfeed-style';
    s.textContent = `
      .nfeed-row { display:flex; justify-content:space-between; gap:1rem; padding:.75rem 1rem; border-bottom:1px solid var(--color-border,#eee); }
      .nfeed-row.unread { background: color-mix(in srgb, var(--color-primary,#2563eb) 7%, transparent); }
      .nfeed-title { font-weight:600; }
      .nfeed-body { color:var(--color-text-muted); font-size:.85rem; margin-top:.15rem; }
      .nfeed-time { color:var(--color-text-muted); font-size:.72rem; margin-top:.2rem; }
      .nfeed-actions { display:flex; gap:.4rem; align-items:flex-start; white-space:nowrap; }`;
    document.head.appendChild(s);
  }
}
customElements.define('page-notifications', PageNotifications);
