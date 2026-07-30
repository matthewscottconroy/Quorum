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

/**
 * Role ladder rank map. Higher rank == more privilege.
 * restricted < member < officer < admin < superadmin.
 */
const ROLE_RANK = { restricted: 1, member: 2, officer: 3, admin: 4, superadmin: 5 };

/** Returns the current user's role string (alias of userRole, defaults to 'member'). */
export function currentRole()       { return _user?.role ?? 'member'; }

/** Returns the current user's linked member id, or null when unlinked. */
export function currentMemberId()   { return _user?.member_id ?? null; }

/**
 * Returns true when the current role ranks at or above `min` on the ladder.
 * @param {'restricted'|'member'|'officer'|'admin'|'superadmin'} min - Minimum role required.
 */
export function hasRole(min)        { return (ROLE_RANK[currentRole()] ?? 0) >= (ROLE_RANK[min] ?? Infinity); }

/** Returns true when the current user is restricted (self-service only). */
export function isRestricted()      { return currentRole() === 'restricted'; }

/** Returns true when the current user is a superadmin. */
export function isSuperadmin()      { return currentRole() === 'superadmin'; }

/** Returns true when the current user has officer-level access or above. */
export function canWrite()          { return hasRole('officer'); }

/** Returns true when the current user has admin-level access or above. */
export function isAdmin()           { return hasRole('admin'); }

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
      if (!retry.ok) throw await retry.json().catch(() => ({ error: retry.statusText }));
      return retry.status === 204 ? null : retry.json();
    }
    clearAuth();
    navigate('#/login');
    throw { error: 'Session expired', code: 'unauthorized' };
  }

  if (!res.ok) throw await res.json().catch(() => ({ error: res.statusText }));
  return res.status === 204 ? null : res.json();
}

/**
 * Downloads an authenticated file (CSV/JSON export) to the user's device.
 * Attaches the in-memory JWT, retries once after a silent refresh on 401, then
 * streams the response body into a temporary object-URL download.
 *
 * @param {string} path - API path, e.g. `/export/members.csv`. Prefixed with `/api/v1`.
 * @param {string} filename - Suggested download filename.
 * @throws {object} `{error, code}` when the request fails.
 */
