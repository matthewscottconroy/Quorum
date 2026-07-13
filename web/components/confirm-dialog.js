/**
 * @module confirm-dialog
 * @description Programmatic modal confirmation dialog. Prefer {@link confirm}
 * over the browser's native `window.confirm` so the dialog respects the app's
 * design system and does not block the main thread.
 *
 * @example
 * import { confirm } from './confirm-dialog.js';
 * if (await confirm('Delete this member?', 'Delete member')) {
 *   await api('DELETE', `/members/${id}`);
 * }
 */

/** @customElement confirm-dialog — shell element; logic is in the {@link confirm} helper. */
class ConfirmDialog extends HTMLElement {}
customElements.define('confirm-dialog', ConfirmDialog);

/**
 * Shows a modal confirmation dialog and returns a Promise that resolves to the
 * user's choice. The dialog is appended to `document.body` and removed on close.
 * Clicking the backdrop resolves to `false`.
 *
 * @param {string} message       - Body text to display inside the dialog.
 * @param {string} [title='Confirm'] - Title shown in the modal header.
 * @returns {Promise<boolean>} Resolves to `true` on confirm, `false` on cancel.
 */
export function confirm(message, title = 'Confirm') {
  return new Promise(resolve => {
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal" style="max-width:400px">
        <div class="modal-header">
          <h2>${esc(title)}</h2>
        </div>
        <div class="modal-body">
          <p>${esc(message)}</p>
        </div>
        <div class="modal-footer">
          <button id="cancel-btn" class="btn-secondary">Cancel</button>
          <button id="confirm-btn" class="btn-danger">Confirm</button>
        </div>
      </div>
    `;
    document.body.appendChild(backdrop);

    backdrop.querySelector('#cancel-btn').addEventListener('click', () => {
      backdrop.remove();
      resolve(false);
    });
    backdrop.querySelector('#confirm-btn').addEventListener('click', () => {
      backdrop.remove();
      resolve(true);
    });
    backdrop.addEventListener('click', e => {
      if (e.target === backdrop) { backdrop.remove(); resolve(false); }
    });
  });
}

function esc(s) { return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }
