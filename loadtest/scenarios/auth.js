import http from 'k6/http';
import { check } from 'k6';

// Logs in via the API token endpoint and returns { token, cookieName }.
// Call ONCE (in setup) and share the result to VUs — per-VU login trips the
// login rate limit (~10/IP/30min).
export function login(base, email, password) {
  const res = http.post(`${base}/api/2.0/user/token`, JSON.stringify({ email, password }), {
    headers: { 'Content-Type': 'application/json' },
  });
  check(res, { 'login 2xx': (r) => r.status === 200 || r.status === 201 });
  if (res.status !== 200 && res.status !== 201) {
    throw new Error(`login failed: ${res.status} ${res.body}`);
  }
  // The session id is the token. Prefer an explicit body field; fall back to the
  // PHPSESSID-style session cookie set on the response.
  let token = '';
  try {
    const b = res.json();
    token = b.authToken || b.token || (b.user && b.user.authToken) || '';
  } catch (e) { /* non-JSON; fall back to cookie */ }
  if (!token) {
    for (const name of Object.keys(res.cookies || {})) {
      if (/sess/i.test(name) && res.cookies[name].length) { token = res.cookies[name][0].value; break; }
    }
  }
  if (!token) throw new Error(`could not extract session token from login response: ${res.body}`);
  return { token };
}

// Headers for authenticated API calls.
export function apiHeaders(token) {
  return { 'Authorization': `api ${token}`, 'Content-Type': 'application/json' };
}
// Headers for authenticated browser GET pages (session cookie; GETs are CSRF-exempt).
export function pageHeaders(token) {
  return { 'Cookie': `PHPSESSID=${token}` };
}
