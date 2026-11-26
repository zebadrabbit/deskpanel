// The screen editor's views: the panel preview, the inspector and the Tour & clock view. Everything from the
// server comes through backend.js (the flasher site's demo swaps in backend.demo.js). The rules the page
// enforces while you drag and type come from sources(); a save the server refuses shows the server's own
// words, on the card it names. The look and the interactions are the approved mockup's.
// ponytail: module globals and a full re-render after each change, as in the mockup; fast enough for 6 x 24 cards.
import * as api from './backend.js';
import * as M from './model.js';

const $ = id => document.getElementById(id);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
const cap = s => s ? s[0].toUpperCase() + s.slice(1) : s;
const CW = 197, RH = 61;   // column and row pitch on the 800x480 panel
const B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';   // the cards document's graph points

// Words and pictures only. Which types exist and their sizes, taps and sources come from the server.
const WHAT = {
  gpu: 'A GPU: temperature, VRAM, state', pool: 'A storage pool: health and usage', 'nas-alerts': 'NAS alert count and level',
  dots: 'Tailscale and service dots', board: 'The Planka boards. Fills a tab', radar: 'The weather radar. Fills a tab',
  number: 'One number, with a sub-line', gauge: 'An arc filled to the value', bar: 'A bar filled to the value',
  graph: 'The last hour, one or two lines', status: 'A dot: up or down',
  light: 'A light or switch: tap to toggle', profile: 'A button that sets every light',
};
const GLYPH = {
  gpu: '<rect x="3" y="7" width="18" height="10" rx="2"/><circle cx="9" cy="12" r="2.5"/><path d="M15 10h3M15 14h3"/>',
  pool: '<ellipse cx="12" cy="7" rx="7" ry="3"/><path d="M5 7v10c0 1.7 3.1 3 7 3s7-1.3 7-3V7M5 12c0 1.7 3.1 3 7 3s7-1.3 7-3"/>',
  'nas-alerts': '<path d="M12 4l9 16H3z"/><path d="M12 10v5M12 17.5v.5"/>',
  dots: '<circle cx="5" cy="12" r="2"/><circle cx="12" cy="12" r="2"/><circle cx="19" cy="12" r="2"/>',
  board: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16M15 4v16"/>',
  radar: '<circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3.5"/><path d="M12 12l6-5"/>',
  number: '<path d="M6 8l3-2v12M14 9a3 3 0 115 2l-5 7h6"/>',
  gauge: '<path d="M5.5 18a8.5 8.5 0 1113 0"/><path d="M12 13l4-4"/>',
  bar: '<rect x="3" y="9" width="18" height="6" rx="3"/><path d="M5 12h8" stroke-width="3"/>',
  graph: '<path d="M3 17l5-6 4 3 4-7 5 4"/><path d="M3 20h18"/>',
  status: '<circle cx="12" cy="12" r="5" fill="currentColor"/>',
  light: '<path d="M12 3a6 6 0 00-4 10.5c.7.7 1 1.5 1 2.5h6c0-1 .3-1.8 1-2.5A6 6 0 0012 3z"/><path d="M9.5 19h5M10.5 21.5h3"/>',
  profile: '<path d="M4 7h10M18 7h2M4 12h4M12 12h8M4 17h10M18 17h2"/><circle cx="16" cy="7" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="16" cy="17" r="2"/>',
  other: '<rect x="4" y="4" width="16" height="16" rx="3"/>',
};
// What the panel does with a touch on a card that handles its own.
const OWN_TAP = {
  light: 'Tap toggles it. Hold for brightness and colour.',
  profile: 'Tap applies it to every light card. Hold the button on the panel to save the current lights.',
};
const TAP_WORDS = {none: 'does nothing', notifications: 'opens the notification history', 'alert-details': 'opens every NAS alert in full'};
const SLOT_WORDS = {temp: 'temperature', util: 'utilisation', vram: 'VRAM', power: 'power', fan: 'fan', procs: 'processes'};
const KINDS = [['sc', 'Shortcut'], ['prom', 'Prometheus'], ['ha', 'Home Assistant'], ['json', 'JSON page']];
const ENV_EXAMPLE = {PROMETHEUS_URL: 'http://<prometheus-host>:9090', HA_URL: 'http://<ha-host>:8123', HA_TOKEN: '<a long-lived token>'};
const CUT = '<p class="hint">The list is cut; type the name if yours is missing.</p>';

let S = null;                                  // the layout being edited, in the source form
let src = null;                                // the server's tables and lists: sources()
let live = {panel: {}, cards: {cards: {}}};    // the server's current values: preview()
let saved = '', etag = '', fromFile = true, liveBase = {}, tested = {}, saving = false;
let cur = 0, sel = null, view = 'screens', K = 1;
let undo = [], redo = [], lastKey = null, refused = null, loadNote = '', drag = null, noClick = false, ui = {};
let shown = {refused: null, info: null};       // what the two live regions last said: rewritten only on a change
let testing = false;                           // a Test is out, or answered under a second ago (the server takes one a second)

const sizes = type => M.sizesFor(src, type);   // the sizes the server lists for a type, the default first

const tab = () => S.tabs[cur];
const card = id => tab().cards.find(c => c.id === id);
const allCards = () => S.tabs.flatMap(t => t.cards);
const T = type => src.types[type] ?? {sizes: [], src: 'none', general: false, tap: false, default_tap: 'none'};
const lim = k => src.limits[k];
const source = () => M.toSource(S, src);
const dirty = () => JSON.stringify(source()) !== saved;
const prom = () => ({instances: [], mounts: [], metrics: [], ...src.prometheus});
const ha = () => ({entities: [], ...src.ha});
// ents is the entities a card of this type may read: all of them, or the domains the server lists for it (a light).
const ents = type => { const d = T(type).entity; return d ? ha().entities.filter(e => d.includes(e.id.split('.')[0])) : ha().entities; };
const titled = type => T(type).general || type === 'light' || type === 'profile';
// fitTitle is an entity's name cut to the title's byte limit, so a filled-in title is one the server takes.
function fitTitle(s) { s = String(s ?? ''); while (M.bytes(s) > lim('title')) s = s.slice(0, -1); return s; }
const byArea = (a, b) => M.wh(a)[0] * M.wh(a)[1] - M.wh(b)[0] * M.wh(b)[1];

// ---------- undo ----------
function snap(key, before) {   // remember the state before a change; typing in one field is one step
  if (key && key === lastKey) return;
  undo.push(before ?? JSON.stringify(S));
  if (undo.length > 100) undo.shift();
  redo = [];
  lastKey = key || null;
}
function restore(json) {
  S = JSON.parse(json);
  lastKey = null;
  cur = Math.min(cur, S.tabs.length - 1);
  if (!card(sel)) sel = null;
  ui = {};
  render();
}
function say(msg, bad) { $('hint').textContent = msg; $('hint').className = bad ? 'bad' : ''; }

// ---------- values, as the inspector edits them ----------
const vals = c => Array.isArray(c.value) ? c.value : [c.value];
function setVal(c, i, v) { if (Array.isArray(c.value)) c.value[i] = v; else c.value = v; }
function kindOf(v) {
  if (typeof v === 'string') return 'sc';
  if (v && 'ha' in v) return 'ha';
  if (v && 'json' in v) return 'json';
  return 'prom';
}
// parseSc reads a shortcut as the server does: name[:arg][@instance], the instance after the last '@'.
function parseSc(s) {
  const at = s.lastIndexOf('@'), head = at < 0 ? s : s.slice(0, at), colon = head.indexOf(':');
  return {name: colon < 0 ? head : head.slice(0, colon), arg: colon < 0 ? '' : head.slice(colon + 1), inst: at < 0 ? '' : s.slice(at + 1)};
}
const scText = (name, arg, inst) => name + (arg ? ':' + arg : '') + (inst ? '@' + inst : '');
const scName = v => kindOf(v) === 'sc' ? parseSc(v).name : '';
const shortcut = name => src.shortcuts.find(s => s.name === name);
const shortcutsFor = type => src.shortcuts.filter(s => s.types.includes(type));
const firstArg = s => s.arg === 'mount' ? prom().mounts[0] || '/' : '';

// newValue is a starting value of a kind for a card type: a shortcut that suits it (a machine picked when it
// needs one), else a query.
function newValue(type, kind, skip = []) {
  if (kind === 'sc') {
    const s = shortcutsFor(type).find(x => !skip.includes(x.name));
    if (s) return scText(s.name, firstArg(s), s.instance === 'required' ? prom().instances[0] || '' : '');
  }
  if (kind === 'ha') return {ha: ha().entities[0]?.id ?? ''};
  if (kind === 'json') return {json: {url: '', path: ''}};
  return {prometheus: 'up'};
}
// ponytail: which fields a general card uses (limits: unit, warn, bad, below; scale: min, max), and that only a graph
// takes two values and net takes a graph of its own, are the README's (spec item 6); sources has no table for them.
// A field that has a value is shown anyway, with why it doesn't apply; the server's message covers a slip.
function takes(c) {
  return {limits: T(c.type).general && c.type !== 'status', scale: c.type === 'graph' || ((c.type === 'gauge' || c.type === 'bar') && kindOf(vals(c)[0]) !== 'sc')};
}
const listSkip = c => Array.isArray(c.value) ? ['net', ...vals(c).map(scName)] : [];

function slotsOf(c) {
  const d = src.slots.default, s = c.slots ?? {};
  return {gauge: s.gauge || d.gauge, line1: {value: s.line1?.value || d.line1.value, as: s.line1?.as || d.line1.as}, line2: [...(s.line2 ?? d.line2)]};
}

// ---------- what each card reads ----------
// The server's value for a card is shown while the card still asks for what the server's layout asked for, a
// tested one while it asks for what was tested; otherwise a dash until Test or Save.
function liveKey(c) {
  const n = M.toSource({tabs: [{name: '', cards: [c]}]}, src).tabs[0].cards[0];
  return JSON.stringify([n.type, n.src, n.value, n.unit, n.min, n.max, n.warn, n.bad, n.below, n.slots]);
}
function setLiveBase(l) {
  liveBase = {};
  for (const t of l.tabs) for (const c of t.cards) liveBase[c.id] = liveKey(c);
}
function reading(c) {
  const k = liveKey(c), t = tested[c.id], general = T(c.type).general;
  if (general && t && t.key === k) return t.out;
  if (liveBase[c.id] === k) return live.cards?.cards?.[c.id] ?? (general ? {st: 'none', sub: 'no reading yet'} : {});
  return general ? {st: 'none', sub: 'Test or Save'} : {};
}
const H = () => live.panel?.homelab ?? {};
function gpuFor(key) {   // the panel names a host's second GPU host#2, as the gpu card's src does
  const per = {};
  for (const g of H().gpu ?? []) {
    if (!g.host) continue;
    per[g.host] = (per[g.host] || 0) + 1;
    if ((per[g.host] > 1 ? g.host + '#' + per[g.host] : g.host) === key) return g;
  }
  return null;
}
const poolFor = name => (H().nas?.pools ?? []).find(p => p.n === name);
function missing(c) {
  if (T(c.type).src === 'gpu' && !src.gpus.some(g => g.src === c.src)) return `no gpu '${c.src ?? ''}'`;
  if (T(c.type).src === 'pool' && !src.pools.some(p => p.name === c.src)) return `no pool '${c.src ?? ''}'`;
  return '';
}
function alertLevel() {
  const real = (H().nas?.alerts ?? []).filter(a => !['INFO', 'NOTICE'].includes(a.lvl));
  return !real.length ? '' : /CRIT|ALERT|EMERG|ERR/.test(real[0].lvl) ? 'bad' : 'warn';
}

