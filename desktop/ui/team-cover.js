// 카드 사진 고르기 (Figma 985:61011 · 967:61079): a floating window over the team popup.
// Unsplash through the server (/v1/unsplash/search; the key stays there), the person's own photo, or none.
// A pick closes the window at once. Covers are {id, url, thumb, author, author_url}; an own photo is {id: "mine", url: data URL, author: "내 사진"}.
window.ArkismCoverPicker = ({ el, icon, request, query, state, notice }) => {
  const WORDS = [["우주", "space"], ["숲", "forest"], ["바다", "sea"], ["도시", "city"], ["추상", "abstract"]];
  const MAX_DATA = 140000; // the server keeps data URLs under 150,000 characters
  const same = (a, b) => !!a && !!b && a.id === b.id;

  // The picture a card shows: the thumbnail for a search hit, the data URL for an own photo.
  const src = (c) => c?.thumb || c?.url || "";

  // Shrinks an image file to a JPEG data URL that fits the server limit.
  function shrink(file) {
    return new Promise((resolve, reject) => {
      const img = new Image();
      const url = URL.createObjectURL(file);
      img.onload = () => {
        URL.revokeObjectURL(url);
        let w = Math.min(1000, img.width);
        for (let attempt = 0; attempt < 6; attempt++) {
          const c = document.createElement("canvas");
          c.width = w; c.height = Math.max(1, Math.round((img.height * w) / img.width));
          c.getContext("2d").drawImage(img, 0, 0, c.width, c.height);
          const data = c.toDataURL("image/jpeg", 0.75);
          if (data.length <= MAX_DATA) return resolve(data);
          w = Math.round(w * 0.8);
        }
        reject(new Error("too_big"));
      };
      img.onerror = () => { URL.revokeObjectURL(url); reject(new Error("not_image")); };
      img.src = url;
    });
  }

  function open({ current, onPick, anchor }) {
    const dlg = el("dialog", "crew-dialog tp-cover");
    const head = el("div", "tp-cover-head");
    const title = el("div", "tp-cover-title");
    title.append(el("b", "", "카드 사진"), el("span", "", "팀 목록 카드에 깔리는 사진이에요"));
    const acts = el("div", "tp-cover-acts");
    const file = el("input"); file.type = "file"; file.accept = "image/*"; file.hidden = true;
    const upload = el("button", "tp-link"); upload.type = "button"; upload.textContent = "↑ 내 사진 올리기";
    const clear = el("button", "tp-link quiet", "배경 없애기"); clear.type = "button";
    acts.append(upload, clear, file);
    head.append(title, acts);

    const box = el("label", "tp-cover-search");
    box.append(icon("magnifying-glass"));
    const q = el("input"); q.type = "search"; q.placeholder = "영어로 찾으면 더 많아요 (예: space, desk)"; q.maxLength = 100;
    box.append(q, el("em", "", "Unsplash"));
    const chips = el("div", "tp-cover-chips");
    const grid = el("div", "tp-cover-grid");
    const status = el("p", "tp-cover-status");
    const foot = el("div", "tp-cover-foot");
    const more = el("button", "tp-link", "더 보기"); more.type = "button"; more.hidden = true;
    foot.append(el("small", "", "Unsplash 무료 사진 · 찍은 사람 이름이 카드 구석에 같이 보여요"), more);
    dlg.append(head, box, chips, status, grid, foot);

    let page = 1, query0 = "", seq = 0;
    const pick = (c) => { dlg.close(); onPick(c); };
    async function find(text, n) {
      query0 = text.trim();
      if (!query0) return;
      const mine = ++seq;
      status.textContent = "찾는 중…";
      try {
        const res = await request(`/v1/unsplash/search?${query({ q: query0, page: n })}`);
        if (mine !== seq) return;
        page = n;
        if (n === 1) grid.replaceChildren();
        for (const ph of res.items || []) {
          const b = el("button", `tp-cover-ph${same(current, ph) ? " on" : ""}`); b.type = "button";
          b.title = ph.alt || "";
          const img = el("img"); img.alt = ph.alt || ""; img.loading = "lazy"; img.src = ph.thumb;
          const by = el("span", "", ph.author); by.setAttribute("data-no-i18n", "");
          b.append(img, by);
          b.addEventListener("click", () => {
            pick({ id: ph.id, url: ph.url, thumb: ph.thumb, author: ph.author, author_url: ph.author_url });
            request(`/v1/unsplash/photos/${encodeURIComponent(ph.id)}/use`, { method: "POST", body: JSON.stringify({ scope: state.scope }) }).catch(() => {}); // Unsplash's download-tracking rule
          });
          grid.append(b);
        }
        status.textContent = grid.childElementCount ? "" : "맞는 사진이 없어요. 다른 말로 찾아 보세요.";
        more.hidden = page >= Math.min(res.total_pages || 1, 50);
      } catch (e) {
        grid.replaceChildren(); more.hidden = true;
        status.textContent = e.message === "dependency_unavailable"
          ? "Unsplash 키가 없어요. 서버에 ARKISM_UNSPLASH_KEY를 넣으면 검색할 수 있어요. 내 사진 올리기는 그대로 쓸 수 있어요."
          : `찾지 못했어요 (${e.message}).`;
      }
    }
    for (const [label, word] of WORDS) {
      const c = el("button", "tp-chip", label); c.type = "button";
      c.addEventListener("click", () => { q.value = label; /* 985:60681 shows the chip word; the search itself goes out in English */ chips.querySelectorAll(".on").forEach((x) => x.classList.remove("on")); c.classList.add("on"); void find(word, 1); });
      chips.append(c);
    }
    q.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) { chips.querySelectorAll(".on").forEach((x) => x.classList.remove("on")); void find(q.value, 1); } });
    more.addEventListener("click", () => find(query0, page + 1));
    clear.addEventListener("click", () => pick(null));
    upload.addEventListener("click", () => file.click());
    file.addEventListener("change", async () => {
      const f = file.files?.[0];
      if (!f) return;
      try { pick({ id: "mine", url: await shrink(f), author: "내 사진", author_url: "" }); }
      catch { notice("사진을 줄이지 못했어요. 다른 사진으로 해 주세요."); }
    });
    dlg.addEventListener("close", () => dlg.remove());
    dlg.addEventListener("click", (e) => { if (e.target === dlg) dlg.close(); });
    document.body.append(dlg);
    dlg.showModal();
    // 985:60681 · 967:60615: the window hangs 8px under the card photo, its left edge on the photo's
    if (anchor) {
      const r = anchor.getBoundingClientRect(), w = dlg.offsetWidth;
      const top = Math.max(16, Math.min(r.bottom + 8, innerHeight - 410)); // ~400px tall once photos load
      dlg.classList.add("anchored");
      dlg.style.left = `${Math.max(16, Math.min(r.left, innerWidth - w - 16))}px`;
      dlg.style.top = `${top}px`;
      dlg.style.maxHeight = `${innerHeight - top - 16}px`;
    }
    chips.firstChild.click();
  }
  return { open, src };
};
