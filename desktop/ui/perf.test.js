// Run with: node --test desktop/ui/*.test.js
const test = require('node:test');
const assert = require('node:assert/strict');
global.window = {};
global.localStorage = { getItem: () => { throw new Error('blocked'); }, setItem: () => { throw new Error('blocked'); } };
require('./perf.js');
const perf = window.ArkismPerf;

test('timing summary gives median, 95th percentile, slowest and largest size per kind, even with storage blocked', () => {
  for (let i = 1; i <= 100; i++) perf.note('canvas.edges', i, i * 2);
  perf.note('canvas.render', 7, 40);
  const s = perf.summary();
  assert.deepEqual(s['canvas.edges'], { n: 100, p50: 51, p95: 96, max: 100, size: 200 });
  assert.deepEqual(s['canvas.render'], { n: 1, p50: 7, p95: 7, max: 7, size: 40 });
});

test('only the last 500 samples per kind are kept', () => {
  perf.clear();
  for (let i = 0; i < 600; i++) perf.note('canvas.drag', i);
  assert.equal(perf.summary()['canvas.drag'].n, 500);
  assert.equal(perf.summary()['canvas.drag'].p50 >= 100, true);
});
