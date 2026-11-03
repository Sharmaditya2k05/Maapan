// k6 load test for the gateway (dwaar) and billing (aadesh).
//
//   k6 run -e BASE_URL=http://localhost:8000 -e PHONE=9876543210 -e OTP=123456 sanchalan/load/maapan.js
//   k6 run -e BASE_URL=https://api.dev.maapan.in -e TOKEN=<access jwt> -e PROFILE=load sanchalan/load/maapan.js
//
// Scenarios (all through dwaar, so auth, rate limits and proxying are part of what is measured):
//   gateway  — reads: GET /api/pehchaan/v1/me, GET /api/aadesh/v1/invoices?limit=20, GET /api/bahi/v1/reports/summary
//   billing  — POS writes: POST /api/aadesh/v1/invoices/preview, then POST /api/aadesh/v1/invoices with an
//              client_uuid + Idempotency-Key, and every 10th bill re-sent with the same client_uuid to
//              check that aadesh deduplicates a POS retry (200 with the same invoice id)
//
// Every bill is a real sale in the target tenant (ledger vouchers, stock movements, a console WhatsApp).
// Point it at a dev / staging tenant made for load tests, never at a store's books. The script refuses
// to run against api.maapan.in unless ALLOW_PROD=1.
//
// Env:
//   BASE_URL   gateway origin (default http://localhost:8000)
//   TOKEN      access token(s); comma-separate several users to stay under dwaar's per-user limit
//              (RATE_LIMIT_USER_PER_MIN, default 600/min per user)
//   PHONE/OTP  log in once in setup() instead (local / demo: OTP 123456). Needs an onboarded tenant.
//   STORE_ID   X-Store-ID header (default: the token's store)
//   ITEM_ID    optional bhandar item for the bill line (default: a free-text line, no stock movement)
//   STATE_CODE the store's state code for the preview call (default 27, Maharashtra)
//   PROFILE    smoke (default) | load | stress | soak
import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const BASE = (__ENV.BASE_URL || 'http://localhost:8000').replace(/\/$/, '');
const PROFILE = __ENV.PROFILE || 'smoke';

if (/(^|\/\/)api\.maapan\.in/.test(BASE) && __ENV.ALLOW_PROD !== '1') {
  throw new Error('refusing to load-test production (set ALLOW_PROD=1 if you really mean it)');
}

const profiles = {
  smoke: { gateway: [{ duration: '30s', target: 2 }], billing: [{ duration: '30s', target: 1 }] },
  load: {
    gateway: [{ duration: '1m', target: 20 }, { duration: '5m', target: 20 }, { duration: '1m', target: 0 }],
    billing: [{ duration: '1m', target: 10 }, { duration: '5m', target: 10 }, { duration: '1m', target: 0 }],
  },
  stress: {
    gateway: [{ duration: '2m', target: 50 }, { duration: '3m', target: 100 }, { duration: '2m', target: 0 }],
    billing: [{ duration: '2m', target: 20 }, { duration: '3m', target: 50 }, { duration: '2m', target: 0 }],
  },
  soak: { gateway: [{ duration: '30m', target: 10 }], billing: [{ duration: '30m', target: 5 }] },
};
const stages = profiles[PROFILE];
if (!stages) throw new Error(`unknown PROFILE ${PROFILE}`);

export const options = {
  scenarios: {
    gateway: { executor: 'ramping-vus', startVUs: 0, stages: stages.gateway, exec: 'gateway', tags: { flow: 'gateway' } },
    billing: { executor: 'ramping-vus', startVUs: 0, stages: stages.billing, exec: 'billing', tags: { flow: 'billing' } },
  },
  thresholds: {
    // Errors other than deliberate 429s.
    'http_req_failed{expected:yes}': ['rate<0.01'],
    'http_req_duration{name:me}': ['p(95)<300'],
    'http_req_duration{name:invoices_list}': ['p(95)<500'],
    'http_req_duration{name:invoice_preview}': ['p(95)<500'],
    'http_req_duration{name:invoice_create}': ['p(95)<1200', 'p(99)<2500'],
    checks: ['rate>0.99'],
    rate_limited: ['count<50'],
  },
  summaryTrendStats: ['avg', 'p(50)', 'p(95)', 'p(99)', 'max'],
};

const rateLimited = new Counter('rate_limited');
const billsCreated = new Counter('bills_created');
const duplicateMismatch = new Counter('idempotency_mismatch');
const billTotal = new Trend('bill_round_trip_ms', true);

