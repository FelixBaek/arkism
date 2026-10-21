// Owns persona settings. Dependencies are explicit live ports, never a global scope.
export function createCrewPersonas(deps) {

  // ---------------------------------------------------------------- 설정 › 워크플로우 › 페르소나
  // Saved personas an AI node picks under 페르소나. One opens at a time and saves on its own.
  let personaOpen = null;

  let personaDraft = null;

  const personaAI = new WeakMap();
 // the AI talk per open draft (ai-chat.js state), gone with the draft
  const personaUses = (id) => deps.workflows().filter((r) => (r.crew.graph?.nodes || []).some((n) => n.config?.persona === id));

  function personaTab({ group }) {
    const g = group("페르소나", "AI 노드의 ‘페르소나’에서 골라 써요. 지시는 그 노드가 일할 때 역할·말투로 들어가요.");
    const list = deps.el("div", "pg-card");
    const rows = [...deps.personas(), ...(personaOpen === "new" ? [{ id: "new", crew: personaDraft }] : [])];
    if (!rows.length) list.append(deps.el("p", "pg-hint", "아직 저장한 페르소나가 없어요."));
    for (const r of rows) {
      const isOpen = personaOpen === r.id;
      const row = deps.el("div", "cat");
      const head = deps.el("button", "cat-head");
      head.type = "button";
      head.setAttribute("aria-expanded", String(isOpen));
      const used = r.id === "new" ? 0 : personaUses(r.id).length;
      head.append(deps.icon("robot", "icon dim"), deps.el("strong", "", r.crew.name || "새 페르소나"), deps.el("span", "cat-rules", r.crew.role || ""),
        deps.el("span", "tag", used ? `노드 ${used}곳에서 씀` : "안 씀"), deps.icon(isOpen ? "caret-down" : "caret-right"));
      head.addEventListener("click", () => {
        personaOpen = isOpen ? null : r.id;
        personaDraft = isOpen ? null : structuredClone(r.crew);
        deps.render(false);
      });
      row.append(head);
      if (isOpen) row.append(personaEditor(r.id === "new" ? null : r));
      list.append(row);
    }
    const add = deps.button("새 페르소나", "dashed", "plus", () => {
      personaOpen = "new";
      personaDraft = { type: "persona", name: "", role: "", instructions: "" };
      deps.render(false);
    });
    add.disabled = personaOpen === "new";
    g.append(list, add);
    return [g];
  }

  function personaEditor(rec) {
    const d = personaDraft;
    const box = deps.el("div", "cat-body");
    const name = deps.input(d.name, { maxLength: 60, placeholder: "꼼꼼한 검토자" });
    const role = deps.input(d.role, { maxLength: 200, placeholder: "PR을 보안·데이터 손실 위주로 봐요" });
    const text = deps.input(d.instructions, { tag: "textarea", rows: 8, maxLength: 20000, placeholder: "너는 ○○다. …한다." });
    name.addEventListener("input", () => { d.name = name.value; });
    role.addEventListener("input", () => { d.role = role.value; });
    text.addEventListener("input", () => { d.instructions = text.value; });
    if (!personaAI.has(d)) personaAI.set(d, { box: {}, text: {} });
    const talk = personaAI.get(d);
    const put = (node, key, v) => { d[key] = v; if (node.isConnected) node.value = v; };
    // AI로 마무리 (ai-chat.js): Opus 5.5 (medium) finishes what is written so far as proposals; nothing is saved until 저장.
    const ai = window.ArkismAIChat.button({ state: talk.box, label: "AI로 마무리", title: "AI로 마무리", sub: "쓴 데까지 보고 빈 칸과 지시를 마무리해요",
      greeting: "어떻게 마무리할까요? '그대로 마무리해 줘'를 누르면 쓴 데까지 보고 빈 칸을 채우고 지시를 번호 목록으로 정리해요.",
      placeholder: "예: 말투는 정중하게, 보안 문제는 꼭 먼저 말하게", quick: ["그대로 마무리해 줘", "더 짧게", "하지 말 것을 넣어 줘"],
      ask: async (want) => {
        if (!(d.name + d.role + d.instructions).trim()) throw new Error(deps.tr("이름이나 지시를 조금이라도 먼저 써 주세요."));
        const prompt = `Finish this persona draft. Keep the author's intent and the sentences already written, fill the empty fields, and turn the instructions into a short numbered list in Korean, opening with who the agent is ("You are …" in Korean), that makes the role, tone, must-dos and must-nots clear. Use only the keys name, role, instructions.\nThe author asks: ${want}\n\nName: ${d.name || "(empty)"}\nOne-line description: ${d.role || "(empty)"}\nInstructions:\n${d.instructions || "(empty)"}`;
        const a = await deps.kit.assist("fill", prompt, { model: "opus 5.5", persona: { crew: { instructions: "You write personas for AI agents, in short, clear Korean." } } });
        if (a.status !== "done") throw new Error(deps.kit.assistError(a));
        const got = deps.kit.parseJSON(a.output || "") || {};
        if (!got.instructions && !got.name) throw new Error(deps.tr("AI 답을 읽지 못했어요. 다시 눌러 주세요."));
        const prop = (node, key, label, max) => {
          const to = got[key] ? String(got[key]).slice(0, max) : "", from = d[key];
          return to && to !== from ? { label, from, to, apply: () => put(node, key, to), undo: () => put(node, key, from) } : null;
        };
        const props = [prop(name, "name", "이름", 60), prop(role, "role", "한 줄 설명", 200), prop(text, "instructions", "지시", 20000)].filter(Boolean);
        return { say: props.length ? "마무리한 곳을 아래에 넣었어요. 보고 적용한 뒤 저장해 주세요." : "", props };
      } });
    const textAI = window.ArkismAIChat.field(text, { state: talk.text, title: "지시 다듬기", preset: "You edit the instructions of a persona for AI agents (who it is, tone, must-dos, must-nots). Write short, clear Korean, as a numbered list. No keys or tokens.",
      context: () => JSON.stringify({ name: d.name, role: d.role }) });
    box.append(ai, deps.field("이름", name), deps.field("한 줄 설명", role, "목록과 노드 선택에서 보여요"), deps.field("지시", textAI, "역할·말투·꼭 지킬 것을 써요. 키나 토큰은 넣지 마세요."));
    const bar = deps.el("div", "crew-actions");
    const ok = deps.button("저장", "primary", null, async () => {
      if (!d.name.trim()) { window.ArkismNeed(name, "이름을 적어 주세요."); return; }
      if (!d.instructions.trim()) { window.ArkismNeed(text, "지시를 적어 주세요."); return; }
      ok.disabled = true;
      try {
        await deps.save(rec, { ...(rec?.crew || {}), type: "persona", name: d.name.trim(), role: d.role.trim(), instructions: d.instructions });
        personaOpen = personaDraft = null;
        deps.notice("페르소나를 저장했어요.");
        await deps.reload();
      } catch (e) { deps.notice(deps.errorText(e)); ok.disabled = false; }
    });
    bar.append(ok, deps.button("취소", "ghost", null, () => { personaOpen = personaDraft = null; deps.render(false); }));
    if (rec) {
      bar.append(deps.button("지우기", "ghost danger", "trash", async () => {
        const users = personaUses(rec.id);
        // A node pointing at a deleted persona would lose its instructions silently.
        if (users.length) { deps.notice(`${users.map((u) => u.crew.name).join(", ")}에서 쓰고 있어요. 노드에서 먼저 바꿔 주세요.`); return; }
        if (!await ask(`'${rec.crew.name}' 페르소나를 지울까요? 되돌릴 수 없어요.`)) return;
        try {
          await deps.request(`/v1/crew/${encodeURIComponent(rec.id)}`, { method: "DELETE", body: JSON.stringify({ scope: deps.state.scope, expected_revision: rec.revision }) });
          personaOpen = personaDraft = null;
          deps.notice("지웠어요.");
          await deps.reload();
        } catch (e) { deps.notice(deps.errorText(e)); }
      }));
    }
    bar.append(ai.dock); // AI로 마무리 at the right end of 저장·취소; its purple list stays above the fields
    box.append(bar);
    return box;
  }

  return {
    get personaTab() { return personaTab; }
  };
}