export async function apiDownload(path, filename) {
  const doFetch = () => fetch('/api/v1' + path, {
    headers: _token ? { Authorization: `Bearer ${_token}` } : {},
    credentials: 'same-origin',
  });
  let res = await doFetch();
  if (res.status === 401) {
    if (await silentRefresh()) {
      res = await doFetch();
    } else {
      clearAuth(); navigate('#/login');
      throw { error: 'Session expired', code: 'unauthorized' };
    }
  }
  if (!res.ok) throw await res.json().catch(() => ({ error: res.statusText }));
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// Single-flight guard: concurrent 401s share one in-flight /auth/refresh call.
let _refreshPromise = null;

function silentRefresh() {
  if (!_refreshPromise) {
    _refreshPromise = doSilentRefresh().finally(() => { _refreshPromise = null; });
  }
  return _refreshPromise;
}

async function doSilentRefresh() {
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
  const data = await res.json().catch(() => null);
  if (!data || !data.access_token) return false; // Malformed/empty 200 body.
  _token          = data.access_token;
  _user           = data.user;
  _tokenExpiresAt = data.expires_at ? new Date(data.expires_at).getTime() : 0;
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
  '#/login':           '<login-page>',
  '#/forgot-password': '<forgot-password-page>',
  '#/reset-password':  '<reset-password-page>',
  '#/ballot':          '<ballot-page>',
  '#/dashboard':       '<page-dashboard>',
  '#/members':         '<page-members>',
  '#/dues':            '<page-dues>',
  '#/budget':          '<page-budget>',
  '#/analytics':       '<page-analytics>',
  '#/currencies':      '<page-fx>',
  '#/notifications':   '<page-notifications>',
  '#/audit':           '<page-audit>',
  '#/meetings':        '<page-meetings>',
  '#/calendar':        '<page-calendar>',
  '#/plans':           '<page-plans>',
  '#/contacts':        '<page-contacts>',
  '#/resources':       '<page-resources>',
  '#/settings':        '<page-settings>',
  '#/my-account':      '<page-my-account>',
  '#/404':             '<page-not-found>',
};

/**
 * Public routes reachable without authentication. `#/reset-password` carries a
 * `?token=…` query, so the router matches on the path portion only.
 */
export const PUBLIC_ROUTES = new Set(['#/login', '#/forgot-password', '#/reset-password', '#/ballot']);

/** Returns the hash with any `?query` stripped, e.g. `#/reset-password?token=x` → `#/reset-password`. */
export function routePath(hash) {
  return (hash || '').split('?')[0];
}

function resolveRoute() {
  const path = routePath(location.hash) || '#/dashboard';
  // Restricted users may only view their own account. Any other (org-wide) route
  // is redirected to #/my-account so the UI never renders a view the backend
  // would 403 anyway. Public (logged-out) routes are left alone.
  if (isRestricted() && path !== '#/my-account' && !PUBLIC_ROUTES.has(path)) {
    if (routePath(location.hash) !== '#/my-account') location.hash = '#/my-account';
    return routes['#/my-account'];
  }
  return routes[path] ?? routes['#/404'];
}

window.addEventListener('hashchange', () => {
  document.dispatchEvent(new CustomEvent('route-changed', { detail: location.hash }));
});

// ─── Component imports (side-effects only) ───────────────────────────────────
import './components/login-page.js';
import './components/forgot-password-page.js';
import './components/reset-password-page.js';
import './components/ballot-page.js';
import './components/app-shell.js';
import './components/nav-bar.js';
import './components/page-dashboard.js';
import './components/page-members.js';
import './components/page-dues.js';
import './components/page-budget.js';
import './components/page-analytics.js';
import './components/page-fx.js';
import './components/page-notifications.js';
import './components/page-audit.js';
import './components/page-meetings.js';
import './components/page-calendar.js';
import './components/page-plans.js';
import './components/page-contacts.js';
import './components/page-resources.js';
import './components/page-settings.js';
import './components/page-my-account.js';
import './components/payment-status-badge.js';
import './components/vote-tally.js';
import './components/confirm-dialog.js';
import './components/toast-notification.js';
import './components/page-not-found.js';

// ─── Boot ────────────────────────────────────────────────────────────────────
async function boot() {
  // Mount the shell now, from module code that runs AFTER all of app.js's
  // top-level state (auth vars, PUBLIC_ROUTES, the route table) is initialized.
  // A static <app-shell> in index.html would instead be upgraded during this
  // module's import phase — its connectedCallback would call isAuthenticated()
  // while `_token`/`PUBLIC_ROUTES` are still in the temporal dead zone, throwing
  // "Cannot access '_token' before initialization".
  if (!document.querySelector('app-shell')) {
    document.body.appendChild(document.createElement('app-shell'));
  }

  const refreshed = await silentRefresh();

  const prevHash = location.hash;
  if (!isAuthenticated() && !PUBLIC_ROUTES.has(routePath(location.hash))) {
    location.hash = '#/login';
  } else if (isAuthenticated() && (!location.hash || location.hash === '#/login')) {
    location.hash = isRestricted() ? '#/my-account' : '#/dashboard';
  }

  // Re-notify the app-shell after a successful session restore. When boot()
  // changed the hash a `hashchange` → `route-changed` will render the shell, so
  // dispatching auth-changed too would cause a second full render / duplicate
  // fetch. Only dispatch in the deep-link case (hash unchanged, no hashchange
  // fires) where otherwise the shell would keep showing the login view.
  if (refreshed && location.hash === prevHash) {
    document.dispatchEvent(new CustomEvent('auth-changed'));
  }
}

export { resolveRoute };

boot();
