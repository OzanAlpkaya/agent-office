// agent-office service worker (ADR 0040). It makes the app installable and
// shows an offline page when the daemon cannot be reached. It never caches the
// app or any /api/ answer: a new daemon must never be paired with an old UI,
// and the data is live.
const CACHE = "agent-office-offline-v1";
const OFFLINE = "/offline.html";

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE).then((c) => c.addAll([OFFLINE, "/icons/icon-192.png"])).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

// Only page loads are answered, from the network first; everything else goes
// to the network untouched.
self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.mode !== "navigate" || new URL(req.url).pathname.startsWith("/api/")) return;
  event.respondWith(fetch(req).catch(() => caches.match(OFFLINE).then((r) => r ?? Response.error())));
});
