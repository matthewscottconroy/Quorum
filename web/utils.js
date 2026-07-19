/**
 * @module utils
 * @description Shared front-end helpers: HTML escaping, date formatting, and a
 * modal helper built on the native `<dialog>` element. Import from components
 * with `import { esc, fmtDate, openModal } from '../utils.js'`.
 */

/**
 * Escapes a value for safe interpolation into an HTML template string.
 * Null and undefined become the empty string.
 * @param {*} s - Value to escape.
 * @returns {string} HTML-safe string.
 */
export function esc(s) {
  return String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

/**
 * Formats an ISO date string as a locale date, or an em dash when empty.
 * @param {string} [iso] - ISO-8601 date string.
 * @param {Intl.DateTimeFormatOptions} [opts] - Optional format options.
 * @returns {string} Formatted date or '—'.
 */
export function fmtDate(iso, opts) {
  return iso ? new Date(iso).toLocaleDateString(undefined, opts) : '—';
}

/**
 * Formats an ISO date-time string as a short locale date-time, or an em dash.
 * @param {string} [iso] - ISO-8601 date-time string.
 * @returns {string} Formatted date-time or '—'.
 */
export function fmtDateTime(iso) {
  return iso
    ? new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit' })
    : '—';
}

/**
 * Opens a modal dialog built on the native `<dialog>` element via
 * `showModal()`, which provides focus trapping and Escape-to-close for free.
 * The dialog carries the existing `.modal` styling; the overlay is styled via
 * `dialog.modal::backdrop` in base.css.
 *
 * The header (title + close button) is rendered automatically; `body` supplies
 * the `.modal-body` / `.modal-footer` markup. The dialog removes itself from
 * the DOM whenever it closes (close button, Escape, or the returned `close`).
 *
 * @param {object} opts
 * @param {string} opts.title - Plain-text title shown in the modal header (escaped internally).
 * @param {string} opts.body - HTML for the modal body and footer.
 * @param {string} [opts.maxWidth] - Optional CSS max-width override, e.g. '680px'.
 * @param {function({dialog: HTMLDialogElement, close: function}): void} [opts.onMount]
 *        - Called after the dialog is shown; convenient for wiring listeners.
 * @returns {{dialog: HTMLDialogElement, close: function}} The dialog element and a function that closes it.
 */
export function openModal({ title, body, maxWidth, onMount } = {}) {
  const dialog = document.createElement('dialog');
  dialog.className = 'modal';
  if (maxWidth) dialog.style.maxWidth = maxWidth;
  dialog.innerHTML = `
    <div class="modal-header">
      <h2>${esc(title ?? '')}</h2>
      <button type="button" class="btn-ghost modal-close" aria-label="Close">✕</button>
    </div>
    ${body ?? ''}
  `;
  document.body.appendChild(dialog);

  const close = () => dialog.close();
  // A showModal() dialog makes the rest of the page inert, so a modal left open
  // across a navigation or auth change would freeze the freshly rendered view
  // (worst case: a login screen stuck behind a stale edit modal). Close it on
  // either event and remove these listeners in the same cleanup so nothing leaks.
  const forceClose = () => dialog.close();
  document.addEventListener('route-changed', forceClose);
  document.addEventListener('auth-changed', forceClose);
  dialog.addEventListener('close', () => {
    document.removeEventListener('route-changed', forceClose);
    document.removeEventListener('auth-changed', forceClose);
    dialog.remove();
  });
  dialog.querySelector('.modal-close').addEventListener('click', close);
  dialog.showModal();
  onMount?.({ dialog, close });
  return { dialog, close };
}

/**
 * Converts a UTC ISO date-time string into the `value` expected by an
 * `<input type="datetime-local">`, i.e. the LOCAL wall-clock time truncated to
 * minutes. Pair with `new Date(input.value).toISOString()` on save (which parses
 * the local string back to UTC) so an unedited open→save round-trips unchanged.
 * @param {string} [iso] - UTC ISO-8601 date-time string.
 * @returns {string} `YYYY-MM-DDTHH:mm` in local time, or '' when empty/invalid.
 */
export function toLocalInputValue(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

// Currency codes whose minor unit is the same as the major unit (0 decimals).
const ZERO_DECIMAL_CURRENCIES = new Set([
  'BIF', 'CLP', 'DJF', 'GNF', 'JPY', 'KMF', 'KRW', 'MGA', 'PYG',
  'RWF', 'UGX', 'VND', 'VUV', 'XAF', 'XOF', 'XPF',
]);
// Currency codes with a three-decimal minor unit.
const THREE_DECIMAL_CURRENCIES = new Set([
  'BHD', 'IQD', 'JOD', 'KWD', 'LYD', 'OMR', 'TND',
]);

/**
 * Returns the number of decimal places (the exponent) for a currency's minor
 * unit, mirroring the backend: 0 for the zero-decimal set, 3 for the
 * three-decimal set, otherwise 2. Input is upper-cased and trimmed.
 * @param {string} code - ISO 4217 currency code.
 * @returns {number} 0, 2, or 3.
 */
export function moneyExponent(code) {
  const c = String(code ?? '').trim().toUpperCase();
  if (ZERO_DECIMAL_CURRENCIES.has(c)) return 0;
  if (THREE_DECIMAL_CURRENCIES.has(c)) return 3;
  return 2;
}

/**
 * Parses a major-unit decimal string into integer minor units WITHOUT using
 * floating point, mirroring the backend Go logic. Rejects a fraction longer than
 * the currency's exponent and any non-numeric input.
 *
 * E.g. parseMoney('100.00', 'USD') === 10000, parseMoney('100', 'USD') === 10000,
 * parseMoney('1000', 'JPY') === 1000, parseMoney('1.5', 'JPY') === null.
 *
 * @param {string} str - Major-unit decimal amount, e.g. '100.00'.
 * @param {string} code - ISO 4217 currency code (selects the exponent).
 * @returns {number|null} Integer minor units, or null on invalid input.
 */
export function parseMoney(str, code) {
  const exp = moneyExponent(code);
  let s = String(str ?? '').trim();
  if (s === '') return null;

  // Strip a single leading sign.
  let neg = false;
  if (s[0] === '+' || s[0] === '-') {
    neg = s[0] === '-';
    s = s.slice(1);
  }
  if (s === '') return null;

  const parts = s.split('.');
  if (parts.length > 2) return null;
  let [intPart, fracPart = ''] = parts;

  // Require an integer part with at least one digit; an empty int part (e.g.
  // '.50') is rejected to match the strict backend parser.
  if (intPart === '') return null;
  if (!/^\d+$/.test(intPart)) return null;
  if (fracPart !== '' && !/^\d+$/.test(fracPart)) return null;

  // A fraction longer than the exponent means more precision than the currency
  // supports (e.g. '1.5' for JPY, or '1.005' for USD).
  if (fracPart.length > exp) return null;

  // Right-pad the fraction to exactly `exp` digits, then concatenate.
  const padded = fracPart.padEnd(exp, '0');
  const digits = intPart + padded;
  const minor = parseInt(digits, 10);
  if (!Number.isFinite(minor)) return null;
  return neg ? -minor : minor;
}

/**
 * Formats integer minor units as a display string for a currency. Prefers
 * `Intl.NumberFormat` currency style; falls back to a plain `<amount> <code>`
 * string when the currency code is not a valid ISO 4217 code (RangeError).
 * Replaces the old `fmt$`.
 * @param {number} minor - Amount in integer minor units.
 * @param {string} code - ISO 4217 currency code.
 * @returns {string} Formatted currency string.
 */
export function formatMoney(minor, code) {
  const c = String(code ?? '').trim().toUpperCase();
  const exp = moneyExponent(c);
  const major = (Number(minor) || 0) / 10 ** exp;
  try {
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: c,
      minimumFractionDigits: exp,
      maximumFractionDigits: exp,
    }).format(major);
  } catch {
    // Intl throws RangeError on a non-ISO currency code (free-text input).
    return `${major.toFixed(exp)} ${c}`.trim();
  }
}

