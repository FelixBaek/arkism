// 설정 › 연결 › 모델 중계기 (Figma 380:28051 설정됨, 380:28336 없음). The desktop host reports whether this PC
// has a usable gateway token (gateway_token_status: set | missing | invalid, never the value) and makes a new
// one (gateway_token_regenerate: writes it, then install-arkism puts it in the agents' settings).
window.ArkismGatewayToken = (ctx) => {
  "use strict";
  const { el, button, tauri, notice } = ctx;
  if (!tauri) return null;
  let later = false; // 나중에: the 확인해 주세요 card stays hidden until the app restarts
  const when = (ms) => new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric" }).format(new Date(ms));
  const TEXT = {
    set: ["설정됨", "", "Claude Code·Codex가 모델 중계기(:8099)를 거칠 때 쓰는 열쇠예요. 다시 만들면 에이전트 설정에 새 토큰을 넣고, 중계기를 다시 켜면 예전 토큰은 막혀요."],
    missing: ["없음", "warn", "토큰이 없어서 모델 중계기(:8099)를 거치는 요청이 막혀요. 다시 만들면 연결된 에이전트 설정에 바로 넣어요."],
    invalid: ["확인 필요", "warn", "토큰 파일이 깨졌거나 다른 사용자도 읽을 수 있게 열려 있어요. 다시 만들면 고쳐져요."]
  };

  // Returns [the 확인해 주세요 card (top of the right column), the 모델 중계기 section]; both fill in when the host answers.
  function side(sideSec) {
    const alert = el("section", "gw-alert");
    alert.hidden = true;
    const line = el("div", "gw-line");
    const note = el("p", "v4-side-note gw-note");
    const sec = sideSec("모델 중계기", line, note);
    sec.classList.add("gw-sec");
    let busy = false;
    async function regenerate(st) {
      if (busy) return;
      if (st?.state === "set" && !confirm("중계기 토큰을 다시 만들까요? 에이전트 설정에 새 토큰을 넣어요.")) return;
      busy = true;
      try {
        const r = await tauri.invoke("gateway_token_regenerate");
        notice(r.agents_updated ? "새 토큰을 만들고 에이전트 설정에 넣었어요. 중계기를 다시 켜면 예전 토큰은 막혀요."
          : "새 토큰을 만들었어요. 에이전트 설정에는 못 넣었어요 — 진단에서 고치기를 눌러 주세요.");
        draw(r.status);
      } catch (e) { notice("토큰을 만들지 못했어요."); }
      busy = false;
    }
    function draw(st) {
      if (!st) { line.replaceChildren(el("span", "gw-label", "중계기 토큰"), el("span", "gw-when", "확인하지 못했어요")); note.textContent = ""; return; }
      const [label, cls, text] = TEXT[st.state] || TEXT.invalid;
      const remake = el("button", "gw-remake", "다시 만들기");
      remake.type = "button";
      remake.addEventListener("click", () => regenerate(st));
      line.replaceChildren(el("span", "gw-label", "중계기 토큰"), el("span", `gw-chip${cls ? ` ${cls}` : ""}`, label));
      if (st.state === "set" && st.updated_ms) line.append(el("span", "gw-when", when(st.updated_ms)));
      line.append(remake);
      note.replaceChildren(el("span", "", text));
      if (st.from_env) note.append(" ", el("span", "", "지금은 ARKISM_GATEWAY_TOKEN 환경 변수의 값을 써요."));
      const needs = st.state !== "set" && !later;
      alert.hidden = !needs;
      if (needs) {
        const acts = el("div", "gw-alert-acts");
        acts.append(button("토큰 다시 만들기", "primary small", null, () => regenerate(st)),
          button("나중에", "ghost small", null, () => { later = true; alert.hidden = true; }));
        alert.replaceChildren(el("p", "", st.state === "missing" ? "중계기 토큰이 없어요. 지금은 Claude Code·Codex 요청이 모델 라우팅을 거치지 못해요." : "중계기 토큰 파일에 문제가 있어요. 지금은 Claude Code·Codex 요청이 모델 라우팅을 거치지 못해요."), acts);
      }
    }
    line.append(el("span", "gw-label", "중계기 토큰"), el("span", "gw-when", "확인하는 중…"));
    tauri.invoke("gateway_token_status").then(draw, () => draw(null));
    return [alert, sec];
  }
  return { side };
};
