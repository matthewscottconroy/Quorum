/**
 * @module toast-notification
 * @description Singleton toast notification system. Import and call {@link toast}
 * to show a transient message anywhere in the app without holding a direct
 * reference to the element.
 *
 * @example
 * import { toast } from './toast-notification.js';
 * toast('Saved successfully', 'success');
 * toast('Something went wrong', 'error');
 */

/**
 * `<toast-notification>` custom element — rendered as a flex container
 * at the bottom-right of the viewport. Individual toasts are appended as
 * children and removed after `duration` milliseconds.
 * @customElement toast-notification
 */
class ToastNotification extends HTMLElement {
  connectedCallback() {
    this.className = 'toast-container';
    this.setAttribute('role', 'status');
    this.setAttribute('aria-live', 'polite');
    // Manual popover: joins the browser top layer so toasts render above an
    // open <dialog> backdrop instead of dimmed beneath it. "manual" opts out
    // of light-dismiss so clicks elsewhere don't close the container.
    this.setAttribute('popover', 'manual');
  }

  /**
   * Appends a toast message and removes it after `duration` ms.
   * @param {string} message - Human-readable text to display.
   * @param {'info'|'success'|'error'|'warning'} [type='info'] - Controls the badge colour via CSS class.
   * @param {number} [duration=3500] - Time in milliseconds before auto-dismiss.
   */
  show(message, type = 'info', duration = 3500) {
    const el = document.createElement('div');
    el.className = `toast toast-${type}`;
    // Errors should interrupt screen readers assertively; do it on the
    // individual toast (role="alert") rather than mutating the shared
    // container's aria-live, which would affect other toasts still visible.
    if (type === 'error') el.setAttribute('role', 'alert');
    el.textContent = message;
    this.appendChild(el);
    this.#promote();
    setTimeout(() => {
      el.remove();
      // Leave the top layer once empty so a later dialog isn't stacked under
      // a stale popover entry.
      if (this.childElementCount === 0) {
        try { this.hidePopover(); } catch { /* not open / unsupported */ }
      }
    }, duration);
  }

  /**
   * (Re-)shows the container as a popover so it sits at the TOP of the top
   * layer — above any dialog opened after the previous toast. No-op in
   * browsers without the Popover API (the container falls back to its
   * z-index positioning).
   */
  #promote() {
    if (typeof this.showPopover !== 'function') return;
    try {
      if (this.matches(':popover-open')) this.hidePopover();
      this.showPopover();
    } catch { /* detached element or transient state; toast still renders */ }
  }
}
customElements.define('toast-notification', ToastNotification);

// Singleton helper so components can call toast() without a reference.
let _toastEl = null;
function getToast() {
  if (!_toastEl) {
    _toastEl = document.createElement('toast-notification');
    document.body.appendChild(_toastEl);
  }
  return _toastEl;
}

/**
 * Shows a transient toast notification. Creates the singleton element on first call.
 * @param {string} message - Text to display.
 * @param {'info'|'success'|'error'|'warning'} [type='info'] - Visual style.
 */
export function toast(message, type = 'info') {
  getToast().show(message, type);
}
