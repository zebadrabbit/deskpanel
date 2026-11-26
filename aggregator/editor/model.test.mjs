// The editor page's layout logic. Run: sh aggregator/editor/test.sh (node --test in Docker).
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import * as m from './model.js';

const demo = JSON.parse(readFileSync(new URL('./demo-data.json', import.meta.url), 'utf8'));
const src = demo.sources;
const num = (id, at, size, more) => ({id, type: 'number', at, size, value: 'cpu', ...more});
const clone = x => JSON.parse(JSON.stringify(x));

test('bytes counts UTF-8 bytes', () => {
  assert.equal(m.bytes('ééééééé'), 14);
  assert.equal(m.bytes('abc'), 3);
});

test('fits refuses an overlap, an off-grid place and a 4x6 on a used tab', () => {
  const tab = {name: 'T', cards: [num('a', [1, 1], '2x2')]};
  assert.equal(m.fits(tab, null, [3, 1], '2x2'), true);
  assert.equal(m.fits(tab, null, [2, 2], '2x2'), false, 'overlap');
  assert.equal(m.fits(tab, null, [4, 1], '2x2'), false, 'off the right edge');
  assert.equal(m.fits(tab, null, [1, 6], '1x2'), false, 'off the bottom');
  assert.equal(m.fits(tab, null, [0, 1], '1x1'), false, 'column 0');
  assert.equal(m.fits(tab, null, [1, 1], '4x6'), false, '4x6 on a used tab');
  assert.equal(m.fits({cards: []}, null, [1, 1], '4x6'), true, '4x6 on an empty tab');
  assert.equal(m.fits(tab, tab.cards[0], [1, 1], '2x2'), true, 'a card fits where it is');
  assert.equal(m.fits(tab, tab.cards[0], [2, 2], '2x2'), true, 'a card may overlap its own old place');
});

test('firstFree scans rows then columns and returns null on a full tab', () => {
  const tab = {cards: [num('a', [1, 1], '1x1')]};
  assert.deepEqual(m.firstFree(tab, '1x1'), [2, 1], 'column-major would give [1, 2]');
  assert.deepEqual(m.firstFree(tab, '2x2'), [2, 1], 'column-major would give [1, 2]');
  tab.cards.push(num('b', [2, 1], '2x2'));
  assert.deepEqual(m.firstFree(tab, '1x1'), [4, 1], 'the rest of row 1 first; column-major would give [1, 2]');
  assert.equal(m.firstFree({cards: [{id: 'b', type: 'board', at: [1, 1], size: '4x6'}]}, '1x1'), null);
  assert.equal(m.firstFree(tab, '4x6'), null);
});

test('sizesFor reads the server table; nearestSize only returns listed sizes', () => {
  assert.deepEqual(m.sizesFor(src, 'pool'), src.types.pool.sizes);
  assert.deepEqual(m.sizesFor(src, 'nope'), []);
  const pool = ['1x2', '1x1', '2x2'];
  for (const [w, h] of [[2, 1], [1.4, 1.4], [3, 6], [0, 0], [2.2, 0.6]]) assert.ok(pool.includes(m.nearestSize(pool, w, h)), `${w}x${h}`);
  assert.equal(m.nearestSize(['1x2', '2x3'], 1.2, 2.2), '1x2');
  assert.equal(m.nearestSize(['1x2', '2x3'], 1.8, 2.9), '2x3');
  assert.equal(m.nearestSize(src.types.gpu.sizes, 1.4, 0.9), '1x1');
  assert.equal(m.nearestSize(src.types.gpu.sizes, 2, 1), '2x2', 'a tie goes to the size listed first');
});

test('move swaps two same-size cards and refuses a different-size overlap', () => {
  const l = {tabs: [{name: 'T', cards: [num('a', [1, 1], '1x2'), num('b', [2, 1], '1x2'), num('c', [3, 1], '2x2')]}]};
  const at = id => l.tabs[0].cards.find(c => c.id === id).at;
  assert.equal(m.move(l, 0, 'a', [2, 1]), true);
  assert.deepEqual([at('a'), at('b')], [[2, 1], [1, 1]], 'swapped');
  assert.equal(m.move(l, 0, 'a', [1, 2]), true, 'a partial drop onto one same-size card swaps too');
  assert.deepEqual([at('a'), at('b')], [[1, 1], [2, 1]]);
  assert.equal(m.move(l, 0, 'a', [3, 1]), false, 'onto the 2x2');
  assert.deepEqual(at('a'), [1, 1], 'unchanged after a refusal');
  assert.equal(m.move(l, 0, 'a', [1, 5]), true);
  assert.deepEqual(at('a'), [1, 5]);
  assert.equal(m.move(l, 0, 'a', [1, 6]), false, 'off the grid');
  assert.equal(m.move(l, 0, 'a', [1, 0]), false, 'off the grid at the top');
  assert.equal(m.swapWith(l.tabs[0], l.tabs[0].cards[0], [2, 1]), l.tabs[0].cards[1]);
  assert.equal(m.swapWith(l.tabs[0], l.tabs[0].cards[0], [3, 1]), null);
});

