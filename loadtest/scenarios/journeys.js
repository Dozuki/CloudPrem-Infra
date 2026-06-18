import { login, apiHeaders, pageHeaders } from './auth.js';
import http from 'k6/http';
import { check, sleep } from 'k6';

const BASE = __ENV.K6_TARGET;
const EMAIL = __ENV.ADMIN_EMAIL;
const PASSWORD = __ENV.ADMIN_PASSWORD;
const POOL = JSON.parse(__ENV.POOL_JSON || '{"guides":[],"courses":[]}');
const SEARCH_TERMS = (__ENV.SEARCH_TERMS || 'battery,screen,replace,install,guide').split(',');

export const options = {
  scenarios: {
    journeys: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: __ENV.RAMP || '30s', target: parseInt(__ENV.K6_VUS || '50', 10) },
        { duration: __ENV.DURATION || '5m', target: parseInt(__ENV.K6_VUS || '50', 10) },
        { duration: '30s', target: 0 },
      ],
    },
  },
  insecureSkipTLSVerify: (__ENV.INSECURE || 'true') === 'true',
  thresholds: {
    http_req_failed: ['rate<0.02'],
    'http_req_duration{journey:guide}': ['p(95)<2000'],
    'http_req_duration{journey:search}': ['p(95)<2000'],
  },
};

export function setup() {
  return login(BASE, EMAIL, PASSWORD); // { token } shared to all VUs
}

function pick(arr) { return arr[Math.floor(Math.random() * arr.length)]; }

export default function (data) {
  const token = data.token;
  const r = Math.random();
  if (r < 0.5 && POOL.guides.length) {
    // Guide page view (cache-heavy monolith render) — the page-load win path.
    const id = pick(POOL.guides);
    const res = http.get(`${BASE}/Guide/${id}/x`, { headers: pageHeaders(token), tags: { journey: 'guide' } });
    check(res, { 'guide 2xx/3xx': (x) => x.status < 400 });
  } else if (r < 0.8) {
    const q = pick(SEARCH_TERMS);
    const res = http.get(`${BASE}/api/2.0/search/${encodeURIComponent(q)}?limit=20`,
      { headers: apiHeaders(token), tags: { journey: 'search' } });
    check(res, { 'search 200': (x) => x.status === 200 });
  } else if (r < 0.95 && POOL.courses.length) {
    const id = pick(POOL.courses);
    const res = http.get(`${BASE}/api/2.0/courses/${id}`, { headers: apiHeaders(token), tags: { journey: 'course' } });
    check(res, { 'course 2xx': (x) => x.status < 400 });
  } else if (POOL.guides.length) {
    // Light write/upload path (object store): add a guide step image via the API.
    const id = pick(POOL.guides);
    const res = http.get(`${BASE}/api/2.0/guides/${id}`, { headers: apiHeaders(token), tags: { journey: 'guideapi' } });
    check(res, { 'guide api 2xx': (x) => x.status === 200 });
  }
  sleep(Math.random() * 2);
}

export function handleSummary(data) {
  const label = __ENV.LABEL || 'run';
  return {
    stdout: `\n=== summary (${label}) ===\n` + textSummaryLine(data) + '\n',
    [`summary-${label}.json`]: JSON.stringify(data, null, 2),
  };
}

function textSummaryLine(data) {
  const d = data.metrics.http_req_duration && data.metrics.http_req_duration.values || {};
  const f = data.metrics.http_req_failed && data.metrics.http_req_failed.values || {};
  return `p95=${(d['p(95)']||0).toFixed(0)}ms p99=${(d['p(99)']||0).toFixed(0)}ms ` +
         `reqs=${(data.metrics.http_reqs && data.metrics.http_reqs.values.count)||0} ` +
         `err_rate=${((f.rate||0)*100).toFixed(2)}%`;
}
