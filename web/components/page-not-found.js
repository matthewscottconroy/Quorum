/**
 * @module components/page-not-found
 * @description Displayed when the user navigates to an unknown hash route.
 * @customElement page-not-found
 */
class PageNotFound extends HTMLElement {
  connectedCallback() {
    this.innerHTML = `
      <div style="text-align:center;padding:4rem 2rem">
        <h2 style="font-size:2rem;margin-bottom:.5rem">404</h2>
        <p style="color:var(--color-text-muted,#666)">Page not found.</p>
        <a href="#/dashboard" style="margin-top:1rem;display:inline-block">Go to dashboard</a>
      </div>`;
  }
}
customElements.define('page-not-found', PageNotFound);