// ---------- drawing a card as the panel would, roughly ----------
// CSP allows no style attributes, so sizes travel as data-* and styleBoxes sets them through the CSSOM.
function place(el, col, row, w, h) {
  Object.assign(el.style, {left: (col - 1) * CW + 'px', top: (row - 1) * RH + 'px', width: w * CW - 12 + 'px', height: h * RH - 11 + 'px'});
}
function styleBoxes(root) {
  for (const el of root.querySelectorAll('[data-box]')) place(el, ...el.dataset.box.split(',').map(Number));
  for (const el of root.querySelectorAll('[data-pct]')) el.style.width = el.dataset.pct + '%';
  for (const el of root.querySelectorAll('[data-col]')) el.style.color = '#' + el.dataset.col;
  // a tile redrawn in place (a resize drag) gets its own edge colour back unless its light is still on
  for (const pc of [root.closest('.pc'), ...root.querySelectorAll('.pc')]) if (pc) pc.style.borderColor = '';
  for (const el of root.querySelectorAll('[data-fill]')) {   // a lit light: the fill's height, and its colour on the tile's edge
    el.style.height = el.dataset.fill + '%';
    const pc = el.closest('.pc');
    if (pc) pc.style.borderColor = '#' + (el.dataset.col ?? 'd8b56a');
  }
  for (const el of root.querySelectorAll('[data-d]')) {
    const d = +el.dataset.d;
    el.style.width = el.style.height = d + 'px';
    el.style.fontSize = (d > 80 ? 24 : 18) + 'px';
  }
}
const clamp = pct => Math.max(0, Math.min(100, +pct || 0));
function ring(pct, txt, cls, d) {
  const C = 2 * Math.PI * 40, a = C * .75, f = a * clamp(pct) / 100;
  const arc = (len, col) => `<circle cx="50" cy="50" r="40" fill="none" stroke="${col}" stroke-width="9" stroke-linecap="round" stroke-dasharray="${len} ${C}" transform="rotate(135 50 50)"/>`;
  return `<div class="ring ${cls}" data-d="${d}"><svg viewBox="0 0 100 100" aria-hidden="true">${arc(a, '#2a303b')}${f > 1 ? arc(f, 'currentColor') : ''}</svg><span>${txt}</span></div>`;
}
const pbar = (pct, cls) => `<div class="pbar ${cls || 'blue'}"><i data-pct="${clamp(pct).toFixed(1)}"></i></div>`;
const decode = s => [...String(s)].map(ch => Math.max(0, B64.indexOf(ch)));
// spark draws lines of 0..63 points, a short one right-aligned as the panel does, and the warn line th.
function spark(lines, th) {
  const y = v => (38 - 36 * Math.max(0, Math.min(1, v / 63))).toFixed(1), x = (i, n) => ((60 - n + i) * 100 / 59).toFixed(1);
  return '<svg class="spark" viewBox="0 0 100 40" preserveAspectRatio="none" aria-hidden="true">' +
    (th != null ? `<line x1="0" x2="100" y1="${y(th)}" y2="${y(th)}" stroke="#d29922" stroke-dasharray="3 3"/>` : '') +
    lines.map(l => `<polyline stroke="${l.col}" points="${l.pts.map((v, i) => x(i, l.pts.length) + ',' + y(v)).join(' ')}"/>`).join('') + '</svg>';
}
const gb = mib => (mib / 1024).toFixed(1);
const size = gib => gib >= 1024 ? (gib / 1024).toFixed(1) + ' TiB' : gib + ' GiB';

function gpuBody(c, w, h) {
  const g = gpuFor(c.src), o = reading(c), sl = slotsOf(c);
  if (!g) return `<div class="mid">${esc(c.src)}</div><div class="sub">no gpu '${esc(c.src)}'</div>`;
  const val = k => ({
    temp: ['Temp', g.temp + '°', g.temp], util: ['Util', g.util + '%', g.util],
    vram: ['VRAM', gb(g.vram) + ' / ' + Math.round(g.vram_t / 1024) + ' GB', g.vram_t ? g.vram / g.vram_t * 100 : 0],
    power: ['Power', g.pwr + ' W', null], fan: ['Fan', o.v || '--', o.f != null ? o.f * 100 : null],
    procs: ['', g.procs + (g.procs === 1 ? ' proc' : ' procs'), null],
  }[k] ?? [k, '', null]);
  const st = (g.up ? g.st || '' : 'off').toUpperCase(), gz = val(sl.gauge), gtxt = sl.gauge === 'vram' ? Math.round(gz[2]) + '%' : gz[1];
  if (w === 1 && h === 1) return `<div class="rowc"><span class="tt">${esc(c.src)}</span><span class="mid">${esc(gtxt)}</span></div>`;
  const pill = w >= 2 || h === 3 ? `<span class="statepill ${esc(st)}">${esc(st)}</span>` : `<span class="sub small">${esc(st.toLowerCase())}</span>`;
  const head = `<div class="hd"><span><span class="${w >= 2 ? 'mid' : ''}">${esc(c.src)}</span>${w >= 2 ? ` <span class="sub">${esc(g.n)}</span>` : ''}</span>${pill}</div>`;
  const l1 = val(sl.line1.value);
  let as = sl.line1.as;
  if (as === 'graph' && w === 1 && h === 2) as = 'bar';
  const line1 = `<div class="sub">${esc(l1[0])} <span class="hi">${esc(l1[1])}</span></div>` +
    (as === 'bar' ? pbar(l1[2]) : as === 'graph' ? `<div class="gline${h === 3 ? ' tall' : ''}">${spark(o.g?.[0] ? [{pts: decode(o.g[0]), col: '#58a6ff'}] : [])}</div>` : '');
  const line2 = w >= 2 && !(h === 2 && as === 'graph') && sl.line2.length ? `<div class="mid">${sl.line2.map(k => esc(val(k)[1])).join(' &nbsp;•&nbsp; ')}</div>` : '';
  const gauge = h === 3 ? ring(gz[2], esc(gtxt), 'blue', w >= 2 ? 96 : 80) : `<div class="big">${esc(gtxt)}</div>`;
  if (w === 1) return head + (h === 3 ? `<div class="center">${gauge}</div>` : gauge) + line1;
  return head + `<div class="gp">${gauge}<div>${line1}${line2}</div></div>`;
}
function poolBody(c, w, h) {
  const p = poolFor(c.src);
  if (!p) return `<div class="tt">${esc(c.src)}</div><div class="sub">no pool '${esc(c.src)}'</div>`;
  const all = p.used + p.avail, pct = all ? p.used / all * 100 : 0;
  return h === 1 ? `<div class="hd"><span class="tt">${esc(p.n)}</span><span>${Math.round(pct)} %</span></div>${pbar(pct)}`
    : `<div class="hd"><span class="tt">${esc(p.n)}</span><span class="${p.ok ? 'ok' : 'bad'}">${esc(p.h)}</span></div>${pbar(pct)}<div class="${w === 2 ? 'big ' : ''}nowrap">${size(p.used)} / ${size(all)}</div>`;
}
function alertsBody(h) {
  const n = H().nas;
  if (!n) return `<div class="cap">NAS</div><div class="sub">no NAS data</div>`;
  const real = (n.alerts ?? []).filter(a => !['INFO', 'NOTICE'].includes(a.lvl)), L = alertLevel() || 'ok';
  const word = real.length ? `${real.length} ${real[0].lvl}` : 'OK';
  return h === 1 ? `<div class="rowc"><span class="cap">NAS</span><span class="${L}">${esc(word)}</span></div>`
    : `<div class="hd"><span class="cap">NAS ALERTS</span>${n.info ? `<span class="vio small">+${esc(n.info)} info</span>` : ''}</div><div class="mid ${L}">${esc(word)}</div>`;
}
function dotsBody(c, w, h) {
  const dot = x => `<span><i class="dot${x.on ? '' : ' down'}"></i>${esc(x.n)}</span>`;
  const row = (name, list, n) => `<span class="cap">${name}</span>${list.slice(0, n).map(dot).join('')}${list.length > n ? `<span class="sub">+${list.length - n}</span>` : ''}`;
  const ts = H().ts ?? [], sv = H().svc ?? [], few = w === 2 ? 1 : 3, which = c.src;   // unset: the server's default, both
  const rows = which === 'tailscale' ? [row('TAILSCALE', ts, 2 * few)] : which === 'services' ? [row('SERVICES', sv, 2 * few)]
    : h === 2 ? [row('TAILSCALE', ts, 2 * few), row('SERVICES', sv, 2 * few)] : [row(few === 1 ? 'TS' : 'TAILSCALE', ts, few) + row(few === 1 ? 'SVC' : 'SERVICES', sv, few)];
  return rows.map(r => `<div class="dotsrow">${r}</div>`).join('');
}
function boardBody() {
  const boards = live.panel?.planka?.boards ?? [];
  if (!boards.length) return '<div class="mid">Planka boards</div>';
  return `<div class="kan">${boards.slice(0, 3).map(b => `<div><span class="cap">${esc(b.n)}</span>${(b.cards ?? []).map(k => `<i>${esc(k.n)}</i>`).join('')}</div>`).join('')}</div>`;
}
function genBody(c, w, h) {
  const o = reading(c), t = esc(c.title || c.id), none = o.st === 'none';
  const L = o.st === 'warn' || o.st === 'bad' ? o.st : '';
  const val = none ? '—' : esc(o.v ?? ''), sub = esc(o.sub ?? ''), pct = (o.f ?? 0) * 100;
  switch (c.type) {
    case 'number': return w === 1 && h === 1
      ? `<div class="rowc"><span class="tt">${t}</span>${none ? `<span class="sub small right">— ${sub}</span>` : `<span class="mid ${L} nowrap">${val}</span>`}</div>`
      : `<div class="tt">${t}</div><div class="big ${L}${w === 2 ? ' huge' : ''}">${val}</div><div class="sub">${sub}</div>`;
    case 'gauge': return `<div class="tt">${t}</div><div class="center">${ring(none ? 0 : pct, val, L || 'ok', w === 2 ? 112 : 72)}</div>` +
      (sub && (none || h === 3) ? `<div class="subc small">${sub}</div>` : '');
    case 'bar': {
      const b = pbar(none ? 0 : pct, L);
      return h === 1 ? `<div class="hd"><span class="tt">${t}</span><span class="${L}">${none ? sub : val}</span></div>${b}`
        : `<div class="hd"><span class="tt">${t}</span><span class="sub">${none ? '' : val}</span></div>${b}<div class="mid ${L}">${none ? '—' : sub || val}</div>${none ? `<div class="sub">${sub}</div>` : ''}`;
    }
    case 'status': {
      const up = !none && o.st !== 'bad';
      return `<div class="rowc start"><span class="dot${up ? '' : ' down'}"></span><span class="ell">${t}</span>${up ? '' : `<span class="bad small push nowrap">${none ? sub : val}</span>`}</div>`;
    }
  }
  // graph
  const net = !Array.isArray(c.value) && scName(c.value) === 'net', two = net || Array.isArray(c.value);
  const lines = none ? [] : (o.g ?? []).map((g, i) => ({pts: decode(g), col: i ? '#c297ff' : '#58a6ff'}));
  const labels = two ? `<span class="blue">${net ? '↓ ' : ''}${val}</span> &nbsp;<span class="vio">${net ? '↑ ' : ''}${esc(o.v2 ?? '')}</span>` : '<span class="gold">last hour</span>';
  return `<div class="hd"><span class="tt">${t}</span><span>${none ? '' : labels}</span></div>` + (two && !none ? '' : `<div class="big ${L}">${val}</div>`) +
    (sub ? `<div class="sub">${sub}</div>` : '') + spark(lines, o.th);
}
// A light: the dot in the light's colour (brass when on without one, muted when off), the title, the state. A card
// the server has no value for yet (new, or its entity changed) shows the entity's state from the picker's list.
function lightBody(c, w, h) {
  let o = reading(c);
  if (o.v == null && o.st == null) { const e = ha().entities.find(x => x.id === c.value?.ha); o = e ? {v: e.state, f: e.state === 'on' ? 1 : 0} : {st: 'none', sub: 'Save to see it'}; }
  const none = o.st === 'none', on = !none && o.f > 0, col = on && /^[0-9a-f]{6}$/i.test(o.v2 ?? '') ? ` data-col="${o.v2}"` : '';
  const dot = `<i class="ldot${on ? ' on' : ''}${h === 2 ? ' lg' : ''}"${col}></i>`, t = esc(c.title || c.id), st = esc(none ? o.sub ?? '' : o.v ?? '');
  // brightness fills the tile from the bottom, behind the text, in the bulb's colour; the tile's edge takes it too
  const fill = on ? `<i class="lfill" data-fill="${clamp(o.f * 100).toFixed(1)}"${col}></i>` : '';
  if (h === 2 && w === 1) return `${fill}<div class="lcol tall"><div class="lrow"><i class="ldot${on ? ' on' : ''}"${col}></i><span class="mid ell">${t}</span></div><span class="${none ? 'sub' : on ? 'big' : 'big sub'}">${st}</span></div>`;
  if (h === 2) return `${fill}<div class="lrow">${dot}<div class="lcol"><span class="mid ell">${t}</span><span class="${none ? 'sub' : on ? 'big' : 'big sub'}">${st}</span></div></div>`;
  return `${fill}<div class="lrow">${dot}<span class="ell">${t}</span><span class="push nowrap${on ? '' : ' sub'}${w === 1 && none ? ' small' : ''}">${st}</span></div>`;
}
function profileBody(c) {
  const sub = reading(c).sub;
  return `<div class="pbtn${sub ? ' unsaved' : ''}"><span class="ell">${esc(c.title || c.id)}</span>${sub ? `<span class="small ell">${esc(sub)}</span>` : ''}</div>`;
}
function body(c, sz) {
  const [w, h] = M.wh(sz || c.size);
  switch (c.type) {
    case 'light': return lightBody(c, w, h);
    case 'profile': return profileBody(c);
    case 'gpu': return gpuBody(c, w, h);
    case 'pool': return poolBody(c, w, h);
    case 'nas-alerts': return alertsBody(h);
    case 'dots': return dotsBody(c, w, h);
    case 'board': return boardBody();
    case 'radar': return '<div class="radar"></div><div class="radar-label mid">Weather radar</div>';
  }
  return T(c.type).general ? genBody(c, w, h) : `<div class="tt">${esc(c.type)}</div>`;
}
function cardClass(c) {
  let cls = 'pc';
  const o = reading(c);
  if (c.id === sel) cls += ' sel';
  if (missing(c) || o.st === 'stale') cls += ' miss';
  if (T(c.type).general && c.type !== 'status' && (o.st === 'warn' || o.st === 'bad')) cls += ' lv-' + o.st;
  if (c.type === 'nas-alerts' && alertLevel()) cls += ' lv-' + alertLevel();
  if (refused?.id === c.id) cls += ' refused';
  if (c.at[1] === 1) cls += ' top';
  return cls;
}

