// The flasher site's stand-in for backend.js (same exports): made-up data from demo-data.json. No password,
// and nothing leaves the page: Save downloads layout.json instead, and Revert goes back to the example layout.
import {fileText} from './model.js';

export const demo = true;

const copy = v => JSON.parse(JSON.stringify(v));
let data;
async function load() {
  if (!data) {
    const r = await fetch('demo-data.json', {cache: 'no-store'});
    if (!r.ok) throw new Error(`demo-data.json: ${r.status} ${r.statusText}`);
    data = await r.json();
  }
  return data;
}

export async function unlock() { return true; }
export function lock() {}

export async function loadLayout() {
  return {layout: copy((await load()).layout), etag: '"demo"', fromFile: true, error: ''};
}

export async function saveLayout(layout) {
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([fileText(layout)], {type: 'application/json'}));
  a.download = 'layout.json';
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  return {ok: true, etag: '"demo"'};
}

export async function sources() { return copy((await load()).sources); }

// test answers as the server does, with the card as /cards serves it: the reading of the example card that has the
// same type and value, if there is one.
export async function test(spec) {
  const d = await load();
  for (const t of d.layout.tabs) for (const c of t.cards) {
    if (c.type === spec.type && JSON.stringify(c.value) === JSON.stringify(spec.value) && d.cards.cards[c.id]) return copy(d.cards.cards[c.id]);
  }
  return {error: 'the demo only has readings for the values already on its cards'};
}

export async function preview() {
  const d = await load();
  return copy({panel: d.panel, cards: d.cards});
}
