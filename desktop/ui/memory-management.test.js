const test = require("node:test");
const assert = require("node:assert/strict");
const management = require("./memory-management.js");
const candidate = (id, overrides = {}) => ({ id, status: "candidate", kind: "user", text: "응답은 짧게", scope: { workspace_id: "w", project_id: "p", user_id: "u" },
  memory_context: { applicability: "응답", domains: ["personal"], evidence_kind: "user_statement" }, ...overrides });

test("approval labels do not turn user speech, trust or old actor names into a human decision", () => {
  for (const record of [
    { status: "active", tags: ["basis:explicit_user"], trust: 1 },
    { status: "active", trust_level: "user_verified", approved_by: "owner", review: { person: true, by: "owner", action: "approve" } },
    { status: "active", approved_by: "service", review: { by: "service", action: "approve" } }
  ]) assert.notEqual(management.approval(record).state, "review_recorded");
  assert.equal(management.approval(candidate("a")).state, "pending");
  const reviewed = { status: "active", approved_by: "owner", review: { receipt_id: "receipt", actor_role: "human", channel: "explicit_review_api", by: "owner", action: "approve" } };
  assert.equal(management.approval(reviewed).state, "review_recorded");
  assert.notEqual(management.approval({ ...reviewed, approved_by: "different" }).state, "review_recorded");
});

test("similar pending requests share a presentation group without changing individual proposals", () => {
  const input = [candidate("a"), candidate("b", { text: "응답은 간결하게", keywords: ["style"] })];
  const before = JSON.stringify(input);
  assert.equal(management.groups(input).length, 1);
  assert.equal(management.groups(input)[0].items.length, 2);
  assert.equal(JSON.stringify(input), before);
});

test("different people, conditions, exceptions, negations and conflicts remain separate", () => {
  const base = candidate("a");
  for (const changed of [
    { scope: { ...base.scope, user_id: "other" } },
    { memory_context: { ...base.memory_context, applicability: "업무 응답" } },
    { memory_context: { ...base.memory_context, valid_from: "2027년부터" } },
    { memory_context: { ...base.memory_context, uncertainty: "예외가 있음" } },
    { text: "응답은 짧게 하지 마" },
    { text: "응답은 안 짧게" },
    { text: "응답은 영어로 짧게" },
    { conflict_id: "conflict" },
    { tags: ["repo:other"] }
  ]) assert.equal(management.groups([base, candidate("b", changed)]).length, 2);
  assert.equal(management.groups([base, candidate("b", { status: "approved" })]).length, 1);
});

test("legacy proposals without qualifiers are only grouped by exact normalized text", () => {
  const a = candidate("a", { memory_context: null });
  assert.equal(management.groups([a, candidate("b", { text: "응답은 간결하게", memory_context: null })]).length, 2);
  assert.equal(management.groups([a, candidate("b", { text: " 응답은  짧게 ", memory_context: null })]).length, 1);
});

test("utterance counts deduplicate retries and preserve uncertainty instead of inventing mentions", () => {
  const known = { user_mention_keys: ["turn1"], source_keys: ["source1"], complete: true };
  const counted = management.evidence([known, known, { user_mention_keys: ["turn2"], source_keys: ["source2"], complete: true }]);
  assert.equal(counted.mentions, 2);
  assert.equal(counted.complete, true);
  assert.match(counted.label, /2회/);
  assert.equal(management.evidence([known, null]).complete, false);
  assert.match(management.evidence([{ source_keys: ["source1"], complete: false }]).label, /언급 횟수 미확인/);
});

test("deletion completion is a server report with separately unknown external copies", () => {
  const status = management.deletionStatus({ privacy_deletion: { state: "completed", report: { live_access_removed: true, runtime_purged: true, derived_purged: true, artifacts_purged: true, backup_expiry_pending: false } } });
  assert.equal(status.label, "서버 처리 완료");
  assert.equal(status.runtimePurged, true);
  assert.match(status.externalVerification, /별도 확인/);
  assert.match(status.backup, /확인 기록 없음/);
  const missing = management.deletionStatus({ privacy_deletion: { state: "completed" } });
  assert.equal(missing.accessRemoved, false);
  assert.equal(missing.artifactsPurged, false);
  assert.match(management.deletionStatus({ privacy_deletion: { state: "requested" } }).label, /요청 접수/);
});

test("a mostly identical rule with another product name or hidden qualifier remains separate", () => {
  const text = "응답은 간결하게 하고 검증된 운영 상태와 근거를 먼저 설명하며 Cedar 설정을 사용한다";
  assert.equal(management.groups([candidate("a", { text }), candidate("b", { text: text.replace("Cedar", "Maple") })]).length, 2);
  assert.equal(management.groups([candidate("a", { text }), candidate("b", { text: text + " 예외적으로" })]).length, 2);
});

