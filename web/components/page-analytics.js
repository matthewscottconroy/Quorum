import { api } from '../app.js';
import { toast } from './toast-notification.js';
import { esc, formatMoney } from '../utils.js';
import './charts.js';

/** A KPI tile. */
function tile(label, value, accent) {
  return `
    <div class="card" style="padding:1rem 1.15rem;border-left:3px solid ${accent}">
      <div style="font-size:.72rem;text-transform:uppercase;letter-spacing:.05em;color:var(--color-text-muted)">${esc(label)}</div>
      <div style="font-size:1.5rem;font-weight:800;font-variant-numeric:tabular-nums;margin-top:.15rem">${esc(value)}</div>
    </div>`;
}

class PageAnalytics extends HTMLElement {
  connectedCallback() {
    this.render();
    this.load();
  }

  render() {
    this.innerHTML = `
      <div class="page-header"><h1>Analytics</h1></div>
      <div id="a-warn" style="display:none"></div>
      <div id="a-kpis" style="display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:.75rem;margin-bottom:1.25rem"></div>
      <div id="a-grid" style="display:grid;grid-template-columns:repeat(auto-fit,minmax(340px,1fr));gap:1rem">
        <div style="grid-column:1/-1;text-align:center;padding:2rem"><span class="spinner"></span></div>
      </div>`;
  }

  /** Builds a titled chart card and returns the element to mount a chart into. */
  card(grid, title, subtitle) {
    const card = document.createElement('div');
    card.className = 'card';
    card.style.padding = '1.1rem 1.25rem';
    card.innerHTML = `
      <div style="font-weight:700;font-size:.95rem">${esc(title)}</div>
      ${subtitle ? `<div style="font-size:.78rem;color:var(--color-text-muted);margin-bottom:.5rem">${esc(subtitle)}</div>` : '<div style="margin-bottom:.5rem"></div>'}
      <div class="card-body"></div>`;
    grid.appendChild(card);
    return card.querySelector('.card-body');
  }

  mountChart(host, tag, data, format) {
    const el = document.createElement(tag);
    host.appendChild(el);
    if (format) el.format = format;
    el.data = data;
    return el;
  }

