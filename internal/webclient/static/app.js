// Zweep web client: the activation, then the same screens as the Android app (alerts, problems, detail, settings).
(() => {
  const $ = (id) => document.getElementById(id);
  const ua = navigator.userAgent;
  const isIOS = /iPhone|iPad|iPod/.test(ua) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  const standalone = window.matchMedia('(display-mode: standalone)').matches || navigator.standalone === true;
  let chosen = '';
  try { chosen = localStorage.getItem('zw_lang') || ''; } catch (e) { chosen = ''; }
  const it = (chosen || navigator.language || 'en').toLowerCase().startsWith('it');
  const T = (en, itText) => (it ? itText : en);
  document.documentElement.lang = it ? 'it' : 'en';

  function h(tag, attrs, ...kids) {
    const e = document.createElement(tag);
    Object.entries(attrs || {}).forEach(([k, v]) => {
      if (k === 'html') e.innerHTML = v; else if (k.startsWith('on')) e.addEventListener(k.slice(2), v); else e.setAttribute(k, v);
    });
    kids.flat().forEach((k) => e.append(k));
    return e;
  }
  // The logo and the motto open every screen before the activation; the app itself keeps the room for the alarms
  $('motto').textContent = T('Your Zabbix alert radar', 'Il radar dei tuoi allarmi Zabbix');
  // The page starts empty (only the background, like the splash screen before it) until the first answer
  // of the server says what to show: no placeholder flashing by at every start
  const show = (...nodes) => { if (!document.body.classList.contains('app')) $('top').hidden = false; $('hero').hidden = false; $('main').replaceChildren(...nodes); };
  const card = (title, ...kids) => h('section', { class: 'card' }, h('h2', {}, title), ...kids);

  // The session is bound to this browser: a key pair the browser does not let anybody export, kept in
  // IndexedDB; the server has the public half and wants every request signed. A copied cookie is useless.
  const keys = (() => {
    let pair = null;
    const store = (mode) => new Promise((res, rej) => {
      const open = indexedDB.open('zweep-web', 1);
      open.onupgradeneeded = () => open.result.createObjectStore('kv');
      open.onsuccess = () => res(open.result.transaction('kv', mode).objectStore('kv'));
      open.onerror = () => rej(open.error);
    });
    const done = (req) => new Promise((res, rej) => { req.onsuccess = () => res(req.result); req.onerror = () => rej(req.error); });
    const load = async () => { if (!pair) { try { pair = (await done((await store('readonly')).get('session'))) || null; } catch (e) { pair = null; } } return pair; };
    const hex = (buf) => [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, '0')).join('');
    const utf8 = (s) => new TextEncoder().encode(s);
    return {
      // create makes a new pair, checks it can be read back and used, and returns the public half
      create: async () => {
        const made = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign']);
        await done((await store('readwrite')).put(made, 'session'));
        pair = null;
        if (!(await load())) throw new Error('key not kept');
        await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, pair.privateKey, utf8('check'));
        return bytesToB64(await crypto.subtle.exportKey('raw', pair.publicKey));
      },
      // header signs one request: time, method, path with query, hash of the body
      header: async (method, path, body) => {
        if (!(await load())) return {};
        const ts = String(Math.floor(Date.now() / 1000));
        const text = ts + '\n' + method + '\n' + path + '\n' + hex(await crypto.subtle.digest('SHA-256', utf8(body || '')));
        return { 'X-Zweep-Sig': ts + '.' + bytesToB64(await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, pair.privateKey, utf8(text))) };
      },
    };
  })();

  async function api(method, path, body) {
    const text = body ? JSON.stringify(body) : undefined;
    let sig = {};
    try { sig = await keys.header(method, path, text); } catch (e) { sig = {}; }
    const r = await fetch(path, { method, headers: Object.assign(body ? { 'Content-Type': 'application/json' } : {}, sig), body: text });
    let data = null;
    if (r.status !== 204) { try { data = await r.json(); } catch (e) { data = null; } }
    if (!r.ok) throw new Error((data && data.error) || ('http_' + r.status));
    return data;
  }

  // iOS says "OS 18_7" also on later versions: the real one is in Version/
  function iosVersion() {
    const v = ua.match(/Version\/(\d+)\.(\d+)/) || ua.match(/OS (\d+)[_.](\d+)/);
    return v ? [Number(v[1]), Number(v[2])] : null;
  }

  function b64ToBytes(s) {
    const pad = '='.repeat((4 - (s.length % 4)) % 4);
    return Uint8Array.from(atob((s + pad).replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
  }
  const bytesToB64 = (buf) => btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

  const ERR = {
    web_disabled: ['The web client was disabled by the administrator.', 'Il client web è stato disattivato dall\'amministratore.'],
    invalid_code: ['The code is not valid, was already used or has expired. Ask for a new one.', 'Il codice non è valido, è già stato usato o è scaduto. Chiedine uno nuovo.'],
    invalid_credentials: ['Wrong username or password.', 'Nome utente o password errati.'],
    password_expired: ['The password has expired. Ask the administrator for a new one or for an activation code.', 'La password è scaduta. Chiedi all\'amministratore una password nuova o un codice di attivazione.'],
    device_limit: ['This user already has all the web clients it may have. Disconnect one, or ask the administrator.', 'Questo utente ha già tutti i client web consentiti. Scollegane uno, oppure chiedi all\'amministratore.'],
    key_required: ['This browser cannot keep the key that protects the session (private window, storage blocked, old version). Use a normal window of a current browser, then ask for a new activation code.', 'Questo browser non riesce a conservare la chiave che protegge la sessione (finestra privata, archiviazione bloccata, versione vecchia). Usa una finestra normale di un browser aggiornato, poi chiedi un nuovo codice di attivazione.'],
    web_not_allowed: ['The web client is not enabled for this user. Ask the administrator.', 'Il client web non è abilitato per questo utente. Chiedi all\'amministratore.'],
    too_many_attempts: ['Too many attempts. Try again later.', 'Troppi tentativi. Riprova più tardi.'],
    test_refused: ['Wait a few seconds between one test alarm and the next.', 'Aspetta qualche secondo tra un allarme di prova e il successivo.'],
    code_not_set: ['Choose a confirmation code first.', 'Prima scegli un codice di conferma.'],
    code_required: ['Enter the confirmation code.', 'Inserisci il codice di conferma.'],
    wrong_code: ['Wrong confirmation code.', 'Codice di conferma errato.'],
    code_blocked: ['Too many wrong codes: blocked for 15 minutes. If you forgot the code, ask the administrator for a new activation.', 'Troppi codici errati: bloccato per 15 minuti. Se hai dimenticato il codice, chiedi all\'amministratore una nuova attivazione.'],
    weak_code: ['Use six digits, not all the same and not in a row (like 111111 or 123456).', 'Usa sei cifre, non tutte uguali e non in fila (come 111111 o 123456).'],
    mode_read_only: ['Acknowledging is not enabled for this Zabbix.', 'La presa in carico non è abilitata per questo Zabbix.'],
    ack_not_allowed: ['The administrator did not allow you to acknowledge.', 'L\'amministratore non ti ha abilitato alla presa in carico.'],
    invalid_text: ['The message is missing or too long.', 'Il messaggio manca o è troppo lungo.'],
    problems_disabled: ['The problem list is not available on this server.', 'L\'elenco dei problemi non è disponibile su questo server.'],
    close_not_allowed: ['The administrator did not allow closing alerts', 'L\'amministratore non ha abilitato la chiusura degli alert'],
    alert_not_open: ['The alert was already closed', 'L\'alert era già chiuso'],
    zabbix_unavailable: ['Zabbix does not answer. Try again in a moment.', 'Zabbix non risponde. Riprova tra un momento.'],
    not_found: ['Not found.', 'Non trovato.'],
    zweep_unavailable: ['The server does not answer. Try again in a moment.', 'Il server non risponde. Riprova tra un momento.'],
  };
  const errText = (e) => {
    if (e.message === 'notifications_blocked' || e.message === 'notifications_required') return notifyHelp(e.message === 'notifications_blocked');
    const m = ERR[e.message];
    return m ? T(m[0], m[1]) : T('Not done: ', 'Non riuscito: ') + e.message;
  };

  // notifyHelp says how to allow the notifications that were refused (blocked: the system or the browser
  // no longer even asks). Where to tap is different on every device, and a private window exists only
  // in a browser, not in an installed app.
  function notifyHelp(blocked, inApp) {
    const android = /Android/.test(ua);
    const firefox = /Firefox\//.test(ua);
    const macSafari = /Macintosh/.test(ua) && /Safari\//.test(ua) && !/Chrome\/|Chromium\/|Edg\//.test(ua);
    // During an activation: what to do next. On an iPhone the app must be opened again after the Settings.
    const again = inApp ? '' : isIOS ? T(' Then open Zweep Web again and tap Activate: the code is still valid until it expires.', ' Poi riapri Zweep Web e tocca Attiva: il codice resta valido finché non scade.') :
      T(' Then tap Activate again: the code is still valid until it expires.', ' Poi tocca di nuovo Attiva: il codice resta valido finché non scade.');
    const lead = blocked ? T('Notifications were refused, so this device cannot receive alarms yet. ', 'Le notifiche sono state negate, quindi questo dispositivo non può ancora ricevere allarmi. ') :
      T('Notifications were not allowed, so this device cannot receive alarms yet. ', 'Le notifiche non sono state consentite, quindi questo dispositivo non può ancora ricevere allarmi. ');
    if (!blocked && !isIOS) return lead + (inApp ? T('Tap Enable notifications again and answer Allow when the browser asks.', 'Tocca di nuovo Attiva le notifiche e rispondi Consenti quando il browser lo chiede.') :
      T('Tap Activate again and answer Allow when the browser asks.', 'Tocca di nuovo Attiva e rispondi Consenti quando il browser lo chiede.'));
    if (isIOS) {
      return lead + T('Open Settings → Apps → Zweep Web → Notifications and turn on Allow Notifications (on iOS 17 or earlier: Settings → Notifications → Zweep Web).',
        'Apri Impostazioni → App → Zweep Web → Notifiche e attiva Consenti notifiche (su iOS 17 o precedenti: Impostazioni → Notifiche → Zweep Web).') + (inApp ? T(' Then open Zweep Web again.', ' Poi riapri Zweep Web.') : again);
    }
    if (android && standalone) {
      return lead + T('Press and hold the Zweep Web icon → App info → Notifications, and allow them. If that is not enough: Chrome → ⋮ → Settings → Site settings → Notifications, and allow this site.',
        'Tieni premuta l’icona di Zweep Web → Informazioni app → Notifiche, e consentile. Se non basta: Chrome → ⋮ → Impostazioni → Impostazioni sito → Notifiche, e consenti questo sito.') + again;
    }
    if (android) {
      return lead + T('Tap the icon at the left of the address → Permissions → Notifications → Allow. In an incognito tab notifications are always refused: use a normal tab.',
        'Tocca l’icona a sinistra dell’indirizzo → Autorizzazioni → Notifiche → Consenti. In una scheda in incognito le notifiche sono sempre negate: usa una scheda normale.') + again;
    }
    if (macSafari) {
      return lead + T('Safari → Settings → Websites → Notifications: set this site to Allow. In a private window notifications are not available: use a normal window.',
        'Safari → Impostazioni → Siti web → Notifiche: imposta questo sito su Consenti. In una finestra privata le notifiche non sono disponibili: usa una finestra normale.') + again;
    }
    if (firefox) {
      return lead + T('Click the padlock at the left of the address and remove the block on notifications. In a private window they are not available: use a normal window.',
        'Clicca il lucchetto a sinistra dell’indirizzo e togli il blocco delle notifiche. In una finestra privata non sono disponibili: usa una finestra normale.') + again;
    }
    return lead + T('Click the icon at the left of the address (padlock or sliders) → Permissions → Notifications → Allow. In a private (InPrivate, incognito) window notifications are always refused: use a normal window.',
      'Clicca l’icona a sinistra dell’indirizzo (lucchetto o cursori) → Autorizzazioni → Notifiche → Consenti. In una finestra privata (InPrivate, in incognito) le notifiche sono sempre negate: usa una finestra normale.') + again;
  }
  // helpInApp: the same help for the button "Enable notifications" of an activated client
  const helpInApp = (e) => (e.message === 'notifications_blocked' || e.message === 'notifications_required' ? notifyHelp(e.message === 'notifications_blocked', true) : errText(e));
  // Edge and Chrome on a computer may ask with a small bell in the address bar instead of a question
  const quietAsk = !isIOS && !/Android/.test(ua) && !/Firefox\//.test(ua) && /Chrome\/|Edg\//.test(ua);

  // ---- screens -------------------------------------------------------------------------------

  // Android has the Zweep app, which keeps its own connection and does not depend on a push service:
  // the web client works there, but it is the second choice
  const APP_URL = 'https://github.com/N1k0droid/zweep/releases/latest';
  // The source code of this Zweep (AGPL-3.0, section 13) and its author: a fork that changes the code
  // puts the address of its own source here
  const SOURCE_URL = 'https://github.com/N1k0droid/zweep';
  const AUTHOR_NAME = 'Nicola Carmelo Gurgone';
  const AUTHOR_URL = 'https://github.com/N1k0droid';
  // androidChoice shows the notice alone, under the logo; the activation comes after the choice
  function androidChoice(next) {
    const points = [
      ['It sounds in silent mode and in Do Not Disturb', 'Suona anche in silenzioso e in Non disturbare'],
      ['It repeats the alarm until you see it', 'Ripete l’allarme finché non lo vedi'],
      ['Its own connection to the server: no servers of Google in between', 'Collegamento diretto al server: nessun server di Google in mezzo'],
      ['It tells the server when an alarm was received and shown', 'Conferma al server quando un allarme è arrivato ed è stato visto'],
      ['Problems, acknowledgement and history in the app', 'Problemi, presa in carico e storico dentro l’app'],
    ];
    const triangle = '<svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3 2 20h20L12 3z"/><path d="M12 10v4"/><path d="M12 17.2v.3"/></svg>';
    const title = h('h2', { class: 'alert', html: triangle });
    title.append(h('span', {}, T('On Android, use the Zweep app', 'Su Android usa l’app Zweep')), h('span', { class: 'sign', html: triangle }));
    show(h('section', { class: 'card warn' }, title,
      h('p', { class: 'center' }, T('The app is more reliable than this page:', 'L’app è più affidabile di questa pagina:')),
      h('ul', {}, points.map((x) => h('li', {}, T(x[0], x[1])))),
      h('p', { class: 'muted small center' }, T('This page is meant for iPhone, iPad and computers.', 'Questa pagina è pensata per iPhone, iPad e computer.')),
      h('div', { class: 'choice' },
        h('a', { class: 'button primary', href: APP_URL, target: '_blank', rel: 'noopener noreferrer' }, T('Download the app', 'Scarica l’app')),
        h('button', { onclick: () => { try { sessionStorage.setItem('zw_web_ok', '1'); } catch (e) { /* asked again next time */ } next(); } },
          T('Continue with the web client', 'Continua con il client web')))));
  }
  const androidUndecided = () => { try { return /Android/.test(ua) && !sessionStorage.getItem('zw_web_ok'); } catch (e) { return false; } };

  function disabled() {
    $('who').textContent = '';
    show(h('section', { class: 'card warn' }, h('h2', {}, T('Web client disabled', 'Client web disattivato')),
      h('p', {}, T('The web client was disabled by the administrator. It is not possible to log in or to receive alarms from here.',
        'Il client web è stato disattivato dall\'amministratore. Da qui non è possibile accedere né ricevere allarmi.')),
      h('p', { class: 'muted' }, T('If you need it, ask your Zweep administrator.', 'Se ti serve, chiedi all\'amministratore di Zweep.'))));
  }

  function unsupported(text) {
    show(h('section', { class: 'card warn' }, h('h2', {}, T('This device cannot be used', 'Questo dispositivo non può essere usato')), h('p', {}, text)));
  }

  function install() {
    show(card(T('First: add to the Home Screen', 'Prima cosa: aggiungi alla schermata Home'),
      h('p', {}, T('On iPhone and iPad a web page can send notifications only when it is installed as an app.',
        'Su iPhone e iPad una pagina web può inviare notifiche solo se è installata come un\'app.')),
      h('ol', {},
        h('li', { html: T('Open this page in <b>Safari</b>.', 'Apri questa pagina in <b>Safari</b>.') }),
        h('li', { html: T('Tap <b>Share</b> (the square with the arrow pointing up).', 'Tocca <b>Condividi</b> (il quadrato con la freccia verso l\'alto).') }),
        h('li', { html: T('Choose <b>Add to Home Screen</b>, then <b>Add</b>.', 'Scegli <b>Aggiungi alla schermata Home</b>, poi <b>Aggiungi</b>.') }),
        h('li', { html: T('Open <b>Zweep</b> from the new icon: the activation continues there.', 'Apri <b>Zweep</b> dalla nuova icona: l\'attivazione continua da lì.') }))));
  }

  // newKey: the public half of a fresh key for this browser, or nothing where the browser cannot keep one
  const newKey = async () => { try { return await keys.create(); } catch (e) { return undefined; } };

  function activate(code, st, withPassword) {
    if (withPassword) { activateWithPassword(st); return; }
    const codeIn = h('input', { autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', placeholder: 'zwe_…', value: code || '' });
    const nameIn = h('input', { maxlength: '40', value: deviceName() });
    const msg = h('p', { class: 'msg' });
    const go = h('button', { class: 'primary wide', onclick: async () => {
      go.disabled = true;
      msg.textContent = '';
      const quiet = setTimeout(() => { if (quietAsk) msg.textContent = T('If the browser shows no question: click the bell icon at the right of the address bar and allow notifications.', 'Se il browser non mostra nessuna richiesta: clicca l’icona della campanella a destra nella barra degli indirizzi e consenti le notifiche.'); }, 2500);
      try {
        await api('POST', '/api/activate', Object.assign({ code: codeIn.value.trim(), name: nameIn.value.trim() }, await forActivation(st)));
        clearTimeout(quiet);
        history.replaceState(null, '', '/');
        start();
      } catch (e) {
        clearTimeout(quiet);
        if (e.message === 'web_disabled') { disabled(); return; }
        msg.textContent = errText(e);
        go.disabled = false;
      }
    } }, T('Activate', 'Attiva'));
    show(card(T('Activate this device', 'Attiva questo dispositivo'),
      h('p', {}, T('Enter the activation code your administrator gave you. It can be used once and expires in a few minutes.',
        'Inserisci il codice di attivazione ricevuto dall\'amministratore. Vale una volta sola e scade in pochi minuti.')),
      h('label', {}, T('Activation code', 'Codice di attivazione'), codeIn),
      h('label', {}, T('Name of this device', 'Nome di questo dispositivo'), nameIn), go, msg,
      st && st.password_login ? h('p', {}, h('button', { onclick: () => activate(code, st, true) }, T('Log in with username and password', 'Accedi con nome utente e password'))) : ''));
  }

  function deviceName() {
    return (isIOS ? (/iPhone/.test(ua) ? 'iPhone' : 'iPad') : /Android/.test(ua) ? 'Android' : /Mac/.test(ua) ? 'Mac' : /Windows/.test(ua) ? 'Windows' : 'Browser') +
      ' · ' + (/Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : 'Safari');
  }

  // Allowed by the administrator only: the password goes to the server and is not kept here
  function activateWithPassword(st) {
    const userIn = h('input', { autocomplete: 'username', autocapitalize: 'off', spellcheck: 'false', maxlength: '64' });
    const passIn = h('input', { type: 'password', autocomplete: 'current-password', maxlength: '256' });
    const nameIn = h('input', { maxlength: '40', value: deviceName() });
    const msg = h('p', { class: 'msg' });
    const go = h('button', { class: 'primary wide', onclick: async () => {
      go.disabled = true;
      msg.textContent = '';
      const quiet = setTimeout(() => { if (quietAsk) msg.textContent = T('If the browser shows no question: click the bell icon at the right of the address bar and allow notifications.', 'Se il browser non mostra nessuna richiesta: clicca l’icona della campanella a destra nella barra degli indirizzi e consenti le notifiche.'); }, 2500);
      try {
        await api('POST', '/api/activate', Object.assign({ username: userIn.value.trim(), password: passIn.value, name: nameIn.value.trim() }, await forActivation(st)));
        passIn.value = '';
        clearTimeout(quiet);
        history.replaceState(null, '', '/');
        start();
      } catch (e) {
        clearTimeout(quiet);
        if (e.message === 'web_disabled') { disabled(); return; }
        msg.textContent = errText(e);
        go.disabled = false;
      }
    } }, T('Log in', 'Accedi'));
    show(card(T('Activate this device', 'Attiva questo dispositivo'),
      h('label', {}, T('Username', 'Nome utente'), userIn),
      h('label', {}, 'Password', passIn),
      h('label', {}, T('Name of this device', 'Nome di questo dispositivo'), nameIn), go, msg,
      h('p', {}, h('button', { onclick: () => activate('', st, false) }, T('Use an activation code', 'Usa un codice di attivazione')))));
  }

  // subscription asks for the permission (first: it must follow the tap), subscribes this browser to its
  // push service and returns what the server needs to send to it
  async function subscription(vapid) {
    const perm = await Notification.requestPermission();
    if (perm === 'denied') throw new Error('notifications_blocked');
    if (perm !== 'granted') throw new Error('notifications_required');
    let reg;
    let sub;
    try {
      reg = await navigator.serviceWorker.register('/sw.js');
      await navigator.serviceWorker.ready;
      sub = await reg.pushManager.getSubscription();
    } catch (e) { throw new Error('push_failed: ' + e.message); }
    // A subscription made for another key (an earlier installation on this address) is refused by
    // the push service: drop it and subscribe with the key of this server
    const key = sub && sub.options && sub.options.applicationServerKey;
    try {
      if (sub && (!key || bytesToB64(key) !== vapid)) { await sub.unsubscribe(); sub = null; }
      if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64ToBytes(vapid) });
    } catch (e) { throw new Error('push_failed: ' + e.message); }
    return { endpoint: sub.endpoint, p256dh: bytesToB64(sub.getKey('p256dh')), auth: bytesToB64(sub.getKey('auth')),
      tz: Intl.DateTimeFormat().resolvedOptions().timeZone, lang: it ? 'it' : 'en' };
  }
  const subscribe = async (vapid) => api('POST', '/api/push', await subscription(vapid));
  // forActivation: what an activation carries besides the code: the push subscription (required unless the
  // administrator says otherwise: a browser that cannot receive alarms is not activated) and the key
  async function forActivation(st) {
    let push;
    try { push = await subscription(st.vapid); } catch (e) { if (st.require_notifications) throw e; }
    return { push, key: await newKey() };
  }

  const SEV = ['nc', 'info', 'warn', 'avg', 'high', 'dis'];

  // The server believes this browser is subscribed: check that the browser agrees, with the key of
  // this server, and repair quietly when it does not (cleared data, a subscription for another key)
  async function ensurePush(st) {
    try {
      if (!st.push || !('Notification' in window) || Notification.permission !== 'granted') return;
      const reg = await navigator.serviceWorker.getRegistration();
      const sub = reg && await reg.pushManager.getSubscription();
      const key = sub && sub.options && sub.options.applicationServerKey;
      // The language too: the server writes some notifications itself
      if (!sub || !key || bytesToB64(key) !== st.vapid || st.lang !== (it ? 'it' : 'en')) await subscribe(st.vapid);
    } catch (e) { /* the next alarm tells the server, and the page asks again */ }
  }

  // ---- the app ---------------------------------------------------------------------------------
  // The same screens as the Android app (android/.../ui): a bottom bar with Alerts, Problems and
  // Settings, a header with its actions, the status strip, the same rows in both lists and one detail
  // screen reached from an alert, from a problem and from a notification. The words of Zabbix stay
  // in English in both languages, as in the app.

  // Material Symbols, the icons of the app
  const ICON = {
    bell: 'M160-200v-60h80v-304q0-84 49.5-150.5T420-798v-22q0-25 17.5-42.5T480-880q25 0 42.5 17.5T540-820v22q81 17 130.5 83.5T720-564v304h80v60H160Zm320-302Zm0 422q-33 0-56.5-23.5T400-160h160q0 33-23.5 56.5T480-80ZM300-260h360v-304q0-75-52.5-127.5T480-744q-75 0-127.5 52.5T300-564v304Z',
    report: 'M480-281q14 0 24.5-10.5T515-316q0-14-10.5-24.5T480-351q-14 0-24.5 10.5T445-316q0 14 10.5 24.5T480-281Zm-30-144h60v-263h-60v263ZM330-120 120-330v-300l210-210h300l210 210v300L630-120H330Zm25-60h250l175-175v-250L605-780H355L180-605v250l175 175Zm125-300Z',
    settings: 'm388-80-20-126q-19-7-40-19t-37-25l-118 54-93-164 108-79q-2-9-2.5-20.5T185-480q0-9 .5-20.5T188-521L80-600l93-164 118 54q16-13 37-25t40-18l20-127h184l20 126q19 7 40.5 18.5T669-710l118-54 93 164-108 77q2 10 2.5 21.5t.5 21.5q0 10-.5 21t-2.5 21l108 78-93 164-118-54q-16 13-36.5 25.5T592-206L572-80H388Zm48-60h88l14-112q33-8 62.5-25t53.5-41l106 46 40-72-94-69q4-17 6.5-33.5T715-480q0-17-2-33.5t-7-33.5l94-69-40-72-106 46q-23-26-52-43.5T538-708l-14-112h-88l-14 112q-34 7-63.5 24T306-642l-106-46-40 72 94 69q-4 17-6.5 33.5T245-480q0 17 2.5 33.5T254-413l-94 69 40 72 106-46q24 24 53.5 41t62.5 25l14 112Zm44-210q54 0 92-38t38-92q0-54-38-92t-92-38q-54 0-92 38t-38 92q0 54 38 92t92 38Zm0-130Z',
    search: 'M796-121 533-384q-30 26-70 40.5T378-329q-108 0-183-75t-75-181q0-106 75-181t182-75q106 0 180.5 75T632-585q0 43-14 83t-42 75l264 262-44 44ZM377-389q81 0 138-57.5T572-585q0-81-57-138.5T377-781q-82 0-139.5 57.5T180-585q0 81 57.5 138.5T377-389Z',
    done_all: 'M294-242 70-466l43-43 181 181 43 43-43 43Zm170 0L240-466l43-43 181 181 384-384 43 43-427 427Zm0-170-43-43 257-257 43 43-257 257Z',
    refresh: 'M480-160q-133 0-226.5-93.5T160-480q0-133 93.5-226.5T480-800q85 0 149 34.5T740-671v-129h60v254H546v-60h168q-38-60-97-97t-137-37q-109 0-184.5 75.5T220-480q0 109 75.5 184.5T480-220q83 0 152-47.5T728-393h62q-29 105-115 169t-195 64Z',
    back: 'M274-450h526v-60H274l248-248-42-42-320 320 320 320 42-42-248-248Z',
  };
  const icon = (name) => '<svg viewBox="0 -960 960 960" aria-hidden="true"><path d="' + ICON[name] + '"/></svg>';
  const iconButton = (name, label, onclick, on) => h('button', { class: 'icon' + (on ? ' on' : ''), 'aria-label': label, title: label, html: icon(name), onclick });

  const pref = (k, d) => { try { const v = localStorage.getItem('zw_' + k); return v === null ? d : v; } catch (e) { return d; } };
  const setPref = (k, v) => { try { localStorage.setItem('zw_' + k, v); } catch (e) { /* kept for this visit only */ } };

  // What is on screen: kept across the refreshes of the page (it refreshes when it comes back to the front)
  const view = { tab: 'alerts', detail: null, form: false, search: false, q: '', acks: {}, key: '' };
  const SEV_NAME = ['Not classified', 'Information', 'Warning', 'Average', 'High', 'Disaster'];
  const sevOf = (n) => Math.min(Math.max(Number(n) || 0, 0), 5);
  // Resolved alerts and problems stay among the active ones for a while: an hour, as in the app, or the choice in Settings
  const resolvedStay = () => (Number(pref('resolved_minutes', '60')) || 60) * 60000;
  const historyMinutes = () => Math.min(Number(pref('history_minutes', '1440')) || 1440, 10080);
  const periodLabel = (m) => (m <= 1440 ? Math.floor(m / 60) + 'h' : Math.floor(m / 1440) + T('d', 'g'));

  function timeShort(ms) {
    const d = new Date(ms);
    const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    return d.toDateString() === new Date().toDateString() ? time : d.toLocaleDateString([], { dateStyle: 'short' }) + ' ' + time;
  }
  function duration(from, to) {
    const m = Math.max(0, Math.floor(((to || Date.now()) - from) / 60000));
    return m < 60 ? m + 'm' : m < 1440 ? Math.floor(m / 60) + 'h ' + (m % 60) + 'm' : Math.floor(m / 1440) + 'd ' + (Math.floor(m / 60) % 24) + 'h';
  }
  const hostsOf = (p) => (p.hosts || []).map((x) => x.name || x.host).join(', ');
  const badge = (sev) => h('span', { class: 'badge sev-' + sevOf(sev) }, SEV_NAME[sevOf(sev)].toUpperCase());
  const label = (text, kind) => h('span', { class: 'st st-' + kind }, text.toUpperCase());

  // One card per event: the problem, its updates and its recovery are one alert, as in the app
  function events(st) {
    const by = new Map();
    (st.alerts || []).slice().reverse().forEach((a) => {
      if (!by.has(a.sid)) by.set(a.sid, []);
      by.get(a.sid).push(a);
    });
    const now = Date.now();
    return [...by.entries()].map(([sid, msgs]) => {
      const f = msgs[0];
      const l = msgs[msgs.length - 1];
      const unread = msgs.some((m) => !m.read);
      return {
        sid, msgs, source: sid.slice(0, Math.max(sid.indexOf(':'), 0)), eventid: sid.slice(sid.lastIndexOf(':') + 1), kind: l.kind,
        sev: Math.max(...msgs.map((m) => m.sev)), host: f.host || '', name: f.name || f.title, sourceName: f.source_name || f.source,
        acked: msgs.some((m) => m.acked), firstAt: (f.event_time || f.ts) * 1000, lastAt: l.ts * 1000, unread,
        notShown: l.outcome === 'not_shown',
        archived: (l.kind === 'recovery' && now - l.ts * 1000 > resolvedStay()) || (l.kind === 'test' && !unread),
      };
    }).sort((a, b) => b.lastAt - a.lastAt);
  }

  // A row of the two lists: severity bar and badge, host, time; name and duration; origin and status
  function row(r, onclick) {
    return h('button', { class: 'erow' + (view.detail && r.sid === view.detail.sid ? ' sel' : ''), onclick },
      h('span', { class: 'sbar sev-' + sevOf(r.sev) }),
      h('span', { class: 'ecol' },
        h('span', { class: 'l1' }, badge(r.sev), h('span', { class: 'host' }, r.host), r.unread ? h('span', { class: 'dot' }) : '', h('span', { class: 'time' }, timeShort(r.at))),
        h('span', { class: 'l2' }, h('span', { class: 'name' + (r.unread ? ' unread' : '') }, r.name), r.dur ? h('span', { class: 'time' }, r.dur) : ''),
        h('span', { class: 'l3' }, h('span', { class: 'origin' }, r.origin), r.labels),
        r.note ? h('span', { class: 'note' }, r.note) : ''));
  }

  // ---- dialogs ---------------------------------------------------------------------------------

  // dialog shows a small window over the page; it returns the function that closes it
  // The back button of the phone closes what is open on top, as in the app: a dialog, then the detail.
  // Each of them adds an entry to the history of the browser when it opens; nothing goes into the address.
  // A close made from the page takes its entry away itself (view.pops counts the "back" still to come).
  function pushMarker(mark) {
    const push = () => { history.pushState({ zw: 1 }, ''); mark.pushed = true; };
    if (view.pops > 0) view.pending = push; else push();
  }
  function dropMarker(mark) {
    if (mark.pushed) { mark.pushed = false; view.pops = (view.pops || 0) + 1; history.back(); } else if (view.pending) view.pending = null;
  }
  window.addEventListener('popstate', () => {
    if (view.pops > 0) {
      view.pops--;
      if (!view.pops && view.pending) { const push = view.pending; view.pending = null; push(); }
      return;
    }
    if (view.dlg) { view.dlg(); return; }
    if (view.detail) { view.detail = null; view.mark = null; view.sideSid = null; app(view.st); start(); }
  });

  // dialog shows a small window over the page; it returns the function that closes it. onDismiss runs
  // when the user leaves it without choosing: back button, Esc, or a tap outside. cls "sheet": a panel
  // that fills the screen under the status strip (a list with a search field, clear of the keyboard).
  function dialog(title, kids, buttons, onDismiss, cls) {
    const d = $('dlg');
    const mark = {};
    let open = true;
    view.form = true;
    const shut = () => { open = false; view.form = false; view.dlg = null; if (d.open) d.close(); d.replaceChildren(); };
    const close = () => { if (!open) return; shut(); dropMarker(mark); };
    const dismiss = () => { if (!open) return; close(); if (onDismiss) onDismiss(); };
    view.dlg = () => { if (!open) return; mark.pushed = false; shut(); if (onDismiss) onDismiss(); };
    d.className = cls || '';
    if (cls === 'sheet') d.style.setProperty('--sheet-top', Math.round($('strip').getBoundingClientRect().bottom) + 'px');
    d.replaceChildren(h('div', { class: 'dwrap' }, h('h2', {}, title), ...kids, h('div', { class: 'dbuttons' }, buttons)));
    d.oncancel = (e) => { e.preventDefault(); dismiss(); };
    d.onclick = (e) => { if (e.target === d) dismiss(); }; // outside the window: the content covers the whole of it
    if (!d.open) d.showModal();
    pushMarker(mark);
    return close;
  }
  const textButton = (text, onclick, cls) => h('button', { class: 'text' + (cls ? ' ' + cls : ''), onclick }, text);
  function confirmDialog(title, text, okText, danger) {
    return new Promise((resolve) => {
      const close = dialog(title, [h('p', {}, text)], [
        textButton(T('Cancel', 'Annulla'), () => { close(); resolve(false); }),
        textButton(okText, () => { close(); resolve(true); }, danger ? 'danger' : '')], () => resolve(false));
    });
  }
  const digits = (text) => {
    const i = h('input', { type: 'password', inputmode: 'numeric', autocomplete: 'off', maxlength: '6', pattern: '[0-9]*' });
    return [h('label', {}, text, i), i];
  };

  // The confirmation code protects what changes something in Zabbix: chosen here, checked by the server
  function chooseCode(st) {
    return new Promise((resolve) => {
      const change = st.code_set && !st.code_fresh;
      const [curL, cur] = digits(T('Current code', 'Codice attuale'));
      const [newL, neu] = digits(T('New code (6 digits)', 'Nuovo codice (6 cifre)'));
      const [repL, rep] = digits(T('Repeat the new code', 'Ripeti il nuovo codice'));
      const msg = h('p', { class: 'msg' });
      const ok = textButton(T('Save', 'Salva'), async () => {
        msg.textContent = '';
        if (neu.value !== rep.value) { msg.textContent = T('The two codes are different.', 'I due codici sono diversi.'); return; }
        ok.disabled = true;
        try {
          await api('POST', '/api/code', { code: neu.value, current: cur.value });
          st.code_set = true; st.code_grace = true;
          close(); resolve(true);
        } catch (e) { msg.textContent = errText(e); ok.disabled = false; }
      });
      const close = dialog(st.code_set ? T('Change the confirmation code', 'Cambia il codice di conferma') : T('Choose a confirmation code', 'Scegli un codice di conferma'),
        [h('p', {}, T('Six digits you will be asked for before an Acknowledge. It is not the code of your phone: choose one for Zweep only.',
          'Sei cifre che ti verranno chieste prima di un Acknowledge. Non è il codice del telefono: scegline uno solo per Zweep.')),
        change ? curL : '', newL, repL, msg],
        [textButton(T('Cancel', 'Annulla'), () => { close(); resolve(false); }), ok], () => resolve(false));
    });
  }

  // withCode runs an action that needs the confirmation code: it asks for the code when the server
  // wants it, and keeps asking while it is wrong. It returns null when the user gives up.
  async function withCode(st, fn) {
    if (!st.code_set && !(await chooseCode(st))) return null;
    if (st.code_grace) {
      try { return await fn(''); } catch (e) { if (e.message !== 'code_required') throw e; st.code_grace = false; }
    }
    return new Promise((resolve, reject) => {
      const [codeL, code] = digits(T('Confirmation code', 'Codice di conferma'));
      const msg = h('p', { class: 'msg' });
      const ok = textButton(T('Confirm', 'Conferma'), async () => {
        ok.disabled = true;
        msg.textContent = '';
        try {
          const out = await fn(code.value);
          st.code_grace = true;
          close(); resolve(out);
        } catch (e) {
          if (['wrong_code', 'code_required', 'code_blocked'].includes(e.message)) { msg.textContent = errText(e); code.value = ''; ok.disabled = false; return; }
          close(); reject(e);
        }
      });
      const close = dialog(T('Confirmation code', 'Codice di conferma'), [codeL, msg], [textButton(T('Cancel', 'Annulla'), () => { close(); resolve(null); }), ok], () => resolve(null));
      code.focus();
    });
  }

  // ---- frame: header, status strip, bottom bar ------------------------------------------------

  const wideQuery = window.matchMedia('(min-width: 960px)');
  const wide = () => wideQuery.matches;

  function frame(st, title, opts, ...nodes) {
    document.body.classList.add('app');
    document.body.classList.toggle('wide', wide());
    document.body.classList.toggle('single', wide() && view.tab === 'settings');
    if (!wide() || view.tab === 'settings') { $('side').hidden = true; $('side').replaceChildren(); view.sideSid = null; }
    $('top').hidden = true; $('hero').hidden = true; $('foot').hidden = true;
    const bar = $('bar');
    bar.hidden = false;
    bar.replaceChildren(opts.back ? iconButton('back', T('Back', 'Indietro'), opts.back) : '',
      h('h1', { class: opts.big ? 'big' : '' }, title), ...(opts.actions || []));
    const strip = $('strip');
    strip.hidden = !!opts.back;
    const up = st.online !== false;
    strip.replaceChildren(h('span', { class: 'muted' }, 'Server status:'), h('span', { class: 'led ' + (up ? 'ok' : 'err') }), h('b', {}, (up ? 1 : 0) + '/1 connected'));
    strip.onclick = () => statusDialog(st);
    const nav = $('nav');
    nav.hidden = !!opts.back;
    const item = (id, name, text) => h('button', { class: view.tab === id ? 'on' : '', onclick: () => { view.tab = id; view.detail = null; view.search = false; view.q = ''; app(view.st); if (id !== 'problems') start(); } },
      h('span', { class: 'pill', html: icon(name) }), h('span', {}, text));
    nav.replaceChildren(item('alerts', 'bell', 'Alerts'), st.problems ? item('problems', 'report', 'Problems') : '', item('settings', 'settings', T('Settings', 'Impostazioni')),
      h('span', { class: 'navbrand', translate: 'no' }, h('img', { src: '/logo.svg', alt: '', width: '26', height: '26' }), 'Zweep Web'), h('span', { class: 'navwho' }, st.username + ' · ' + st.name));
    view.pull = opts.pull || null;
    const main = $('main');
    const key = view.tab + '/' + (view.detail && !wide() ? view.detail.sid : '');
    const top = key === view.key ? main.scrollTop : 0;
    view.key = key;
    main.replaceChildren(...nodes);
    main.scrollTop = top;
  }

  function statusDialog(st) {
    const up = st.online !== false;
    const close = dialog('Server status', [
      h('p', { class: 'strong' }, h('span', { class: 'led ' + (up ? 'ok' : 'err') }), ' ' + location.host),
      h('p', { class: up ? 'done' : 'msg' }, up ? T('Connected', 'Connesso') : T('Reconnecting…', 'Riconnessione…')),
      h('p', { class: 'muted small' }, st.username + ' · ' + st.name),
      h('p', { class: 'muted small' }, st.push ? T('Notifications are on for this device.', 'Le notifiche sono attive su questo dispositivo.') :
        T('Notifications are off on this device.', 'Le notifiche sono spente su questo dispositivo.'))],
    [textButton(T('Close', 'Chiudi'), () => close())]);
  }

  // A filter that offers one choice out of a few: the chip shows the current one
  function choice(options, value, onchange) {
    const s = h('select', { class: 'chip' + (value !== options[0][0] ? ' on' : ''), onchange: () => onchange(s.value) },
      options.map(([k, text]) => { const o = h('option', { value: k }, text); if (k === value) o.selected = true; return o; }));
    return s;
  }
  const sevShown = (key) => new Set(pref(key, '0,1,2,3,4,5').split(',').filter(Boolean).map(Number));
  function sevFilter(key, changed) {
    const shown = sevShown(key);
    return h('button', { class: 'chip' + (shown.size < 6 ? ' on' : ''), onclick: () => {
      const boxes = [5, 4, 3, 2, 1, 0].map((s) => {
        const i = h('input', { type: 'checkbox' });
        i.checked = shown.has(s);
        return [s, i, h('label', { class: 'check' }, i, SEV_NAME[s])];
      });
      const done = () => { setPref(key, boxes.filter((b) => b[1].checked).map((b) => b[0]).join(',')); close(); changed(); };
      const close = dialog('Severity', boxes.map((b) => b[2]), [
        textButton(T('Show all', 'Mostra tutte'), () => { boxes.forEach((b) => { b[1].checked = true; }); done(); }), textButton(T('Apply', 'Applica'), done)]);
    } }, shown.size === 6 ? 'Severity' : 'Severity (' + shown.size + '/6)');
  }
  const banner = (text, cls) => h('p', { class: 'banner ' + (cls || '') }, text);
  const empty = (title, hint) => h('div', { class: 'empty' }, h('b', {}, title), hint ? h('span', {}, hint) : '');
  const searchBox = (hint, paint) => {
    const i = h('input', { class: 'search', type: 'search', placeholder: hint, value: view.q, oninput: () => { view.q = i.value; paint(); } });
    return i;
  };

  // ---- Alerts ------------------------------------------------------------------------------------

  function openDetail(st, d, from) {
    view.detail = { sid: d.sid, source: d.source, eventid: d.eventid, from };
    view.mark = {};
    pushMarker(view.mark);
    app(view.st);
  }

  function alertsScreen(st) {
    const all = events(st);
    const list = h('div', { class: 'rows' });
    const paint = () => {
      const mode = pref('alerts_view', 'active');
      const sev = sevShown('alerts_sev');
      const status = pref('alerts_status', 'all');
      const q = view.q.trim().toLowerCase();
      const shown = all.filter((e) => (mode === 'all' || e.archived === (mode === 'history')) && sev.has(sevOf(e.sev)) &&
        (status === 'all' || (status === 'open' && e.kind !== 'recovery') || (status === 'resolved' && e.kind === 'recovery') || (status === 'acked' && e.acked && e.kind !== 'recovery')) &&
        (!q || [e.name, e.host, e.sourceName].some((x) => (x || '').toLowerCase().includes(q))));
      // On a wide screen the name of the app is in the menu and the header counts the alerts, as Problems does
      const head = $('bar').querySelector('h1');
      if (wide() && head && view.tab === 'alerts') head.textContent = 'Alerts (' + shown.length + ')';
      if (!shown.length) {
        list.replaceChildren(all.length ? empty(T('No matching alerts', 'Nessun allarme corrisponde'), T('Try changing your filters.', 'Prova a cambiare i filtri.')) :
          empty(T('No alerts yet', 'Ancora nessun allarme'), T('Alerts escalated to this device by Zabbix will appear here.', 'Qui compariranno gli allarmi che Zabbix inoltra a questo dispositivo.')));
        return;
      }
      list.replaceChildren(...shown.map((e) => {
        const open = e.kind !== 'recovery' && e.kind !== 'test';
        return row({ sid: e.sid, sev: e.sev, host: e.host, at: e.firstAt, unread: e.unread, name: e.name, dur: open ? duration(e.firstAt) : '', origin: e.sourceName,
          labels: e.kind === 'test' ? label('Test', 'test') : e.kind === 'recovery' ? label('Resolved', 'ok') : e.acked ? label('Acknowledged', 'ack') : label('Problem', 'open'),
          note: (st.silenced || []).includes(e.sid) ? T('Silenced', 'Silenziato') : e.notShown ? T('Not notified on this device', 'Non notificato su questo dispositivo') : '' }, () => openDetail(st, e, 'alerts'));
      }));
    };
    const active = all.filter((e) => !e.archived).length;
    const nodes = [];
    if (view.search) nodes.push(searchBox(T('Search host, alarm, source', 'Cerca host, allarme, sorgente'), paint));
    nodes.push(h('div', { class: 'chips' },
      choice([['active', T('Active', 'Attivi') + ' (' + active + ')'], ['history', T('History', 'Storico') + ' (' + (all.length - active) + ')'], ['all', T('Show all', 'Mostra tutti') + ' (' + all.length + ')']],
        pref('alerts_view', 'active'), (v) => { setPref('alerts_view', v); app(view.st); }),
      sevFilter('alerts_sev', () => app(view.st)),
      choice([['all', T('All', 'Tutti')], ['open', T('Open only', 'Solo aperti')], ['resolved', 'Resolved'], ['acked', 'Acknowledged']],
        pref('alerts_status', 'all'), (v) => { setPref('alerts_status', v); app(view.st); })));
    if (!st.push) {
      const b = h('button', { class: 'text', onclick: async () => {
        b.disabled = true;
        try { await subscribe(st.vapid); start(); } catch (e) { b.disabled = false; dialog(T('Notifications', 'Notifiche'), [h('p', {}, helpInApp(e))], [textButton(T('Close', 'Chiudi'), () => view.dlg && view.dlg())]); }
      } }, T('Enable notifications', 'Attiva le notifiche'));
      nodes.push(h('div', { class: 'banner warn' }, T('Notifications are off: alarms do not reach this device.', 'Le notifiche sono spente: gli allarmi non arrivano a questo dispositivo.'), b));
    }
    nodes.push(list);
    frame(st, wide() ? 'Alerts' : 'Zweep Web', { big: !wide(), pull: start, actions: [
      iconButton('done_all', T('Mark all read', 'Leggi tutti'), async () => {
        if (!(await confirmDialog(T('Mark all read', 'Leggi tutti'), T('Mark all alerts as read?', 'Segnare tutti gli allarmi come letti?'), T('Mark all read', 'Leggi tutti')))) return;
        try { await api('POST', '/api/read', { all: true }); } catch (e) { /* shown again at the next refresh */ }
        start();
      }),
      iconButton('search', T('Search', 'Cerca'), () => { view.search = !view.search; if (!view.search) view.q = ''; app(view.st); }, view.search)] }, ...nodes);
    paint();
  }

  // ---- Problems ----------------------------------------------------------------------------------

  const inHostGroups = (groups, chosenGroups) => !chosenGroups.length || (groups || []).some((g) => chosenGroups.some((c) => g === c || g.startsWith(c + '/')));
  const prefList = (k) => pref(k, '').split('\n').filter(Boolean);
  function hostGroupFilter(groups, changed) {
    const chosenGroups = prefList('problems_hg');
    return h('button', { class: 'chip' + (chosenGroups.length ? ' on' : ''), onclick: () => {
      const all = [...new Set([...groups, ...chosenGroups])].sort((a, b) => a.localeCompare(b));
      const boxes = all.map((g) => {
        const i = h('input', { type: 'checkbox' });
        i.checked = chosenGroups.includes(g);
        return [g, i, h('label', { class: 'check' }, i, g)];
      });
      const find = h('input', { type: 'search', placeholder: T('Search host group…', 'Cerca host group…'), oninput: () => {
        boxes.forEach((b) => { b[2].hidden = !b[0].toLowerCase().includes(find.value.trim().toLowerCase()); });
      } });
      const close = dialog('Host group', [find, h('div', { class: 'scroll' }, boxes.length ? boxes.map((b) => b[2]) : h('p', { class: 'muted' }, T('No host group', 'Nessun host group'))),
        h('p', { class: 'muted small' }, T('No host group selected: all are shown.', 'Nessun host group selezionato: si vedono tutti.'))],
      [textButton(T('Cancel', 'Annulla'), () => close()),
        textButton(T('Apply', 'Applica'), () => { setPref('problems_hg', boxes.filter((b) => b[1].checked).map((b) => b[0]).join('\n')); close(); changed(); })], null, 'sheet');
    } }, chosenGroups.length ? 'Host group (' + chosenGroups.length + ')' : 'Host group');
  }

  // The three views of the app filter one list: the open problems plus those resolved in the last 7 days
  function problemsScreen(st) {
    const list = h('div', { class: 'rows' }, h('p', { class: 'muted pad' }, '…'));
    const notes = h('div', {});
    const chips = h('div', { class: 'chips' });
    let rows = view.problems || [];
    const ms = (t) => new Date(t).getTime();
    const paint = () => {
      const now = Date.now();
      const mode = pref('problems_view', 'recent');
      const sev = sevShown('problems_sev');
      const status = pref('problems_status', 'all');
      const groups = prefList('problems_hg');
      const q = view.q.trim().toLowerCase();
      const inView = rows.filter((p) => {
        const open = p.status !== 'resolved';
        if (mode === 'history') return now - ms(p.clock) <= historyMinutes() * 60000;
        if (mode === 'recent') return open || (p.r_clock && now - ms(p.r_clock) <= resolvedStay());
        return open;
      });
      const shown = inView.filter((p) => {
        const open = p.status !== 'resolved';
        return sev.has(sevOf(p.severity)) && inHostGroups(p.hostgroups, groups) &&
          (status === 'all' || (status === 'unacked' && open && !p.acknowledged) || (status === 'acked' && open && p.acknowledged) || (status === 'resolved' && !open)) &&
          (!q || [p.name, hostsOf(p), (p.hostgroups || []).join(' ')].some((x) => (x || '').toLowerCase().includes(q)));
      }).sort((a, b) => ms(b.clock) - ms(a.clock));
      $('bar').querySelector('h1').textContent = 'Problems (' + shown.length + ')';
      chips.replaceChildren(
        sevFilter('problems_sev', paint),
        hostGroupFilter([...new Set(inView.flatMap((p) => p.hostgroups || []))], paint),
        choice([['recent', 'Recent'], ['problems', 'Problems'], ['history', 'History · ' + periodLabel(historyMinutes())]], mode, (v) => { setPref('problems_view', v); paint(); }),
        choice([['all', T('All', 'Tutti')], ['unacked', 'Unacknowledged'], ['acked', 'Acknowledged'], ['resolved', 'Resolved']], status, (v) => { setPref('problems_status', v); paint(); }));
      if (!shown.length) { list.replaceChildren(h('p', { class: 'muted pad' }, T('No problems match the filters', 'Nessun problem corrisponde ai filtri'))); return; }
      list.replaceChildren(...shown.map((p) => {
        const open = p.status !== 'resolved';
        return row({ sid: p.source + ':' + p.eventid, sev: p.severity, host: hostsOf(p), at: ms(p.clock), name: p.name, dur: duration(ms(p.clock), open || !p.r_clock ? 0 : ms(p.r_clock)), origin: p.source,
          labels: [open ? '' : label('Resolved', 'ok'), p.suppressed ? label('Suppressed', 'muted') : '', p.acknowledged ? label('Acknowledged', 'ack') : open ? label('Unacknowledged', 'open') : ''] },
        () => openDetail(st, { sid: p.source + ':' + p.eventid, source: p.source, eventid: p.eventid }, 'problems'));
      }));
    };
    const load = async () => {
      let d;
      try { d = await api('GET', '/api/problems?resolved=604800'); } catch (e) { notes.replaceChildren(banner(errText(e), 'err')); return; }
      if (view.tab !== 'problems' || view.detail) return;
      rows = view.problems = d.problems || [];
      const stale = (d.sources || []).filter((x) => x.stale).map((x) => x.id);
      notes.replaceChildren(stale.length ? banner(T('Data not updated for: ', 'Dati non aggiornati per: ') + stale.join(', ') + T(' (Zabbix API unreachable)', ' (Zabbix API non raggiungibile)'), 'err') : '');
      paint();
    };
    const nodes = [];
    if (view.search) nodes.push(searchBox(T('Search host, problem, host group', 'Cerca host, problem, host group'), paint));
    nodes.push(chips, notes, list);
    frame(st, 'Problems (' + rows.length + ')', { pull: load, actions: [
      iconButton('refresh', T('Refresh', 'Aggiorna'), load),
      iconButton('search', T('Search', 'Cerca'), () => { view.search = !view.search; if (!view.search) view.q = ''; app(view.st); }, view.search)] }, ...nodes);
    paint();
    load();
  }

  // ---- Detail ------------------------------------------------------------------------------------

  function historyRow(x) {
    const who = x.author_kind === 'app_user' ? (x.author_name || '') + ' (via Zweep)' : x.author_name || 'Zabbix user';
    const a = x.actions || [];
    const what = [a.includes('unack') ? 'unacknowledged' : '', a.includes('close') ? 'closed' : '',
      a.includes('severity') && x.old_severity != null && x.new_severity != null ? 'severity ' + SEV_NAME[sevOf(x.old_severity)] + ' → ' + SEV_NAME[sevOf(x.new_severity)] : '',
      a.includes('suppress') ? 'suppressed' : ''].filter(Boolean).join(' · ');
    // The update that carried the acknowledgement has the label of the Alerts on the right; the others nothing
    return h('div', { class: 'hrow' },
      h('div', { class: 'hhead' }, h('b', {}, timeShort(new Date(x.clock).getTime()) + '  ' + who),
        a.includes('ack') ? label('Acknowledged', 'ack') : ''),
      what ? h('span', {}, what) : '', x.message ? h('span', {}, '“' + x.message + '”') : '');
  }

  function detailScreen(st, pane) {
    const r = view.detail;
    const ev = events(st).find((e) => e.sid === r.sid);
    if (ev && ev.unread) {
      ev.msgs.forEach((m) => { m.read = true; });
      api('POST', '/api/read', { sid: ev.sid }).catch(() => {});
    }
    const back = () => { const mark = view.mark; view.detail = null; view.mark = null; if (mark) dropMarker(mark); app(view.st); start(); };
    const body = h('div', { class: 'detail' });
    const paneTitle = h('h1', {});
    const title = () => (pane ? paneTitle : $('bar').querySelector('h1'));
    const field = (k, v) => (v ? h('div', { class: 'field' }, h('span', {}, k), h('span', {}, v)) : '');
    const paint = (d, failed) => {
      const p = d && d.problem;
      const sev = p ? p.severity : ev ? ev.sev : 0;
      const resolved = p ? p.status === 'resolved' : !!ev && ev.kind === 'recovery';
      const start = p ? new Date(p.clock).getTime() : ev ? ev.firstAt : 0;
      title().textContent = SEV_NAME[sevOf(sev)] + ' · ' + (resolved ? 'Resolved' : 'Problem');
      const kids = [h('h2', { class: 'title' }, p ? p.name : ev ? ev.name : ''), h('div', {}, badge(sev)),
        field('Host', p ? hostsOf(p) : ev ? ev.host : ''), field('Host groups', p ? (p.hostgroups || []).join(', ') : ''),
        field(T('Started', 'Inizio'), start ? timeShort(start) + '  (' + duration(start, resolved && p && p.r_clock ? new Date(p.r_clock).getTime() : 0) + ')' : ''),
        field('Tags', p ? (p.tags || []).map((t) => (t.value ? t.tag + ':' + t.value : t.tag)).join('  ') : ''),
        field(T('Source', 'Sorgente'), ev ? ev.sourceName : r.source)];
      // Zabbix did not answer this time and what is shown is a copy: say so, where it is read and before acting on it
      const asOf = d && d.data_as_of ? new Date(d.data_as_of).getTime() : 0;
      const stale = asOf > 0 && Date.now() - asOf > 120000 ? T('Zabbix does not answer: data as of ', 'Zabbix non risponde: dati fermi alle ') + timeShort(asOf) +
        T('. Later updates, an Acknowledge by a colleague included, may not show.', '. Aggiornamenti successivi, compreso un Acknowledge di un collega, potrebbero non vedersi.') : '';
      if (stale) kids.push(h('p', { class: 'msg' }, stale));
      else if (asOf) kids.push(h('p', { class: 'muted small' }, T('Data as of ', 'Dati aggiornati alle ') + timeShort(asOf)));
      if (failed) kids.push(h('p', { class: 'msg' }, T('Details from Zabbix are not available right now.', 'I dettagli da Zabbix ora non sono disponibili.')));
      // Actions, above the history: each asks for a confirmation first
      const acts = [];
      const amsg = h('p', { class: 'small' });
      if (d && /^https?:\/\//.test(d.frontend_url || '')) {
        acts.push(h('button', { class: 'outline', onclick: async () => {
          if (await confirmDialog(T('Open in Zabbix', 'Apri in Zabbix'), T('Open this problem in the Zabbix frontend? It opens outside Zweep and asks for your Zabbix login.',
            'Aprire questo problema nel frontend di Zabbix? Si apre fuori da Zweep e chiede il tuo accesso a Zabbix.'), T('Open', 'Apri'))) window.open(d.frontend_url, '_blank', 'noopener,noreferrer');
        } }, T('Open in Zabbix ↗', 'Apri in Zabbix ↗')));
      }
      const silenced = ev && (st.silenced || []).includes(r.sid);
      if (ev && ev.kind !== 'test' && (!resolved || silenced)) {
        acts.push(h('button', { class: 'outline', onclick: async () => {
          const ok = silenced ? await confirmDialog(T('Unmute', 'Riattiva'), T('Notify this alarm on this device again?', 'Tornare a notificare questo allarme su questo dispositivo?'), T('Unmute', 'Riattiva')) :
            await confirmDialog(T('Silence', 'Silenzia'), T('No more notifications for this alarm on this device, its recovery included. It stays in the list and you can unmute it. The other alarms are not affected.',
              'Nessuna altra notifica per questo allarme su questo dispositivo, rientro compreso. Resta nell\'elenco e puoi riattivarlo. Gli altri allarmi non cambiano.'), T('Silence', 'Silenzia'));
          if (!ok) return;
          try {
            await api('POST', '/api/silence', { sid: r.sid, on: !silenced });
            st.silenced = (st.silenced || []).filter((x) => x !== r.sid).concat(silenced ? [] : [r.sid]);
            if (view.detail === r) paint(d, failed);
          } catch (e) { amsg.className = 'small msg'; amsg.textContent = errText(e); }
        } }, silenced ? T('Unmute', 'Riattiva') : T('Silence', 'Silenzia')));
      }
      if (st.close && ev && ev.kind !== 'recovery' && ev.kind !== 'test') {
        const b = h('button', { class: 'outline danger', onclick: async () => {
          const title = T('Force close the alert', 'Chiusura forzata dell\'alert');
          const first = [h('p', {}, T('The alert is closed as Resolved for every operator who received it. Use it only for alerts left open by mistake (recovery never arrived, event deleted in Zabbix). Zabbix is not changed.',
            'L\'alert viene chiuso come Resolved per tutti gli operatori che lo hanno ricevuto. Usala solo per alert rimasti aperti per errore (resolved mai arrivato, evento cancellato in Zabbix). Zabbix non viene modificato.'))];
          if (p && p.status !== 'resolved') first.push(h('p', { class: 'msg strongmsg' }, T('Warning: Zabbix still shows this problem as OPEN.', 'Attenzione: in Zabbix questo problema risulta ancora APERTO.')));
          const go = await new Promise((resolve) => {
            const close = dialog(title, first, [textButton(T('Cancel', 'Annulla'), () => { close(); resolve(false); }), textButton(T('Continue', 'Continua'), () => { close(); resolve(true); })], () => resolve(false));
          });
          if (!go) return;
          if (!(await confirmDialog(title, T('If Zabbix later sends news about this problem (acknowledge, comment, severity), the alert opens again. Close the alert now?',
            'Se in seguito Zabbix manda novità su questo problema (acknowledge, commento, severity), l\'alert si riapre. Chiudere ora l\'alert?'), T('Close for good', 'Chiudi definitivamente'), true))) return;
          b.disabled = true;
          try {
            const out = await withCode(st, (code) => api('POST', '/api/close', { sid: r.sid, code }));
            if (out === null) { b.disabled = false; return; }
            amsg.className = 'small done'; amsg.textContent = T('Alert closed for every operator', 'Alert chiuso per tutti gli operatori');
            setTimeout(() => { if (view.detail === r) { view.sideSid = null; start(); } }, 1500);
          } catch (e) { amsg.className = 'small msg'; amsg.textContent = errText(e); b.disabled = false; }
        } }, T('Force close', 'Chiudi forzatamente'));
        acts.push(b);
      }
      if (acts.length) {
        kids.push(h('h3', { class: 'section' }, T('Actions', 'Azioni')), h('div', { class: 'actions' }, acts),
          silenced ? h('p', { class: 'muted small' }, T('Silenced: no new notifications for this alarm', 'Silenziato: nessuna nuova notifica per questo allarme')) : '', amsg);
      }
      kids.push(h('h3', { class: 'section' }, T('History', 'Storico')));
      if (d) {
        const hist = (d.history || []).slice().sort((a, b) => new Date(a.clock) - new Date(b.clock));
        if (!hist.length) kids.push(h('p', { class: 'muted small' }, T('No updates yet', 'Nessun aggiornamento')));
        hist.forEach((x) => kids.push(historyRow(x)));
      } else if (ev) {
        ev.msgs.forEach((m) => kids.push(h('p', { class: 'small' }, timeShort(m.ts * 1000) + '  ' + ({ problem: 'Problem', recovery: 'Resolved', update: 'Updated', test: 'Test' }[m.kind] || m.kind))));
      }
      // "Update problem", with the words of Zabbix: a message alone (Update), or an acknowledgement with it
      if (st.problems && !resolved && (!ev || ev.kind !== 'test') && (d || failed)) {
        kids.push(h('h3', { class: 'section' }, 'Update problem'));
      }
      if (st.problems && !resolved && !d && failed) {
        // Nothing is done blind: without the data nobody knows what colleagues did in the meantime
        kids.push(h('p', { class: 'muted small' }, T('To update or acknowledge the problem the data of Zabbix are needed: try again when it answers.',
          'Per aggiornare o prendere in carico il problema servono i dati di Zabbix: riprova quando risponde.')));
      }
      if (st.problems && d && !resolved) {
        const acked = !!p.acknowledged;
        const text = h('textarea', { maxlength: '1000', rows: '2', placeholder: T('Message (required)', 'Message (obbligatorio)') });
        const state = h('p', { class: 'small' });
        const buttons = [];
        const busy = (on) => buttons.forEach((x) => { x.disabled = on; });
        const run = async (action) => {
          const message = text.value.trim();
          if (!message) { state.className = 'small msg'; state.textContent = T('Write a message first.', 'Scrivi prima un messaggio.'); return; }
          state.className = 'small'; state.textContent = '';
          const question = action === 'ack' ? (acked ? T('The problem is already acknowledged: Zabbix keeps one Acknowledge per problem, so your message is added to it. Do you want to continue?', 'Il problema risulta già Acknowledged: Zabbix tiene un solo Acknowledge per problema, quindi viene aggiunto il tuo messaggio. Vuoi continuare?') :
            T('Send this Acknowledge to Zabbix?', 'Inviare questo Acknowledge a Zabbix?')) :
            acked ? T('Add this message to the problem?', 'Aggiungere questo messaggio al problema?') :
              T('No Acknowledge: the problem stays unacknowledged. Do you want to continue?', 'Nessun Acknowledge: il problema resta non preso in carico. Vuoi continuare?');
          const go = await new Promise((resolve) => {
            const close = dialog(action === 'ack' ? 'Acknowledge' : 'Update', [h('p', {}, question), h('p', { class: 'strong' }, '“' + message + '”'),
              stale ? h('p', { class: 'msg' }, stale) : ''],
            [textButton(T('No', 'No'), () => { close(); resolve(false); }), textButton(T('Yes', 'Sì'), () => { close(); resolve(true); })], () => resolve(false));
          });
          if (!go) return;
          busy(true);
          try {
            const a = await withCode(st, (code) => api('POST', '/api/ack', { source: r.source, eventid: r.eventid, text: message, action, code }));
            if (!a) { busy(false); return; }
            text.value = '';
            state.textContent = T('Sent, waiting for Zabbix', 'Inviato, in attesa di Zabbix');
            let res = a;
            for (let i = 0; i < 15 && res.state !== 'confirmed' && res.state !== 'rejected'; i++) {
              await new Promise((ok) => setTimeout(ok, 1000));
              try { res = await api('GET', '/api/ack/' + a.request_id); } catch (e) { /* asked again */ }
            }
            if (res.state === 'confirmed') {
              view.acks[r.sid] = action === 'ack' && !acked ? '✓ Acknowledged in Zabbix' :
                action === 'ack' ? T('✓ Message added in Zabbix (the problem was already acknowledged)', '✓ Messaggio aggiunto in Zabbix (il problema era già Acknowledged)') :
                  T('✓ Message added in Zabbix', '✓ Messaggio aggiunto in Zabbix');
              if (view.detail === r) { view.sideSid = null; app(view.st); }
              return;
            }
            if (res.state === 'rejected') { state.className = 'small msg'; state.textContent = T('✗ Rejected: ', '✗ Rifiutato: ') + (res.reason || '-'); }
          } catch (e) { state.className = 'small msg'; state.textContent = errText(e); }
          busy(false);
        };
        // Both always. Filled, to tell them from the actions above: Update blue, Acknowledge in the red of the brand.
        buttons.push(h('button', { class: 'primary', onclick: () => run('message') }, 'Update'),
          h('button', { class: 'primary red', onclick: () => run('ack') }, 'Acknowledge'));
        kids.push(text, h('div', { class: 'actions after-field' }, buttons), state);
        if (view.acks[r.sid]) kids.push(h('p', { class: 'done small' }, view.acks[r.sid]));
      }
      body.replaceChildren(...kids.flat());
    };
    if (pane) {
      const side = $('side');
      side.hidden = false;
      side.replaceChildren(h('div', { class: 'sidehead' }, paneTitle, h('button', { class: 'text', onclick: back }, T('Close', 'Chiudi'))), body);
      if (view.sideSid !== r.sid) side.scrollTop = 0;
      view.sideSid = r.sid;
    } else frame(st, '', { back }, body);
    paint(null, false);
    if (st.problems && (!ev || ev.kind !== 'test')) {
      api('GET', '/api/problems/' + encodeURIComponent(r.source) + '/' + encodeURIComponent(r.eventid))
        .then((d) => { if (view.detail === r) paint(d, false); }, () => { if (view.detail === r) paint(null, true); });
    }
  }

  // ---- Settings ----------------------------------------------------------------------------------

  // Edge and Chrome say when the page can be installed: kept for the button in Settings
  let installPrompt = null;
  window.addEventListener('beforeinstallprompt', (e) => { e.preventDefault(); installPrompt = e; });

  function settingsScreen(st) {
    const msg = h('p', { class: 'msg' });
    const up = st.online !== false;
    const nodes = [
      h('h3', { class: 'section' }, 'Server'),
      h('div', { class: 'box' },
        h('p', { class: 'strong' }, h('span', { class: 'led ' + (up ? 'ok' : 'err') }), ' ' + location.host),
        h('p', { class: 'muted small' }, st.username + ' · ' + st.name),
        h('p', { class: up ? 'done small' : 'msg' }, up ? T('Connected', 'Connesso') : T('Reconnecting…', 'Riconnessione…')),
        h('div', { class: 'actions' },
          h('button', { class: 'outline', onclick: async () => {
            try { await api('POST', '/api/test'); msg.textContent = T('Test alarm requested: it arrives in a few seconds.', 'Allarme di prova richiesto: arriva tra pochi secondi.'); } catch (e) { msg.textContent = errText(e); }
          } }, T('Send test alarm', 'Invia allarme di prova')),
          h('button', { class: 'outline danger', onclick: async () => {
            if (!(await confirmDialog('Logout', T('Log out of this server? This device is disconnected: you will need a new activation code.',
              'Uscire da questo server? Questo dispositivo viene scollegato: servirà un nuovo codice di attivazione.'), 'Logout', true))) return;
            try { await api('POST', '/api/logout'); } catch (e) { /* the session is gone anyway */ }
            start();
          } }, 'Logout')), msg),
      h('h3', { class: 'section' }, T('Notifications', 'Notifiche')),
      h('div', { class: 'box' }, h('p', { class: st.push ? 'done' : 'msg' }, st.push ? T('On for this device.', 'Attive su questo dispositivo.') : T('Off: alarms do not reach this device.', 'Spente: gli allarmi non arrivano a questo dispositivo.')),
        st.push ? '' : h('div', { class: 'actions' }, h('button', { class: 'primary', onclick: async (e) => {
          e.target.disabled = true;
          try { await subscribe(st.vapid); start(); } catch (err) { msg.textContent = helpInApp(err); e.target.disabled = false; }
        } }, T('Enable notifications', 'Attiva le notifiche')))),
    ];
    if (st.problems) {
      nodes.push(h('h3', { class: 'section' }, T('Confirmation code', 'Codice di conferma')),
        h('div', { class: 'box' }, h('p', { class: 'muted small' }, st.code_set ? T('Asked before an Acknowledge or a forced close.', 'Viene chiesto prima di un Acknowledge o di una chiusura forzata.') : T('Not chosen yet: you will be asked at the first Acknowledge.', 'Non ancora scelto: ti verrà chiesto al primo Acknowledge.')),
          h('div', { class: 'actions' }, h('button', { class: 'outline', onclick: async () => { if (await chooseCode(st)) app(view.st); } }, st.code_set ? T('Change', 'Cambia') : T('Choose', 'Scegli')))));
    }
    const pick = (key, def, options) => h('div', { class: 'chips flat' }, options.map(([v, text]) =>
      h('button', { class: 'chip' + (pref(key, def) === v ? ' on' : ''), onclick: () => { setPref(key, v); app(view.st); } }, text)));
    nodes.push(h('h3', { class: 'section' }, T('Data', 'Dati')),
      h('div', { class: 'box' },
        h('p', {}, T('Resolved stay in Active for:', 'I resolved restano negli Attivi per:')), pick('resolved_minutes', '60', [['5', '5 min'], ['60', '1 h'], ['1440', '24 h']]),
        st.problems ? h('p', { class: 'gap' }, T('Problem history:', 'Storico problemi:')) : '',
        st.problems ? pick('history_minutes', '1440', [['60', '1h'], ['180', '3h'], ['720', '12h'], ['1440', '24h'], ['10080', '7' + T('d', 'g')]]) : '',
        st.problems ? h('p', { class: 'muted small' }, T('Period of the History view in the Problems tab.', 'Periodo della vista History nella scheda Problems.')) : '',
        h('p', { class: 'muted small' }, T('These choices stay in this browser. The alarms are kept by the server, not by this device.', 'Queste scelte restano in questo browser. Gli allarmi li conserva il server, non questo dispositivo.'))),
      h('h3', { class: 'section' }, T('Language', 'Lingua')),
      h('div', { class: 'box' }, h('div', { class: 'chips flat' }, [['', T('Automatic', 'Automatica')], ['it', 'Italiano'], ['en', 'English']].map(([v, text]) =>
        h('button', { class: 'chip' + (chosen === v ? ' on' : ''), onclick: () => { try { if (v) localStorage.setItem('zw_lang', v); else localStorage.removeItem('zw_lang'); } catch (e) { return; } location.reload(); } }, text)))));
    if (!standalone && !isIOS && !/Android/.test(ua)) {
      const mac = /Macintosh/.test(ua) && /Safari\//.test(ua) && !/Chrome\/|Edg\//.test(ua);
      nodes.push(h('h3', { class: 'section' }, T('Install as an app', 'Installa come app')),
        h('div', { class: 'box' },
          h('p', { class: 'muted small' }, T('Not required: notifications arrive in this tab too. Installed, Zweep Web has its own window and icon.',
            'Non è obbligatorio: le notifiche arrivano anche da questa scheda. Installato, Zweep Web ha la sua finestra e la sua icona.')),
          installPrompt ? h('div', { class: 'actions' }, h('button', { class: 'outline', onclick: async () => { const p = installPrompt; installPrompt = null; p.prompt(); try { await p.userChoice; } catch (e) { /* closed */ } app(view.st); } }, T('Install', 'Installa'))) :
            h('p', { class: 'small' }, mac ? T('In Safari: File → Add to Dock.', 'In Safari: File → Aggiungi al Dock.') :
              T('In Edge or Chrome: the install icon at the right of the address bar, or the menu → Apps → Install.', 'In Edge o Chrome: l\'icona di installazione a destra della barra degli indirizzi, oppure il menu → App → Installa.'))));
    }
    nodes.push(h('h3', { class: 'section' }, T('About', 'Informazioni')), h('div', { class: 'box' }, h('p', { class: 'muted small' }, 'Zweep Web · ' + (st.version || '')),
      h('p', { class: 'muted small' }, st.bound ? T('Session bound to this browser.', 'Sessione legata a questo browser.') : T('Session not bound to this browser: it was activated before this protection existed. Log out and activate again to bind it.', 'Sessione non legata a questo browser: è stata attivata prima che esistesse questa protezione. Fai Logout e riattiva per legarla.'))),
      h('div', { class: 'actions centered after-box' }, h('button', { class: 'text', onclick: () => aboutSheet(st) }, T('Credits and source code', 'Crediti e codice sorgente'))));
    frame(st, T('Settings', 'Impostazioni'), {}, h('div', { class: 'settings' }, nodes));
  }

  // aboutSheet: the same information as About in the Android app (author, license and source, components,
  // trademark)
  function aboutSheet(st) {
    const link = (url, text) => h('a', { href: url, target: '_blank', rel: 'noopener noreferrer' }, text || url);
    const part = (title, ...kids) => [h('h3', { class: 'section' }, title), ...kids];
    const close = dialog(T('About', 'Informazioni'), [h('div', { class: 'scroll about' },
      h('p', { class: 'strong' }, 'Zweep Web ' + (st.version || '')),
      h('p', { class: 'muted' }, T('Your Zabbix alert radar', 'Il radar dei tuoi allarmi Zabbix')),
      ...part(T('Author', 'Autore'),
        h('p', { class: 'center' }, T('Zweep is designed and developed by:', 'Zweep è ideato e sviluppato da:')),
        h('p', { class: 'center strong' }, AUTHOR_NAME),
        h('p', { class: 'center' }, link(AUTHOR_URL, 'github.com/N1k0droid')),
        h('p', { class: 'center muted small' }, T('Is Zweep useful to you? A ⭐ on GitHub and a follow help the project grow.',
          'Zweep ti è utile? Una ⭐ su GitHub e un follow aiutano il progetto a crescere.'))),
      ...part(T('License', 'Licenza'),
        h('p', {}, T('Zweep is free software: GNU Affero General Public License v3.0 only. You can get, study and modify the source code.',
          'Zweep è software libero: GNU Affero General Public License v3.0 (solo). Puoi ottenere, studiare e modificare il codice sorgente.')),
        h('p', {}, T('Source code: ', 'Codice sorgente: '), link(SOURCE_URL))),
      ...part(T('Open source components', 'Componenti open source'),
        h('p', {}, T('This page uses no external libraries. The server and the web client plugin: ', 'Questa pagina non usa librerie esterne. Il server e il plugin del client web: '),
          link(SOURCE_URL + '/blob/main/THIRD_PARTY_NOTICES.md', 'THIRD_PARTY_NOTICES.md'))),
      ...part(T('Trademarks', 'Marchi'),
        h('p', { class: 'muted small' }, T('Zabbix is a trademark of Zabbix LLC. Zweep is an independent product, not affiliated with or endorsed by Zabbix LLC.',
          'Zabbix è un marchio di Zabbix LLC. Zweep è un prodotto indipendente, non affiliato né approvato da Zabbix LLC.'))))],
    [textButton(T('Close', 'Chiudi'), () => close())], null, 'sheet');
  }

  // The Zweep logo of the strip of pull to refresh, as in the app: small, the red disc with its Z, the
  // ring of the radar, the six alarms at their own sizes. shown 0..1: how much of it is there while the
  // list is pulled (it grows, the alarms appear one by one). pulse: milliseconds since the pulse began,
  // negative when still: a wave leaves the disc and the red bands of the logo appear behind it, each
  // alarm lights up with its halo as the wave reaches it, the whole logo stays a moment, then the bands
  // and the halos fade. A halo never comes near the disc of the Z. Units: those of the drawing of the logo.
  const PULSE_MS = 1500; // one pulse in all: the wave goes out in 850 ms, the logo stays whole, then fades from 1050 ms
  const LOGO_ALARMS = [[121.9, -156.0, 15, 1.00, 45.0, 0.52], [-162.8, -94.0, 11.5, 0.88, 32.0, 0.45], [-126.6, 122.3, 9, 0.75, 23.0, 0.39],
    [152.8, 74.5, 7, 0.67, 16.8, 0.34], [-23.1, -164.4, 5, 0.59, 11.3, 0.30], [53.0, 212.5, 4, 0.54, 8.6, 0.27]];
  const LOGO_BANDS = [[103.5, 140, 0.42], [140, 168, 0.31], [168, 198, 0.21], [198, 227, 0.11]];
  const LOGO_Z = [[-40, -48], [40, -48], [40, -31], [-15.38, 31], [40, 31], [40, 48], [-40, 48], [-40, 31], [15.38, -31], [-40, -31]];
  function drawLogo(ctx, size, shown, pulse) {
    const c = size / 2;
    const u = size / 464 * (0.6 + 0.4 * shown);
    const unit = (v) => Math.min(1, Math.max(0, v));
    const circle = (r, width, color) => { ctx.beginPath(); ctx.arc(c, c, r, 0, Math.PI * 2); ctx.lineWidth = width; ctx.strokeStyle = color; ctx.stroke(); };
    ctx.clearRect(0, 0, size, size);
    let lit = LOGO_ALARMS.map((_, i) => (shown * LOGO_ALARMS.length > i ? 1 : 0));
    if (pulse >= 0) {
      const p = pulse % PULSE_MS;
      const f = 1 - Math.pow(1 - unit(p / 850), 2); // quick at first, then slower
      const front = 103.5 + (227 - 103.5) * f;
      const fade = 1 - unit((p - 1050) / 400);
      // The trail: each band is there as far as the wave has gone
      LOGO_BANDS.forEach(([from, to, op]) => { if (front > from) { const end = Math.min(to, front); circle((from + end) / 2 * u, (end - from) * u, `rgba(229,57,47,${op * fade})`); } });
      if (f < 1) circle(front * u, Math.max(3 * u, 0.8), `rgba(229,57,47,${0.5 * (1 - 0.6 * f)})`);
      lit = LOGO_ALARMS.map(([x, y]) => unit((front - Math.hypot(x, y) + 12) / 24) * fade);
    }
    circle(160 * u, Math.max(2.5 * u, 0.75), `rgba(255,255,255,${0.35 * shown})`);
    LOGO_ALARMS.forEach(([dx, dy, r, op, halo, haloOp], i) => {
      const x = c + dx * u; const y = c + dy * u; const on = lit[i];
      if (pulse >= 0 && on > 0) {
        const reach = Math.min(halo * (1 + 0.25 * on), Math.hypot(dx, dy) - 111.5 - 18) * u; // well before the disc of the Z
        const g = ctx.createRadialGradient(x, y, 0, x, y, reach);
        g.addColorStop(0, `rgba(255,106,58,${haloOp * on * shown})`); g.addColorStop(1, 'rgba(211,47,46,0)');
        ctx.fillStyle = g; ctx.beginPath(); ctx.arc(x, y, reach, 0, Math.PI * 2); ctx.fill();
      }
      ctx.fillStyle = `rgba(229,57,47,${op * (0.35 + 0.65 * on) * shown})`;
      ctx.beginPath(); ctx.arc(x, y, Math.max(r * u, 0.7), 0, Math.PI * 2); ctx.fill();
    });
    ctx.fillStyle = `rgba(211,47,46,${0.4 + 0.6 * shown})`;
    ctx.beginPath(); ctx.arc(c, c, 111.5 * u, 0, Math.PI * 2); ctx.fill();
    ctx.fillStyle = `rgba(255,255,255,${0.4 + 0.6 * shown})`;
    ctx.beginPath(); LOGO_Z.forEach(([x, y], i) => (i ? ctx.lineTo(c + x * u, c + y * u) : ctx.moveTo(c + x * u, c + y * u))); ctx.closePath(); ctx.fill();
  }

  (() => {
    const main = $('main');
    const pull = $('pull');
    let y0 = null;
    let dy = 0;
    let busy = false;
    const show = (px) => { pull.style.height = px + 'px'; pull.hidden = px <= 0; };
    // The logo is drawn on a canvas of 52 CSS pixels, at the density of the screen
    const SIZE = 52;
    const ratio = Math.min(window.devicePixelRatio || 1, 3);
    const cv = h('canvas', { width: String(Math.round(SIZE * ratio)), height: String(Math.round(SIZE * ratio)) });
    pull.replaceChildren(cv);
    const logo = (shown, pulse) => { const ctx = cv.getContext('2d'); ctx.setTransform(ratio, 0, 0, ratio, 0, 0); drawLogo(ctx, SIZE, shown, pulse); };
    // Pulses while fn works, then to the end of the pulse it is in: seen, and never cut short. It moves
    // also where animations are reduced (many devices are set so): the pulse is what says that the
    // refresh is happening, a second and a half of it, not an ornament.
    const pulseWhile = async (fn) => {
      const start = performance.now();
      let on = true;
      const tick = () => { if (!on) return; logo(1, performance.now() - start); requestAnimationFrame(tick); };
      requestAnimationFrame(tick);
      try { await fn(); } catch (e) { /* the list says what went wrong */ }
      const spent = performance.now() - start;
      await new Promise((ok) => setTimeout(ok, PULSE_MS - spent % PULSE_MS));
      on = false;
    };
    main.addEventListener('touchstart', (e) => { y0 = !busy && view.pull && !view.form && main.scrollTop <= 0 && e.touches.length === 1 ? e.touches[0].clientY : null; dy = 0; }, { passive: true });
    main.addEventListener('touchmove', (e) => {
      if (y0 === null) return;
      dy = e.touches[0].clientY - y0;
      if (dy <= 0 || main.scrollTop > 0) { y0 = null; show(0); return; }
      logo(Math.min(dy / 140, 1), -1);
      show(Math.min(dy / 2.5, 64));
    }, { passive: true });
    main.addEventListener('touchend', async () => {
      if (y0 === null) return;
      y0 = null;
      const fn = view.pull;
      if (dy <= 140 || !fn) { show(0); return; }
      busy = true;
      show(64);
      await pulseWhile(fn);
      show(0);
      busy = false;
    });
  })();

  function app(st) {
    view.st = st;
    // The number on the icon: open alarms not read yet. iOS changes it only with a notification or from here.
    try {
      if (navigator.setAppBadge) { if (st.badge > 0) navigator.setAppBadge(st.badge).catch(() => {}); else navigator.clearAppBadge().catch(() => {}); }
    } catch (e) { /* not an installed app */ }
    ensurePush(st);
    if (!st.problems && view.tab === 'problems') view.tab = 'alerts';
    // Opened from a notification: that alarm was seen, and its detail opens as in the app
    const a = view.open || new URLSearchParams(location.search).get('a');
    view.open = '';
    if (a) {
      history.replaceState(null, '', '/');
      const m = (st.alerts || []).find((x) => x.id === a);
      if (m) {
        const e = events(st).find((x) => x.sid === m.sid);
        if (view.mark) dropMarker(view.mark);
        view.tab = 'alerts'; view.detail = { sid: e.sid, source: e.source, eventid: e.eventid, from: 'alerts' };
        view.mark = {}; pushMarker(view.mark);
      }
    }
    if (view.tab === 'settings') view.detail = view.detail && !wide() ? view.detail : null;
    if (view.detail && !wide()) { detailScreen(st); return; }
    if (view.tab === 'problems') problemsScreen(st);
    else if (view.tab === 'settings') settingsScreen(st);
    else alertsScreen(st);
    if (!wide() || view.tab === 'settings') return;
    // Side by side: the list stays, the detail of the chosen row is on the right. A refresh of the list
    // leaves an open detail as it is (what is being written in it, too).
    const side = $('side');
    side.hidden = false;
    if (!view.detail) {
      view.sideSid = null;
      side.replaceChildren(empty(view.tab === 'problems' ? T('Select a problem', 'Seleziona un problem') : T('Select an alert', 'Seleziona un allarme'),
        T('Its details and actions appear here.', 'Qui compaiono i dettagli e le azioni.')));
    } else if (view.sideSid !== view.detail.sid) detailScreen(st, true);
  }

  // Back to the screens before the activation (logged out, turned off)
  function plain() {
    if (view.mark) { dropMarker(view.mark); view.mark = null; }
    document.body.classList.remove('app');
    $('top').hidden = false; $('foot').hidden = false;
    document.body.classList.remove('wide', 'single');
    $('bar').hidden = true; $('strip').hidden = true; $('nav').hidden = true; $('side').hidden = true; $('side').replaceChildren();
    view.detail = null; view.tab = 'alerts';
  }

  async function start() {
    let st;
    try { st = await api('GET', '/api/state'); } catch (e) {
      if (e.message === 'network_not_allowed') {
        show(h('section', { class: 'card warn' }, h('h2', {}, T('Not allowed from this network', 'Non consentito da questa rete')),
          h('p', {}, T('The administrator allows the web client only from some networks (for example the office or the VPN). Alarms still reach this device; to open them, connect to an allowed network.',
            'L’amministratore consente il client web solo da alcune reti (per esempio l’ufficio o la VPN). Gli allarmi arrivano comunque a questo dispositivo; per aprirli, collegati a una rete consentita.'))));
        return;
      }
      if (document.body.classList.contains('app')) return; // keep what is on screen: the next refresh tries again
      show(card(T('No connection', 'Nessuna connessione'), h('p', {}, T('The server does not answer. Check the network and reopen the app.', 'Il server non risponde. Controlla la rete e riapri l\'app.'))));
      return;
    }
    $('foot').textContent = 'Zweep Web';
    // The server was updated while this page stayed open (an installed app may stay open for days): load
    // the new page, unless the user is in the middle of something. The version is told to activated
    // clients only.
    if (st.version) {
      if (!view.version) view.version = st.version;
      else if (view.version !== st.version && idle() && !document.querySelector('dialog[open]') &&
        ![...document.querySelectorAll('textarea')].some((x) => x.value)) { location.reload(); return; }
    }
    if (st.enabled && st.activated) { app(st); return; }
    plain();
    if (!st.enabled) { disabled(); return; }
    $('who').textContent = '';
    const v = isIOS ? iosVersion() : null;
    if (v && (v[0] < 16 || (v[0] === 16 && v[1] < 4))) {
      unsupported(T('Web notifications on iPhone and iPad need iOS 16.4 or later. Update the system, then open this page again.',
        'Le notifiche web su iPhone e iPad richiedono iOS 16.4 o successivo. Aggiorna il sistema, poi riapri questa pagina.'));
      return;
    }
    if (isIOS && !standalone) { install(); return; }
    if (!('serviceWorker' in navigator) || !('PushManager' in window)) {
      unsupported(T('This browser does not support web notifications. Use Safari on iPhone, iPad or Mac, or Chrome, Edge or Firefox on a computer.',
        'Questo browser non supporta le notifiche web. Usa Safari su iPhone, iPad o Mac, oppure Chrome, Edge o Firefox su un computer.'));
      return;
    }
    const code = new URLSearchParams(location.search).get('code');
    if (androidUndecided()) { androidChoice(() => activate(code, st, false)); return; }
    activate(code, st, false);
  }

  if (navigator.serviceWorker) navigator.serviceWorker.addEventListener('message', (e) => { if (e.data && 'open' in e.data) { if (e.data.open) view.open = e.data.open; if (!view.form) start(); } });
  // Refreshed when it comes back to the front and every half minute, unless the user is writing
  const idle = () => !view.form && !/^(INPUT|TEXTAREA|SELECT)$/.test((document.activeElement || {}).tagName || '');
  // Before the activation a message on screen (how to allow notifications, a refusal) stays there when the
  // user comes back from the settings of the phone
  const told = () => !document.body.classList.contains('app') && [...document.querySelectorAll('#main .msg')].some((x) => x.textContent);
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible' && idle() && !told()) start(); });
  setInterval(() => { if (document.visibilityState === 'visible' && document.body.classList.contains('app') && idle() && (!view.detail || wide())) start(); }, 30000);
  // The window changed size (or the tablet was turned): the same content in the other layout
  wideQuery.addEventListener('change', () => { if (document.body.classList.contains('app') && !view.form) { view.sideSid = null; app(view.st); } });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !view.form && view.detail && wide()) { const mark = view.mark; view.detail = null; view.mark = null; if (mark) dropMarker(mark); app(view.st); }
  });
  start();
})();
