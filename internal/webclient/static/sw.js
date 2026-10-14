// Service worker of the Zweep web client: shows the alarms that arrive as Web Push and tells the
// plugin when one was shown or opened. On iOS the system shows the declarative form by itself; where
// that is not supported this worker shows the same message.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

// The session is bound to the key of this browser (see app.js): the receipt is signed with it
async function signature(method, path, body) {
  try {
    const pair = await new Promise((res, rej) => {
      const open = indexedDB.open('zweep-web', 1);
      open.onupgradeneeded = () => open.result.createObjectStore('kv');
      open.onsuccess = () => { const get = open.result.transaction('kv', 'readonly').objectStore('kv').get('session'); get.onsuccess = () => res(get.result); get.onerror = () => rej(get.error); };
      open.onerror = () => rej(open.error);
    });
    if (!pair) return {};
    const utf8 = (s) => new TextEncoder().encode(s);
    const hex = (buf) => [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, '0')).join('');
    const b64 = (buf) => btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    const ts = String(Math.floor(Date.now() / 1000));
    const text = ts + '\n' + method + '\n' + path + '\n' + hex(await crypto.subtle.digest('SHA-256', utf8(body)));
    return { 'X-Zweep-Sig': ts + '.' + b64(await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, pair.privateKey, utf8(text))) };
  } catch (e) { return {}; }
}

async function receipt(id) {
  if (!id) return;
  const body = JSON.stringify({ id });
  const sig = await signature('POST', '/api/receipt', body);
  await fetch('/api/receipt', { method: 'POST', keepalive: true, headers: Object.assign({ 'Content-Type': 'application/json' }, sig), body }).catch(() => {});
}

self.addEventListener('push', (event) => {
  let d = {};
  try { d = event.data ? event.data.json() : {}; } catch (e) { d = { title: 'Zweep' }; }
  // Declarative form (web_push: 8030): the notification is described under "notification"
  const n = d.notification || d;
  const open = d.open || n.navigate || '/';
  event.waitUntil((async () => {
    // Shown first: a push that shows nothing can cost the permission, the receipt can wait
    await self.registration.showNotification(n.title || 'Zweep', {
      body: n.body || '', tag: n.tag || d.tag || undefined, renotify: !!(n.tag || d.tag), data: { id: d.id || '', open },
      icon: '/icon-192.png', badge: '/badge-96.png',
    });
    await receipt(d.id);
  })());
});

self.addEventListener('notificationclick', (event) => {
  const data = event.notification.data || {};
  event.notification.close();
  event.waitUntil((async () => {
    const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    if (all.length) { all[0].postMessage({ open: data.id }); return all[0].focus(); }
    return self.clients.openWindow(data.open || '/');
  })());
});
