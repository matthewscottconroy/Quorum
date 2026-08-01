/**
 * `<vote-tally for="2" against="1" abstain="0">` — a hand-rolled SVG stacked bar
 * visualizing a motion's ballots, with a compact legend. Zero dependencies; the
 * first primitive in the app's SVG chart set (bar/line/donut come later for the
 * analytics phase). Reacts to attribute changes so it can update live.
 */
class VoteTally extends HTMLElement {
  static get observedAttributes() { return ['for', 'against', 'abstain']; }
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

    this.innerHTML = `
      <div style="display:flex;flex-direction:column;gap:.3rem">
        <svg viewBox="0 0 ${W} 8" preserveAspectRatio="none" width="100%" height="8" role="img"
             aria-label="${f} for, ${a} against, ${ab} abstain" style="display:block;border-radius:4px;overflow:hidden;background:var(--color-border)">
          ${total ? `
            <rect x="0" y="0" width="${fW}" height="8" fill="var(--color-success)"></rect>
            <rect x="${fW}" y="0" width="${aW}" height="8" fill="var(--color-danger)"></rect>
            <rect x="${fW + aW}" y="0" width="${abW}" height="8" fill="var(--color-text-muted)"></rect>
          ` : ''}
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
