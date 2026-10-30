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
  const api = { approval, groups, evidence, deletionStatus };
  root.ArkismMemoryManagement = api;
  if (typeof window === "undefined" && typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof window !== "undefined" ? window : globalThis);
