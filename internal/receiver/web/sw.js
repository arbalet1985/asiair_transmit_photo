// The page always comes from the receiver; the service worker only lets the
// phone install it as an app and shows a short message while offline.
const OFFLINE = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>ASIAIR — нет связи</title><body style="font:16px system-ui;padding:24px;background:#12151b;color:#e3e7ee">
<h2>Нет связи с приёмником</h2><p>Проверьте интернет (или Tailscale на телефоне) и что домашний компьютер включён.</p>
<button onclick="location.reload()" style="font:inherit;padding:10px 18px">Повторить</button></body>`;

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", e => e.waitUntil(self.clients.claim()));
self.addEventListener("fetch", e => {
  if (e.request.mode !== "navigate") return; // everything else goes to the network as usual
  e.respondWith(fetch(e.request).catch(() => new Response(OFFLINE, { headers: { "Content-Type": "text/html; charset=utf-8" } })));
});