test('renameTab rewrites taps and tour.skip, refuses a duplicate and a 16-byte name', () => {
  const l = {tour: {skip: ['Old', 'Other']}, tabs: [
    {name: 'Old', cards: [num('x', [1, 1], '1x1', {tap: 'tab:Other'})]},
    {name: 'Other', cards: [num('y', [1, 1], '1x1', {tap: 'tab:Old'}), num('z', [2, 1], '1x1', {tap: 'tab:Oldest'})]},
  ]};
  assert.equal(m.renameTab(l, 0, 'New', 15), '');
  assert.equal(l.tabs[0].name, 'New');
  assert.equal(l.tabs[1].cards[0].tap, 'tab:New');
  assert.equal(l.tabs[1].cards[1].tap, 'tab:Oldest', 'only an exact match');
  assert.deepEqual(l.tour.skip, ['New', 'Other']);
  const before = clone(l);
  assert.notEqual(m.renameTab(l, 0, 'Other', 15), '', 'duplicate');
  assert.notEqual(m.renameTab(l, 0, 'abcdefghijklmnop', 15), '', '16 bytes');
  assert.notEqual(m.renameTab(l, 0, 'éééééééé', 15), '', '16 bytes in 8 characters');
  assert.notEqual(m.renameTab(l, 0, '', 15), '', 'empty');
  assert.deepEqual(l, before, 'a refused rename changes nothing');
  assert.equal(m.renameTab(l, 0, 'ééééééé', 15), '', '14 bytes is fine');
  assert.equal(l.tabs[1].cards[0].tap, 'tab:ééééééé');
  assert.equal(m.renameTab(l, 0, 'ééééééé', 15), '', 'renaming to its own name is fine');
});

test('deleteTab resets taps to none and drops the skip entry', () => {
  const l = {tour: {skip: ['Gone', 'Keep']}, tabs: [
    {name: 'Keep', cards: [num('x', [1, 1], '1x1', {tap: 'tab:Gone'}), num('w', [2, 1], '1x1', {tap: 'notifications'})]},
    {name: 'Gone', cards: [num('y', [1, 1], '1x1')]},
  ]};
  m.deleteTab(l, 1);
  assert.deepEqual(l.tabs.map(t => t.name), ['Keep']);
  assert.equal(l.tabs[0].cards[0].tap, 'none');
  assert.equal(l.tabs[0].cards[1].tap, 'notifications');
  assert.deepEqual(l.tour.skip, ['Keep']);
});

test('newId is unique across all tabs and at most 23 bytes', () => {
  const l = {tabs: [{name: 'A', cards: [num('cpu', [1, 1], '1x1')]}, {name: 'B', cards: [num('cpu-2', [1, 1], '1x1')]}]};
  assert.equal(m.newId(l, 'cpu', 23), 'cpu-3');
  assert.equal(m.newId(l, 'ram', 23), 'ram');
  const long = 'a'.repeat(30);
  const first = m.newId(l, long, 23);
  assert.equal(first.length, 23);
  l.tabs[0].cards.push(num(first, [2, 1], '1x1'));
  const second = m.newId(l, long, 23);
  assert.ok(second !== first && m.bytes(second) <= 23, second);
  assert.equal(m.newId(l, 'gpu-workstation#2', 23), 'gpu-workstation-2');
  for (let i = 0; i < 12; i++) {
    const id = m.newId(l, long, 23);
    assert.ok(m.bytes(id) <= 23 && !l.tabs.some(t => t.cards.some(c => c.id === id)), id);
    l.tabs[1].cards.push(num(id, [1, 1], '1x1'));
  }
});

