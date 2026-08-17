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
import { esc, openModal } from '../utils.js';

/**
 * Shows a modal confirmation dialog and returns a Promise that resolves to the
 * user's choice. Built on the shared {@link openModal} helper, so dismissing
 * the dialog (Escape or the header close button) resolves to `false`.
 *
 * @param {string} message       - Body text to display inside the dialog.
 * @param {string} [title='Confirm'] - Title shown in the modal header.
 * @returns {Promise<boolean>} Resolves to `true` on confirm, `false` on cancel.
 */
export function confirm(message, title = 'Confirm') {
  return new Promise(resolve => {
    const { dialog, close } = openModal({
      title,
      maxWidth: '400px',
      body: `
        <div class="modal-body">
          <p style="white-space:pre-line">${esc(message)}</p>
        </div>
        <div class="modal-footer">
          <button id="cancel-btn" class="btn-secondary">Cancel</button>
          <button id="confirm-btn" class="btn-danger">Confirm</button>
        </div>
      `,
    });

    let confirmed = false;
    dialog.querySelector('#confirm-btn').addEventListener('click', () => { confirmed = true; close(); });
    dialog.querySelector('#cancel-btn').addEventListener('click', () => close());
    // Clicking the backdrop dismisses the dialog (targets the <dialog> itself).
    dialog.addEventListener('click', e => { if (e.target === dialog) close(); });
    dialog.addEventListener('close', () => resolve(confirmed), { once: true });
  });
}