  async load() {
    let overview, membership, attendance, governance, payments;
    try {
      [overview, membership, attendance, governance, payments] = await Promise.all([
        api('GET', '/analytics/overview'),
        api('GET', '/analytics/membership'),
        api('GET', '/analytics/attendance'),
        api('GET', '/analytics/governance'),
        api('GET', '/analytics/payments'),
      ]);
    } catch {
      this.querySelector('#a-grid').innerHTML = '<div class="empty-state" style="grid-column:1/-1"><p>Failed to load analytics.</p></div>';
      toast('Failed to load analytics', 'error');
      return;
    }

    const cur = overview.currency;
    const money = v => formatMoney(v, cur);

    // Money is converted into the reporting currency via the configured FX
    // rates. Warn only about currencies that had no rate and were left out.
    const warn = this.querySelector('#a-warn');
    const unconvertible = overview.unconvertible_currencies ?? [];
    if (unconvertible.length) {
      warn.style.display = 'block';
      warn.innerHTML = `<div style="background:var(--color-warning);color:#fff;border-radius:var(--radius);padding:.6rem .9rem;font-size:.85rem;margin-bottom:1rem">
        ⚠ Totals are shown in ${esc(cur)}. Amounts in ${unconvertible.map(esc).join(', ')} were left out because no exchange rate into ${esc(cur)} is configured — add rates under Currencies to include them.</div>`;
    } else if (overview.mixed_currencies) {
      warn.style.display = 'block';
      warn.innerHTML = `<div style="background:var(--color-info,#0891b2);color:#fff;border-radius:var(--radius);padding:.6rem .9rem;font-size:.85rem;margin-bottom:1rem">
        Your money spans more than one currency; totals are converted into ${esc(cur)} using the configured exchange rates.</div>`;
    } else {
      warn.style.display = 'none';
    }

    this.querySelector('#a-kpis').innerHTML = [
      tile('Active members', overview.active_members, 'var(--color-primary)'),
      tile('Payments YTD', money(overview.ytd_payments_minor), 'var(--color-success)'),
      tile('Outstanding dues', money(overview.outstanding_minor), 'var(--color-warning)'),
      tile('Open motions', overview.open_motions, 'var(--color-info,#0891b2)'),
      tile('Upcoming meetings', overview.upcoming_meetings, 'var(--color-text-muted)'),
    ].join('');

    const grid = this.querySelector('#a-grid');
    grid.innerHTML = '';

    // Membership
    this.mountChart(this.card(grid, 'Active members by tier', 'current roster composition'),
      'donut-chart', membership.by_tier);
    this.mountChart(this.card(grid, 'New members', 'joined per month, last 12 months'),
      'line-chart', membership.growth);
    this.mountChart(this.card(grid, 'Members by status', ''),
      'donut-chart', membership.by_status);

    // Attendance
    this.mountChart(this.card(grid, 'Meeting attendance', `avg ${attendance.avg_present.toFixed(1)} present per meeting`),
      'bar-chart', (attendance.meetings ?? []).map(m => ({ label: m.label, value: m.present })));

    // Per-member attendance: the bylaws number ("after N consecutive
    // absences…") plus a CSV for the secretary's records.
    const attHost = this.card(grid, 'Attendance by member', 'last 12 months · streak = current consecutive absences');
    api('GET', '/analytics/attendance/members').then(res => {
      const rows = res?.members ?? [];
      if (!rows.length) { attHost.insertAdjacentHTML('beforeend', '<p style="font-size:.85rem;color:var(--color-text-muted)">No tracked meetings in range.</p>'); return; }
      attHost.insertAdjacentHTML('beforeend', `
        <div class="table-scroll" style="max-height:280px;overflow-y:auto">
          <table style="font-size:.83rem">
            <thead><tr><th>Member</th><th class="num">Attended</th><th class="num">Rate</th><th class="num">Absent streak</th></tr></thead>
            <tbody>${rows.map(m2 => `
              <tr>
                <td>${esc(m2.name)}</td>
                <td class="num">${m2.present}/${m2.meetings}</td>
                <td class="num">${m2.meetings ? Math.round(100 * m2.present / m2.meetings) : 0}%</td>
                <td class="num" ${m2.absent_streak >= 3 ? 'style="color:var(--color-danger);font-weight:700"' : ''}>${m2.absent_streak}</td>
              </tr>`).join('')}</tbody>
          </table>
        </div>
        <button class="btn-ghost" id="att-csv" style="font-size:.75rem;margin-top:.4rem">Download CSV</button>`);
      attHost.querySelector('#att-csv')?.addEventListener('click', () => {
        const csv = ['member,attended,held,rate,absent_streak',
          ...rows.map(m2 => `"${(m2.name ?? '').replace(/"/g, '""')}",${m2.present},${m2.meetings},${m2.meetings ? (m2.present / m2.meetings).toFixed(2) : '0'},${m2.absent_streak}`)].join('\n');
        const a = document.createElement('a');
        a.href = URL.createObjectURL(new Blob([csv], { type: 'text/csv' }));
        a.download = 'attendance-by-member.csv';
        a.click();
        URL.revokeObjectURL(a.href);
      });
    }).catch(() => {});

    // Governance
    this.mountChart(this.card(grid, 'Motion outcomes', `${governance.total_motions} motions total`),
      'donut-chart', governance.by_outcome);
    this.mountChart(this.card(grid, 'Vote split', 'ballots cast across all motions'),
      'donut-chart', governance.votes);

    // Payments
    this.mountChart(this.card(grid, 'Payments collected', 'per month, last 12 months'),
      'line-chart', payments.monthly, money);

    const duesHost = this.card(grid, 'Dues invoices by status', `${money(payments.outstanding_minor)} outstanding`);
    this.mountChart(duesHost, 'bar-chart', (payments.dues_by_status ?? []).map(s => ({ label: s.status, value: s.count })));
    // A compact amount legend under the count bars.
    if ((payments.dues_by_status ?? []).length) {
      const legend = document.createElement('div');
      legend.style.cssText = 'display:flex;flex-wrap:wrap;gap:.75rem;margin-top:.5rem;font-size:.78rem;color:var(--color-text-muted)';
      legend.innerHTML = payments.dues_by_status.map(s =>
        `<span><strong style="color:var(--color-text)">${esc(s.status)}</strong> ${s.count} · ${esc(money(s.amount_minor))}</span>`).join('');
      duesHost.appendChild(legend);
    }
  }
}
customElements.define('page-analytics', PageAnalytics);
