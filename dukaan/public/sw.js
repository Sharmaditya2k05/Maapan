/* Maapan POS service worker.
 * - App shell + Next static assets: cache-first (hashed filenames, safe to keep).
 * - Page navigations: network-first, fall back to the cached page (or /pos) so the counter opens offline.
 * - API: network-first; only the POS catalog GET is cached as an offline fallback (IndexedDB holds the
 *   authoritative offline copy). Writes are never intercepted — the app queues them in IndexedDB.
 * Bump VERSION to invalidate old caches on deploy.
 */
const VERSION = 'v1'
const SHELL = `maapan-shell-${VERSION}`
const RUNTIME = `maapan-runtime-${VERSION}`
const PRECACHE = ['/', '/pos', '/login', '/manifest.webmanifest', '/favicon-64.png', '/brand/mark-512.png', '/brand/wordmark.png', '/brand/wordmark-light.png']

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(SHELL).then((c) => c.addAll(PRECACHE).catch(() => undefined)).then(() => self.skipWaiting()))
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => ![SHELL, RUNTIME].includes(k)).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  )
})

const isCatalog = (url) => url.pathname.endsWith('/bhandar/v1/items/pos-catalog')
const isApi = (url) => url.pathname.includes('/api/') || url.pathname.match(/\/v1\//)

self.addEventListener('fetch', (event) => {
  const req = event.request
  if (req.method !== 'GET') return
  const url = new URL(req.url)

  if (url.origin === self.location.origin && url.pathname.startsWith('/_next/static/')) {
    event.respondWith(cacheFirst(req, SHELL))
    return
  }
  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req)
        .then((res) => { putCache(SHELL, req, res.clone()); return res })
        .catch(async () => (await caches.match(req)) || (await caches.match('/pos')) || Response.error()),
    )
    return
  }
  if (isCatalog(url)) {
    event.respondWith(networkFirst(req, RUNTIME))
    return
  }
  if (isApi(url)) return // straight to network; app handles offline
  if (url.origin === self.location.origin) event.respondWith(cacheFirst(req, RUNTIME))
})

async function cacheFirst(req, cacheName) {
  const hit = await caches.match(req)
  if (hit) return hit
  const res = await fetch(req)
  putCache(cacheName, req, res.clone())
  return res
}

async function networkFirst(req, cacheName) {
  try {
    const res = await fetch(req)
    putCache(cacheName, req, res.clone())
    return res
  } catch {
    return (await caches.match(req)) || Response.error()
  }
}

function putCache(name, req, res) {
  if (res && res.ok && res.type !== 'opaque') caches.open(name).then((c) => c.put(req, res)).catch(() => undefined)
}
