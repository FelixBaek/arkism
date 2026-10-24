// 코드 리뷰 (Figma 15 · 15b): PRs the CI reviewed, collected by the server.
function stepDotClass(s) {
  if (!s) return "";
  if (s.status === "success") return "ok";
  if (/fail/.test(s.status)) return "bad";
  return "run";
}
window.ArkismReviews = (ctx) => {
  "use strict";
  const { state, $, el, button, render, request, query, notice, detailShell, markSelected, openDetail, ago, assist, headExtras, confirmPop } = ctx;

  const doc = () => state.data.reviews || null;
  const items = () => doc()?.items || [];
  const DAY = 864e5;
  const dur = (a, b) => (a && b ? Math.max(0, (new Date(b) - new Date(a)) / 1000) : null);
  const fmt = (s) => {
    if (s == null) return "—";
    if (s < 60) return `${Math.round(s)}초`;
    return `${Math.floor(s / 60)}분 ${Math.round(s % 60)}초`;
  };
  const blocking = (it) => it.findings.filter((f) => f.severity === "blocking").length;
  const advisory = (it) => it.findings.filter((f) => f.severity === "advisory").length;
  const repoShort = (r) => r.split("/")[1] || r;
  const DISMISS_KEY = "arkism.reviewDismissed";
  const dismissed = () => { try { return JSON.parse(localStorage.getItem(DISMISS_KEY) || "[]"); } catch (_) { return []; } };
  const findingKey = (it, f) => `${it.repo}#${it.number}:${f.path}:${f.line}:${f.text.slice(0, 40)}`;
  const openFindings = (it) => it.findings.filter((f) => !dismissed().includes(findingKey(it, f)));

  async function load() {
    try { return (await request(`/v1/code_reviews?${query()}`)).code_reviews || null; } catch (_) { return null; }
  }
  async function reload() { state.data.reviews = await load(); render(false); }

  function resultTag(it) {
    if (it.state === "running") return el("span", "cn-badge accent", "AI가 읽는 중");
    if (it.state === "failed") return el("span", "cn-badge warn", "검사 실패");
    const b = blocking(it), a = advisory(it);
    if (b) return el("span", "cn-badge warn", `고칠 것 ${b}건`);
    if (a) return el("span", "cn-badge warn", `권고 ${a}건`);
    return el("span", "cn-badge soft", "문제 없음");
  }
  // Summary tile (same look as the 연결 screens): label, value + unit, one note line.
  function tile(label, value, unit, note, warn) {
    const t = el("div", "cn-tile");
    const v = el("div", "cn-tile-value");
    v.append(el("strong", warn ? "warn" : "", value));
    if (unit) v.append(el("span", "", unit));
    t.append(el("p", "", label), v);
    if (note) t.append(el("small", "", note));
    return t;
  }
  function dots(it) {
    const by = Object.fromEntries((it.steps || []).map((s) => [s.name, s]));
    const row = el("span", "rv-dots");
    for (const [k, l] of [["quality", "품질"], ["ai", "AI 리뷰"], ["comment", "코멘트"]]) {
      const s = by[k];
      const d = el("span", `rv-dot ${stepDotClass(s)}`);
      d.append(el("i"), l);
      row.append(d);
    }
    return row;
  }
  function itemRow(it) {
    const rowId = `review:${it.repo}#${it.number}`;
    const r = el("button", `rv-row${state.selected === rowId ? " selected" : ""}`);
    r.type = "button";
    r.dataset.id = rowId;
    const t = el("div", "rv-title");
    t.append(el("strong", "", it.title), el("small", "mono", `${repoShort(it.repo)} · #${it.number} · ${it.branch}`));
    r.append(t, dots(it), resultTag(it), el("small", "rv-when", it.state === "running" ? "방금" : ago(it.finished_at || it.started_at)));
    r.addEventListener("click", () => { state.selected = `review:${it.repo}#${it.number}`; markSelected(); detail(); openDetail(); });
    return r;
  }

  function renderView() {
    ["#controls", "#chips"].forEach((s) => $(s).replaceChildren());
    headExtras({ aside: [button("새로고침", "ghost", null, reload), button("리뷰 설정", "", null, openSettings)] });
    const d = doc();
    const table = $("#table");
    table.className = "rv-wrap";
    if (!d?.collected_at) {
      $("#stats").replaceChildren();
      table.replaceChildren(el("div", "empty", d ? "서버가 아직 GitHub에서 리뷰를 모으지 않았어요. arkism-agents.timer가 10분마다 모아요." : "이 서버는 아직 코드 리뷰를 지원하지 않아요."));
      return;
    }
    const all = items();
    const now = Date.now();
    const week = all.filter((it) => now - new Date(it.started_at || 0) < 7 * DAY);
    const lastWeek = all.filter((it) => { const t = now - new Date(it.started_at || 0); return t >= 7 * DAY && t < 14 * DAY; });
    const times = week.map((it) => dur(it.started_at, it.finished_at)).filter((x) => x != null);
    const avg = times.length ? times.reduce((a, b) => a + b, 0) / times.length : null;
    const fix = week.reduce((n, it) => n + openFindings(it).length, 0);
    const fixBlocking = week.reduce((n, it) => n + openFindings(it).filter((f) => f.severity === "blocking").length, 0);
    const running = all.filter((it) => it.state === "running");
    const model = all.find((it) => it.model)?.model || "";
    let fixNote = "없어요";
    if (fixBlocking) fixNote = `blocking ${fixBlocking}건`;
    else if (fix) fixNote = `권고 · ${fix}건 확인 대기`;
    const tiles = el("div", "cn-tiles");
    tiles.append(
      tile("이번 주 검토", String(week.length), "건", `지난주보다 ${week.length - lastWeek.length >= 0 ? "+" : ""}${week.length - lastWeek.length}`),
      tile("평균 시간", fmt(avg), "", model),
      tile("고칠 것", String(fix), "건", fixNote, fix > 0),
      tile("처리 중", String(running.length), "", "러너 10대 중"));
    $("#stats").replaceChildren(tiles);
    const done = all.filter((it) => it.state !== "running");
    const head = (t, n) => { const h = el("h2", "rv-head", t); h.append(el("output", "", String(n))); return h; };
    const parts = [];
    if (running.length) { const l = el("div", "rv-list"); running.forEach((it) => l.append(itemRow(it))); parts.push(head("지금 처리 중", running.length), l); }
    const l2 = el("div", "rv-list");
    done.slice(0, 30).forEach((it) => l2.append(itemRow(it)));
    parts.push(head("최근 끝난 PR", done.length), l2, el("p", "cn-note", `레포 ${(d.repos || []).length}곳 · ${ago(d.collected_at)} 모음`));
    table.replaceChildren(...parts);
  }

  function sec(title, aside) {
    const s = el("section", "cn-sec");
    const h = el("div", "cn-sec-head");
    h.append(el("h3", "", title));
    if (aside) h.append(el("small", "", aside));
    s.append(h);
    return s;
  }
  function facts(pairs) {
    const dl = el("dl", "cn-facts");
    pairs.filter(([, v]) => v).forEach(([k, v]) => dl.append(el("dt", "", k), el("dd", "", v)));
    return dl;
  }
  function summary() {
    const body = detailShell("코드 리뷰");
    const d = el("div", "cn-detail");
    const h = el("div", "cn-head");
    h.append(el("p", "cn-kind", "코드 리뷰"), el("h2", "cn-title", "PR을 올리면 AI가 읽고 지적해요"), el("p", "cn-desc", "서버의 CI가 품질 검사와 AI 리뷰를 돌리고, 결과를 PR 코멘트로 남겨요. 여기서는 그 결과를 모아 봐요."));
    d.append(h);
    const s = doc()?.settings;
    if (s) {
      const sc = sec("지금 설정");
      sc.append(facts([["리뷰하는 AI", s.provider === "claude" ? "Claude" : "Codex"], ["지적이 나오면", s.on_blocking === "block" ? "심각한 건 병합 막기" : "알림만"], ["꺼 둔 레포", (s.repos_off || []).map(repoShort).join(", ") || "없어요"]]));
      d.append(sc);
    }
    body.append(d);
  }
  const STEP_NAMES = { runner: "러너 시작", quality: "품질 검사", ai: "AI 리뷰", comment: "PR 코멘트 게시" };
  function stepNote(it, s) {
    if (s.name === "ai") return it.model;
    if (s.name === "runner") return `대기 ${fmt(dur(s.started_at, s.finished_at))}`;
    if (s.name === "quality") return it.quality === "success" ? "새 문제 0" : it.quality;
    return "";
  }
  function stepRow(it, s) {
    const ok = s.status === "success" || s.status === "done";
    const failed = /fail/.test(s.status);
    let cls = "";
    if (ok) cls = "ok";
    else if (failed) cls = "warn";
    let ic = "clock-counter-clockwise";
    if (ok) ic = "check-circle";
    else if (failed) ic = "warning-circle";
    const li = el("li", cls);
    li.dataset.icon = ic;
    const t = el("div", "rv-step-text");
    t.append(el("span", "", STEP_NAMES[s.name] || s.name), el("small", "rv-note", stepNote(it, s) || ""));
    li.append(el("i", "rv-step-dot"), t);
    return li;
  }
  const fixTalk = new Map(); // AI로 고치기 talks per finding (ai-chat.js state)
  function findingCard(it, f) {
    const isBlocking = f.severity === "blocking";
    const card = el("div", "rv-finding");
    const top = el("div", "rv-finding-top");
    top.append(el("span", "cn-badge warn", isBlocking ? "blocking" : "권고"), el("code", "", `${f.path}:${f.line}`));
    const acts = el("div", "rv-finding-acts");
    // AI로 고치기 (ai-chat.js): say what to change, the AI answers how it would fix it, and 적용 (after a confirm)
    // has the server clone the PR branch, Claude Code change this spot, and the commit go to the same PR.
    const key = findingKey(it, f);
    if (!fixTalk.has(key)) fixTalk.set(key, {});
    const fixBtn = it.pr_state !== "open" ? button("AI로 고치기", "small", null, null) : window.ArkismAIChat.button({ state: fixTalk.get(key), label: "AI로 고치기", title: "AI로 고치기",
      sub: "어떻게 고칠지 말하면 방법을 알려 주고, 적용하면 커밋해요", greeting: `${f.path}:${f.line} 지적을 어떻게 고칠까요? 그냥 보내면 지적대로 고쳐요.`,
      placeholder: "예: 테스트도 같이 고쳐 줘", quick: ["지적대로 고쳐 줘", "가장 작은 변경으로"],
      ask: async (want) => {
        const a = await assist("explain", `A code review found this at ${f.path}:${f.line} in PR #${it.number} (${it.repo}):\n${f.text}\n\nThe owner says: ${want}\n\nIn two short sentences, say how you would fix it. No code.`);
        if (a.status !== "done") throw new Error(`AI가 답하지 못했어요 (${(a.error || "실패").slice(0, 120)}).`);
        return { say: (a.output || "").trim(), props: [{ label: "커밋", from: null, to: `${f.path}:${f.line} → ${it.branch}\n${want}`, apply: async () => {
          if (!await confirmPop("AI로 고치기", `${f.path}:${f.line} 지적을 AI가 고쳐서 ${it.branch} 브랜치에 커밋할까요?\n\nPR #${it.number}에 새 커밋이 올라가고 CI가 다시 검사해요.`, "AI로 고치기")) return false;
          notice("AI가 고치는 중이에요… (몇 분)");
          try {
            const r = await assist("fix", JSON.stringify({ repo: it.repo, number: it.number, path: f.path, line: f.line, text: `${f.text}\n\n${want}` }));
            if (r.status === "done") { notice(r.output.split("\n")[0]); return true; }
            notice(`고치지 못했어요: ${r.error || "알 수 없음"}`);
          } catch (e) { notice(`고치지 못했어요 (${e.message}).`); }
          return false;
        } }] };
      } });
    if (it.pr_state !== "open") { fixBtn.disabled = true; fixBtn.title = "열린 PR에서만 고칠 수 있어요"; }
    acts.append(button("무시", "ghost small", null, () => {
      try { localStorage.setItem(DISMISS_KEY, JSON.stringify([...dismissed(), findingKey(it, f)])); } catch (_) { /* private window */ }
      render(false);
      detail();
    }), fixBtn.dock || fixBtn); // AI at the right end of the row; its purple list under the card's text
    card.append(top, el("p", "", f.text), acts);
    if (fixBtn.dock) card.append(fixBtn);
    return card;
  }
  async function rerunReview(it) {
    try {
      state.data.reviews = (await request("/v1/code_reviews/rerun", { method: "POST", body: JSON.stringify({ scope: state.scope, repo: it.repo, number: it.number }) })).code_reviews;
      notice("다시 검사를 요청했어요. 서버가 1분 안에 시작해요.");
      detail();
    } catch (e) { notice(`요청하지 못했어요 (${e.message}).`); }
  }
  function stepsSection(it) {
    const steps = sec("처리 과정", it.finished_at ? `총 ${fmt(dur(it.started_at, it.finished_at))}` : "실행 중");
    const ul = el("ul", "rv-steps");
    for (const s of it.steps || []) ul.append(stepRow(it, s));
    steps.append(ul);
    return steps;
  }
  function findingsSection(it) {
    const open = openFindings(it);
    const fs = sec(`AI 지적 · ${open.length}건`, `blocking ${open.filter((f) => f.severity === "blocking").length}건`);
    if (!open.length) fs.append(el("p", "cn-note", it.state === "running" ? "AI가 아직 읽고 있어요." : "남은 지적이 없어요."));
    // AI가 고쳐서 올리기: the same per-spot fix, one after another, each as its own commit on the PR branch.
    if (open.length > 1) {
      const all = button("AI가 고쳐서 올리기", "small", null, async () => {
        if (!await confirmPop("AI가 고쳐서 올리기", `남은 지적 ${open.length}건을 AI가 하나씩 고쳐서 ${it.branch} 브랜치에 커밋할까요?\n\nPR #${it.number}에 커밋이 올라가고 CI가 다시 검사해요.`, "고쳐서 올리기")) return;
        all.disabled = true;
        let ok = 0;
        const fails = [];
        for (const [i, f] of open.entries()) {
          all.replaceChildren(`고치는 중… ${i + 1}/${open.length}`);
          try {
            const a = await assist("fix", JSON.stringify({ repo: it.repo, number: it.number, path: f.path, line: f.line, text: f.text }));
            if (a.status === "done") ok++;
            else fails.push(`${f.path}:${f.line} ${a.error || ""}`.trim());
          } catch (e) { fails.push(`${f.path}:${f.line} ${e.message}`); }
        }
        notice(fails.length ? `${ok}건 고쳤고 ${fails.length}건은 못 고쳤어요: ${fails[0]}` : `${ok}건 모두 고쳐서 올렸어요.`);
        all.disabled = false;
        all.replaceChildren("AI가 고쳐서 올리기");
      });
      all.classList.add("rv-fix-all");
      all.disabled = it.pr_state !== "open";
      if (it.pr_state !== "open") all.title = "열린 PR에서만 고칠 수 있어요";
      fs.append(all);
    }
    for (const f of open) fs.append(findingCard(it, f));
    return fs;
  }
  function rerunNote(it) {
    const rr = (doc()?.reruns || []).filter((q) => q.repo === it.repo && q.number === it.number).slice(-1)[0];
    if (!rr) return null;
    return el("p", "cn-note", { pending: "다시 검사를 요청했어요. 서버가 1분 안에 시작해요.", done: `다시 검사를 시작했어요 (${ago(rr.done_at)}).`, failed: `다시 검사하지 못했어요: ${rr.error || ""}` }[rr.status] || "");
  }
  function detailFoot(it) {
    const foot = el("div", "cn-btns");
    foot.append(button("다시 검사", "primary", null, () => rerunReview(it)), button("GitHub에서 열기", "", null, () => window.open(it.url, "_blank")));
    return foot;
  }
  function detail() {
    const id = state.selected?.startsWith("review:") ? state.selected.slice(7) : null;
    const it = items().find((x) => `${x.repo}#${x.number}` === id);
    if (!it) return summary();
    const body = detailShell(`PR #${it.number} · 코드 리뷰`);
    const d = el("div", "cn-detail");
    const h = el("div", "cn-head");
    h.append(el("p", "cn-kind", `PR #${it.number} · 코드 리뷰`), el("h2", "cn-title", it.title), el("p", "cn-desc mono", `${repoShort(it.repo)} · ${it.branch} → ${it.base} · ${it.head_sha.slice(0, 7)}${it.pr_state === "merged" ? " · 병합됨" : ""}`));
    d.append(h, stepsSection(it), findingsSection(it));
    const note = rerunNote(it);
    if (note) d.append(note);
    d.append(detailFoot(it));
    body.append(d);
  }

  // 15b 코드 리뷰 설정
  function openSettings() {
    const d = doc();
    const s = structuredClone(d?.settings || { provider: "codex", on_blocking: "notify", repos_off: [] });
    s.repos_off ||= [];
    const dlg = el("dialog", "crew-dialog rv-settings");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const t = el("div", "");
    t.append(el("h2", "", "코드 리뷰 설정"), el("p", "", "PR을 올리면 서버가 자동으로 검사해요. 여기서 누가 읽고, 어디까지 할지 정해요."));
    head.append(t);
    const body = el("div", "dialog-body");
    const choice = (opts, cur, onPick) => {
      const g = el("div", "rv-choice");
      for (const [k, title, sub] of opts) {
        const b = el("button", `start-card${cur() === k ? " on" : ""}`);
        b.type = "button";
        const tt = el("div", "");
        tt.append(el("strong", "", title), el("small", "dim", sub));
        b.append(tt);
        b.addEventListener("click", () => { onPick(k); g.querySelectorAll(".start-card").forEach((x, i) => x.classList.toggle("on", opts[i][0] === k)); });
        g.append(b);
      }
      return g;
    };
    const model = items().find((x) => x.model)?.model || "";
    body.append(el("h3", "", "리뷰하는 AI"), el("p", "pg-hint", "코드를 쓴 AI와 다른 쪽을 고르면 같은 실수를 같이 놓치지 않아요."),
      choice([["codex", "Codex", model.startsWith("codex") ? `${model.split(" / ")[1] || ""} · 지금 사용 중` : "gpt-6.1-sol"], ["claude", "Claude", "sonnet · 구독 필요"]], () => s.provider, (k) => { s.provider = k; }));
    body.append(el("h3", "", "검사할 레포"), el("p", "pg-hint", "켜 두면 새 PR마다 품질 검사와 AI 리뷰가 돌아요."));
    const list = el("div", "pg-card");
    for (const r of d?.repos || []) {
      const row = el("div", "setting");
      const tt = el("div");
      const last = items().find((x) => x.repo === r.name);
      const lastNote = last ? `마지막 검사 ${ago(last.finished_at || last.started_at)}` : "검사 기록 없음";
      tt.append(el("strong", "", repoShort(r.name)), el("small", "", `PR ${r.prs}개 · ${lastNote}`));
      const on = !s.repos_off.includes(r.name);
      const sw = el("button", "switch");
      sw.type = "button";
      sw.setAttribute("role", "switch");
      sw.setAttribute("aria-checked", String(on));
      sw.setAttribute("aria-label", `${r.name} 검사`);
      sw.append(el("i"));
      sw.addEventListener("click", () => {
        const now = sw.getAttribute("aria-checked") !== "true";
        sw.setAttribute("aria-checked", String(now));
        s.repos_off = s.repos_off.filter((x) => x !== r.name);
        if (!now) s.repos_off.push(r.name);
      });
      row.append(tt, sw);
      list.append(row);
    }
    body.append(list, el("h3", "", "지적이 나오면"), el("p", "pg-hint", "알림만이면 PR에 코멘트만 남겨요. 막으면 blocking 지적이 있는 PR은 검사가 실패해 병합할 수 없어요."),
      choice([["notify", "알림만", "PR에 코멘트만 남겨요"], ["block", "심각한 건 막기", "blocking이면 병합 불가"]], () => s.on_blocking, (k) => { s.on_blocking = k; }));
    const foot = el("div", "dialog-foot");
    const cancel = button("취소", "ghost");
    cancel.type = "submit";
    cancel.value = "cancel";
    const ok = button("저장", "primary");
    ok.type = "submit";
    ok.value = "ok";
    foot.append(cancel, ok);
    form.append(head, body, foot);
    form.addEventListener("submit", async (e) => {
      if (e.submitter?.value !== "ok") return;
      e.preventDefault();
      try {
        state.data.reviews = (await request("/v1/code_reviews/settings", { method: "POST", body: JSON.stringify({ scope: state.scope, settings: s }) })).code_reviews;
        dlg.close();
        notice("저장했어요. 서버가 1분 안에 CI 설정에 반영해요.");
        render(false);
      } catch (error_) { notice(`저장하지 못했어요 (${error_.message}).`); }
    });
    dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
  }

  return {
    load, reload, render: renderView, detail, openSettings,
    count: () => items().filter((it) => it.state === "running" || openFindings(it).length).length,
    head: () => [],
    views: { reviews: { title: "코드 리뷰", sub: () => { const d = doc(); const wk = items().filter((it) => Date.now() - new Date(it.started_at || 0) < 7 * DAY).length; return d?.collected_at ? `PR을 올리면 AI가 읽고 지적해요. 레포 ${(d.repos || []).length}곳, 이번 주 ${wk}건을 검토했어요.` : "PR을 올리면 AI가 읽고 지적해요."; } } }
  };
};
