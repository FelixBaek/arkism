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