test('toSource omits default taps and default slots and round-trips the demo layout unchanged', () => {
  const l = {tabs: [{name: 'T', cards: [
    {id: 'n', type: 'nas-alerts', at: [1, 1], size: '1x2', tap: 'alert-details'},
    {id: 'n2', type: 'nas-alerts', at: [2, 1], size: '1x2', tap: 'none'},
    {id: 'g', type: 'gpu', at: [1, 3], size: '2x3', src: 'workstation', tap: 'none', slots: {gauge: 'temp', line1: {value: 'vram', as: 'bar'}, line2: ['util', 'power']}},
    {slots: {gauge: 'temp', line1: {value: 'vram', as: 'graph'}}, size: '2x3', at: [3, 3], type: 'gpu', id: 'h', src: 'server'},
    {id: 'x', type: 'number', at: [3, 1], size: '1x1', title: '', unit: '', warn: null, bad: 0, below: false, value: 'cpu', tap: ''},
  ]}]};
  const [n, n2, g, h, x] = m.toSource(l, src).tabs[0].cards;
  assert.equal('tap' in n, false, "nas-alerts' own default");
  assert.equal(n2.tap, 'none', 'none is not the nas-alerts default');
  assert.equal('tap' in g || 'slots' in g, false, 'default tap and slots');
  assert.deepEqual(h.slots, {line1: {value: 'vram', as: 'graph'}}, 'only what differs from the default');
  assert.deepEqual(Object.keys(h), ['id', 'type', 'at', 'size', 'src', 'slots'], "layout.json's key order");
  assert.deepEqual(x, {id: 'x', type: 'number', at: [3, 1], size: '1x1', value: 'cpu', bad: 0}, 'empty fields gone, 0 kept');

  // the demo layout comes back as it went in, apart from taps spelled out at their default
  const want = clone(demo.layout);
  for (const t of want.tabs) for (const c of t.cards) if (c.tap === src.types[c.type].default_tap) delete c.tap;
  const out = m.toSource(demo.layout, src);
  assert.deepEqual(out, want);
  assert.deepEqual(m.toSource(out, src), out, 'idempotent');
  assert.deepEqual(JSON.parse(m.fileText(out)), out, 'fileText is the same JSON');
});

test('toSource round-trips every kind of field, dropping only taps written out at their default', () => {
  const card = (id, type, at, size, more) => ({id, type, at, size, ...more});
  const given = {tour: {on: false, idle_min: 10, clock: false, skip: ['B']}, tabs: [
    {name: 'A', cards: [
      card('two', 'graph', [1, 1], '2x2', {title: 'Two', tap: 'tab:B', value: ['cpu@server:9100', {prometheus: 'up{job="a"}'}], unit: '%', warn: 80, bad: 95, min: 0, max: 100}),
      card('js', 'number', [3, 1], '1x1', {tap: 'none', value: {json: {url: 'http://app.lan/api/status', path: 'queue.depth', every: 60, header_env: 'CARD_APP_TOKEN'}}, unit: 'jobs'}),
      card('ha', 'bar', [4, 1], '1x1', {value: {ha: 'light.kitchen', attr: 'brightness', every: 20}, min: 0, max: 255}),
      card('free', 'bar', [3, 2], '1x2', {value: 'disk:/@server:9100', warn: 20, bad: 10, below: true}),
      card('g', 'gpu', [1, 3], '2x3', {src: 'workstation', tap: 'none', slots: {line1: {value: 'fan', as: 'graph'}, line2: []}}),
      card('alerts', 'nas-alerts', [3, 4], '1x2', {tap: 'none'}),
      card('alerts-2', 'nas-alerts', [4, 4], '1x2', {tap: 'alert-details'}),
    ]},
    {name: 'B', cards: [card('board', 'board', [1, 1], '4x6', {tap: 'none'})]},
  ]};
  const want = clone(given);
  for (const id of ['js', 'g']) delete want.tabs[0].cards.find(c => c.id === id).tap;   // none: their default
  delete want.tabs[0].cards.find(c => c.id === 'alerts-2').tap;                          // alert-details: nas-alerts' default
  delete want.tabs[1].cards[0].tap;
  const out = m.toSource(given, src);
  assert.deepEqual(out, want);
  assert.equal(out.tabs[0].cards.find(c => c.id === 'alerts').tap, 'none', "nas-alerts' none is not its default: kept");
  assert.deepEqual(out.tabs[0].cards.find(c => c.id === 'g').slots.line2, [], 'an empty line 2 is kept');
  assert.deepEqual(m.toSource(out, src), out, 'idempotent');
  assert.deepEqual(JSON.parse(m.fileText(out)), out, 'fileText is the same JSON');
});