export function setup() {
  let tokens = (__ENV.TOKEN || '').split(',').map((t) => t.trim()).filter(Boolean);
  if (tokens.length === 0) {
    if (!__ENV.PHONE) throw new Error('set TOKEN or PHONE (+ OTP)');
    const req = http.post(`${BASE}/api/pehchaan/v1/auth/otp/request`, JSON.stringify({ phone: __ENV.PHONE }),
      { headers: { 'Content-Type': 'application/json' } });
    check(req, { 'otp requested': (r) => r.status === 202 });
    const res = http.post(`${BASE}/api/pehchaan/v1/auth/otp/verify`,
      JSON.stringify({ phone: __ENV.PHONE, otp: __ENV.OTP || '123456' }), { headers: { 'Content-Type': 'application/json' } });
    if (res.status !== 200) throw new Error(`login failed: ${res.status} ${res.body}`);
    const s = res.json();
    if (!s.tenant_id) throw new Error('this phone has no tenant yet: onboard it in dukaan first');
    tokens = [s.access_token];
  }
  // Warm up (wakes sleeping services on the Render demo; harmless elsewhere).
  http.get(`${BASE}/wake`, { timeout: '120s', tags: { name: 'wake' } });
  return { tokens };
}

function headers(data, extra) {
  const tok = data.tokens[(__VU - 1) % data.tokens.length];
  const h = { Authorization: `Bearer ${tok}`, 'Content-Type': 'application/json' };
  if (__ENV.STORE_ID) h['X-Store-ID'] = __ENV.STORE_ID;
  return Object.assign(h, extra || {});
}

function tagged(name) {
  return { tags: { name, expected: 'yes' }, responseCallback: http.expectedStatuses({ min: 200, max: 299 }, 429) };
}

function note429(res) {
  if (res.status === 429) rateLimited.add(1);
}

export function gateway(data) {
  group('reads', () => {
    let r = http.get(`${BASE}/api/pehchaan/v1/me`, Object.assign({ headers: headers(data) }, tagged('me')));
    note429(r);
    check(r, { 'me 200': (x) => x.status === 200 || x.status === 429 });

    r = http.get(`${BASE}/api/aadesh/v1/invoices?limit=20`, Object.assign({ headers: headers(data) }, tagged('invoices_list')));
    note429(r);
    check(r, { 'invoices 200': (x) => x.status === 200 || x.status === 429 });

    r = http.get(`${BASE}/api/bahi/v1/reports/summary`, Object.assign({ headers: headers(data) }, tagged('ledger_summary')));
    note429(r);
    check(r, { 'summary 200/403': (x) => x.status === 200 || x.status === 403 || x.status === 429 }); // cashiers lack report:read
  });
  sleep(1 + Math.random());
}

const STATE = __ENV.STATE_CODE || '27';
const tenders = [{ mode: 'cash', amount_paise: 100000 }]; // ₹1,000 cash → change returned

function taxLine() {
  return { hsn: '1905', qty: 1 + Math.floor(Math.random() * 3), unit_price_paise: 10000, tax_rate_bp: 500, price_includes_tax: true };
}

export function billing(data) {
  const tax = taxLine();
  const key = `k6-${PROFILE}-${__VU}-${__ITER}-${Date.now()}`;
  const line = Object.assign({ name: 'Load test item' }, tax);
  if (__ENV.ITEM_ID) line.item_id = __ENV.ITEM_ID;
  const body = JSON.stringify({ doc_type: 'sale', lines: [line], tenders, client_uuid: key, notes: `k6 ${PROFILE} vu${__VU} it${__ITER}` });
  group('pos bill', () => {
    // The POS computes live totals with preview (gst.Line shape, no names) before saving.
    const pv = JSON.stringify({ store_state: STATE, place_of_supply: STATE, lines: [tax], tenders });
    const p = http.post(`${BASE}/api/aadesh/v1/invoices/preview`, pv, Object.assign({ headers: headers(data) }, tagged('invoice_preview')));
    note429(p);
    check(p, { 'preview 200': (x) => x.status === 200 || x.status === 429 });

    const t0 = Date.now();
    const c = http.post(`${BASE}/api/aadesh/v1/invoices`, body,
      Object.assign({ headers: headers(data, { 'Idempotency-Key': key }) }, tagged('invoice_create')));
    note429(c);
    const ok = check(c, { 'bill created': (x) => x.status === 201 || x.status === 200 || x.status === 429 });
    if (!ok || c.status === 429) return;
    billTotal.add(Date.now() - t0);
    billsCreated.add(1);

    // Every 10th bill: resend it, as a POS does after a dropped connection.
    if (__ITER % 10 === 0) {
      const again = http.post(`${BASE}/api/aadesh/v1/invoices`, body,
        Object.assign({ headers: headers(data, { 'Idempotency-Key': key }) }, tagged('invoice_retry')));
      const same = again.status < 300 && c.json('id') !== undefined && again.json('id') === c.json('id');
      if (!same && again.status !== 429) duplicateMismatch.add(1);
      check(again, { 'retry returns the same bill': () => same || again.status === 429 });
    }
  });
  sleep(2 + Math.random() * 2); // a cashier rings a bill every few seconds
}
