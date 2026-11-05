// @ts-check
/* Review presentation never changes a proposal, its trust, or its approval. */
(function (root) {
  "use strict";
  // Case can distinguish units and identifiers (MB/mb, Path/PATH).
  const normalized = text => String(text || "").normalize("NFKC").replace(/\s+/gu, " ").trim();
  const scopeKey = r => JSON.stringify([r.scope?.workspace_id, r.scope?.project_id, r.scope?.user_id, r.scope?.agent_id]);
  function approval(record) {
    const review = record?.review;
    if (review?.receipt_id && review.actor_role === "human" && review.channel === "explicit_review_api" && review.by === record.approved_by && ["approve", "edit_approve"].includes(review.action)) {
      return { state: "review_recorded", label: "사람이 확인하고 저장했어요", detail: "누가 언제 확인했는지 기록이 남아 있어요." };
    }
    if (["candidate", "conflicted"].includes(record?.status)) return { state: "pending", label: "검토 대기", detail: "사람이 저장 여부를 결정해 주세요." };
    if (record?.approved_by || review?.by || record?.trust_level === "user_verified") {
      return { state: "legacy_unknown", label: "예전 방식으로 저장됨", detail: "예전에 저장돼 사람이 직접 확인했다는 기록이 없어요. 쓰는 데는 문제없어요." };
    }
    return { state: "unknown", label: "확인 기록 없음 (예전 방식)", detail: "누가 확인했는지 기록이 남지 않았어요. 쓰는 데는 문제없어요." };
  }
  // Qualifiers must agree before any presentation-only similarity comparison.
  function boundary(r) {
    const c = r.memory_context || {};
    const polarity = (normalized(r.text).match(/\b(?:not|never|only|before|after|except)\b|(?:^|\s)안(?=\s)|않|없이|금지|제외|말고|하지\s*마|개인|업무|한국어|영어|\d+(?:\.\d+)?/gu) || []).sort();
    return JSON.stringify([scopeKey(r), r.kind, r.reach, r.reach_project, r.conflict_id || "", (r.tags || []).filter(t => t.startsWith("repo:") || t.startsWith("review:")).sort(),
      [...(c.domains || [])].sort(), c.applicability || "", c.uncertainty || "", c.valid_from || "", c.evidence_kind || "", c.semantic_kind || "", polarity]);
  }
  function wording(text) {
    // Preserve order, signs, operators and units. Only these explicit wording
    // variants may change; token sets would erase direction and numeric signs.
    return normalized(text).replace(/(^|\s)(?:간결하게|간단히|핵심만|요약해서|짧게)(?=\s|[.,!?。！？]|$)/gu, "$1 short ")
      .replace(/(^|\s)(?:(?:답변|대답|응답|설명)(?=은|는|을|를|이|가|\s|$)|(?:답해|말해)(?=\s|[.,!?。！？]|$))/gu, "$1 response ")
      .replace(/\s+/gu, " ").trim();
  }
  function groups(records) {
    const result = [], indexed = new Map();
    for (const record of records) {
      if (record.status !== "candidate") continue;
      const key = boundary(record), exact = normalized(record.text), variant = wording(record.text);
      // boundary already includes applicability. A qualified wording match is
      // allowed only with two distinct words; otherwise retain exact matching.
      const text = record.memory_context?.applicability && new Set(variant.match(/[\p{L}\p{N}]+/gu) || []).size >= 2 ? variant : exact;
      const lookup = JSON.stringify([key, text]);
      let group = indexed.get(lookup);
      if (!group) {
        group = { id: record.id, boundary: key, items: [] };
        indexed.set(lookup, group); result.push(group);
      }
      group.items.push(record);
    }
    return result;
  }
  function evidence(summaries) {
    const mentions = new Set(), sources = new Set();
    let complete = true;
    for (const s of summaries) {
      if (!s || !s.complete) complete = false;
      for (const key of s?.user_mention_keys || []) mentions.add(key);
      for (const key of s?.source_keys || []) sources.add(key);
    }
    const label = mentions.size ? `서로 다른 사용자 발화 ${mentions.size}회${complete ? "" : " 이상"} 확인${complete ? "" : " · 일부 근거 미확인"}` : sources.size ? `근거 ${sources.size}개 · 언급 횟수 미확인` : "근거 확인 불가 · 언급 횟수 미확인";
    return { mentions: mentions.size, sources: sources.size, complete, label };
  }
  function deletionStatus(job) {
    const d = job?.privacy_deletion || {}, r = d.report || {};
    const labels = { requested: "삭제 요청 접수", authorized: "처리 허용됨", quarantined: "접근 차단 단계", discovering_impact: "영향 범위 확인 중", purging_runtime: "실행 데이터 정리 중", purging_derived: "파생 데이터 정리 중", waiting_for_retention_or_backup: "백업 보관 기한 대기", verifying: "서버 검증 중", completed: "서버 처리 완료", partial: "일부 처리됨", failed: "처리 실패", held: "보류 중", cancelled: "취소됨" };
    return { label: labels[d.state] || "처리 상태 미확인", accessRemoved: r.live_access_removed === true,
      runtimePurged: r.runtime_purged === true, derivedPurged: r.derived_purged === true, artifactsPurged: r.artifacts_purged === true,
      backup: r.backup_expiry_pending ? "보관 기한 대기" : r.backup_oldest_retained_at ? "보관 시점 확인 기록 있음" : "확인 기록 없음",
      externalVerification: "외부 사본 직접 검증은 별도 확인" };
  }
  // 기억 관리 › 태그 (GET /v1/tags): "session:" tags record where a memory came from; the server refuses edits to them.
  const SYSTEM_TAG = "session:";
  function tagRows(list) {
    return (list?.items || []).filter(t => t && typeof t.tag === "string" && t.tag).map(t => ({ tag: t.tag, count: Number(t.count) || 0, system: t.tag.startsWith(SYSTEM_TAG) }));
  }
  // Renaming onto a tag that already exists merges the two; anything else is a plain rename. null = not allowed.
  function tagChange(rows, from, to) {
    const name = String(to || "").trim();
    if (!name || name === from || from.startsWith(SYSTEM_TAG) || name.startsWith(SYSTEM_TAG) || /[\s,]/u.test(name) || name.length > 64) return null;
    const into = rows.find(r => r.tag === name);
    return { from, to: name, merge: !!into, moving: rows.find(r => r.tag === from)?.count || 0, into: into?.count || 0 };
  }
  // GET /v1/stats in plain numbers; a missing field stays unknown (null), never 0.
  function statTiles(stats) {
    const n = v => (typeof v === "number" ? v : null);
    const byStatus = stats?.memories?.by_status; // the server leaves out statuses with no memories: present map, missing key = 0
    return { active: byStatus && typeof byStatus === "object" ? n(byStatus.active ?? 0) : null, total: n(stats?.memories?.total), pending: n(stats?.pending_candidates), sources: n(stats?.sources?.total),
      kinds: Object.entries(stats?.memories?.by_kind || {}).filter(([, c]) => typeof c === "number" && c > 0).sort((a, b) => b[1] - a[1]) };
  }
  // 기억 관리 › 활동 기록 (GET /v1/audit-events): "verb:collection[:detail]" in plain words; anything unknown keeps its code.
  const VERB = { write: "바꿈", create: "만듦", update: "고침", delete: "지움", review: "검토함", read: "읽음", start: "시작함", decide: "결정함", expire: "기한 지나 정리됨", cancel: "취소함", erase: "지움", finish: "끝냄", confirm: "확인함" };
  const THING = { memories: "기억", memory_candidates: "기억 후보", sources: "자료", space_configs: "공간 설정", directives: "규칙", entities: "이름", observations: "관찰",
    mental_models: "정리 틀", wiki_pages: "위키", webhooks: "웹훅", jobs: "작업", tags: "태그", session_archives: "대화 원문", memory_relations: "기억 연결", crew: "워크플로우", crew_runs: "워크플로우 실행", providers: "모델 연결", skills: "스킬", llm_requests: "모델 호출 기록", audit_events: "활동 기록" };
  function auditLine(e) {
    const [verb, thing = "", ...rest] = String(e?.action || "").split(":");
    const known = VERB[verb] && (!thing || THING[thing]);
    const blocked = rest.includes("defense_blocked") || e?.decision === "deny" || e?.decision === "denied";
    const what = known ? `${THING[thing] || "항목"} ${VERB[verb]}` : "";
    return { what, code: e?.action || "", blocked, person: e?.actor_type === "human", known: !!known };
  }
  // 기억 관리 › 모델 호출 (GET /v1/llm-requests): what each server-side call was for
  const OPERATION = { embed: "검색용 색인 만들기", rerank: "검색 결과 다시 고르기", reflect: "질문에 답하기", wiki_memory_extract: "파일에서 기억 뽑기", wiki_entities: "이름 찾기",
    wiki_analyse: "위키 내용 살피기", wiki_write: "위키 쓰기", wiki_observation: "관찰 정리 제안", wiki_causal: "원인과 결과 제안", wiki_mental_model: "정리 틀 만들기" };
  const opLabel = (op) => OPERATION[op] || "";
  // GET /v1/llm-requests/stats rows (day × operation) folded into days, oldest first, and operations, busiest first
  function modelStats(stats) {
    const days = new Map(), ops = new Map();
    for (const r of stats?.items || []) {
      const d = days.get(r.day) || { day: r.day, calls: 0, errors: 0, tokens: 0 };
      d.calls += r.calls || 0; d.errors += r.errors || 0; d.tokens += r.total_tokens || 0; days.set(r.day, d);
      const o = ops.get(r.operation) || { operation: r.operation, calls: 0, errors: 0, tokens: 0 };
      o.calls += r.calls || 0; o.errors += r.errors || 0; o.tokens += r.total_tokens || 0; ops.set(r.operation, o);
    }
    const all = [...days.values()];
    return { days: all.sort((a, b) => String(a.day).localeCompare(String(b.day))), ops: [...ops.values()].sort((a, b) => b.calls - a.calls),
      calls: all.reduce((n, d) => n + d.calls, 0), errors: all.reduce((n, d) => n + d.errors, 0), tokens: all.reduce((n, d) => n + d.tokens, 0) };
  }
  // GET /v1/stats/timeseries points (bucket × key) as one total per bucket, oldest first, and one per key, biggest first
  function series(doc) {
    const buckets = new Map(), keys = new Map();
    for (const p of doc?.points || []) {
      buckets.set(p.bucket, (buckets.get(p.bucket) || 0) + (p.count || 0));
      keys.set(p.key || "", (keys.get(p.key || "") || 0) + (p.count || 0));
    }
    return { buckets: [...buckets].sort((a, b) => String(a[0]).localeCompare(String(b[0]))), keys: [...keys].sort((a, b) => b[1] - a[1]), total: [...keys.values()].reduce((n, c) => n + c, 0) };
  }
  const api = { approval, groups, evidence, deletionStatus, tagRows, tagChange, statTiles, auditLine, opLabel, modelStats, series };
  root.ArkismMemoryManagement = api;
  if (typeof window === "undefined" && typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof window !== "undefined" ? window : globalThis);