function renderPreview() {
  if (drag) return;   // the dragged card's element must survive; endDrag and cancelDrag render
  const a = document.activeElement, keep = a?.closest?.('#panel') ? a.id : null;
  let h = '';
  for (let r = 1; r <= M.ROWS; r++) for (let col = 1; col <= M.COLS; col++) h += `<div class="cell" data-box="${col},${r},1,1"></div>`;
  for (const c of tab().cards) {
    const [w, hh] = M.wh(c.size);
    h += `<div class="${cardClass(c)}" id="pc-${esc(c.id)}" data-id="${esc(c.id)}" data-box="${c.at[0]},${c.at[1]},${w},${hh}" tabindex="0" role="button" aria-label="${esc(c.type)} card ${esc(c.id)}, ${esc(c.size)} at column ${c.at[0]} row ${c.at[1]}">` +
      `<div class="body${w === M.COLS && hh === M.ROWS ? ' full' : ''}">${body(c)}</div>` +
      (c.id === sel && sizes(c.type).length > 1 ? '<div class="rz" title="Drag to resize"></div>' : '') +
      (refused?.id === c.id ? `<div class="flag">${esc(refused.msg)}</div>` : '') + '</div>';
  }
  $('grid').innerHTML = h + '<div id="target" hidden></div>';
  styleBoxes($('grid'));
  $('tb').innerHTML = S.tabs.map((t, i) => `<button type="button" role="tab" id="tab-${i}" data-i="${i}" aria-selected="${i === cur}" tabindex="${i === cur ? 0 : -1}" title="Click to open. Drag to reorder. Double-click to rename.">${esc(t.name)}</button>`).join('');
  const now = new Date();
  $('sbtime').textContent = now.toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});
  $('sbdate').textContent = now.toLocaleDateString([], {weekday: 'short', month: 'short', day: 'numeric'});
  refocus(keep);
}
// refocus gives the focus back to the element that had it before a re-render, except a card that is no longer the
// selected one: focusing a card selects it, so that would undo the new selection.
function refocus(id) {
  if (!id || (id.startsWith('pc-') && id !== 'pc-' + sel) || document.activeElement === $(id)) return;
  $(id)?.focus({preventScroll: true});
}

// ---------- inspector ----------
const opt = (v, label, selv, dis) => `<option value="${esc(v)}"${v === selv ? ' selected' : ''}${dis ? ' disabled' : ''}>${esc(label ?? v)}</option>`;
const extra = (v, list, note) => v && !list.includes(v) ? opt(v, `${v} (${note})`, v) : '';
const nfield = (id, label, val, ph) => `<div class="field"><label for="${id}">${label}</label><input id="${id}" type="number" step="any" data-set="${id}" value="${esc(val ?? '')}"${ph ? ` placeholder="${ph}"` : ''}></div>`;
function textField(id, label, set, val, max, ph) {
  return `<div class="field"><label for="${id}">${label}</label><input id="${id}" ${set} value="${esc(val ?? '')}"${max ? ` data-max="${max}"` : ''}${ph ? ` placeholder="${esc(ph)}"` : ''} aria-describedby="${id}err" spellcheck="false"><p class="err" id="${id}err"></p></div>`;
}
const everyField = (i, v) => `<div class="field"><label for="every${i}">Look every (seconds)</label><input id="every${i}" type="number" step="1" data-vf="${i}:every" value="${esc(v ?? '')}" placeholder="the default"></div>`;
// notSetUp says which .env lines a source still needs (the server names them), or that it isn't answering.
function notSetUp(what, x, then) {
  if (!x.configured) {
    const lines = (x.needs ?? []).map(n => `<code>${esc(n)}=${esc(ENV_EXAMPLE[n] ?? '…')}</code>`);
    return `<p class="note">${what} isn't set up.${lines.length ? ` Add ${lines.join(' and ')} to the server's <code>.env</code> and recreate the container; then ${then}.` : ''}</p>`;
  }
  return x.error ? `<p class="note">${what} didn't answer the editor: <code>${esc(x.error)}</code></p>` : '';
}
const promNote = () => notSetUp('Prometheus', prom(), 'its machines and metrics are listed here');
// choose is a dropdown of the server's list, or a text field that suggests it when there is no list or it is cut.
function choose(id, vf, val, list, first, ph) {
  if (list.length && !prom().truncated) return `<select id="${id}" data-vf="${vf}">${first}${list.map(m => opt(m, m, val)).join('')}${extra(val, list, 'not seen now')}</select>`;
  return `<input id="${id}" data-vf="${vf}" value="${esc(val)}" placeholder="${esc(ph)}" spellcheck="false"${list.length ? ` list="${id}-list"` : ''}>` +
    (list.length ? `<datalist id="${id}-list">${list.map(m => `<option value="${esc(m)}">`).join('')}</datalist>` : '');
}

