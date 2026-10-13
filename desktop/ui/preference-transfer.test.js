// Run with: node --test desktop/ui/*.test.js
const test = require('node:test');
const assert = require('node:assert/strict');
global.window = {};
require('./preference-transfer.js');

test('export omits credentials and account content', () => {
  const storage = {
    'arkism.token': 'private-token', 'arkism.email': 'private-email',
    'arkism.server': 'private-server', 'arkism.scope': '{"workspace_id":"private"}',
    'arkism.keptMemories': '["private-memory"]', 'arkism.skillSuggest': '{"items":["private-draft"]}',
    'arkism.prefs': JSON.stringify({agentsOff: ['grok'], categories: [], comfyUrl: 'http://localhost:8188', collectUnused: false, token: 'private-token'}),
    'arkism.look': '{"theme":"dark","font":"lg","zoom":1.2}', 'arkism.skillSort': 'name', 'arkism.skillDir': '-1',
    'arkism.routing.policyOpen': '{"routing":true,"failover":false}'
  };
  const original = JSON.stringify(storage);
  const text = window.ArkismPreferenceTransfer.export(k => storage[k] ?? null);
  const data = JSON.parse(text);
  assert.equal(data.product, 'arkism-preferences'); assert.equal(data.version, 1);
  assert.deepEqual(data.values.agentsOff, ['grok']); assert.deepEqual(data.values.categories, []);
  assert.equal(data.values['routing.policyOpen'].routing, true);
  assert.deepEqual(data.values.look, {theme: 'dark', font: 'lg', zoom: 1.2});
  assert.equal(data.values.skillDir, '-1'); assert(!text.includes('private')); assert(!text.includes('collectUnused'));
  assert.equal(JSON.stringify(storage), original);
  assert.throws(() => window.ArkismPreferenceTransfer.export(k => k.endsWith('.prefs') ? '{bad' : null));
});

test('memory export binds identity without credentials or bodies', () => {
  const storage = {'arkism.token': 'SECRET', 'arkism.email': 'SECRET', 'arkism.skillSuggest': 'SECRET',
    'arkism.prefs': '{"collectUnused":false,"token":"SECRET"}', 'arkism.keptMemories': '["a","b","a"]'};
  const before = JSON.stringify(storage);
  const identity = {server: 'https://example.invalid', role: 'human', principal_id: 'owner', token: 'SECRET',
    scope: {workspace_id: 'workspace', project_id: 'project', user_id: 'owner', secret: 'SECRET'}};
  const text = window.ArkismPreferenceTransfer.exportMemory(k => storage[k] ?? null, identity), data = JSON.parse(text);
  assert.equal(data.product, 'arkism-memory-preferences'); assert.equal(data.source, 'legacy-unscoped'); assert.equal(data.principal, 'owner');
  assert.deepEqual(data.values, {collect: false, kept: ['a', 'b']}); assert.equal(data.scope.agent_id, '');
  assert(!text.includes('SECRET')); assert.equal(JSON.stringify(storage), before);
  assert.throws(() => window.ArkismPreferenceTransfer.exportMemory(k => storage[k] ?? null, {...identity, role: 'reader'}));
  assert.throws(() => window.ArkismPreferenceTransfer.exportMemory(k => k.endsWith('keptMemories') ? '[null]' : null, identity));
  assert.throws(() => window.ArkismPreferenceTransfer.exportMemory(k => k.endsWith('keptMemories') ? '["bad\\nkey"]' : null, identity));
});

test('skill export preserves drafts and exclusions strictly', () => {
  const identity = {server: 'https://example.invalid', role: 'human', principal_id: 'owner', scope: {workspace_id: 'workspace', project_id: 'project'}};
  const item = {name: 'legacy-draft', title: '가져온 제안', why: '반복 요청', skill_md: '가'.repeat(20000), examples: ['첫 요청', '다음 요청'], token: 'SECRET'};
  const storage = {'arkism.skillSuggest': JSON.stringify({items: [item], dismissed: ['skip-me', 'skip-me'], token: 'SECRET'}), 'arkism.similarDismissed': '["a|b|c","missing|pair"]', 'arkism.token': 'SECRET'};
  const before = JSON.stringify(storage), run = () => window.ArkismPreferenceTransfer.exportSkills(k => storage[k] ?? null, identity);
  const output = run(), data = JSON.parse(output);
  assert.equal(data.product, 'arkism-skill-preferences'); assert.equal(data.source, 'legacy-unscoped');
  assert.equal(data.values.items[0].skill_md, item.skill_md); assert.equal(data.values.items[0].examples, '첫 요청\n다음 요청');
  assert.deepEqual(data.values.dismissed, ['skip-me']); assert.deepEqual(data.values.similarDismissed, ['a|b|c', 'missing|pair']);
  assert(!output.includes('SECRET')); assert.equal(JSON.stringify(storage), before);
  for (const bad of [null, {items: [null], dismissed: []}, {items: [{...item, examples: [{}]}], dismissed: []}, {items: [{...item, skill_md: ' '}], dismissed: []}, {items: [{...item, skill_md: '가'.repeat(20001)}], dismissed: []}, {items: [], dismissed: [null]}]) {
    storage['arkism.skillSuggest'] = JSON.stringify(bad); assert.throws(run);
  }
  storage['arkism.skillSuggest'] = '{"items":[],"dismissed":[]}'; storage['arkism.similarDismissed'] = '[null]'; assert.throws(run);
});

