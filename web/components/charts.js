/**
 * Hand-rolled, dependency-free SVG chart components for the analytics dashboard.
 * Each takes its data via a `.data` property (array) and an optional `.format`
 * function for value labels, and re-renders on assignment. They inherit the app's
 * color tokens so they theme automatically.
 *
 *   <bar-chart>   vertical bars        data: [{label, value}]
 *   <line-chart>  time series + area   data: [{x, y}]
 *   <donut-chart> proportions + legend data: [{label, value}]
 */
import { esc } from '../utils.js';

// Categorical palette for donut segments. Chosen to stay legible on both themes.
const PALETTE = ['#2563eb', '#16a34a', '#d97706', '#0891b2', '#7c3aed', '#db2777', '#64748b', '#0ea5e9'];

const identity = v => String(v);

class BaseChart extends HTMLElement {
  set data(v) { this._data = Array.isArray(v) ? v : []; this._render(); }
  get data() { return this._data ?? []; }
  set format(fn) { this._format = typeof fn === 'function' ? fn : identity; this._render(); }
  connectedCallback() { this._render(); }
  _render() {} // overridden
}

class BarChart extends BaseChart {
  _render() {
    const data = this._data ?? [];
    const fmt = this._format ?? identity;
    const H = Number(this.getAttribute('height')) || 220;
    const accent = this.getAttribute('accent') || 'var(--color-primary)';
    const pad = { t: 20, r: 10, b: 34, l: 12 };
    const slot = 64;
    const W = Math.max(220, pad.l + pad.r + data.length * slot);
    const chartH = H - pad.t - pad.b;
    const max = Math.max(1, ...data.map(d => Number(d.value) || 0));
    const bw = Math.min(40, slot * 0.62);

    if (!data.length) { this.innerHTML = emptyChart(H); return; }

    const bars = data.map((d, i) => {
      const val = Number(d.value) || 0;
      const h = (val / max) * chartH;
      const x = pad.l + i * slot + (slot - bw) / 2;
      const y = pad.t + chartH - h;
      return `
        <rect x="${x}" y="${y}" width="${bw}" height="${Math.max(0, h)}" rx="3" fill="${accent}"></rect>
        <text x="${x + bw / 2}" y="${y - 5}" text-anchor="middle" font-size="11" font-weight="600" fill="var(--color-text)">${esc(fmt(val))}</text>
        <text x="${x + bw / 2}" y="${H - 12}" text-anchor="middle" font-size="10.5" fill="var(--color-text-muted)">${esc(truncate(d.label, 10))}</text>`;
    }).join('');

    const label = data.map(d => `${d.label} ${fmt(Number(d.value) || 0)}`).join(', ');
    this.innerHTML = `<svg viewBox="0 0 ${W} ${H}" width="100%" height="${H}" preserveAspectRatio="xMidYMid meet" role="img" aria-label="${esc(label)}">
      <line x1="${pad.l}" y1="${pad.t + chartH}" x2="${W - pad.r}" y2="${pad.t + chartH}" stroke="var(--color-border)"></line>
      ${bars}
    </svg>`;
  }
}

class LineChart extends BaseChart {
  _render() {
    const data = this._data ?? [];
    const fmt = this._format ?? identity;
    const H = Number(this.getAttribute('height')) || 220;
    const accent = this.getAttribute('accent') || 'var(--color-primary)';
    const pad = { t: 18, r: 14, b: 30, l: 14 };
    const W = 640;
    const chartH = H - pad.t - pad.b, chartW = W - pad.l - pad.r;

    if (!data.length) { this.innerHTML = emptyChart(H); return; }

    const max = Math.max(1, ...data.map(d => Number(d.y) || 0));
    const n = data.length;
    const xAt = i => pad.l + (n === 1 ? chartW / 2 : (i / (n - 1)) * chartW);
    const yAt = v => pad.t + chartH - ((Number(v) || 0) / max) * chartH;

    const pts = data.map((d, i) => `${xAt(i)},${yAt(d.y)}`).join(' ');
    const area = `${pad.l},${pad.t + chartH} ${pts} ${xAt(n - 1)},${pad.t + chartH}`;
    // Sparse x labels: first, middle, last (avoids crowding on 12 months).
    const labelIdx = new Set([0, Math.floor((n - 1) / 2), n - 1]);
    const xlabels = data.map((d, i) => labelIdx.has(i)
      ? `<text x="${xAt(i)}" y="${H - 10}" text-anchor="middle" font-size="10.5" fill="var(--color-text-muted)">${esc(d.x)}</text>` : '').join('');
    const dots = data.map((d, i) => `<circle cx="${xAt(i)}" cy="${yAt(d.y)}" r="${i === n - 1 ? 4 : 2.5}" fill="${accent}"></circle>`).join('');
    const lastVal = `<text x="${xAt(n - 1)}" y="${yAt(data[n - 1].y) - 8}" text-anchor="end" font-size="11" font-weight="600" fill="var(--color-text)">${esc(fmt(data[n - 1].y))}</text>`;

    // Uniform (meet) scaling with a width-derived height avoids the mark/text
    // distortion that "none" caused when the card width differed from the viewBox.
    const label = data.map(d => `${d.x}: ${fmt(Number(d.y) || 0)}`).join(', ');
    this.innerHTML = `<svg viewBox="0 0 ${W} ${H}" width="100%" preserveAspectRatio="xMidYMid meet" role="img" aria-label="${esc(label)}" style="display:block;height:auto">
      <polygon points="${area}" fill="${accent}" opacity="0.12"></polygon>
      <polyline points="${pts}" fill="none" stroke="${accent}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"></polyline>
      ${dots}${lastVal}${xlabels}
    </svg>`;
  }
}

