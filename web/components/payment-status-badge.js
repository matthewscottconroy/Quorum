/**
 * `<payment-status-badge>` custom element.
 *
 * Renders a colour-coded badge for a payment or invoice status value.
 * The `status` attribute drives both the CSS modifier class and the visible label.
 *
 * @customElement payment-status-badge
 * @attr {string} status - One of `pending`, `paid`, `overdue`, `cancelled`, or `none`.
 *
 * @example
 * <payment-status-badge status="overdue"></payment-status-badge>
 */
class PaymentStatusBadge extends HTMLElement {
  static observedAttributes = ['status'];

  connectedCallback()              { this.render(); }
  attributeChangedCallback()       { this.render(); }

  render() {
    const s = this.getAttribute('status') ?? 'none';
    const span = document.createElement('span');
    span.className = `badge badge-${s}`;
    span.textContent = s;
    this.replaceChildren(span);
  }
}
customElements.define('payment-status-badge', PaymentStatusBadge);