test('toSource round-trips light and profile cards as written, key order intact', () => {
  const given = {tabs: [{name: 'Lights', cards: [
    {id: 'desk', type: 'light', at: [1, 2], size: '2x1', title: 'Desk', value: {ha: 'light.desk'}},
    {id: 'fan', type: 'light', at: [3, 2], size: '1x1', value: {ha: 'switch.fan'}},
    {id: 'morning', type: 'profile', at: [1, 1], size: '1x1', title: 'Morning'},
  ]}]};
  const out = m.toSource(given, src);
  assert.deepEqual(out, given);
  assert.deepEqual(out.tabs[0].cards.map(c => Object.keys(c)), [
    ['id', 'type', 'at', 'size', 'title', 'value'], ['id', 'type', 'at', 'size', 'value'], ['id', 'type', 'at', 'size', 'title']]);
  assert.deepEqual(Object.keys(out.tabs[0].cards[0].value), ['ha'], 'no attr, no every');
  // keys in another order, an empty title and the default tap spelled out come back in the file's form
  const messy = {tabs: [{name: 'Lights', cards: [
    {value: {ha: 'light.desk'}, title: 'Desk', size: '2x1', tap: 'none', at: [1, 2], type: 'light', id: 'desk'},
    {title: '', value: {ha: 'switch.fan'}, size: '1x1', at: [3, 2], type: 'light', id: 'fan'},
    {title: 'Morning', tap: 'none', size: '1x1', at: [1, 1], type: 'profile', id: 'morning'},
  ]}]};
  assert.equal(JSON.stringify(m.toSource(messy, src)), JSON.stringify(given));
  assert.match(m.fileText(out), /^ {8}\{"id": "desk", "type": "light", "at": \[1, 2\], "size": "2x1", "title": "Desk", "value": \{"ha": "light.desk"\}\},$/m);
  assert.match(m.fileText(out), /^ {8}\{"id": "morning", "type": "profile", "at": \[1, 1\], "size": "1x1", "title": "Morning"\}$/m);
  assert.deepEqual(m.sizesFor(src, 'light'), ['1x1', '1x2', '2x1', '2x2']);
  assert.deepEqual(m.sizesFor(src, 'profile'), ['1x1', '2x1']);
  assert.equal(m.nearestSize(m.sizesFor(src, 'profile'), 2, 2), '2x1', 'a profile never grows to 2x2');
});

test('toSource takes a layout with no tabs or no cards (null in the JSON) without throwing', () => {
  assert.deepEqual(m.toSource({tabs: null}, src), {tabs: []});
  assert.deepEqual(m.toSource({tabs: [{name: 'A', cards: null}]}, src), {tabs: [{name: 'A', cards: []}]});
});

test('toSource keeps the tour block the page was given, minus an empty skip', () => {
  const tabs = [{name: 'A', cards: []}];
  assert.equal('tour' in m.toSource({tabs}, src), false);
  assert.equal('tour' in m.toSource({tour: {skip: []}, tabs}, src), false);
  const out = m.toSource({tour: {skip: ['A'], tab_s: 30, on: false}, tabs}, src);
  assert.deepEqual(out.tour, {on: false, tab_s: 30, skip: ['A']});
  assert.deepEqual(Object.keys(out.tour), ['on', 'tab_s', 'skip']);
  assert.deepEqual(Object.keys(out), ['tour', 'tabs']);
});

test('fileText writes one card per line, as layout-default does', () => {
  const text = m.fileText({tour: {tab_s: 30}, tabs: [
    {name: 'A', cards: [{id: 'a', type: 'number', at: [1, 1], size: '1x1', title: 'x, y: "z"', value: {prometheus: 'up{job="a"}'}}]},
    {name: 'B', cards: []},
  ]});
  assert.equal(text, [
    '{',
    '  "tour": {"tab_s": 30},',
    '  "tabs": [',
    '    {',
    '      "name": "A",',
    '      "cards": [',
    '        {"id": "a", "type": "number", "at": [1, 1], "size": "1x1", "title": "x, y: \\"z\\"", "value": {"prometheus": "up{job=\\"a\\"}"}}',
    '      ]',
    '    },',
    '    {',
    '      "name": "B",',
    '      "cards": [',
    '      ]',
    '    }',
    '  ]',
    '}',
    ''].join('\n'));
});
