/* Review presentation never changes a proposal, its trust, or its approval. */
(function (root) {
  "use strict";
  const normalized = text => String(text || "").normalize("NFKC").toLowerCase().replace(/\s+/gu, " ").trim();
  const scopeKey = r => JSON.stringify([r.scope?.workspace_id, r.scope?.project_id, r.scope?.user_id, r.scope?.agent_id]);
  function approval(record) {
    const review = record?.review;
    if (review?.receipt_id && review.actor_role === "human" && review.channel === "explicit_review_api" && review.by === record.approved_by && ["approve", "edit_approve"].includes(review.action)) {
      return { state: "review_recorded", label: "사람 계정의 검토 기록", detail: "검토 요청과 처리 계정이 기록돼 있어요." };
    }
    if (["candidate", "conflicted"].includes(record?.status)) return { state: "pending", label: "검토 대기", detail: "사람이 저장 여부를 결정해 주세요." };
    if (record?.approved_by || review?.by || record?.trust_level === "user_verified") {
      return { state: "legacy_unknown", label: "과거 승인 기록 · 직접 확인 미입증", detail: "계정 이름과 기존 점수만으로 직접 사용자 확인을 증명하지 않아요." };
    }
    return { state: "unknown", label: "승인 경로 미확인", detail: "누가 어떤 경로로 확인했는지 기록이 부족해요." };
  }
  // Qualifiers must agree before any presentation-only similarity comparison.
  function boundary(r) {
    const c = r.memory_context || {};
    const polarity = (normalized(r.text).match(/\b(?:not|never|only|before|after|except)\b|(?:^|\s)안(?=\s)|않|없이|금지|제외|말고|하지\s*마|개인|업무|한국어|영어|\d+(?:\.\d+)?/gu) || []).sort();
    return JSON.stringify([scopeKey(r), r.kind, r.reach, r.reach_project, r.conflict_id || "", (r.tags || []).filter(t => t.startsWith("repo:") || t.startsWith("review:")).sort(),
      [...(c.domains || [])].sort(), c.applicability || "", c.uncertainty || "", c.valid_from || "", c.evidence_kind || "", c.semantic_kind || "", polarity]);
  }
  function terms(text) {
    return new Set(normalized(text).replace(/간결하게|간단히|핵심만|요약해서|짧게/gu, " short ")
      .replace(/답변|대답|응답|설명|답해|말해/gu, " response ").match(/[\p{L}\p{N}]+/gu) || []);
  }
  function similar(a, b) {
    if (normalized(a.text) === normalized(b.text)) return true;
    // Legacy proposals without explicit scope qualifiers are only grouped exactly.
    if (!a.memory_context?.applicability || !b.memory_context?.applicability) return false;
    const x = terms(a.text), y = terms(b.text);
    const overlap = [...x].filter(t => y.has(t)).length;
    // Only recognized wording variants may differ. A changed proper name or
    // unmodelled qualifier must not disappear inside an approximate group.
    return overlap >= 2 && overlap === x.size && overlap === y.size;
  }
  function groups(records) {
    const result = [];
    for (const record of records) {
      if (record.status !== "candidate") continue;
      const key = boundary(record);
      // Every member must agree; do not connect a chain through a weak middle match.
      const group = result.find(g => g.boundary === key && g.items.every(item => similar(item, record)));
      if (group) group.items.push(record);
      else result.push({ id: record.id, boundary: key, items: [record] });
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
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof window !== "undefined" ? window : globalThis);
