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
    document.body.appendChild(this);
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
    el.textContent = message;
    this.appendChild(el);
    setTimeout(() => el.remove(), duration);
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