function scUI(c, i, v) {
  const p = parseSc(v), s = shortcut(p.name), P = prom(), need = s?.instance === 'required', inList = Array.isArray(c.value);
  let h = `<div class="two"><div class="field"><label for="sc${i}">Shortcut</label><select id="sc${i}" data-vf="${i}:sc-name">` +
    src.shortcuts.map(x => {
      const off = !x.types.includes(c.type) ? ` (${x.types.join(', ')} only)` : inList && x.name === 'net' ? ' (not with a second line)' : '';
      return opt(x.name, x.name + off, p.name, !!off);
    }).join('') + (s ? '' : opt(p.name, p.name + ' (unknown)', p.name)) + '</select></div>';
  h += `<div class="field"><label for="inst${i}">Machine</label>` +
    choose(`inst${i}`, `${i}:sc-inst`, p.inst, P.instances, need ? p.inst ? '' : opt('', 'pick one', '') : opt('', 'all machines', p.inst), need ? 'host:9100' : 'all machines') + '</div></div>';
  if (s?.arg) h += `<div class="field"><label for="arg${i}">${esc(cap(s.arg))}</label>` +
    choose(`arg${i}`, `${i}:sc-arg`, p.arg, s.arg === 'mount' ? P.mounts : [], p.arg ? '' : opt('', 'pick one', ''), s.arg === 'mount' ? '/' : '') + '</div>';
  if (P.truncated && P.instances.length) h += CUT;
  if (need && !p.inst) h += `<p class="note"><code>${esc(p.name)}</code> reads one machine: pick it.</p>`;
  else if (!p.inst) h += '<p class="hint">Across every machine Prometheus watches.</p>';
  return h + promNote();
}
function promList(i, v) {
  const P = prom(), q = (ui['pq' + i] ?? '').toLowerCase(), hits = P.metrics.filter(n => n.toLowerCase().includes(q));
  if (!P.metrics.length) return '<p>No metric names to list. Write the query below.</p>';
  if (!hits.length) return '<p>No metric matches. Write the query yourself below.</p>' + (P.truncated ? CUT : '');
  return hits.slice(0, 60).map(n => `<button type="button" data-act="pickprom" data-i="${i}" data-v="${esc(n)}" aria-pressed="${v.prometheus === n}">${esc(n)}</button>`).join('') +
    (hits.length > 60 ? `<p>${hits.length - 60} more: type to narrow them down.</p>` : '') + (P.truncated ? CUT : '');
}
function promUI(c, i, v) {
  return promNote() + `<div class="field"><label for="pq${i}">Find a metric</label><input id="pq${i}" type="search" data-act="search" data-i="${i}" data-k="pq" value="${esc(ui['pq' + i] ?? '')}" placeholder="type part of a name"></div>
    <div class="pick" id="pqres${i}">${promList(i, v)}</div>
    <div class="field"><label for="q${i}">Query (PromQL that returns one number; at most ${lim('prom')} bytes)</label><textarea class="q" id="q${i}" rows="2" data-vf="${i}:prometheus" data-max="${lim('prom')}" aria-describedby="q${i}err" spellcheck="false">${esc(v.prometheus ?? '')}</textarea><p class="err" id="q${i}err"></p></div>`;
}
function haList(c, i, v) {
  const q = (ui['haq' + i] ?? '').toLowerCase(), hits = ents(c.type).filter(e => (e.name + ' ' + e.id).toLowerCase().includes(q));
  const cut = ha().truncated ? CUT : '';
  if (!hits.length) return '<p>No entity matches.</p>' + cut;
  return hits.slice(0, 60).map(e => `<button type="button" data-act="pickha" data-i="${i}" data-v="${esc(e.id)}" aria-pressed="${v.ha === e.id}"><em>${esc(e.state)}${e.unit ? ' ' + esc(e.unit) : ''}</em>${esc(e.name || e.id)}<span>${esc(e.id)}</span></button>`).join('') +
    (hits.length > 60 ? `<p>${hits.length - 60} more: type to narrow them down.</p>` : '') + cut;
}
function haUI(c, i, v) {
  const A = ha(), e = A.entities.find(x => x.id === v.ha);
  let h = notSetUp('Home Assistant', A, 'your entities are listed here');
  if (A.entities.length) h += `<div class="field"><label for="haq${i}">Find an entity</label><input id="haq${i}" type="search" data-act="search" data-i="${i}" data-k="haq" value="${esc(ui['haq' + i] ?? '')}" placeholder="name or entity id"></div><div class="pick" id="haqres${i}">${haList(c, i, v)}</div>`;
  h += textField(`ent${i}`, 'Entity id', `data-vf="${i}:ha"`, v.ha, 0, 'sensor.living_room_temperature');
  h += `<div class="field"><label for="attr${i}">Show</label>` + (e
    ? `<select id="attr${i}" data-vf="${i}:attr">${opt('', `its state (${e.state}${e.unit ? ' ' + e.unit : ''})`, v.attr ?? '')}${e.attrs.map(a => opt(a, 'attribute ' + a, v.attr)).join('')}${extra(v.attr, e.attrs, 'not seen now')}</select>`
    : `<input id="attr${i}" data-vf="${i}:attr" value="${esc(v.attr ?? '')}" placeholder="its state, or an attribute's name" spellcheck="false">`) + '</div>';
  if (v.attr) h += '<p class="hint">An attribute has no unit of its own: set Unit below.</p>';
  return h + everyField(i, v.every);
}
// lightUI is the HA picker cut down to the domains a light card takes: no attribute, no interval.
function lightUI(c) {
  const A = ha(), v = c.value ?? {}, doms = T(c.type).entity.join(' or ');
  let h = `<h3>Which ${esc(c.type)}</h3>` + notSetUp('Home Assistant', A, `your ${esc(doms)} entities are listed here`);
  if (ents(c.type).length) h += `<div class="field"><label for="haq0">Find a ${esc(doms)}</label><input id="haq0" type="search" data-act="search" data-i="0" data-k="haq" value="${esc(ui.haq0 ?? '')}" placeholder="name or entity id"></div><div class="pick" id="haqres0">${haList(c, 0, v)}</div>`;
  else if (A.configured && !A.error) h += `<p class="note">Home Assistant lists no ${esc(doms)} entity. Type its id below.</p>`;
  return h + textField('ent0', 'Entity id', 'data-vf="0:ha"', v.ha, 0, T(c.type).entity[0] + '.desk');
}
function jsonUI(c, i, v) {
  const j = v.json ?? {}, names = src.card_env ?? [];
  return textField(`url${i}`, "URL of the app's JSON status page", `data-vf="${i}:url"`, j.url, 0, 'http://app.lan/api/status') +
    textField(`path${i}`, 'Path to the value in it', `data-vf="${i}:path"`, j.path, 0, 'queue.depth') +
    `<div class="field"><label for="hdr${i}">Send a header</label><select id="hdr${i}" data-vf="${i}:header_env">${opt('', 'none', j.header_env ?? '')}${names.map(n => opt(n, n, j.header_env)).join('')}${extra(j.header_env, names, 'not in .env')}</select>
    <p class="hint">${names.length ? "Only the names of the server's <code>CARD_*</code> variables are listed; their values never reach this page."
      : "To send a login, add a line like <code>CARD_APP_TOKEN=Authorization: Bearer …</code> to the server's <code>.env</code> and recreate the container. Its name is then listed here, never its value."}</p></div>` + everyField(i, j.every);
}
function valueUI(c, i) {
  const v = vals(c)[i], k = kindOf(v), none = !shortcutsFor(c.type).length;
  let h = '<div class="seg" role="group" aria-label="Where the value comes from">' + KINDS.map(([kk, n]) =>
    `<button type="button" id="kind-${i}-${kk}" data-act="kind" data-i="${i}" data-k="${kk}" aria-pressed="${k === kk}"${kk === 'sc' && none ? ` disabled title="No shortcut suits a ${esc(c.type)} card"` : ''}>${n}</button>`).join('') + '</div>';
  h += k === 'sc' ? scUI(c, i, v) : k === 'prom' ? promUI(c, i, v) : k === 'ha' ? haUI(c, i, v) : jsonUI(c, i, v);
  return h + `<div class="test"><button type="button" class="b" id="test${i}" data-act="test" data-i="${i}" title="Asks the source once for this card's value"${testing ? ' disabled' : ''}>Test</button><span id="testout${i}" role="status">${ui['t' + i] ?? ''}</span></div>`;
}

function renderTabInsp() {
  const t = tab(), n = allCards().filter(x => x.tap === 'tab:' + t.name).length, visits = !(S.tour?.skip ?? []).includes(t.name);
  const full = t.cards.some(c => M.wh(c.size)[0] === M.COLS && M.wh(c.size)[1] === M.ROWS);
  $('insp').innerHTML = `<h2>Tab</h2><p class="hint">No card selected. Click a card to edit it.</p>
    <div class="field"><label for="tabname">Name, shown in the bottom bar</label><input id="tabname" value="${esc(t.name)}" aria-describedby="tabnameerr"><p class="hint" id="tabnameerr">${M.bytes(t.name)} of ${lim('tab_name')} bytes. Plain letters: the tab font has no accents.</p></div>
    <div class="field"><span class="lab">Place in the bar: ${cur + 1} of ${S.tabs.length}</span><div class="chips"><button type="button" class="b" id="i-left" data-act="tableft"${cur === 0 ? ' disabled' : ''}>Move left</button><button type="button" class="b" id="i-right" data-act="tabright"${cur === S.tabs.length - 1 ? ' disabled' : ''}>Move right</button></div></div>
    <p>${t.cards.length} of ${lim('cards')} cards.${full ? ' A 4x6 card fills this tab, so nothing else fits.' : ''}</p>
    <label class="check"><input type="checkbox" id="tabtour" data-act="tabtour"${visits ? ' checked' : ''}> The idle tour visits this tab</label>
    ${n ? `<p class="hint">${n} card${n > 1 ? 's jump' : ' jumps'} here on a tap. Renaming keeps ${n > 1 ? 'them' : 'it'} pointed here.</p>` : ''}
    <div class="acts"><button type="button" class="b danger" id="i-del" data-act="tabdel"${S.tabs.length < 2 ? ' disabled' : ''}>Delete tab</button></div>`;
}
function renderInsp() {
  const c = card(sel), t = tab();
  if (!c) return renderTabInsp();
  const ty = T(c.type), [w, hh] = M.wh(c.size), err = refused?.id === c.id ? refused.msg : '';
  let h = `<h2>${esc(c.type)} card</h2><p class="hint">${esc(WHAT[c.type] ?? '')}</p>` +
    (err ? `<p class="note badnote">The server refused the save: <code>${esc(err)}</code></p>` : '') +
    `<div class="field"><label for="cid">Id (yours alone, at most ${lim('id')} bytes)</label><input id="cid" value="${esc(c.id)}" aria-describedby="ciderr" spellcheck="false"><p class="err" id="ciderr"></p></div>`;
  if (titled(c.type)) h += textField('title', `Title (at most ${lim('title')} bytes)`, 'data-set="title"', c.title, lim('title'));
  h += '<div class="field"><span class="lab">Size (columns x rows)</span><div class="chips">' + sizes(c.type).map(sz => {
    const ok = sz === c.size || M.fits(t, c, c.at, sz);
    return `<button type="button" class="chip" id="size-${esc(sz)}" data-act="size" data-v="${esc(sz)}" aria-pressed="${sz === c.size}"${ok ? '' : ' disabled title="No room for it here"'}>${esc(sz)}</button>`;
  }).join('') + '</div>' + (sizes(c.type).length === 1 ? `<p class="hint">${esc(c.type)} only comes as ${esc(sizes(c.type)[0])}.</p>` : '') + `</div>
    <div class="two"><div class="field"><label for="col">Column (1 to ${M.COLS})</label><input id="col" type="number" min="1" max="${M.COLS}" value="${c.at[0]}" data-act="pos"></div>
    <div class="field"><label for="row">Row (1 to ${M.ROWS})</label><input id="row" type="number" min="1" max="${M.ROWS}" value="${c.at[1]}" data-act="pos"></div></div><p class="err" id="poserr"></p>`;

  if (ty.src === 'gpu') {
    const known = src.gpus.some(g => g.src === c.src), sl = slotsOf(c), SL = src.slots, noBar = SL.no_bar ?? [];
    h += '<h3>Which GPU</h3><div class="field"><label for="src">GPU, as the agents report it</label><select id="src" data-act="srcsel">' +
      src.gpus.map(g => { const r = gpuFor(g.src); return opt(g.src, `${g.src}: ${g.name}${r ? `, ${r.temp}°, ${r.up ? r.st : 'off'}` : ''}`, c.src); }).join('') +
      opt('?', 'another name (type it)', known ? '' : '?') + '</select></div>' +
      (src.gpus.length ? '' : `<p class="note">No GPU agents answered. List them in <code>GPU_AGENTS=name=http://host:9835</code> in the server's <code>.env</code> and recreate the container.</p>`) +
      (known ? '' : textField('srctxt', 'GPU agent name', 'data-set="src"', c.src, lim('src')) + `<p class="hint">Not found right now. The card draws dimmed, saying <code>no gpu '…'</code>, until a GPU by that name appears.</p>`) +
      `<h3>What it shows</h3>
      <div class="field"><label for="sgauge">Gauge (the ring or big number)</label><select id="sgauge" data-slot="gauge">${SL.gauge.map(v => opt(v, SLOT_WORDS[v] ?? v, sl.gauge)).join('')}</select></div>
      <div class="two"><div class="field"><label for="sl1">Line 1</label><select id="sl1" data-slot="line1.value">${SL.line1.map(v => opt(v, (SLOT_WORDS[v] ?? v) + (noBar.includes(v) && sl.line1.as === 'bar' ? ' (has no bar)' : ''), sl.line1.value, noBar.includes(v) && sl.line1.as === 'bar')).join('')}</select></div>
      <div class="field"><label for="sas">drawn as</label><select id="sas" data-slot="line1.as">${SL.as.map(v => opt(v, v + (v === 'bar' && noBar.includes(sl.line1.value) ? ` (${sl.line1.value} has no bar)` : ''), sl.line1.as, v === 'bar' && noBar.includes(sl.line1.value))).join('')}</select></div></div>
      <div class="two">${[0, 1].map(n => `<div class="field"><label for="sl2${n}">Line 2, ${n ? 'second' : 'first'}</label><select id="sl2${n}" data-act="line2" data-n="${n}">${opt('', 'nothing', sl.line2[n] ?? '')}${SL.line2.map(v => opt(v, SLOT_WORDS[v] ?? v, sl.line2[n])).join('')}</select></div>`).join('')}</div>` +
      [w === 1 && hh === 1 && 'At 1x1 only the host name and the gauge number are drawn.',
       w === 1 && hh === 2 && sl.line1.as === 'graph' && 'At 1x2 a graph has no room, so line 1 draws as a bar.',
       w === 1 && hh > 1 && sl.line2.length && 'Line 2 needs a card 2 columns wide; it is hidden here.',
       w === 2 && hh === 2 && sl.line1.as === 'graph' && sl.line2.length && 'At 2x2 the graph leaves no room for line 2, so it is hidden. 2x3 shows both.'].filter(Boolean).map(n => `<p class="note">${n}</p>`).join('');
  }
  if (ty.src === 'pool') {
    const known = src.pools.some(p => p.name === c.src);
    h += '<h3>Which pool</h3><div class="field"><label for="src">Pool found on the NAS</label><select id="src" data-act="srcsel">' +
      src.pools.map(p => { const r = poolFor(p.name); return opt(p.name, p.name + (r ? `: ${r.h.toLowerCase()}, ${size(r.used)} of ${size(r.used + r.avail)}` : ''), c.src); }).join('') +
      opt('?', 'another name (type it)', known ? '' : '?') + '</select></div>' +
      (src.pools.length ? '' : `<p class="note">TrueNAS reports no pools. It is set up with <code>TRUENAS_URL</code>, <code>TRUENAS_API_KEY</code> and <code>TRUENAS_CERT_SHA256</code> in the server's <code>.env</code>.</p>`) +
      (known ? '' : textField('srctxt', 'Pool name', 'data-set="src"', c.src, lim('src')) + `<p class="hint">Not found right now. The card draws dimmed, saying <code>no pool '…'</code>.</p>`);
  }
  if (ty.src === 'dots') {
    h += `<h3>Which dots</h3><div class="seg" role="group" aria-label="Which dots">${src.dots.map(d => `<button type="button" id="dots-${esc(d)}" data-act="dots" data-v="${esc(d)}" aria-pressed="${c.src === d}">${esc(cap(d))}</button>`).join('')}</div>` +
      (c.src ? '' : '<p class="hint">Not set, so the panel shows its default: both rows.</p>');
  }
  if (ty.entity) h += lightUI(c);
  if (ty.general) {
    const tk = takes(c), two = Array.isArray(c.value), net = scName(vals(c)[0]) === 'net', first = vals(c)[0];
    h += `<h3>${c.type === 'graph' ? 'First line' : 'Value'}</h3>` + valueUI(c, 0);
    if (c.type === 'graph') h += two
      ? `<h3 class="violet">Second line</h3>${valueUI(c, 1)}<button type="button" class="b mt" id="line-del" data-act="line-del">Remove the second line</button>`
      : `<button type="button" class="b mt" id="line-add" data-act="line-add"${net ? ' disabled' : ''}>Add a second line</button>` + (net ? '<p class="hint"><code>net</code> already draws two lines (down and up), so it can\'t share a graph.</p>' : '');
    const has = (...k) => k.some(x => c[x] !== undefined), scBar = (c.type === 'gauge' || c.type === 'bar') && kindOf(first) === 'sc';
    if (tk.limits || has('unit', 'warn', 'bad', 'below')) {
      h += `<h3>How it reads</h3>` + (tk.limits ? '' : `<p class="note">A ${esc(c.type)} card doesn't use a unit or limits: clear these.</p>`) +
        textField('unit', `Unit, after the number (at most ${lim('unit')} bytes)`, 'data-set="unit"', c.unit, lim('unit')) +
        `<div class="chips">${src.units.map(u => `<button type="button" class="chip" id="unit-${esc(u)}" data-act="unit" data-v="${esc(u)}" aria-pressed="${c.unit === u}">${esc(u)}</button>`).join('')}</div>` +
        (tk.limits && kindOf(first) === 'sc' ? '<p class="hint">A shortcut formats itself; a unit is rarely needed.</p>' : '');
      h += `<div class="two">${nfield('warn', 'Amber from', c.warn)}${nfield('bad', 'Red from', c.bad)}</div>
        <label class="check"><input type="checkbox" id="below" data-set="below"${c.below ? ' checked' : ''}> Lower is worse (free space, say)</label>` +
        (['ram', 'disk'].includes(scName(first)) ? '<p class="hint">For ram and disk these compare the percent used.</p>' : '');
    }
    if (!tk.limits) h += '<p class="hint">A status card is up and green for a value above 0, or a word like on, up or running; anything else is down and red.</p>';
    if (tk.scale || has('min', 'max')) {
      h += (tk.scale ? '' : `<p class="note">${scBar ? 'A shortcut sets its own scale' : `A ${esc(c.type)} card draws no scale`}: clear these.</p>`) +
        `<div class="two">${nfield('min', 'Scale from', c.min, '0')}${nfield('max', 'to', c.max, c.type === 'graph' ? 'auto' : '100')}</div>`;
    } else if (scBar) h += '<p class="hint">A shortcut sets its own scale. A query, an entity or a JSON value gets "Scale from / to" here.</p>';
  }
  if (ty.tap) {
    const def = ty.default_tap, tv = c.tap || def, names = S.tabs.map(x => 'tab:' + x.name), gone = tv.startsWith('tab:') && !names.includes(tv);
    h += `<h3>On a tap</h3><div class="field"><label for="tap">Tapping the card</label><select id="tap" data-act="tap"${gone ? ' aria-invalid="true"' : ''}>` +
      src.taps.map(x => opt(x, (TAP_WORDS[x] ?? x) + (x === def ? ' (its default)' : ''), tv)).join('') +
      names.map(x => opt(x, 'jumps to the tab ' + x.slice(4), tv)).join('') + (gone ? opt(tv, `jumps to ${tv.slice(4)} (no such tab)`, tv) : '') + '</select></div>';
  } else h += `<p class="hint">${OWN_TAP[c.type] ?? `A ${esc(c.type)} card handles its own taps.`}</p>`;
  const others = S.tabs.filter(x => x !== t);
  h += `<div class="acts"><button type="button" class="b" id="dup" data-act="dup">Duplicate</button><button type="button" class="b danger" id="del" data-act="del" title="Delete key">Delete</button>
    <select id="moveto" data-act="moveto" aria-label="Move to another tab"${others.length ? '' : ' disabled'}><option value="">Move to tab…</option>${others.map(x => opt(x.name, x.name)).join('')}</select></div>`;
  $('insp').innerHTML = h;
}

