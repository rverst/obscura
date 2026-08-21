// Service worker.
//
// Written by hand rather than generated. The caching rules here are decided by
// what the content actually is -- immutable derivatives, a shell that must never
// go stale, and an API that must never be served from a cache -- and a plugin's
// defaults would have to be argued with on every one of those points.

const SHELL = "obscura-shell-v1";
const MEDIA = "obscura-media-v1";

/** Thumbnails and previews kept offline. Roughly a few hundred thumbnails. */
const MEDIA_LIMIT = 400;

self.addEventListener("install", (event) => {
  // The shell is tiny, so it is worth having before the first offline visit.
  event.waitUntil(
    caches.open(SHELL).then((cache) => cache.addAll(["/", "/manifest.webmanifest"])),
  );
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((k) => k !== SHELL && k !== MEDIA).map((k) => caches.delete(k))),
      )
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;

  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  // Never cache the API. Responses depend on a session, and a stale library
  // listing or a cached 401 would be worse than being offline.
  if (url.pathname.startsWith("/api/") && !isDerivative(url)) return;

  // Derivatives whose URL names the blob they contain. That token is what makes
  // them safe to keep: a different photo means a different URL, so a cached
  // copy can never be the wrong picture.
  if (isDerivative(url) && url.searchParams.has("v")) {
    event.respondWith(cacheFirst(req));
    return;
  }
  if (url.pathname.startsWith("/api/")) return;

  // Fingerprinted assets: same reasoning, the filename names the content.
  if (url.pathname.startsWith("/assets/")) {
    event.respondWith(cacheFirst(req));
    return;
  }

  // The shell: network first, so an upgrade is picked up immediately, with the
  // cache as the offline fallback.
  event.respondWith(networkFirst(req));
});

function isDerivative(url) {
  return /^\/api\/media\/\d+\/(thumb|preview)$/.test(url.pathname);
}

async function cacheFirst(req) {
  const cache = await caches.open(isImage(req) ? MEDIA : SHELL);
  const hit = await cache.match(req);
  if (hit) return hit;

  const resp = await fetch(req);
  if (resp.ok) {
    await cache.put(req, resp.clone());
    if (isImage(req)) void trim(cache, MEDIA_LIMIT);
  }
  return resp;
}

async function networkFirst(req) {
  const cache = await caches.open(SHELL);
  try {
    const resp = await fetch(req);
    if (resp.ok) await cache.put(req, resp.clone());
    return resp;
  } catch (err) {
    const hit = (await cache.match(req)) || (await cache.match("/"));
    if (hit) return hit;
    throw err;
  }
}

function isImage(req) {
  return isDerivative(new URL(req.url));
}

/**
 * Keeps the media cache bounded.
 *
 * Oldest-first, which approximates least-recently-added. A phone's storage
 * quota is finite and an unbounded photo cache is a good way to have the
 * browser evict everything at once, including the shell.
 */
async function trim(cache, limit) {
  const keys = await cache.keys();
  if (keys.length <= limit) return;
  for (const key of keys.slice(0, keys.length - limit)) {
    await cache.delete(key);
  }
}