/**
 * Wraps an async click handler so its button is disabled for the duration of the
 * in-flight work, preventing double-click duplicate submissions. Re-enables in a
 * `finally`, which is harmless if the handler removed the element (e.g. closed a
 * modal) on success.
 * @param {HTMLButtonElement} btn - The button to guard.
 * @param {function(...*): Promise<*>} fn - The async handler to run.
 * @returns {function(...*): Promise<*>} A guarded handler.
 */
export function guardButton(btn, fn) {
  return async (...a) => {
    if (btn.disabled) return;
    btn.disabled = true;
    try { return await fn(...a); }
    finally { btn.disabled = false; }
  };
}

/**
 * Opens a type-to-confirm deletion modal for a permanently-destructive action.
 * The Delete button stays disabled until the typed value trims-and-case-folds
 * equal to `name`; on click it invokes `onConfirm(typedValue)` (guarded against
 * double-submit). If `onConfirm` resolves the modal closes; if it throws the
 * modal stays open so the caller can surface an error and let the user retry.
 *
 * @param {object} opts
 * @param {string} opts.noun - Human label for the entity, e.g. 'meeting', 'user'.
 * @param {string} opts.name - The exact text the user must type to confirm (shown, escaped).
 * @param {function(string): Promise<*>} opts.onConfirm - Called with the typed (trimmed) value.
 * @returns {{dialog: HTMLDialogElement, close: function}} The dialog and its close function.
 */
export function confirmDelete({ noun = 'record', name = '', onConfirm } = {}) {
  const safeName = esc(name);
  const { dialog, close } = openModal({
    title: `Delete ${noun}`,
    maxWidth: '440px',
    body: `
      <div class="modal-body">
        <p style="margin-bottom:.75rem">This will <strong>permanently delete</strong> this ${esc(noun)}. This action <strong>cannot be undone</strong>, and affected people will be notified.</p>
        <p style="margin-bottom:.75rem">To confirm, type <strong>${safeName}</strong> below.</p>
        <div class="form-group">
          <label for="confirm-del-inp">Type to confirm</label>
          <input id="confirm-del-inp" autocomplete="off" placeholder="${safeName}">
        </div>
      </div>
      <div class="modal-footer">
        <button class="btn-secondary" id="confirm-del-cancel">Cancel</button>
        <button class="btn-danger" id="confirm-del-go" disabled>Delete permanently</button>
      </div>
    `,
  });

  const target = String(name).trim().toLowerCase();
  const input  = dialog.querySelector('#confirm-del-inp');
  const delBtn = dialog.querySelector('#confirm-del-go');
  const matches = () => input.value.trim().toLowerCase() === target;

  input.addEventListener('input', () => { delBtn.disabled = !matches(); });
  dialog.querySelector('#confirm-del-cancel').addEventListener('click', close);
  delBtn.addEventListener('click', guardButton(delBtn, async () => {
    if (!matches()) return;
    // On success close the modal; a thrown error leaves it open for retry.
    await onConfirm?.(input.value.trim());
    close();
  }));

  return { dialog, close };
}
