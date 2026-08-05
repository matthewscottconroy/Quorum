import { api, apiDownload, isAdmin } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, fmtDateTime } from '../utils.js';
import { assembleMinutesText, openHeatmapModal } from './word-heatmap.js';

// Printable PDF reports (officer+; the audit report is admin-only). Every
// download here — and every export anywhere in Quorum — is recorded in the
// tamper-evident audit log: who exported what, and when.
class PageReports extends HTMLElement {
  connectedCallback() {
    this.render();
    this.loadMeetings();
    this.loadSprints();
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
          <div style="display:flex;gap:.5rem;flex-wrap:wrap">
            <button class="btn-primary" id="mt-pdf">Download PDF</button>
            <button class="btn-secondary" id="mt-md">Markdown</button>
            <button class="btn-secondary" id="mt-heatmap" title="In-app preview; not an export, so nothing is downloaded or logged">🔥 Heat map</button>
          </div>
        </div>

        <div class="card" style="padding:1.1rem">
          <h3 style="margin:0 0 .3rem;font-size:1rem">Sprint report</h3>
          <p style="font-size:.83rem;color:var(--color-text-muted);margin:0 0 .8rem">
            Sprint analytics: points committed vs done, breakdowns by type, assignee, and status, blocked work, and the card inventory.</p>
          <select id="sp-sel" style="width:100%;margin-bottom:.6rem"><option>Loading…</option></select>
          <button class="btn-primary" id="sp-pdf">Download PDF</button>
        </div>

        ${isAdmin() ? `
        <div class="card" style="padding:1.1rem">
          <h3 style="margin:0 0 .3rem;font-size:1rem">CPA export pack</h3>
          <p style="font-size:.83rem;color:var(--color-text-muted);margin:0 0 .8rem">
            One ZIP for your accountant: sealed statements PDF, trial balance, general ledger,
            income statement, balance sheet, funds, receivables aging, and the evidence file
            (audit-chain head + verification instructions). CSVs are machine-readable.</p>
          <div style="display:flex;gap:.4rem;margin-bottom:.6rem">
            <input id="pk-from" type="date" style="flex:1">
            <input id="pk-to" type="date" style="flex:1">
          </div>
          <button class="btn-primary" id="pk-dl">Download pack</button>
        </div>

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
    this.querySelector('#mt-heatmap').addEventListener('click', async () => {
      const id = this.querySelector('#mt-sel').value;
      if (!id) { toast('Choose a meeting', 'error'); return; }
      try {
        const [mt, entries, motions] = await Promise.all([
          api('GET', `/meetings/${id}`),
          api('GET', `/meetings/${id}/minutes`),
          api('GET', `/meetings/${id}/motions`).catch(() => []),
        ]);
        openHeatmapModal(mt.title, assembleMinutesText(mt, entries, motions));
      } catch { toast('Failed to load the meeting', 'error'); }
    });
    this.querySelector('#sp-pdf').addEventListener('click', () => {
      const id = this.querySelector('#sp-sel').value;
      if (!id) { toast('Choose a sprint', 'error'); return; }
      apiDownload(`/reports/sprints/${id}.pdf`, 'sprint-report.pdf').catch(() => toast('Download failed', 'error'));
    });
    this.querySelector('#pk-dl')?.addEventListener('click', () => {
      const from = this.querySelector('#pk-from').value, to = this.querySelector('#pk-to').value;
      if (!from || !to) { toast('Pick the date range', 'error'); return; }
      apiDownload(`/reports/accounting-pack.zip?from=${from}&to=${to}`, `quorum-accounting-${from}-${to}.zip`)
        .catch(err => toast(err.error ?? 'Download failed', 'error'));
    });
    this.querySelector('#mt-md').addEventListener('click', () => {
      const id = this.querySelector('#mt-sel').value;
      if (!id) { toast('Choose a meeting', 'error'); return; }
      apiDownload(`/meetings/${id}/minutes.md`, 'minutes.md').catch(() => toast('Download failed', 'error'));
    });
  }

  async loadSprints() {
    const sel = this.querySelector('#sp-sel');
    try {
      const sprints = await api('GET', '/sprints') ?? [];
      sel.innerHTML = sprints.length
        ? sprints.map(s => `<option value="${esc(s.id)}">${esc(s.name)} (${esc(s.status)})</option>`).join('')
        : '<option value="">No sprints yet</option>';
    } catch { sel.innerHTML = '<option value="">Failed to load sprints</option>'; }
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