function setField(c, k, v) { if (v === '' || v == null || v === false) delete c[k]; else c[k] = v; }
// setValField sets one field of value i: "sc-name", "sc-arg", "sc-inst", "prometheus", or a key of the ha or json object.
function setValField(c, spec, v) {
  const [i, f] = [+spec.split(':')[0], spec.split(':')[1]], old = vals(c)[i];
  if (f === 'every') v = v === '' ? '' : Math.round(+v);
  let nv;
  if (f.startsWith('sc-')) {
    const p = parseSc(old);
    if (f === 'sc-name') {
      const s = shortcut(v);
      [p.name, p.arg] = [v, s?.arg ? p.arg || firstArg(s) : ''];
      if (s?.instance === 'required' && !p.inst) p.inst = prom().instances[0] || '';
    } else if (f === 'sc-arg') p.arg = v;
    else p.inst = v;
    nv = scText(p.name, p.arg, p.inst);
  } else if (f === 'prometheus') nv = {prometheus: v};
  else {
    const o = kindOf(old) === 'ha' ? {...old} : {...old.json};
    if (v === '' && f !== 'ha' && f !== 'url' && f !== 'path') delete o[f];
    else o[f] = v;
    nv = kindOf(old) === 'ha' ? o : {json: o};
  }
  setVal(c, i, nv);
}
function setSlot(c, path, v) {
  const s = slotsOf(c);
  if (path === 'gauge') s.gauge = v;
  else s.line1[path.split('.')[1]] = v;
  c.slots = s;
}

// ---------- tour & clock ----------
const tourNow = () => ({...src.tour, ...S.tour, skip: S.tour?.skip ?? []});
function lapText() {
  const o = tourNow(), stops = S.tabs.filter(t => !o.skip.includes(t.name)), lap = (o.clock ? o.clock_s : 0) + stops.length * o.tab_s;
  return !o.on ? 'The tour is off. The panel stays on the tab you left it on.' : !stops.length && !o.clock ? 'Nothing to visit: tick a tab or the clock.' :
    `After <b>${esc(o.idle_min)} min</b> alone: ` + [o.clock ? `clock <b>${esc(o.clock_s)} s</b>` : '', ...stops.map(t => `${esc(t.name)} <b>${esc(o.tab_s)} s</b>`)].filter(Boolean).join(' → ') +
    `, then round again. One lap takes ${Math.floor(lap / 60)} min ${lap % 60} s.`;
}
function renderSettings() {
  const o = tourNow(), skip = o.skip;
  const n = (k, label, unit) => `<div class="n"><label for="t-${k}">${label}</label><input id="t-${k}" type="number" step="1" value="${esc(o[k])}" data-tour="${k}"><span class="dim">${unit}</span></div>`;
  $('settings').innerHTML = `<div><h2>Tour &amp; clock</h2>
    <p class="dim lead">Left alone, the panel tours its tabs and comes home to the clock, so nothing sits on screen all day.</p>
    <label class="check"><input type="checkbox" id="t-on" data-tour="on"${o.on ? ' checked' : ''}> Tour the tabs when nobody is using the panel</label>
    <fieldset${o.on ? '' : ' disabled'}>
    ${n('idle_min', 'Start after this long alone', 'minutes')}
    ${n('tab_s', 'Each tab stays', 'seconds')}
    <label class="check"><input type="checkbox" id="t-clock" data-tour="clock"${o.clock ? ' checked' : ''}> The clock is part of the tour</label>
    ${n('clock_s', 'The clock stays', 'seconds')}
    <h3>Tabs the tour visits</h3>
    ${S.tabs.map((t, i) => `<label class="check"><input type="checkbox" id="tt-${i}" data-tourtab="${i}"${skip.includes(t.name) ? '' : ' checked'}> ${esc(t.name)}</label>`).join('')}
    </fieldset>
    <div id="lap">${lapText()}</div>
    <p class="hint mt">${api.demo ? 'These are part of layout.json, so the download takes them along; a panel picks them up with the layout, no reflash.'
      : 'These reach the panel with the layout, about 20 seconds after Save; no reflash.'} A panel with older firmware keeps its built-in timings.</p></div>`;
}

// ---------- render everything ----------
// refused is the last thing that went wrong with the file: kind net (no answer), error (the save failed in the page),
// load (Revert or Reload failed), stale (412) or bad (the server refused the save; id is the card it named, if any).
function refusedHTML() {
  if (refused.kind === 'net') return "<b>Can't reach the server.</b> <span>Your changes are still here.</span>";
  if (refused.kind === 'error') return `<b>Not saved.</b> <span>${esc(refused.msg)} Your changes are still here.</span>`;
  if (refused.kind === 'load') return `<b>Couldn't load the layout.</b> <span>${esc(refused.msg)} Your changes are still here.</span><button type="button" class="b" id="retryload">Try again</button>`;
  const said = `<b>Not saved.</b> <span>The server says: <code>${esc(refused.msg)}</code></span>`;
  if (refused.kind === 'stale') return said + '<button type="button" class="b" id="reload">Reload</button><span class="dim">Someone changed the file since this page loaded it. View JSON, then Download, keeps a copy of your version first.</span>';
  return said + (refused.id ? '<button type="button" class="b" id="showcard">Show the card</button>' : '');
}
// region writes a live region only when what it says changes, so a screen reader hears each message once.
function region(id, html) {
  $(id).hidden = !html;
  if (shown[id] !== html) $(id).innerHTML = shown[id] = html;
}
function renderTop() {
  const d = dirty(), pill = d ? 'Unsaved changes' : fromFile ? 'Saved' : 'Not saved yet';
  if ($('pill').textContent !== pill) $('pill').textContent = pill;   // a status region too
  $('pill').className = d || !fromFile ? 'dirty' : '';
  $('undo').disabled = !undo.length;
  $('redo').disabled = !redo.length;
  region('refused', refused ? refusedHTML() : '');
  region('info', refused ? '' : loadNote);
  if (!$('drawer').hidden) $('jsonout').textContent = M.fileText(source());
}
function renderTabctl() {
  $('tabctl').innerHTML = `<button type="button" class="b" id="c-left" data-act="tableft"${cur === 0 ? ' disabled' : ''} aria-label="Move tab left">◀ Move</button><button type="button" class="b" id="c-right" data-act="tabright"${cur === S.tabs.length - 1 ? ' disabled' : ''} aria-label="Move tab right">Move ▶</button>
    <button type="button" class="b" id="c-rename" data-act="tabrename">Rename</button><button type="button" class="b danger" id="tabdel" data-act="tabdel"${S.tabs.length < 2 ? ' disabled' : ''}>Delete tab</button>
    <button type="button" class="b" id="tabadd" data-act="tabadd"${S.tabs.length >= lim('tabs') ? ` disabled title="${lim('tabs')} tabs is the most the panel takes"` : ''}>+ Add tab</button><span class="dim">${S.tabs.length} of ${lim('tabs')} tabs</span>`;
}
function render() {
  if (!S) return;
  const a = document.activeElement, keep = a && a.id;
  $('screens').hidden = view !== 'screens';
  $('settings').hidden = view !== 'settings';
  $('vScreens').setAttribute('aria-pressed', view === 'screens');
  $('vSettings').setAttribute('aria-pressed', view === 'settings');
  renderTop();
  if (view === 'settings') renderSettings();
  else {
    renderPreview();
    renderInsp();
    renderTabctl();
    fit();
  }
  refocus(keep);
}
function fit() {
  const f = $('fit');
  if (!f.clientWidth) return;
  K = Math.max(.3, Math.min(1.7, (f.clientWidth - 16) / 800, (f.clientHeight - 16) / 480));
  $('scalebox').style.width = 800 * K + 'px';
  $('scalebox').style.height = 480 * K + 'px';
  $('panel').style.transform = `scale(${K})`;
}
new ResizeObserver(fit).observe($('fit'));
const focusCard = () => { const el = sel && $('pc-' + sel); if (el) el.focus({preventScroll: true}); };
function select(id) {
  if (sel === id) return;
  // a card with the focus is the selected one (C3), so deselecting takes the focus off it too
  if (id === null && document.activeElement?.closest?.('.pc')) document.activeElement.blur();
  [sel, ui, lastKey] = [id, {}, null];
  render();
}
function goTab(i) {   // a new tab ends the current undo step: typing on another tab is its own step
  [cur, sel, ui, lastKey] = [i, null, {}, null];
  say('');
  render();
}
function toast(msg) {
  const t = $('toast');
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(t.timer);
  t.timer = setTimeout(() => { t.hidden = true; }, 6000);
}
function ask(msg, yes, fn) {
  $('dlgmsg').textContent = msg;
  $('dlgyes').textContent = yes;
  $('dlgyes').onclick = () => { $('dlg').close(); fn(); };
  $('dlg').showModal();
}
$('dlgno').onclick = () => $('dlg').close();

