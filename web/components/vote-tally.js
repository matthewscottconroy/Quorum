/**
 * `<vote-tally for="2" against="1" abstain="0">` — a hand-rolled SVG stacked bar
 * visualizing a motion's ballots, with a compact legend. Zero dependencies; the
 * first primitive in the app's SVG chart set (bar/line/donut come later for the
 * analytics phase). Reacts to attribute changes so it can update live.
 */
class VoteTally extends HTMLElement {
  static get observedAttributes() { return ['for', 'against', 'abstain', 'threshold']; }
  connectedCallback() { this.render(); }
  attributeChangedCallback() { if (this.isConnected) this.render(); }

  render() {
    const f  = Math.max(0, parseInt(this.getAttribute('for'), 10)     || 0);
    const a  = Math.max(0, parseInt(this.getAttribute('against'), 10) || 0);
    const ab = Math.max(0, parseInt(this.getAttribute('abstain'), 10) || 0);
    const total = f + a + ab;
    const W = 100;
    const seg = n => (total ? (n / total) * W : 0);
    const fW = seg(f), aW = seg(a), abW = seg(ab);

    // The pass line: where the FOR segment must reach, within the counted
    // (for+against) span, for the motion to carry. Abstentions don't count
    // toward the bar, so the marker lives inside that span only.
    const threshold = this.getAttribute('threshold');
    let markX = null;
    if (threshold && f + a > 0) {
      const share = { majority: 0.5, two_thirds: 2 / 3, unanimous: 1 }[threshold];
      if (share != null) markX = share * (fW + aW);
    }

    this.innerHTML = `
      <div style="display:flex;flex-direction:column;gap:.3rem">
        <svg viewBox="0 0 ${W} 8" preserveAspectRatio="none" width="100%" height="8" role="img"
             aria-label="${f} for, ${a} against, ${ab} abstain${markX != null ? ' — the marker shows the passing bar' : ''}" style="display:block;border-radius:4px;overflow:hidden;background:var(--color-border)">
          ${total ? `
            <rect x="0" y="0" width="${fW}" height="8" fill="var(--color-success)"></rect>
            <rect x="${fW}" y="0" width="${aW}" height="8" fill="var(--color-danger)"></rect>
            <rect x="${fW + aW}" y="0" width="${abW}" height="8" fill="var(--color-text-muted)"></rect>
          ` : ''}
          ${markX != null ? `<rect x="${Math.min(markX, W - 1.2)}" y="0" width="1.2" height="8" fill="var(--color-text)"></rect>` : ''}
        </svg>
        <div style="display:flex;gap:.9rem;font-size:.78rem;font-variant-numeric:tabular-nums;color:var(--color-text-muted)">
          <span style="color:var(--color-success);font-weight:600">✓ ${f} for</span>
          <span style="color:var(--color-danger);font-weight:600">✗ ${a} against</span>
          <span>– ${ab} abstain</span>
        </div>
      </div>`;
  }
}
customElements.define('vote-tally', VoteTally);
