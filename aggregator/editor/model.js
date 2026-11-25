// The editor's layout logic: plain objects in, plain objects out, no DOM. Layouts are in the source form
// (what layout.json holds). The server owns every rule; this is the grid's arithmetic and the
// bookkeeping a drag, a rename or a save needs.

// ponytail: the panel's grid. The sources endpoint sends no cols/rows; the preview's pixel drawing is fixed to it too.
export const COLS = 4, ROWS = 6;

export const bytes = s => new TextEncoder().encode(s).length;
export const wh = size => String(size).split('x').map(Number);
const clone = v => JSON.parse(JSON.stringify(v));

function overlaps(a, as, b, bs) {
  const [aw, ah] = wh(as), [bw, bh] = wh(bs);
  return a[0] < b[0] + bw && b[0] < a[0] + aw && a[1] < b[1] + bh && b[1] < a[1] + ah;
}
function inGrid(at, size) {
  const [w, h] = wh(size);
  return Number.isInteger(at[0]) && Number.isInteger(at[1]) && at[0] >= 1 && at[1] >= 1 && at[0] + w - 1 <= COLS && at[1] + h - 1 <= ROWS;
}
const hits = (tab, card, at, size) => tab.cards.filter(o => o !== card && overlaps(at, size, o.at, o.size));

// fits: card (null for a new one) can sit at `at` with `size` on tab. A 4x6 covers the grid, so it only fits an empty tab.
export function fits(tab, card, at, size) {
  return inGrid(at, size) && hits(tab, card, at, size).length === 0;
}

// firstFree is the first place a card of `size` fits, row by row, or null.
export function firstFree(tab, size) {
  for (let row = 1; row <= ROWS; row++) for (let col = 1; col <= COLS; col++) if (fits(tab, null, [col, row], size)) return [col, row];
  return null;
}

export const sizesFor = (sources, type) => sources.types[type]?.sizes ?? [];

// nearestSize is the listed size closest to w x h (fractional, from a corner drag).
export function nearestSize(sizes, w, h) {
  let best = sizes[0], d = Infinity;
  for (const s of sizes) {
    const [sw, sh] = wh(s), e = Math.hypot(sw - w, sh - h);
    if (e < d) [best, d] = [s, e];
  }
  return best;
}

// swapWith is the card that card would trade places with when dropped at `at`: the only one it would
// cover, and the same size. Else null.
export function swapWith(tab, card, at) {
  if (!inGrid(at, card.size)) return null;
  const h = hits(tab, card, at, card.size);
  return h.length === 1 && h[0].size === card.size ? h[0] : null;
}

// move puts card `id` at `at`, or swaps it with the one same-size card there. False (and nothing changed) otherwise.
export function move(layout, tabIdx, id, at) {
  const tab = layout.tabs[tabIdx], c = tab.cards.find(x => x.id === id);
  if (!c) return false;
  if (fits(tab, c, at, c.size)) {
    c.at = [at[0], at[1]];
    return true;
  }
  const o = swapWith(tab, c, at);
  if (!o) return false;
  [c.at, o.at] = [o.at, c.at];
  return true;
}

// renameTab renames tab i; taps and tour.skip follow. '' when done, else why not (nothing changed).
export function renameTab(layout, i, name, max) {
  if (!name) return 'A tab needs a name.';
  if (bytes(name) > max) return `${bytes(name)} bytes: ${max} is the most.`;
  if (layout.tabs.some((t, k) => k !== i && t.name === name)) return 'Another tab has that name.';
  const old = layout.tabs[i].name;
  for (const t of layout.tabs) for (const c of t.cards) if (c.tap === 'tab:' + old) c.tap = 'tab:' + name;
  if (layout.tour?.skip) layout.tour.skip = layout.tour.skip.map(s => s === old ? name : s);
  layout.tabs[i].name = name;
  return '';
}

// deleteTab removes tab i: taps that jumped to it do nothing now, and the tour forgets it.
export function deleteTab(layout, i) {
  const [gone] = layout.tabs.splice(i, 1);
  for (const t of layout.tabs) for (const c of t.cards) if (c.tap === 'tab:' + gone.name) c.tap = 'none';
  if (layout.tour?.skip) layout.tour.skip = layout.tour.skip.filter(s => s !== gone.name);
  return gone;
}

// newId is base made of a-z, 0-9 and '-', at most max bytes, unique across every tab (-2, -3, ... when taken).
export function newId(layout, base, max) {
  const ids = new Set(layout.tabs.flatMap(t => t.cards.map(c => c.id)));
  base = String(base).toLowerCase().replace(/[^a-z0-9-]/g, '-').slice(0, max) || 'card';
  let id = base;
  for (let n = 2; ids.has(id); n++) id = base.slice(0, max - String(n).length - 1) + '-' + n;
  return id;
}

// Key orders of layout.json, as the server writes it (Go struct order); unknown keys follow, kept.
const CARD_KEYS = ['id', 'type', 'at', 'size', 'src', 'title', 'tap', 'value', 'unit', 'warn', 'bad', 'min', 'max', 'below', 'slots'];
const TOUR_KEYS = ['on', 'idle_min', 'clock_s', 'tab_s', 'clock', 'skip'];
const empty = v => v === undefined || v === null || v === '' || v === false;

function ordered(o, keys, drop) {
  const out = {};
  for (const k of [...keys, ...Object.keys(o)]) if (!(k in out) && !drop(k, o[k])) out[k] = clone(o[k]);
  return out;
}

function slotsOut(s, d) {
  if (!s) return undefined;
  const o = {};
  if (s.gauge && s.gauge !== d.gauge) o.gauge = s.gauge;
  if (s.line1 && (s.line1.value !== d.line1.value || s.line1.as !== d.line1.as)) o.line1 = {value: s.line1.value, as: s.line1.as};
  if (s.line2 && JSON.stringify(s.line2) !== JSON.stringify(d.line2)) o.line2 = [...s.line2];
  return Object.keys(o).length ? o : undefined;
}

// toSource is the layout to save: empty fields, taps at their type's default and default slots left out.
export function toSource(layout, sources) {
  const out = {};
  if (layout.tour) {
    const tour = ordered(layout.tour, TOUR_KEYS, (k, v) => v == null || k === 'skip' && !v.length);
    if (Object.keys(tour).length) out.tour = tour;
  }
  out.tabs = (layout.tabs ?? []).map(t => ({name: t.name, cards: (t.cards ?? []).map(c => {
    const o = ordered({...c, slots: slotsOut(c.slots, sources.slots.default)}, CARD_KEYS, (k, v) => empty(v));
    if (o.tap === (sources.types[c.type]?.default_tap ?? 'none')) delete o.tap;
    return o;
  })}));
  return out;
}

// oneLine is compact JSON with a space after each ':' and ',' outside strings, as the server writes a card.
const oneLine = v => JSON.stringify(v).replace(/("(?:[^"\\]|\\.)*")|([:,])/g, (m, str, p) => str ?? p + ' ');

// fileText is the file Save writes (the server's handLayout format: one card per line).
export function fileText(src) {
  let s = '{\n';
  if (src.tour) s += '  "tour": ' + oneLine(src.tour) + ',\n';
  s += '  "tabs": [\n' + src.tabs.map(t => '    {\n      "name": ' + oneLine(t.name) + ',\n      "cards": [\n' +
    t.cards.map(c => '        ' + oneLine(c)).join(',\n') + (t.cards.length ? '\n' : '') + '      ]\n    }').join(',\n') + '\n  ]\n}\n';
  return s;
}