test("wording groups preserve signs, units, comparisons and directional word order", () => {
  for (const [first, second] of [
    ["허용 값 -1", "허용 값 1"],
    ["허용 값 1 %", "허용 값 1"],
    ["허용 값 < 1", "허용 값 > 1"],
    ["비용 $ 1", "비용 € 1"],
    ["속도 1 MB/s", "속도 1 mb/s"],
    ["환경 변수 Path 유지", "환경 변수 PATH 유지"],
    ["Maple 에서 Cedar 로 복사", "Cedar 에서 Maple 로 복사"],
    ["설명서 는 짧게", "응답서 는 짧게"],
    ["자동설명은 짧게", "자동응답은 짧게"],
    ["표현은 자동짧게", "표현은 자동간결하게"]
  ]) assert.equal(management.groups([candidate("a", { text: first }), candidate("b", { text: second })]).length, 2);
});

test("audit lines, model-call names and the two stats folds read in plain words, unknown codes kept", () => {
  assert.deepEqual(management.auditLine({ action: "write:memories", actor: "felix", actor_type: "human" }), { what: "기억 바꿈", code: "write:memories", blocked: false, person: true, known: true });
  assert.equal(management.auditLine({ action: "write:memories:defense_blocked", actor_type: "agent" }).blocked, true);
  assert.equal(management.auditLine({ action: "read:memories", decision: "deny" }).blocked, true);
  assert.deepEqual(management.auditLine({ action: "zap:things" }), { what: "", code: "zap:things", blocked: false, person: false, known: false });
  assert.equal(management.opLabel("embed"), "검색용 색인 만들기"); assert.equal(management.opLabel("rerank"), "검색 결과 다시 고르기"); assert.equal(management.opLabel("new_op"), "");
  const m = management.modelStats({ items: [{ day: "2026-11-02", operation: "embed", calls: 10, errors: 1, total_tokens: 100 }, { day: "2026-11-01", operation: "reflect", calls: 2, errors: 0, total_tokens: 900 }, { day: "2026-11-02", operation: "reflect", calls: 1, errors: 0, total_tokens: 50 }] });
  assert.deepEqual(m.days.map((d) => [d.day, d.calls]), [["2026-11-01", 2], ["2026-11-02", 11]]);
  assert.deepEqual(m.ops.map((o) => [o.operation, o.calls]), [["embed", 10], ["reflect", 3]]);
  assert.deepEqual([m.calls, m.errors, m.tokens], [13, 1, 1050]);
  assert.deepEqual(management.modelStats(null), { days: [], ops: [], calls: 0, errors: 0, tokens: 0 });
  const s = management.series({ points: [{ bucket: "2026-11-02", key: "user", count: 2 }, { bucket: "2026-11-01", key: "user", count: 1 }, { bucket: "2026-11-02", key: "fact", count: 5 }] });
  assert.deepEqual(s, { buckets: [["2026-11-01", 1], ["2026-11-02", 7]], keys: [["fact", 5], ["user", 3]], total: 8 });
});

test("model calls split by kind and fold per model; stats links come out busiest first", () => {
  assert.deepEqual(["embedding_query", "embed", "rerank", "recall_rerank", "reflect", "wiki_write", ""].map(management.callKind), ["embedding", "embedding", "rerank", "rerank", "llm", "llm", "llm"]);
  const items = [{ request: { generator: { model: "large" }, trace_id: "t1", total_tokens: 10 } }, { request: { generator: { model: "large" }, job_id: "j1", total_tokens: 5 } },
    { request: { provider: "embedder", trace_id: "t1", total_tokens: 2 } }, { request: {} }];
  assert.deepEqual(management.callGroups(items), [["large", 2, 15], ["embedder", 1, 2]], "a call with no model is left out");
  assert.deepEqual(management.callGroups(null), []);
  assert.deepEqual(management.statTiles({ links: { supports: 2, causes: 7, empty: 0 } }).links, [["causes", 7], ["supports", 2]]);
  assert.deepEqual(management.statTiles({}).links, []);
});

test("정리 상태 counts waiting and failed consolidation passes and topic summaries, with the newest finished pass", () => {
  const st = management.syncStatus([{ status: "pending", outcome: "ok", last_run: "2026-11-05T01:00:00Z" }, { status: "done", outcome: "model_budget_exceeded", last_run: "2026-11-05T03:00:00Z" }, { status: "failed" }],
    [{ stale: true, last_refresh_at: "2026-11-04T00:00:00Z" }, { last_outcome: { code: "refresh_failed_error" }, last_refresh_at: "2026-11-05T02:00:00Z" }, { status: "proposed", stale: true }]);
  assert.deepEqual(st, { consolidation: { waiting: 1, failed: 2, last: "2026-11-05T03:00:00Z" }, topics: { waiting: 1, failed: 1, last: "2026-11-05T02:00:00Z" } });
  assert.deepEqual(management.syncStatus(null, null), { consolidation: { waiting: 0, failed: 0, last: null }, topics: { waiting: 0, failed: 0, last: null } });
});
