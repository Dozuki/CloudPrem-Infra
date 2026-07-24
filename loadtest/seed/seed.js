import { login, apiHeaders } from '../scenarios/auth.js';
import http from 'k6/http';
import { check } from 'k6';

const BASE = __ENV.K6_TARGET;
const EMAIL = __ENV.ADMIN_EMAIL;
const PASSWORD = __ENV.ADMIN_PASSWORD;
const N_GUIDES = parseInt(__ENV.SEED_GUIDES || '300', 10);
const N_COURSES = parseInt(__ENV.SEED_COURSES || '30', 10);
const N_USERS = parseInt(__ENV.SEED_USERS || '50', 10);
const CATEGORY = __ENV.SEED_CATEGORY || 'LoadTest';

export const options = { vus: 1, iterations: 1, insecureSkipTLSVerify: true };

const pool = { guides: [], courses: [], users: [], category: CATEGORY };

export default function () {
  const { token } = login(BASE, EMAIL, PASSWORD);
  const h = apiHeaders(token);

  // Ensure the category exists. Categories are CATEGORY-namespace wiki documents;
  // /api/2.0/categories has no POST endpoint (404s) and guide creation 422s on a
  // missing category, so this must succeed first. Idempotent: re-creating an
  // existing wiki title is a no-op error we ignore.
  http.post(`${BASE}/api/2.0/wikis`,
    JSON.stringify({ namespace: 'CATEGORY', title: CATEGORY, contents: 'Synthetic content for load testing.' }),
    { headers: h });

  for (let i = 0; i < N_GUIDES; i++) {
    const res = http.post(`${BASE}/api/2.0/guides`,
      JSON.stringify({ category: CATEGORY, type: 'how-to', title: `LoadTest Guide ${i}` }),
      { headers: h });
    if (res.status === 200 || res.status === 201) {
      const id = res.json('guideid'); if (id) pool.guides.push(id);
    }
  }
  for (let i = 0; i < N_USERS; i++) {
    const res = http.post(`${BASE}/api/2.0/users`,
      JSON.stringify({ username: `LoadTest User ${i}`, unique_username: `loadtest-${i}-${Date.now()}`,
        password: 'LoadTest!2026', email: `loadtest-${i}-${Date.now()}@example.com` }),
      { headers: h });
    if (res.status === 200 || res.status === 201) {
      const id = res.json('userid'); if (id) pool.users.push(id);
    }
  }
  for (let i = 0; i < N_COURSES; i++) {
    const stageGuide = pool.guides[i % Math.max(pool.guides.length, 1)];
    const res = http.post(`${BASE}/api/2.0/courses`,
      JSON.stringify({ title: `LoadTest Course ${i}`, contents: 'Load test course.',
        stages: stageGuide ? [{ docid: stageGuide, doctype: 'GUIDE' }] : [] }),
      { headers: h });
    if (res.status === 200 || res.status === 201) {
      const id = res.json('courseid') || res.json('id'); if (id) pool.courses.push(id);
    }
  }
  check(pool, { 'seeded some guides': (p) => p.guides.length > 0 });
  console.log(`seeded guides=${pool.guides.length} courses=${pool.courses.length} users=${pool.users.length}`);
}

export function handleSummary() {
  return { 'pool.json': JSON.stringify(pool, null, 2) };
}
