// Run with: node --test desktop/ui/*.test.js
const test = require('node:test');
const assert = require('node:assert/strict');
global.window = {};
require('./catalog.js');
const catalog = window.ArkismCatalog;

test('skill sources normalize to owner/repo[/path][@ref] like the host and server expect', () => {
  const cases = {
    'o/r': 'o/r', ' o/r.git ': 'o/r', 'o/r/skills/pdf@v1.2': 'o/r/skills/pdf@v1.2',
    'https://github.com/o/r': 'o/r', 'github.com/o/r/tree/main/skills/pdf': 'o/r/skills/pdf@main',
    'https://github.com/o/r/blob/abc/x/SKILL.md': 'o/r/x@abc', 'https://github.com/o/r?tab=readme': 'o/r',
  };
  for (const [input, want] of Object.entries(cases)) assert.equal(catalog.parseSource(input), want, input);
  for (const bad of ['o', '', 'o/r/../x', 'o/r/./x', 'https://github.com/o/r/issues/1', 'o/r; rm -rf ~', 'o/r@..', 'o/r/a b'])
    assert.equal(catalog.parseSource(bad), '', bad);
});

test('MCP presets fill a valid dialog: name, method and a program or https address', () => {
  assert.ok(catalog.PRESETS.length >= 5);
  const ids = new Set();
  for (const p of catalog.PRESETS) {
    assert.match(p.name, /^[A-Za-z0-9@._/-]{1,128}$/);
    assert.ok(!ids.has(p.id)); ids.add(p.id);
    assert.ok(p.desc && p.link.startsWith('https://github.com/'));
    if (p.how === 'http') assert.ok(p.url.startsWith('https://') && !p.command);
    else { assert.equal(p.how, 'stdio'); assert.match(p.command, /^(npx|uvx|docker) /); assert.ok(p.needs); }
    if (p.edit) assert.ok(p.command.endsWith(p.edit));
  }
});

test('install targets and sizes', () => {
  assert.deepEqual(catalog.TARGETS.map(([k]) => k), ['claude_code', 'codex', 'agents', 'zcode']);
  assert.equal(catalog.size(512), '512B');
  assert.equal(catalog.size(2048), '2KB');
  assert.equal(catalog.size(5 << 20), '5.0MB');
});