// ---------- palette ----------
function renderPalette() {
  const order = [...Object.keys(WHAT).filter(k => k in src.types), ...Object.keys(src.types).filter(k => !(k in WHAT))];
  $('pal').innerHTML = order.map(k => `<button type="button" class="pal" data-type="${esc(k)}" id="pal-${esc(k)}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${GLYPH[k] ?? GLYPH.other}</svg><b>${esc(k)}</b><span>${esc(WHAT[k] ?? '')}</span></button>`).join('');
}
function makeCard(type) {
  const ty = T(type), c = {id: '', type, at: [1, 1], size: sizes(type)[0]};
  if (ty.src === 'gpu') c.src = src.gpus[0]?.src ?? '';
  if (ty.src === 'pool') c.src = src.pools[0]?.name ?? '';
  if (ty.general) { c.title = cap(type); c.value = newValue(type, 'sc'); }
  if (ty.entity) {   // the first light or switch no card has yet, else the first
    const used = allCards().filter(x => x.type === type).map(x => x.value?.ha), e = ents(type).find(x => !used.includes(x.id)) ?? ents(type)[0];
    c.value = {ha: e?.id ?? ''};
    if (e?.name) c.title = fitTitle(e.name);
  }
  if (type === 'profile') c.title = 'Profile';
  c.id = M.newId(S, (ty.src === 'gpu' || ty.src === 'pool') && c.src ? type + '-' + c.src : type, lim('id'));
  return c;
}
const freeTabName = base => { let n = base, k = 2; while (S.tabs.some(x => x.name === n)) n = `${base} ${k++}`; return n; };
const fillsTab = type => { const [w, h] = M.wh(sizes(type)[0]); return w === M.COLS && h === M.ROWS; };
function addCard(type, at) {
  const t = tab(), def = sizes(type)[0];
  if (fillsTab(type) && t.cards.length) return offerNewTab(type);
  if (t.cards.length >= lim('cards')) return say(`${lim('cards')} cards is the most a tab takes.`, 1);
  let spot = null;
  if (at) {
    if (!M.fits(t, null, at, def)) { render(); return say("Refused: that spot overlaps a card or leaves the grid.", 1); }
    spot = {at, size: def};
  } else for (const sz of [def, ...sizes(type).slice(1).sort(byArea)]) {
    const a = M.firstFree(t, sz);
    if (a) { spot = {at: a, size: sz}; break; }
  }
  if (!spot) return say(`No room on this tab for a ${type} card.`, 1);
  snap();
  const c = makeCard(type);
  [c.at, c.size] = [spot.at, spot.size];
  t.cards.push(c);
  sel = c.id;
  ui = {};
  render();
  focusCard();
  say(`Added ${c.id} at column ${c.at[0]}, row ${c.at[1]}${spot.size !== def ? `, as ${spot.size} (the usual ${def} did not fit)` : ''}.`);
}
// offerNewTab: a card that fills a tab (board, radar) can't go on a used one, so offer a tab of its own.
function offerNewTab(type) {
  if (S.tabs.length >= lim('tabs')) return say(`${type} fills a whole tab and this one has cards; ${lim('tabs')} tabs is the most the panel takes, so make room first.`, 1);
  ask(`${cap(type)} fills a whole tab, and this one has cards. Add it on a new tab?`, 'Add on a new tab', () => {
    snap();
    S.tabs.push({name: freeTabName(cap(type)), cards: []});
    cur = S.tabs.length - 1;
    const c = makeCard(type);
    tab().cards.push(c);
    sel = c.id;
    ui = {};
    render();
    say(`Added ${c.id} on a new tab, ${tab().name}.`);
  });
}
$('pal').addEventListener('click', e => { const b = e.target.closest('.pal'); if (b && !noClick) addCard(b.dataset.type); });
// capture keeps a drag's pointer events coming when the pointer leaves the window; without it they still reach window.
const capture = (el, e) => { try { el.setPointerCapture(e.pointerId); } catch { /* not capturable: the window listeners still see the drag */ } };
$('pal').addEventListener('pointerdown', e => {
  const b = e.target.closest('.pal');
  if (!b || e.button) return;
  capture(b, e);
  drag = {mode: 'new', type: b.dataset.type, x0: e.clientX, y0: e.clientY};
});

// ---------- drag, resize, tab reorder ----------
function showTarget(at, w, h, ok) {
  const t = $('target');
  t.hidden = false;
  place(t, at[0], at[1], w, h);
  t.className = ok ? '' : 'bad';
}
const NO_ROOM = "Can't go there: it would overlap a card or leave the grid.";
$('grid').addEventListener('pointerdown', e => {
  if (e.button) return;
  let el = e.target.closest('.pc');
  if (!el) return select(null);
  const id = el.dataset.id, rz = e.target.classList.contains('rz');
  e.preventDefault();
  select(id);
  el = $('pc-' + id);   // select re-rendered the grid
  el.focus({preventScroll: true});
  capture(el, e);
  drag = {mode: rz ? 'size' : 'move', c: card(id), el, x0: e.clientX, y0: e.clientY};
});
// The browser's own focus step on a press (mousedown) would aim at the element the rebuild just replaced, so it is kept
// from running; pointerdown and pointerup focus the selected card themselves (not relying on Chrome's handling of a
// prevented pointerdown, which Firefox doesn't share).
$('grid').addEventListener('mousedown', e => e.preventDefault());
$('grid').addEventListener('focusin', e => {   // Tab onto a card selects it: the keys act on the card with the focus ring
  const el = e.target.closest('.pc');
  if (el && el.dataset.id !== sel && !drag) select(el.dataset.id);
});
$('tb').addEventListener('pointerdown', e => {
  const b = e.target.closest('button');
  if (!b || e.button) return;
  capture(b, e);
  drag = {mode: 'tab', i: +b.dataset.i, x0: e.clientX, y0: e.clientY};
});
$('tb').addEventListener('click', e => {
  const b = e.target.closest('button');
  if (!b || noClick || (+b.dataset.i === cur && !sel)) return;
  if (document.activeElement?.closest?.('.pc')) document.activeElement.blur();
  goTab(+b.dataset.i);
});
$('tb').addEventListener('dblclick', () => act('tabrename'));
addEventListener('pointermove', e => {
  if (!drag) return;
  if (!(e.buttons & 1)) return cancelDrag();   // the button came up where we didn't see it (outside the window)
  const dx = (e.clientX - drag.x0) / K, dy = (e.clientY - drag.y0) / K;
  if (!drag.moved && Math.hypot(dx, dy) * K < 5) return;
  drag.moved = true;
  const g = $('grid').getBoundingClientRect(), px = (e.clientX - g.left) / K, py = (e.clientY - g.top) / K;
  if (drag.mode === 'move') {
    const c = drag.c, [w, h] = M.wh(c.size);
    drag.el.classList.add('dragging');
    drag.el.style.transform = `translate(${dx}px,${dy}px)`;
    const at = [Math.round(((c.at[0] - 1) * CW + dx) / CW) + 1, Math.round(((c.at[1] - 1) * RH + dy) / RH) + 1];
    const ok = M.fits(tab(), c, at, c.size), sw = ok ? null : M.swapWith(tab(), c, at);
    showTarget(sw ? sw.at : at, w, h, ok || !!sw);
    drag.cand = {at, ok: ok || !!sw};
    say(ok ? `${c.size} at column ${at[0]}, row ${at[1]}` : sw ? `Drop to swap with ${sw.id}.` : NO_ROOM, !ok && !sw);
  } else if (drag.mode === 'size') {
    const c = drag.c, [w0, h0] = M.wh(c.size), best = M.nearestSize(sizes(c.type), w0 + dx / CW, h0 + dy / RH), [w, h] = M.wh(best);
    if (drag.cand?.size !== best) {
      const b = drag.el.querySelector('.body');
      b.innerHTML = body(c, best);
      styleBoxes(b);
      place(drag.el, c.at[0], c.at[1], w, h);
    }
    const ok = M.fits(tab(), c, c.at, best);
    showTarget(c.at, w, h, ok);
    drag.cand = {size: best, ok};
    say(ok ? `${best}` : `No room for ${best} here.`, !ok);
  } else if (drag.mode === 'new') {
    const def = sizes(drag.type)[0], [w, h] = M.wh(def);
    document.body.style.cursor = 'copy';
    if (px < 0 || py < 0 || px > 776 || py > 355) { $('target').hidden = true; drag.cand = null; say('Drop it on the grid.'); return; }
    const at = fillsTab(drag.type) ? [1, 1] : [Math.min(Math.floor(px / CW) + 1, M.COLS), Math.min(Math.floor(py / RH) + 1, M.ROWS)], ok = M.fits(tab(), null, at, def);
    const offer = !ok && fillsTab(drag.type) && tab().cards.length;
    showTarget(at, w, h, ok);
    drag.cand = {at, ok};
    say(ok ? `${def} at column ${at[0]}, row ${at[1]}` : offer ? `${cap(drag.type)} fills a whole tab: drop it to add it on a new tab.` : NO_ROOM, !ok && !offer);
  } else if (drag.mode === 'tab') {
    const n = S.tabs.length, to = Math.max(0, Math.min(n - 1, Math.floor((e.clientX - g.left + 12 * K) / (800 * K / n))));
    drag.to = to;
    [...$('tb').children].forEach((b, i) => { b.className = i === to && to !== drag.i ? (to < drag.i ? 'drop' : 'dropr') : ''; });
  }
});
// noClick swallows the click that follows a drag or a cancelled one; the next press of a button or a key clears it.
addEventListener('pointerdown', () => { noClick = false; }, true);
addEventListener('keydown', () => { noClick = false; }, true);
// cancelDrag drops a drag without changing anything: Escape, pointercancel, or a button let go out of sight.
function cancelDrag() {
  if (!drag) return;
  const moved = drag.moved;
  drag = null;
  document.body.style.cursor = '';
  noClick = true;   // moved or not: letting go afterwards adds no card and opens no tab
  if (moved) render();
  say('Cancelled: nothing moved.');
}
function endDrag() {
  if (!drag) return;
  const d = drag;
  drag = null;
  document.body.style.cursor = '';
  if (!d.moved) {
    if (d.mode === 'move' || d.mode === 'size') focusCard();   // a click on a card: the keys go to it
    return;
  }
  noClick = true;
  if (d.mode === 'tab') return d.to != null && d.to !== d.i ? moveTab(d.i, d.to) : render();
  const k = d.cand;
  if (d.mode === 'new') { if (k) addCard(d.type, k.at); else { say(''); render(); } return; }
  if (!k) return render();
  if (!k.ok) say('Refused: the card stays where it was.', 1);
  else if (d.mode === 'move') {
    const before = JSON.stringify(S), sw = M.fits(tab(), d.c, k.at, d.c.size) ? null : M.swapWith(tab(), d.c, k.at);
    M.move(S, cur, d.c.id, k.at);
    if (JSON.stringify(S) !== before) snap(null, before);
    say(sw ? `Swapped with ${sw.id}.` : '');
  } else if (k.size !== d.c.size) { snap(); d.c.size = k.size; say(''); }
  render();
  focusCard();
}
addEventListener('pointerup', endDrag);
addEventListener('pointercancel', cancelDrag);

