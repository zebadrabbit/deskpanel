// The only part of the page that talks to the aggregator. The flasher site's demo replaces this file with
// backend.demo.js, which has the same exports.
export const demo = false;

const KEY = 'deskpanel-editor-password';
let token = '';
try { token = localStorage.getItem(KEY) || ''; } catch { /* private window: ask every time */ }

// ask is fetch, with "no answer at all" as an error of its own (offline), so a bug never reads as a network failure.
async function ask(path, init) {
  try {
    return await fetch('/api/v1/editor/' + path, {cache: 'no-store', ...init, headers: {Authorization: 'Bearer ' + token, ...init?.headers}});
  } catch {
    throw Object.assign(new Error("Can't reach the server."), {offline: true});
  }
}

// call sends the password as the Bearer token. A 401 anywhere forgets it and tells the page to show the gate.
async function call(method, path, body, headers = {}) {
  const r = await ask(path, {method, body, headers});
  if (r.status === 401) {
    lock();
    dispatchEvent(new Event('editor-locked'));
    throw new Error('locked');
  }
  return r;
}

// reply is the server's JSON, or {message: its text} when it sent none.
async function reply(r) {
  const t = await r.text();
  try { return JSON.parse(t); } catch { return {message: t.trim() || `${r.status} ${r.statusText}`}; }
}
async function json(r) {
  const d = await reply(r);
  if (!r.ok) throw new Error(d.message || `${r.status} ${r.statusText}`);
  return d;
}

// unlock checks a password (or the remembered one, with none given) and keeps it when right. It throws when the
// server can't be asked at all, so the gate can tell "wrong password" from "no answer".
export async function unlock(password) {
  if (password !== undefined) token = password;
  if (!token) return false;
  const r = await ask('ping');
  if (r.status === 401) {
    lock();
    return false;
  }
  if (r.status !== 204) throw new Error((await reply(r)).message);
  try { localStorage.setItem(KEY, token); } catch { /* kept for this page only */ }
  return true;
}

export function lock() {
  token = '';
  try { localStorage.removeItem(KEY); } catch { /* nothing kept */ }
}

export async function loadLayout() {
  const r = await call('GET', 'layout');
  const d = await json(r);
  if (!Array.isArray(d.layout?.tabs)) throw new Error('layout not ready yet');   // as the server's 503 says it
  return {layout: d.layout, etag: r.headers.get('ETag'), fromFile: !!d.from_file, error: d.error || ''};
}

export async function saveLayout(layout, etag) {
  const r = await call('PUT', 'layout', JSON.stringify(layout), {'Content-Type': 'application/json', 'If-Match': etag});
  const d = await reply(r);
  if (r.ok) return {ok: true, etag: d.etag};
  return {ok: false, status: r.status, message: d.message || `${r.status} ${r.statusText}`, tab: d.tab ?? -1, card: d.card ?? -1};
}

export async function sources() {
  return json(await call('GET', 'sources'));
}

// test is the card as /cards would serve it, or {error}; busy marks a 429 (another test running, or one a second).
export async function test(spec) {
  const r = await call('POST', 'test', JSON.stringify(spec), {'Content-Type': 'application/json'});
  const d = await reply(r);
  return r.ok ? d : {error: d.error || d.message, busy: r.status === 429};
}

export async function preview() {
  return json(await call('GET', 'preview'));
}
