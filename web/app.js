/**
 * @module app
 * @description Core application module. Manages in-memory auth state, provides
 * the central API fetch helper, and owns the hash-based client-side router.
 *
 * Auth tokens are kept exclusively in module-level variables — never written to
 * localStorage — so they cannot be exfiltrated by XSS. The HttpOnly
 * `quorum_refresh` cookie is managed by the browser and sent automatically on
 * every fetch with `credentials: 'same-origin'`.
 */

// ─── Auth state (module-level, never touches the DOM) ───────────────────────
let _token          = null;
let _user           = null;
let _tokenExpiresAt = 0; // Unix ms; 0 means unknown/expired

/** Returns the current JWT access token, or null if not authenticated. */
export function getToken()          { return _token; }

/** Returns the current user object (id, email, role), or null. */
export function getUser()           { return _user; }

/** Returns true when a valid access token is held in memory. */
export function isAuthenticated()   { return !!_token; }

/** Returns the authenticated user's role string, defaulting to 'member'. */
export function userRole()          { return _user?.role ?? 'member'; }

/** Returns true when the current user has officer-level access or above. */
export function canWrite()          { return ['officer','admin'].includes(userRole()); }

/** Returns true when the current user has admin-level access. */
export function isAdmin()           { return userRole() === 'admin'; }

/**
 * Stores the access token and user profile in memory.
 * @param {string} token      - JWT access token returned by /auth/login or /auth/refresh.
 * @param {object} user       - User profile object {id, email, role}.
 * @param {string} [expiresAt] - ISO-8601 expiry from the login response. Optional.
 */
export function setAuth(token, user, expiresAt) {
  _token          = token;
  _user           = user;
  _tokenExpiresAt = expiresAt ? new Date(expiresAt).getTime() : 0;
}

/** Clears all in-memory auth state. The HttpOnly refresh cookie is cleared server-side via /auth/logout. */
export function clearAuth() {
  _token          = null;
  _user           = null;
  _tokenExpiresAt = 0;
}

// ─── API helper ──────────────────────────────────────────────────────────────

/**
 * Authenticated fetch wrapper for all Quorum API calls.
 *
 * - Attaches the in-memory JWT as `Authorization: Bearer <token>`.
 * - Aborts requests that take longer than 30 seconds.
 * - On 401, attempts a silent token refresh via the HttpOnly cookie, then
 *   retries once. If the refresh also fails, clears auth and redirects to login.
 * - Returns the parsed JSON body, or null for 204 No Content responses.
 * - Throws the parsed error JSON on non-2xx responses.
 *
 * @param {'GET'|'POST'|'PATCH'|'PUT'|'DELETE'} method - HTTP verb.
 * @param {string} path - API path, e.g. `/members?limit=50`. Prefixed with `/api/v1`.
 * @param {object} [body] - Request body, serialised to JSON. Omit for GET/DELETE.
 * @returns {Promise<object|null>} Parsed response body.
 * @throws {object} Parsed error response `{error, code}` on failure.
 */
export async function api(method, path, body) {
  // Pre-emptively refresh if the token is known to be expired (saves one round-trip).
  if (_token && _tokenExpiresAt > 0 && Date.now() >= _tokenExpiresAt && path !== '/auth/login') {
    const ok = await silentRefresh();
    if (!ok) { clearAuth(); navigate('#/login'); throw { error: 'Session expired', code: 'unauthorized' }; }
  }

  const headers = { 'Content-Type': 'application/json' };
  if (_token) headers['Authorization'] = `Bearer ${_token}`;

  const ac = new AbortController();
  const timer = setTimeout(() => ac.abort(), 30_000);
  let res;
  try {
    res = await fetch('/api/v1' + path, {
      method,
      headers,
      body: body != null ? JSON.stringify(body) : undefined,
      signal: ac.signal,
    });
  } finally {
    clearTimeout(timer);
  }

  if (res.status === 401 && path !== '/auth/login') {
    // Try silent token refresh before giving up.
    const refreshed = await silentRefresh();
    if (refreshed) {
      headers['Authorization'] = `Bearer ${_token}`;
      const ac2 = new AbortController();
      const t2 = setTimeout(() => ac2.abort(), 30_000);
      let retry;
      try {
        retry = await fetch('/api/v1' + path, { method, headers, body: body != null ? JSON.stringify(body) : undefined, signal: ac2.signal });
      } finally {
        clearTimeout(t2);
      }
      if (!retry.ok) throw await retry.json();
      return retry.status === 204 ? null : retry.json();
    }
    clearAuth();
    navigate('#/login');
    throw { error: 'Session expired', code: 'unauthorized' };
  }

  if (!res.ok) throw await res.json().catch(() => ({ error: res.statusText }));
  return res.status === 204 ? null : res.json();
}

async function silentRefresh() {
  let res;
  try {
    res = await fetch('/api/v1/auth/refresh', {
      method: 'POST',
      credentials: 'same-origin',
    });
  } catch {
    // Network error — do not clear auth; the token may still be valid when connectivity returns.
    return false;
  }
  if (res.status === 401) return false; // Refresh token expired or revoked.
  if (!res.ok) return false;            // Unexpected server error; stay logged in optimistically.
  const data = await res.json();
  _token          = data.access_token;
  _user           = data.user;
  _tokenExpiresAt = 0; // Refresh response doesn't include expires_at; reset to unknown.
  return true;
}

// ─── Router ──────────────────────────────────────────────────────────────────

/**
 * Navigates to a hash route, e.g. `navigate('#/members')`.
 * Triggers a `hashchange` event which the app-shell component listens to.
 * @param {string} hash - Full hash string including the `#` prefix.
 */
export function navigate(hash) {
  location.hash = hash;
}

const routes = {
  '#/login':     '<login-page>',
  '#/dashboard': '<page-dashboard>',
  '#/members':   '<page-members>',
  '#/dues':      '<page-dues>',
  '#/meetings':  '<page-meetings>',
  '#/plans':     '<page-plans>',
  '#/contacts':  '<page-contacts>',
  '#/resources': '<page-resources>',
  '#/settings':  '<page-settings>',
  '#/404':       '<page-not-found>',
};

function resolveRoute() {
  const hash = location.hash || '#/dashboard';
  if (hash in routes) return routes[hash];
  navigate('#/404');
  return routes['#/404'];
}

window.addEventListener('hashchange', () => {
  document.dispatchEvent(new CustomEvent('route-changed', { detail: location.hash }));
});

// ─── Component imports (side-effects only) ───────────────────────────────────
import './components/login-page.js';
import './components/app-shell.js';
import './components/nav-bar.js';
import './components/page-dashboard.js';
import './components/page-members.js';
import './components/page-dues.js';
import './components/page-meetings.js';
import './components/page-plans.js';
import './components/page-contacts.js';
import './components/page-resources.js';
import './components/page-settings.js';
import './components/payment-status-badge.js';
import './components/confirm-dialog.js';
import './components/toast-notification.js';
import './components/page-not-found.js';

// ─── Boot ────────────────────────────────────────────────────────────────────
async function boot() {
  await silentRefresh();

  if (!isAuthenticated() && location.hash !== '#/login') {
    location.hash = '#/login';
  } else if (isAuthenticated() && (!location.hash || location.hash === '#/login')) {
    location.hash = '#/dashboard';
  }
}

export { resolveRoute };

boot();