// ---------- actions (buttons and dropdowns in the inspector and under the preview) ----------
function moveTab(from, to) {
  snap();
  const [t] = S.tabs.splice(from, 1);
  S.tabs.splice(to, 0, t);
  cur = to;
  sel = null;
  render();
}
// runTest asks the server for the whole card once (both lines of a graph) and keeps the answer, the card as /cards
// would serve it, for as long as the card still asks for the same; the preview draws it like a polled one. Only the
// Test line and the preview are touched, so a field being typed in keeps its place. A busy answer (429) is about the
// button, not the card: it is shown, never kept.
function runTest(c, i) {
  if (testing) return;
  const id = c.id, key = liveKey(c);
  const show = html => { if (sel !== id) return; ui['t' + i] = html; const el = $('testout' + i); if (el) el.innerHTML = html; };
  const buttons = on => { for (const b of document.querySelectorAll('[data-act=test]')) b.disabled = !on; };
  testing = true;
  buttons(false);
  show('<span class="dim">testing…</span>');
  const done = () => setTimeout(() => { testing = false; buttons(true); }, 1000);
  const {type, value, unit, min, max, warn, bad, below} = c;
  api.test({type, value, unit, min, max, warn, bad, below}).then(r => {
    done();
    if (r.busy) return show(`<span class="err">${esc(r.error)}</span>`);
    const now = allCards().find(x => x.id === id);
    if (!now || liveKey(now) !== key) return show('');   // changed while asking: the answer is for another card
    const o = r.error ? {st: 'none', sub: r.error} : r;
    tested[id] = {key, out: o};
    const L = o.st === 'warn' || o.st === 'bad' ? o.st : 'ok';
    show(o.st === 'none' ? `<span class="err">${esc(o.sub)}</span>` : `<span class="${L}">now: ${esc(o.v ?? '')}${o.v2 ? ' / ' + esc(o.v2) : ''}${o.sub ? ' · ' + esc(o.sub) : ''}</span>`);
    renderPreview();
  }).catch(e => {
    done();
    if (e.message !== 'locked') show(`<span class="err">${esc(e.message)}</span>`);
  });
}
function act(a, el) {
  const c = card(sel), t = tab(), i = el ? +el.dataset.i || 0 : 0, v = el && el.dataset.v;
  switch (a) {
    case 'tabadd': {
      if (S.tabs.length >= lim('tabs')) return;
      snap();
      S.tabs.push({name: freeTabName('Tab'), cards: []});
      cur = S.tabs.length - 1;
      sel = null;
      render();
      $('tabname').focus();
      $('tabname').select();
      return;
    }
    case 'tabrename': sel = null; render(); $('tabname').focus(); $('tabname').select(); return;
    case 'tableft': return moveTab(cur, cur - 1);
    case 'tabright': return moveTab(cur, cur + 1);
    case 'tabdel': {
      const n = allCards().filter(x => x.tap === 'tab:' + t.name && !t.cards.includes(x)).length;
      return ask(`Delete the tab "${t.name}" and its ${t.cards.length} card${t.cards.length === 1 ? '' : 's'}?` +
        (n ? ` ${n} card${n > 1 ? 's' : ''} on other tabs jump${n > 1 ? '' : 's'} here on a tap; ${n > 1 ? 'they' : 'it'} will do nothing instead.` : ''), 'Delete tab',
      () => { snap(); M.deleteTab(S, cur); cur = Math.max(0, cur - 1); sel = null; render(); say('Deleted. Undo brings it back.'); });
    }
    case 'tabtour': {
      snap();
      S.tour ??= {};
      const skip = (S.tour.skip ?? []).filter(x => x !== t.name);
      if (!el.checked) skip.push(t.name);
      S.tour.skip = S.tabs.map(x => x.name).filter(x => skip.includes(x));
      break;
    }
    case 'del': if (!c) return; snap(); t.cards.splice(t.cards.indexOf(c), 1); sel = null; say(`Deleted ${c.id}. Undo brings it back.`); break;
    case 'dup': {
      if (t.cards.length >= lim('cards')) return say(`${lim('cards')} cards is the most a tab takes.`, 1);
      const at = M.firstFree(t, c.size);
      if (!at) return say(`No room on this tab for another ${c.size}.`, 1);
      snap();
      const d = JSON.parse(JSON.stringify(c));
      [d.id, d.at] = [M.newId(S, c.id.replace(/-\d+$/, ''), lim('id')), at];
      t.cards.push(d);
      sel = d.id;
      ui = {};
      say(`Copied as ${d.id}.`);
      break;
    }
    case 'moveto': {
      const to = S.tabs.find(x => x.name === el.value);
      if (!to) return;
      if (to.cards.length >= lim('cards')) { say(`${to.name} already has ${lim('cards')} cards.`, 1); break; }
      const at = M.firstFree(to, c.size);
      if (!at) { say(`No room for a ${c.size} card on ${to.name}.`, 1); break; }
      snap();
      t.cards.splice(t.cards.indexOf(c), 1);
      c.at = at;
      to.cards.push(c);
      cur = S.tabs.indexOf(to);
      say(`Moved to ${to.name}.`);
      break;
    }
    case 'size': if (!M.fits(t, c, c.at, v)) return; snap(); c.size = v; break;
    case 'dots': snap(); c.src = v; break;
    case 'srcsel': snap(); c.src = el.value === '?' ? '' : el.value; break;
    case 'line2': { snap(); const s = slotsOf(c); s.line2 = [$('sl20').value, $('sl21').value].filter(Boolean); c.slots = s; break; }
    case 'tap': snap(); c.tap = el.value; break;
    case 'unit': snap(); setField(c, 'unit', c.unit === v ? '' : v); break;
    case 'kind': snap(); setVal(c, i, newValue(c.type, el.dataset.k, listSkip(c))); delete ui['t' + i]; break;
    case 'pickprom': snap(); setVal(c, i, {prometheus: v}); delete ui['t' + i]; break;
    case 'pickha': {
      snap();
      const old = vals(c)[i], nv = {ha: v}, name = id => ha().entities.find(e => e.id === id)?.name;
      if (old.every) nv.every = old.every;
      // a light's title follows the pick while it is empty or still the last entity's name
      if (T(c.type).entity && (!c.title || c.title === fitTitle(name(old.ha))) && name(v)) c.title = fitTitle(name(v));
      setVal(c, i, nv);
      delete ui['t' + i];
      break;
    }
    case 'test': return runTest(c, i);
    case 'line-add': snap(); c.value = [c.value, newValue('graph', 'sc', ['net', scName(c.value)])]; break;
    case 'line-del': snap(); c.value = vals(c)[0]; delete ui.t1; break;
    default: return;
  }
  render();
}
for (const id of ['insp', 'tabctl']) {
  $(id).addEventListener('click', e => { const el = e.target.closest('button[data-act]'); if (el && !el.disabled) act(el.dataset.act, el); });
}
$('insp').addEventListener('change', e => {
  const el = e.target, c = card(sel);
  if (el.matches('select[data-act], input[type=checkbox][data-act]')) return act(el.dataset.act, el);
  if (!c || !(el.tagName === 'SELECT' || el.type === 'checkbox')) return;
  snap();
  if (el.dataset.vf) { setValField(c, el.dataset.vf, el.value); delete ui['t' + el.dataset.vf.split(':')[0]]; }
  else if (el.dataset.slot) setSlot(c, el.dataset.slot, el.value);
  else if (el.dataset.set) setField(c, el.dataset.set, el.type === 'checkbox' ? el.checked : el.value);
  render();
});
$('insp').addEventListener('input', e => {
  const el = e.target, c = card(sel);
  if (el.id === 'tabname') {
    const before = JSON.stringify(S), err = M.renameTab(S, cur, el.value.trim(), lim('tab_name'));
    el.setAttribute('aria-invalid', !!err);
    $('tabnameerr').textContent = err || `${M.bytes(el.value.trim())} of ${lim('tab_name')} bytes. Plain letters: the tab font has no accents.`;
    $('tabnameerr').className = err ? 'err' : 'hint';
    if (!err && JSON.stringify(S) !== before) snap('tabname' + cur, before);
    renderPreview();
    renderTop();
    return;
  }
  if (!c) return;
  if (el.id === 'cid') {
    const v = el.value.trim();
    const bad = !v ? 'A card needs an id.' : M.bytes(v) > lim('id') ? `${M.bytes(v)} bytes: ${lim('id')} is the most.` : allCards().some(x => x !== c && x.id === v) ? 'Another card has that id.' : '';
    el.setAttribute('aria-invalid', !!bad);
    $('ciderr').textContent = bad;
    if (bad) return;
    snap('id');
    c.id = sel = v;
    renderPreview();
    renderTop();
    return;
  }
  if (el.dataset.act === 'pos') {
    const at = [+$('col').value, +$('row').value], ok = M.fits(tab(), c, at, c.size);
    $('poserr').textContent = ok ? '' : NO_ROOM;
    $('col').setAttribute('aria-invalid', !ok);
    $('row').setAttribute('aria-invalid', !ok);
    if (!ok) return;
    snap('pos' + c.id);
    c.at = at;
    renderPreview();
    renderTop();
    return;
  }
  if (el.dataset.act === 'search') {
    const i = +el.dataset.i;
    ui[el.dataset.k + i] = el.value;
    $(el.dataset.k + 'res' + i).innerHTML = el.dataset.k === 'pq' ? promList(i, vals(c)[i]) : haList(c, i, vals(c)[i]);
    return;
  }
  if (el.tagName === 'SELECT' || el.type === 'checkbox' || !(el.dataset.vf || el.dataset.set)) return;
  const max = +el.dataset.max, errEl = $(el.id + 'err');
  if (max && M.bytes(el.value) > max) {
    el.setAttribute('aria-invalid', true);
    if (errEl) errEl.textContent = `${M.bytes(el.value)} bytes: ${max} is the most.`;
    return;
  }
  el.removeAttribute('aria-invalid');
  if (errEl) errEl.textContent = '';
  snap((el.dataset.vf || el.dataset.set) + c.id);
  if (el.dataset.vf) {
    setValField(c, el.dataset.vf, el.value);
    const i = el.dataset.vf.split(':')[0];
    delete ui['t' + i];
    if ($('testout' + i)) $('testout' + i).textContent = '';
  } else setField(c, el.dataset.set, el.type === 'number' ? (el.value === '' ? null : +el.value) : el.value);
  renderPreview();
  renderTop();
});
// Only the key you change is written; the others stay out of the file, so the server's defaults apply. The view is
// updated in place (the lap line, the fieldset), never rebuilt, so Tab and clicks land where they were aimed.
function tourEdit(e) {
  const el = e.target, number = el.type === 'number';
  if (number !== (e.type === 'input')) return;   // numbers as you type, tick boxes when they change
  if (el.dataset.tour) {
    snap(number ? 'tour-' + el.dataset.tour : null);
    S.tour ??= {};
    const k = el.dataset.tour;
    if (!number) S.tour[k] = el.checked;
    else if (el.value === '') delete S.tour[k];
    else S.tour[k] = Math.round(+el.value);
  } else if (el.dataset.tourtab) {
    snap();
    S.tour ??= {};
    const name = S.tabs[+el.dataset.tourtab].name, skip = (S.tour.skip ?? []).filter(x => x !== name);
    if (!el.checked) skip.push(name);
    S.tour.skip = S.tabs.map(x => x.name).filter(x => skip.includes(x));
  } else return;
  $('lap').innerHTML = lapText();
  $('settings').querySelector('fieldset').disabled = !tourNow().on;
  renderTop();
}
$('settings').addEventListener('input', tourEdit);
$('settings').addEventListener('change', tourEdit);