test('preference import restores an export and rejects a bad file without writing', () => {
  const T = window.ArkismPreferenceTransfer;
  const source = {
    'arkism.look': '{"theme":"light","font":"xl","zoom":1.3}', 'arkism.designGroup': 'purpose', 'arkism.skillSort': 'name',
    'arkism.routing.policyOpen': '{"routing":true,"failover":false}', 'arkism.token': 'private-token',
    'arkism.prefs': JSON.stringify({agentsOff: ['grok'], categories: [{id: 'c', name: '코드', icon: 'code', rules: [['name', '*review*']]}], comfyUrl: 'http://127.0.0.1:8188', collectUnused: false})
  };
  const text = T.export(k => source[k] ?? null);
  const target = {'arkism.prefs': '{"collectUnused":false}'};
  T.import(text, k => target[k] ?? null, (k, v) => { target[k] = v; });
  assert.deepEqual(JSON.parse(target['arkism.look']), {theme: 'light', font: 'xl', zoom: 1.3});
  assert.equal(target['arkism.designGroup'], 'purpose');
  const prefs = JSON.parse(target['arkism.prefs']);
  assert.deepEqual(prefs.agentsOff, ['grok']); assert.equal(prefs.categories[0].rules[0][1], '*review*');
  assert.equal(prefs.collectUnused, false); assert.equal(target['arkism.token'], undefined);
  const untouched = {}, write = (k, v) => { untouched[k] = v; };
  const bad = JSON.parse(text); bad.values.comfyUrl = 'javascript:alert(1)';
  assert.throws(() => T.import(JSON.stringify(bad), () => null, write));
  assert.throws(() => T.import('{"product":"other","version":1,"values":{}}', () => null, write));
  assert.deepEqual(untouched, {});
});

test('memory and skill imports apply only to the account that exported them', () => {
  const T = window.ArkismPreferenceTransfer;
  const identity = {role: 'human', principal_id: 'management:owner', server: 'http://127.0.0.1:8091',
    scope: {workspace_id: 'default', project_id: 'default', user_id: 'management:owner', agent_id: ''}};
  const src = {'arkism.prefs': '{"collectUnused":false}', 'arkism.keptMemories': '["m1","m2"]',
    'arkism.skillSuggest': JSON.stringify({items: [{name: 'tidy-notes', title: 't', why: 'w', skill_md: '# s', examples: ['a', 'b']}], dismissed: ['old-one']}),
    'arkism.similarDismissed': '["a|b"]'};
  const memory = T.exportMemory(k => src[k] ?? null, identity);
  const skills = T.exportSkills(k => src[k] ?? null, identity);
  const dst = {'arkism.keptMemories': '["m0"]'}, read = k => dst[k] ?? null, write = (k, v) => { dst[k] = v; };
  assert.equal(T.importMemory(memory, identity, read, write), 2);
  assert.deepEqual(JSON.parse(dst['arkism.keptMemories']), ['m0', 'm1', 'm2']);
  assert.equal(JSON.parse(dst['arkism.prefs']).collectUnused, false);
  assert.equal(T.importSkills(skills, identity, read, write), 1);
  assert.deepEqual(JSON.parse(dst['arkism.skillSuggest']).items[0].examples, ['a', 'b']);
  const before = JSON.stringify(dst);
  assert.throws(() => T.importMemory(memory, {...identity, principal_id: 'someone-else'}, read, write), /other_account/);
  assert.throws(() => T.importSkills(skills, {...identity, scope: {...identity.scope, project_id: 'other'}}, read, write), /other_scope/);
  assert.equal(JSON.stringify(dst), before);
});
