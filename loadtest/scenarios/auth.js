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
  // API auth and browser auth are DIFFERENT credentials here and both are needed:
  // the body's authToken authenticates /api/2.0 calls, while page GETs need the
  // session cookie the same response sets. They are distinct values, so sending
  // the API token as a cookie silently 302s every page to /Login - the load test
  // then measures redirects instead of rendered pages (which is what it did until
  // this was fixed; the guide journey never touched a guide).
  let token = '';
  try {
    const b = res.json();
    token = b.authToken || b.token || (b.user && b.user.authToken) || '';
  } catch (e) { /* non-JSON; the cookie below is the fallback */ }

  // The cookie is named per site (session_<siteid>, e.g. session_2), not PHPSESSID,
  // so match on the pattern rather than hardcoding a name. Several are set,
  // including deleted ones carrying the literal value "deleted"; take the last
  // real value.
  let cookieName = '', cookieValue = '';
  for (const name of Object.keys(res.cookies || {})) {
    if (!/^session/i.test(name)) continue;
    for (const c of res.cookies[name]) {
      if (c.value && c.value !== 'deleted') { cookieName = name; cookieValue = c.value; }
    }
  }
  if (!token) token = cookieValue;
  if (!token) throw new Error(`could not extract session token from login response: ${res.body}`);
  if (!cookieValue) throw new Error(`login set no usable session cookie: ${JSON.stringify(Object.keys(res.cookies || {}))}`);
  return { token, cookieName, cookieValue };
}

// Headers for authenticated API calls.
export function apiHeaders(token) {
  return { 'Authorization': `api ${token}`, 'Content-Type': 'application/json' };
}
// Headers for authenticated browser GET pages (session cookie; GETs are CSRF-exempt).
// Takes the whole login() result, not a token: the cookie name is site-specific.
export function pageHeaders(auth) {
  return { 'Cookie': `${auth.cookieName}=${auth.cookieValue}` };
}