// ---------- loading, saving, the gate ----------
function applyLoaded(r) {
  S = M.toSource(r.layout, src);
  saved = JSON.stringify(S);
  [etag, fromFile, refused, ui] = [r.etag, r.fromFile, null, {}];
  cur = Math.min(cur, S.tabs.length - 1);
  if (!card(sel)) sel = null;
  setLiveBase(S);
  loadNote = r.error ? `<span><code>layout.json</code> on the server has a mistake: <code>${esc(r.error)}</code>. The panel, and this page, show the last good layout (or the default) instead. Save replaces the file; the old one is kept as <code>layout.json.bak</code>.</span>`
    : r.fromFile ? '' : '<span>There is no <code>layout.json</code> on the server yet: this is the generated default the panel shows. Save creates the file.</span>';
}
const FROM = api.demo ? 'the example layout' : 'the file as it is on the server';
async function reloadAll() {
  const before = S && JSON.stringify(S);
  try {
    src = await api.sources();
    applyLoaded(await api.loadLayout());
    live = await api.preview().catch(() => live);
  } catch (e) {
    if (e.message === 'locked') return;
    refused = {kind: 'load', msg: e.message};
    return render();
  }
  renderPalette();
  if (before && JSON.stringify(S) !== before) { undo.push(before); redo = []; lastKey = null; }
  render();
  say(before ? `Back to ${FROM}. Undo brings your version back.` : '');
}
function revert() {
  if (dirty()) ask(`Go back to ${FROM}? Your unsaved changes go; Undo brings them back.`, 'Go back', reloadAll);
  else reloadAll();
}
async function save() {
  if (saving) return;   // Ctrl+S twice would race the page's own save into a 412
  saving = $('save').disabled = true;
  const body = source();
  let r;
  try { r = await api.saveLayout(body, etag); } catch (e) { r = {thrown: e}; }
  saving = $('save').disabled = false;
  $('toast').hidden = true;
  if (r.thrown) {
    if (r.thrown.message !== 'locked') refused = r.thrown.offline ? {kind: 'net'} : {kind: 'error', msg: r.thrown.message};
  } else if (r.ok) {
    [etag, saved, refused, fromFile, loadNote] = [r.etag, JSON.stringify(body), null, true, ''];
    setLiveBase(body);   // what the server now has, not what the page has become meanwhile
    toast(api.demo ? 'Downloaded layout.json. It goes in aggregator/config/.' : 'Saved. The panel follows in about 20 seconds.');
  } else if (r.status === 412) refused = {kind: 'stale', msg: r.message};
  else {
    // tab and card index the layout that was sent; find that card (or tab) in the page as it is now
    const sentTab = body.tabs[r.tab], id = r.card >= 0 ? sentTab?.cards[r.card]?.id ?? null : null;
    const i = id ? S.tabs.findIndex(t => t.cards.some(c => c.id === id)) : S.tabs.findIndex(t => t.name === sentTab?.name);
    refused = {kind: 'bad', msg: r.message, id: i >= 0 ? id : null};
    if (i >= 0) [view, cur, sel, ui] = ['screens', i, refused.id, {}];
  }
  render();
}
function showLoading(msg, retry) {
  for (const id of ['app', 'gate', 'drawer']) $(id).hidden = true;
  $('loading').hidden = false;
  $('loadmsg').textContent = msg;
  $('retry').hidden = !retry;
}
async function start() {
  if (!S) {
    showLoading('Loading…');
    try {
      const [s, l, p] = await Promise.all([api.sources(), api.loadLayout(), api.preview().catch(e => { if (e.message === 'locked') throw e; return null; })]);
      src = s;
      if (p) live = p;
      applyLoaded(l);
    } catch (e) {
      if (e.message !== 'locked') showLoading(e.offline ? e.message : "Can't load the editor: " + e.message, true);
      return;
    }
    renderPalette();
  }
  for (const id of ['loading', 'gate']) $(id).hidden = true;
  $('app').hidden = false;
  render();
}
function showGate(msg) {
  for (const id of ['app', 'drawer', 'loading']) $(id).hidden = true;
  $('gate').hidden = false;
  $('gateerr').textContent = msg || '';
  $('pw').value = '';
  $('pw').focus();
}
// boot: the demo needs no password; otherwise a remembered one goes straight in, a server that doesn't answer says so.
function boot() {
  if (api.demo) return start();
  showLoading('Loading…');
  api.unlock().then(ok => ok ? start() : showGate(), e => showLoading(e.offline ? `${e.message} Is the aggregator running?` : "Can't open the editor: " + e.message, true));
}
$('gateform').addEventListener('submit', async e => {
  e.preventDefault();
  $('unlock').disabled = true;
  $('gateerr').textContent = '';
  let ok = false;
  try { ok = await api.unlock($('pw').value); } catch (err) { $('gateerr').textContent = err.message; }
  $('unlock').disabled = false;
  if (ok) { $('pw').value = ''; start(); } else if (!$('gateerr').textContent) { $('gateerr').textContent = 'Wrong password.'; $('pw').select(); }
});
addEventListener('editor-locked', () => showGate("The server didn't take the password this page had. Enter it again; your changes are still here."));
$('retry').onclick = boot;
$('lock').onclick = () => { api.lock(); showGate(); };

// ---------- top bar ----------
$('vScreens').onclick = () => { view = 'screens'; render(); };
$('vSettings').onclick = () => { view = 'settings'; render(); };
const doUndo = () => { if (undo.length) { redo.push(JSON.stringify(S)); restore(undo.pop()); say('Undone.'); } };
const doRedo = () => { if (redo.length) { undo.push(JSON.stringify(S)); restore(redo.pop()); say('Redone.'); } };
$('undo').onclick = doUndo;
$('redo').onclick = doRedo;
$('revert').onclick = revert;
$('save').onclick = save;
$('refused').addEventListener('click', e => {
  if (e.target.id === 'reload') return revert();
  if (e.target.id === 'retryload') return reloadAll();
  if (e.target.id !== 'showcard' || !refused?.id) return;
  const i = S.tabs.findIndex(t => t.cards.some(c => c.id === refused.id));
  if (i < 0) return;
  [view, cur, sel] = ['screens', i, refused.id];
  render();
  focusCard();
});
$('viewjson').onclick = () => { $('drawer').hidden = false; renderTop(); $('closejson').focus(); };
$('closejson').onclick = () => { $('drawer').hidden = true; $('viewjson').focus(); };
$('copy').onclick = async () => {
  let ok = true;
  try { await navigator.clipboard.writeText(M.fileText(source())); } catch {   // plain http: no clipboard API, so select and copy
    const r = document.createRange();
    r.selectNodeContents($('jsonout'));
    getSelection().removeAllRanges();
    getSelection().addRange(r);
    try { ok = document.execCommand('copy'); } catch { ok = false; }
  }
  $('copy').textContent = ok ? 'Copied' : "Couldn't copy: it's selected, press Ctrl+C";
  setTimeout(() => { $('copy').textContent = 'Copy'; }, ok ? 1500 : 4000);
};
$('download').onclick = () => {
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([M.fileText(source())], {type: 'application/json'}));
  a.download = 'layout.json';
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
};
addEventListener('beforeunload', e => { if (S && dirty()) { e.preventDefault(); e.returnValue = ''; } });

// ---------- keyboard ----------
addEventListener('keydown', e => {
  if (drag && e.key === 'Escape') { e.preventDefault(); return cancelDrag(); }
  if ($('app').hidden || $('dlg').open) return;
  if (e.key === 'Escape' && !$('drawer').hidden) return $('closejson').click();
  const typing = e.target.matches('input, textarea, select');
  if ((e.ctrlKey || e.metaKey) && !typing) {
    const k = e.key.toLowerCase();
    if (k === 'z' && !e.shiftKey) { e.preventDefault(); return doUndo(); }
    if (k === 'y' || (k === 'z' && e.shiftKey)) { e.preventDefault(); return doRedo(); }
  }
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') { e.preventDefault(); return save(); }
  if (view !== 'screens' || typing) return;
  const onTab = e.target.closest('#tb [role=tab]');
  if (onTab) {   // the tab bar is a tablist: arrows, Home and End move between tabs
    const n = S.tabs.length, i = {ArrowLeft: cur - 1, ArrowRight: cur + 1, Home: 0, End: n - 1}[e.key];
    if (i === undefined) return;
    e.preventDefault();
    goTab((i + n) % n);
    $('tab-' + cur).focus();
    return;
  }
  const onCard = e.target.closest('.pc');
  if (!onCard) return;   // card keys act on the card with the focus, which is the selected one
  const c = card(onCard.dataset.id);
  if (!c) return;
  if (sel !== c.id) select(c.id);
  if (e.key === 'Escape') { select(null); return $('grid').focus({preventScroll: true}); }   // keys stay in the panel
  const d = {ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1]}[e.key];
  if (d) {
    e.preventDefault();
    const at = [c.at[0] + d[0], c.at[1] + d[1]], before = JSON.stringify(S), sw = M.fits(tab(), c, at, c.size) ? null : M.swapWith(tab(), c, at);
    if (!M.move(S, cur, c.id, at)) {
      say(`Can't move there: it would overlap a card or leave the grid.`, 1);
      const [w, h] = M.wh(c.size);
      showTarget(at, w, h, false);
      setTimeout(() => { if (!drag && $('target')) $('target').hidden = true; }, 500);
      return;
    }
    snap('key' + c.id, before);
    render();
    focusCard();
    say(sw ? `Swapped with ${sw.id}.` : `${c.id} at column ${c.at[0]}, row ${c.at[1]}`);
  } else if (e.key === 'Delete') {
    e.preventDefault();
    const i = tab().cards.indexOf(c);
    act('del');
    const next = tab().cards[Math.min(i, tab().cards.length - 1)];   // the next card takes the focus (and so the selection)
    if (next) $('pc-' + next.id).focus({preventScroll: true});
    else $('grid').focus({preventScroll: true});
  }
});

// The server's values, again every 15 s while the editor is open and in view, and at once when it comes back.
async function poll() {
  if (api.demo || !S || $('app').hidden || document.hidden) return;
  try { live = await api.preview(); } catch { return; }
  if (view === 'screens') renderPreview();   // a no-op under a drag
}
setInterval(poll, 15000);
document.addEventListener('visibilitychange', () => { if (!document.hidden) poll(); });

// ---------- start ----------
if (api.demo) {
  $('demobar').hidden = false;
  $('lock').hidden = true;
  $('save').textContent = 'Download layout.json';
  $('revert').title = 'Back to the example layout';
}
boot();