class DonutChart extends BaseChart {
  _render() {
    const data = (this._data ?? []).filter(d => (Number(d.value) || 0) > 0);
    const fmt = this._format ?? identity;
    const total = data.reduce((s, d) => s + (Number(d.value) || 0), 0);
    const cx = 90, cy = 90, rO = 78, rI = 50;

    if (!total) { this.innerHTML = emptyChart(180); return; }

    let segments;
    if (data.length === 1) {
      // A single 100% slice: draw a full ring (an arc from start to start is degenerate).
      segments = `<circle cx="${cx}" cy="${cy}" r="${(rO + rI) / 2}" fill="none" stroke="${PALETTE[0]}" stroke-width="${rO - rI}"></circle>`;
    } else {
      let a0 = -Math.PI / 2;
      segments = data.map((d, i) => {
        const a1 = a0 + ((Number(d.value) || 0) / total) * 2 * Math.PI;
        const path = annularSector(cx, cy, rO, rI, a0, a1);
        a0 = a1;
        return `<path d="${path}" fill="${PALETTE[i % PALETTE.length]}"></path>`;
      }).join('');
    }

    const legend = data.map((d, i) => `
      <div style="display:flex;align-items:center;gap:.45rem;font-size:.82rem">
        <span style="width:11px;height:11px;border-radius:3px;background:${PALETTE[i % PALETTE.length]};flex:none"></span>
        <span style="flex:1">${esc(d.label)}</span>
        <span style="font-variant-numeric:tabular-nums;font-weight:600">${esc(fmt(d.value))}</span>
      </div>`).join('');

    const label = data.map(d => `${d.label} ${fmt(Number(d.value) || 0)}`).join(', ');
    this.innerHTML = `
      <div style="display:flex;align-items:center;gap:1.25rem;flex-wrap:wrap">
        <svg viewBox="0 0 180 180" width="160" height="160" role="img" aria-label="${esc(label)}" style="flex:none">
          ${segments}
          <text x="90" y="86" text-anchor="middle" font-size="24" font-weight="700" fill="var(--color-text)">${esc(String(total))}</text>
          <text x="90" y="104" text-anchor="middle" font-size="11" fill="var(--color-text-muted)">total</text>
        </svg>
        <div style="display:flex;flex-direction:column;gap:.4rem;min-width:150px;flex:1">${legend}</div>
      </div>`;
  }
}

// ---- helpers ----

function annularSector(cx, cy, rO, rI, a0, a1) {
  const p = (r, a) => [cx + r * Math.cos(a), cy + r * Math.sin(a)];
  const large = (a1 - a0) > Math.PI ? 1 : 0;
  const [x0, y0] = p(rO, a0), [x1, y1] = p(rO, a1), [x2, y2] = p(rI, a1), [x3, y3] = p(rI, a0);
  return `M${x0},${y0} A${rO},${rO} 0 ${large} 1 ${x1},${y1} L${x2},${y2} A${rI},${rI} 0 ${large} 0 ${x3},${y3} Z`;
}

function truncate(s, n) {
  s = String(s ?? '');
  return s.length > n ? s.slice(0, n - 1) + '…' : s;
}

function emptyChart(h) {
  return `<div style="height:${h}px;display:flex;align-items:center;justify-content:center;color:var(--color-text-muted);font-size:.85rem">No data yet</div>`;
}

customElements.define('bar-chart', BarChart);
customElements.define('line-chart', LineChart);
customElements.define('donut-chart', DonutChart);
