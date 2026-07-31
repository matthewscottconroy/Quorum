import { api, apiDownload, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDateTime } from '../utils.js';

// Printable PDF reports (officer+; the audit report is admin-only). Every
// download here — and every export anywhere in Quorum — is recorded in the
// tamper-evident audit log: who exported what, and when.
class PageReports extends HTMLElement {
  connectedCallback() {
    this.render();
    this.loadMeetings();
  }

  render() {
    this.innerHTML = `
      <div class="page-header"><h1>Reports</h1></div>
      <p style="color:var(--color-text-muted);font-size:.85rem;margin-bottom:1rem">
        🧾 Every export is recorded in the audit log (who, what, when) — admins can review them under
        <a href="#/audit">Audit log</a> by filtering for “EXPORT”. PDFs are watermarked with the exporter
        and time, and carry an embedded SHA-256 integrity stamp that is also written to the audit entry —
        anyone can verify a document offline with <code>ops/verify-pdf-export.py</code>.</p>

      <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:1rem">
        <div class="card" style="padding:1.1rem">
          <h3 style="margin:0 0 .3rem;font-size:1rem">Member roster</h3>
          <p style="font-size:.83rem;color:var(--color-text-muted);margin:0 0 .8rem">
            All members grouped by status, with contact details, tier, join date, and dues standing.</p>
          <button class="btn-primary" data-pdf="/reports/members.pdf" data-name="quorum-members.pdf">Download PDF</button>
        </div>

        <div class="card" style="padding:1.1rem">
          <h3 style="margin:0 0 .3rem;font-size:1rem">Dues &amp; receivables</h3>
          <p style="font-size:.83rem;color:var(--color-text-muted);margin:0 0 .8rem">
            Invoices by status (per currency — never summed at par) and recent payments from the ledger.</p>
          <button class="btn-primary" data-pdf="/reports/dues.pdf" data-name="quorum-dues.pdf">Download PDF</button>
        </div>

        <div class="card" style="padding:1.1rem">
          <h3 style="margin:0 0 .3rem;font-size:1rem">Meeting minutes</h3>
          <p style="font-size:.83rem;color:var(--color-text-muted);margin:0 0 .8rem">
            The recording secretary's document: attendance, proceedings, motions with votes, decisions.</p>
          <select id="mt-sel" style="width:100%;margin-bottom:.6rem"><option>Loading…</option></select>
          <div style="display:flex;gap:.5rem">
            <button class="btn-primary" id="mt-pdf">Download PDF</button>
            <button class="btn-secondary" id="mt-md">Markdown</button>
          </div>
        </div>

        ${isAdmin() ? `
        <div class="card" style="padding:1.1rem">
          <h3 style="margin:0 0 .3rem;font-size:1rem">Audit log</h3>
          <p style="font-size:.83rem;color:var(--color-text-muted);margin:0 0 .8rem">
            Recent audit entries with the hash-chain status stamped at generation time. For third-party
            verification use the evidence CSV on the Audit page.</p>
          <button class="btn-primary" data-pdf="/reports/audit.pdf" data-name="quorum-audit.pdf">Download PDF</button>
        </div>` : ''}
      </div>
    `;
    this.querySelectorAll('[data-pdf]').forEach(btn => {
      btn.addEventListener('click', () => {
        apiDownload(btn.dataset.pdf, btn.dataset.name).catch(() => toast('Download failed', 'error'));
      });
    });
    this.querySelector('#mt-pdf').addEventListener('click', () => {
      const id = this.querySelector('#mt-sel').value;
      if (!id) { toast('Choose a meeting', 'error'); return; }
      apiDownload(`/reports/meetings/${id}/minutes.pdf`, 'minutes.pdf').catch(() => toast('Download failed', 'error'));
    });
    this.querySelector('#mt-md').addEventListener('click', () => {
      const id = this.querySelector('#mt-sel').value;
      if (!id) { toast('Choose a meeting', 'error'); return; }
      apiDownload(`/meetings/${id}/minutes.md`, 'minutes.md').catch(() => toast('Download failed', 'error'));
    });
  }

  async loadMeetings() {
    const sel = this.querySelector('#mt-sel');
    try {
      const page = await api('GET', '/meetings?limit=100');
      const meetings = page?.data ?? [];
      sel.innerHTML = meetings.length
        ? meetings.map(m =>
            `<option value="${esc(m.id)}">${esc(m.title)} — ${esc(fmtDateTime(m.scheduled_at))}${m.minutes_finalized_at ? ' ✓ finalized' : ''}</option>`).join('')
        : '<option value="">No meetings yet</option>';
    } catch {
      sel.innerHTML = '<option value="">Failed to load meetings</option>';
    }
  }
}
customElements.define('page-reports', PageReports);
