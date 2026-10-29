// 이미지·슬라이드 프롬프트 (modules/imageViews.mjs): gallery filters, detail, the 실행 page, the editor and AI naming.
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const { createDocument, house, flush } = require("./testdata/views-dom.js");
const mod = import("./modules/imageViews.mjs");

function setup({ prompts = [], requests = {}, stored = {}, crew = [], clipboard = true, comfyUrl = "" } = {}) {
  const doc = createDocument();
  globalThis.document = doc;
  const opened = [], copied = [], downloads = [];
  globalThis.window = { open: (u) => opened.push(u) };
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: { clipboard: { writeText: async (t) => { if (!clipboard) throw new Error("denied"); copied.push(t); } } } });
  let answer = true, text = "http://comfy:8188/";
  globalThis.ask = async () => answer;
  globalThis.askText = async () => text;
  globalThis.URL.createObjectURL = () => "blob:x";
  globalThis.URL.revokeObjectURL = () => {};
  const h = house(doc);
  const add = (tag, id, parent = doc.body) => { const n = Object.assign(h.el(tag), { id }); parent.append(n); return n; };
  const main = h.el("div", "main"); main.append(h.el("div", "page-head")); doc.body.append(main);
  for (const id of ["controls", "chips", "stats", "table", "p-images", "prompt-error", "prompt-auto", "p-kind-pills", "p-engine-pills", "prompt-dialog-title", "p-tag-list"]) add("div", id);
  add("input", "search");
  const back = add("button", "prompt-dialog-back"); back.append(h.el("span"));
  const file = add("input", "p-image-file");
  file.click = () => { file.clicked = (file.clicked || 0) + 1; };
  const dialog = add("dialog", "prompt-dialog");
  const form = add("form", "prompt-form", dialog);
  for (const id of ["p-title", "p-tags", "p-negative", "p-size", "p-count", "p-seed", "p-script-name", "p-experts"]) add("input", id, form);
  for (const id of ["p-prompt", "p-script"]) add("textarea", id, form);
  for (const [id, opts] of [["p-kind", ["image", "slide"]], ["p-engine", ["gemini", "gpt", "claude", "grok", "comfyui", "other"]]]) {
    const s = add("select", id, form);
    for (const v of opts) { const o = h.el("option", "", v.toUpperCase()); o.value = v; s.append(o); }
  }
  add("button", "p-title-ai", form);
  const calls = [], notices = [], log = [], runs = [], assists = [];
  const state = { imageTab: "image", engine: "all", imgCat: "all", selected: null, promptRun: null, scope: { project_id: "p" }, data: { prompts, crew } };
  const deps = {
    ...h, state,
    agentLogo: (src, name) => h.el("img", "logo", name),
    segment: (opts, cur, on) => { const s = h.el("div", "segment"); s.pick = on; s.opts = opts; return s; },
    gallery: { isOpen: () => state.galleryOpen, archiveChip: () => h.el("button", "archive-chip"), renderArchive: (k, t) => log.push(["archive", k, t]), cardShortcut: (k, p, card) => card, deleteButton: () => h.el("button", "del") },
    render: (x) => log.push(["render", x]), matches: (t) => !state.query || t.toLowerCase().includes(state.query), markSelected: () => log.push(["mark"]), renderDetail: () => log.push(["detail"]), openDetail: () => log.push(["open"]),
    store: (k, v) => log.push(["store", k, v]), stored: (k) => stored[k], notice: (t) => notices.push(t), query: () => "q=1",
    detailShell: (t) => { const b = h.el("div", "detail"); b.heading = t; doc.body.append(b); return b; },
    crew: {
      runWorkflow: async (rec, t) => { runs.push([rec.crew.handle, t]); return state.runOk ?? true; },
      assist: async (kind, t, o) => { assists.push([kind, t, o]); if (state.assist instanceof Error) throw state.assist; return typeof state.assist === "function" ? state.assist() : state.assist ?? { status: "done", output: "ok" }; },
      parseJSON: (t) => { try { return JSON.parse(t); } catch { return null; } }, assistError: (a) => `오류 ${a.error}`,
    },
    pages: { prefs: { comfyUrl }, savePrefs: () => log.push(["prefs"]) },
    trimSlash: (u) => u.replace(/\/+$/, ""), shortDate: (d) => `D(${d})`, fillList: (id, v) => log.push(["fill", id, v.length]),
    request: async (path, opt) => { calls.push([path, opt && JSON.parse(opt.body)]); const r = requests[path]; if (r instanceof Error) throw r; return typeof r === "function" ? r(opt) : r ?? {}; },
  };
  return { doc, h, deps, state, calls, notices, log, opened, copied, runs, assists, dialog, form, file, downloads, setAnswer: (v) => { answer = v; }, setText: (v) => { text = v; } };
}
async function views(o) { const s = setup(o); const { createImageViews } = await mod; s.v = createImageViews(s.deps); return s; }

const P = (id, ip, extra = {}) => ({ id, revision: 2, updated_at: "2026-10-02T00:00:00Z", created_at: "2026-10-01T00:00:00Z", created_by: "felix", image_prompt: { kind: "image", engine: "gemini", title: id, prompt: "p", tags: [], ...ip }, ...extra });

test("gallery: model tabs, category and script chips, sort, search and the card click", async () => {
  const prompts = [
    P("b", { title: "Banana", engine: "codex", tags: ["음식"], images: ["data:1"], prompt: "a {fruit}" }, { updated_at: "2026-10-05" }),
    P("a", { title: "Apple", engine: "gemini", tags: ["음식", "과일"], script: "x" }, { created_at: "2026-10-09" }),
    P("c", { title: "Cat", engine: "comfyui_api", tags: [] }),
    P("s", { title: "Deck", kind: "slide", size: "16:9", script: "py" }),
    P("t", { title: "Deck 2", kind: "slide" }),
  ];
  const s = await views({ prompts, stored: { "arkism.promptSort": "name" } });
  s.state.engine = "grok"; s.state.imgCat = "missing";
  s.v.renderImages();
  assert.equal(s.state.engine, "all");
  assert.equal(s.state.imgCat, "all");
  const seg = s.doc.querySelector(".segment");
  assert.deepEqual(seg.opts.map((o) => o[0]), ["all", "gemini", "gpt", "comfyui"]);
  const names = () => s.doc.querySelectorAll("#table .prompt-card strong").map((x) => x.textContent);
  assert.deepEqual(names(), ["Apple", "Banana", "Cat"]);
  assert.equal(s.doc.querySelector("#table .prompt-tile img").src, "data:1");
  const chips = s.doc.querySelectorAll("#chips .chip");
  assert.deepEqual(chips.map((c) => c.textContent), ["전체", "과일", "음식", "스크립트 있음"]);
  chips[3].click();
  assert.equal(s.state.imgCat, "\u0000script");
  s.v.renderImages();
  assert.deepEqual(names(), ["Apple"]);
  s.state.imgCat = "음식"; s.v.renderImages();
  assert.deepEqual(names(), ["Apple", "Banana"]);
  seg.pick("gpt"); s.v.renderImages();
  assert.deepEqual(names(), ["Banana"]);
  s.state.engine = "all"; s.state.imgCat = "all";
  s.v.renderImages();
  const sort = s.doc.querySelector("#chips select");
  sort.value = "created"; await sort.fire("change");
  assert.deepEqual(names(), ["Apple", "Banana", "Cat"]);
  sort.value = "updated"; await sort.fire("change");
  assert.deepEqual(names(), ["Banana", "Apple", "Cat"]);
  const search = s.doc.querySelector("#chips .pane-search input");
  search.value = "Cat "; await search.fire("input");
  assert.equal(s.doc.querySelector("#search").value, "Cat ");
  assert.deepEqual(names(), ["Cat"]);
  s.state.query = "zzz"; s.v.renderImages();
  assert.match(s.doc.querySelector("#table").textContent, /조건에 맞는 프롬프트가 없어요/);
  s.state.query = "";
  s.v.renderImages();
  s.doc.querySelector("#table .prompt-card").click();
  assert.equal(s.state.selected, "prompt:b");
  assert.ok(s.doc.querySelector("#table .pc-meta .logo"));
  s.state.imageTab = "slide";
  s.v.renderImages();
  assert.deepEqual(names(), ["Deck", "Deck 2"]);
  assert.equal(s.doc.querySelector("#table .pc-num"), null);
  assert.match(s.doc.querySelector("#table .pc-meta").textContent, /16:9 · 스크립트 연결됨/);
  s.state.galleryOpen = true; s.v.renderImages();
  assert.deepEqual(s.log.at(-1), ["archive", "prompts", "slide"]);
  const e = await views({ prompts: [] });
  e.v.renderImages();
  assert.match(e.doc.querySelector("#table").textContent, /아직 프롬프트가 없어요/);
  e.state.promptRun = "gone"; e.v.renderImages();
  assert.equal(e.state.promptRun, null);
  e.v.imagesSummary();
  assert.match(e.doc.body.textContent, /프롬프트 보관함/);
  e.state.imageTab = "slide"; e.v.imagesSummary();
});

test("detail: thumbnails swap, variables fill, script copy and download, run hands it to the workflow", async () => {
  const flow = { crew: { type: "workflow", handle: "painter", name: "Painter" } };
  const p = P("a", { title: "Fruit", prompt: "a {fruit} on {table}", images: ["i1", "i2"], negative: "blur", size: "1024×1024", count: 2, seed: "7", script: "print()", script_name: "run.py", experts: ["$painter"] });
  const s = await views({ prompts: [p], crew: [flow, { crew: { type: "expert", handle: "x" } }] });
  s.v.promptDetail(p);
  const body = s.doc.querySelector(".detail");
  const thumbs = body.querySelectorAll(".prompt-thumbs button");
  thumbs[1].click();
  assert.equal(body.querySelector(".prompt-tile img").src, "i2");
  assert.ok(thumbs[1].classList.contains("active") && !thumbs[0].classList.contains("active"));
  const inputs = body.querySelectorAll("label.v4g-field input");
  inputs[0].value = "apple"; await inputs[0].fire("input");
  assert.match(body.textContent, /\$painter 워크플로우 연결됨/);
  const btn = (label, root = body) => root.querySelectorAll(".btn").find((b) => b.textContent.includes(label));
  const copy = btn("프롬프트 복사");
  await copy.click();
  assert.deepEqual(s.copied, ["a {fruit} on {table}"]);
  assert.equal(copy.lastChild.textContent, "복사됨");
  const realCreate = s.doc.createElement;
  s.doc.createElement = (t) => { const n = realCreate(t); if (t === "a") n.click = () => { s.downloads.push(n.download); }; return n; };
  btn("스크립트 내려받기").click();
  assert.deepEqual(s.downloads, ["run.py"]);
  const run = btn("스크립트 실행");
  await run.click();
  assert.deepEqual(s.runs[0], ["painter", "a apple on {table}\n\n참고할 스크립트 (run.py):\nprint()\n크기: 1024×1024\n개수: 2"]);
  assert.match(body.querySelector(".v4g-out").textContent, /\$painter에 맡겼어요/);
  s.state.runOk = false;
  await run.click();
  assert.match(body.querySelector(".v4g-out").textContent, /맡기지 못했어요/);
  btn("이 프롬프트로 만들기").click();
  assert.equal(s.state.promptRun, "a");
  btn("고치기").click();
  assert.equal(s.dialog.open, true);
  assert.equal(s.doc.querySelector("#prompt-dialog-title").textContent, "프롬프트 고치기");
});

test("copy failure, slide downloads, and a prompt with no workflow copies instead of running", async () => {
  const slide = P("s", { kind: "slide", title: "Deck", script: "py", prompt: "plain" });
  const s = await views({ prompts: [slide], clipboard: false });
  s.v.promptDetail(slide);
  const body = s.doc.querySelector(".detail");
  const btn = (label) => body.querySelectorAll(".btn").find((b) => b.textContent.includes(label));
  const copy = btn("프롬프트 복사");
  await copy.click();
  assert.equal(copy.lastChild.textContent, "복사 안 됨");
  assert.match(s.notices.at(-1), /복사하지 못했어요 \(denied\)/);
  assert.match(body.textContent, /연결된 워크플로우 없음/);
  assert.match(body.textContent, /쓰는 워크플로우\(\$이름\)를 정하면/);
  const realCreate = s.doc.createElement;
  s.doc.createElement = (t) => { const n = realCreate(t); if (t === "a") n.click = () => s.downloads.push(n.download); return n; };
  btn("스크립트 내려받기 (PPTX)").click();
  const second = body.querySelectorAll(".btn").filter((b) => b.textContent === "스크립트 내려받기");
  second[0].click();
  assert.deepEqual(s.downloads, ["make_slide.py", "make_slide.py"]);
  s.state.promptRun = "s";
  s.v.runPrompt(null);
  await flush();
  assert.match(s.notices.at(-1), /쓰는 워크플로우가 없어서/);
  s.state.promptRun = "none";
  s.v.runPrompt(null);
  // An image prompt without script names its download workflow.json.
  const img = P("w", { script: "{}" });
  s.v.promptDetail(img);
  s.doc.querySelectorAll(".detail")[1].querySelectorAll(".btn").find((b) => b.textContent === "스크립트 내려받기").click();
  assert.equal(s.downloads.at(-1), "workflow.json");
});

test("ComfyUI prompts ask for the address once, send the escaped prompt and open the page", async () => {
  const p = P("c", { engine: "comfyui", prompt: 'say "{word}"', script: '{"text":"{prompt}"}' });
  const s = await views({ prompts: [p] });
  s.v.promptDetail(p);
  const body = s.doc.querySelector(".detail");
  const btn = (label) => body.querySelectorAll(".btn").find((b) => b.textContent.includes(label));
  assert.match(body.textContent, /ComfyUI API/);
  const input = body.querySelector("label.v4g-field input");
  input.value = "hi"; await input.fire("input");
  await btn("ComfyUI로 만들기").click();
  await flush();
  assert.equal(s.deps.pages.prefs.comfyUrl, "http://comfy:8188");
  assert.deepEqual(JSON.parse(s.assists[0][1]), { url: "http://comfy:8188", workflow: '{"text":"say \\"hi\\""}' });
  assert.equal(s.notices.at(-1), "ok");
  s.state.assist = { status: "failed", error: "down" };
  await btn("ComfyUI로 만들기").click(); await flush();
  assert.match(s.notices.at(-1), /만들지 못했어요: down/);
  s.state.assist = { status: "failed" };
  await btn("ComfyUI로 만들기").click(); await flush();
  assert.match(s.notices.at(-1), /알 수 없음/);
  s.state.assist = new Error("net");
  await btn("ComfyUI로 만들기").click(); await flush();
  assert.match(s.notices.at(-1), /\(net\)/);
  await btn("ComfyUI에서 열기").click(); await flush();
  assert.deepEqual(s.opened, ["http://comfy:8188"]);
  assert.equal(s.copied.at(-1), '{"text":"say \\"hi\\""}');
  // No address given: nothing happens.
  const n = await views({ prompts: [p], clipboard: false });
  n.setText("");
  n.v.promptDetail(p);
  const b2 = n.doc.querySelector(".detail").querySelectorAll(".btn");
  await b2.find((b) => b.textContent === "ComfyUI로 만들기").click(); await flush();
  await b2.find((b) => b.textContent === "ComfyUI에서 열기").click(); await flush();
  assert.equal(n.assists.length, 0);
  assert.equal(n.opened.length, 0);
  // A saved address and blocked clipboard still opens the page; a script-less ComfyUI prompt only opens.
  const k = await views({ prompts: [p], clipboard: false, comfyUrl: "http://saved" });
  const bare = P("d", { engine: "comfyui" });
  k.v.promptDetail(bare);
  await k.doc.querySelector(".detail").querySelectorAll(".btn").find((b) => b.textContent === "ComfyUI에서 열기").click(); await flush();
  k.v.promptDetail(p);
  await k.doc.querySelectorAll(".detail")[1].querySelectorAll(".btn").find((b) => b.textContent === "ComfyUI에서 열기").click(); await flush();
  assert.deepEqual(k.opened, ["http://saved", "http://saved"]);
});

test("the 실행 page fills variables live, sets the ComfyUI address and its side detail", async () => {
  const flow = { crew: { type: "workflow", handle: "painter", name: "Painter" } };
  const p = P("c", { engine: "comfyui", prompt: "a {x}", script: "{prompt}", experts: ["Painter"] });
  const s = await views({ prompts: [p], crew: [flow] });
  s.state.promptRun = "c";
  s.v.renderImages();
  assert.ok(s.doc.querySelector(".page-head .page-back"));
  const input = s.doc.querySelector("#table label input");
  input.value = "cat"; await input.fire("input");
  assert.equal(s.doc.querySelector("#table .v4g-filled").textContent, "a cat");
  assert.match(s.doc.querySelector("#table").textContent, /주소 없음/);
  const btn = (label) => s.doc.querySelectorAll("#table .btn").find((b) => b.textContent.includes(label));
  await btn("연결 설정").click(); await flush();
  assert.equal(s.deps.pages.prefs.comfyUrl, "http://comfy:8188");
  assert.deepEqual(s.log.at(-1), ["render", false]);
  s.v.renderImages();
  assert.match(s.doc.querySelector("#table").textContent, /주소 저장됨/);
  s.setText("http://comfy:8188");
  await btn("연결 설정").click(); await flush();
  s.setText(null);
  await btn("연결 설정").click(); await flush();
  assert.ok(btn("ComfyUI로 만들기"));
  s.doc.querySelector(".page-back").click();
  assert.equal(s.state.promptRun, null);
  s.state.promptRun = "c";
  s.v.runPrompt(null);
  await flush();
  assert.equal(s.runs[0][0], "painter");
  assert.match(s.doc.querySelector("#prompt-run-out").textContent, /\$painter에 맡겼어요/);
  s.v.promptRunDetail(p);
  const side = s.doc.querySelector(".detail");
  assert.match(side.textContent, /\$painter 워크플로우에 맡겨요/);
  // A slide without script or variables, and an empty ComfyUI address on the run page.
  const slide = P("s", { kind: "slide", prompt: "plain" });
  const t = await views({ prompts: [slide, P("d", { engine: "comfyui", prompt: "x" })] });
  t.state.promptRun = "s"; t.v.renderImages();
  assert.match(t.doc.querySelector("#table").textContent, /변수가 없어요/);
  t.v.promptRunDetail(slide);
  assert.match(t.doc.querySelector(".detail").textContent, /스크립트가 없어요/);
  t.v.promptRunDetail(P("y", { kind: "slide", script: "py" }));
  assert.match(t.doc.querySelectorAll(".detail")[1].textContent, /PPTX 만들기/);
  t.v.promptRunDetail(P("z", { script: "py" }));
  t.state.promptRun = "d"; t.v.renderImages();
  t.setText(undefined);
  await t.doc.querySelectorAll("#table .btn").find((b) => b.textContent === "연결 설정").click(); await flush();
  assert.equal(t.deps.pages.prefs.comfyUrl, "");
});

test("duplicate and archive from the detail, with their failures", async () => {
  const p = P("a", { title: "Fruit" });
  const s = await views({ prompts: [p], requests: { "/v1/prompts": { id: "copy" }, "/v1/prompts?q=1": { items: [p] }, "/v1/prompts/a": {} } });
  s.v.promptRunDetail(p);
  const body = s.doc.querySelector(".detail");
  const btn = (label) => body.querySelectorAll(".btn").find((b) => b.textContent === label);
  await btn("복제").click(); await flush();
  assert.equal(s.calls[0][1].prompt.title, "Fruit 사본");
  assert.equal(s.state.selected, "prompt:copy");
  s.setAnswer(false);
  await btn("보관함에서 빼기").click(); await flush();
  assert.equal(s.calls.length, 2);
  s.setAnswer(true);
  await btn("보관함에서 빼기").click(); await flush();
  assert.deepEqual(s.calls[2][1], { scope: { project_id: "p" }, expected_revision: 2, archive: true });
  assert.match(s.notices.at(-1), /보관함에서 뺐어요/);
  btn("고치기").click();
  assert.equal(s.dialog.open, true);
  for (const [m, re] of [["secret_detected", /키나 토큰/], ["invalid_request", /이미지 크기/], ["boom", /\(boom\)/]]) {
    const f = await views({ prompts: [p], requests: { "/v1/prompts": new Error(m), "/v1/prompts/a": new Error(m) } });
    f.v.promptDetail(p);
    const b = f.doc.querySelector(".detail").querySelectorAll(".btn");
    await b.find((x) => x.textContent === "복제").click(); await flush();
    assert.match(f.notices.at(-1), re);
    await b.find((x) => x.textContent === "보관함에서 빼기").click(); await flush();
    assert.match(f.notices.at(-1), new RegExp(`처리하지 못했어요: ${m}`));
  }
  const e = await views({ prompts: [p], requests: { "/v1/prompts": {}, "/v1/prompts?q=1": {} } });
  e.v.promptDetail(p);
  await e.doc.querySelector(".detail").querySelectorAll(".btn").find((x) => x.textContent === "복제").click(); await flush();
  assert.equal(e.state.selected, null);
  assert.equal(e.v.copyTitle("가나다라", 5), "가나 사본");
});

test("editor: pills, images up to four, paste, save and save errors", async () => {
  globalThis.createImageBitmap = async (f) => { if (f.name === "bad") throw new Error("decode"); return { width: 4000, height: 2000, close() {} }; };
  const p = P("a", { title: "Old", tags: ["x", "y"], images: ["i1"], experts: ["$e"], count: 3 });
  const s = await views({ prompts: [p], requests: { "/v1/prompts": { id: "n", image_prompt: { kind: "slide" } }, "/v1/prompts?q=1": {}, "/v1/prompts/a": { id: "a", image_prompt: { kind: "image" } } } });
  s.v.openPromptEditor(null);
  assert.equal(s.doc.querySelector("#prompt-dialog-title").textContent, "프롬프트 추가");
  assert.equal(s.doc.querySelector("#prompt-dialog-back span").textContent, "이미지");
  const kindPills = () => s.doc.querySelectorAll("#p-kind-pills button");
  assert.deepEqual(kindPills().map((b) => b.getAttribute("aria-checked")), ["true", "false"]);
  kindPills()[1].click();
  assert.equal(s.doc.querySelector("#p-kind").value, "slide");
  await s.doc.querySelector("#p-engine").fire("change");
  assert.ok(s.doc.querySelector("#p-engine-pills .logo"));
  s.doc.querySelector("#p-images .img-add").click();
  assert.equal(s.file.clicked, 1);
  const file = (name, type = "image/png") => ({ name, type });
  s.file.files = [file("a"), file("t", "text/plain"), file("bad"), file("b")];
  await s.file.fire("change"); await flush();
  assert.equal(s.file.value, "");
  assert.match(s.doc.querySelector("#prompt-error").textContent, /bad를 줄이지 못해/);
  assert.equal(s.doc.querySelectorAll("#p-images .img-thumb").length, 2);
  s.doc.querySelectorAll("#p-images .img-thumb button")[0].click();
  assert.equal(s.doc.querySelectorAll("#p-images .img-thumb").length, 1);
  // The canvas never shrinks below the limit: the image is skipped with a note.
  s.doc._dataUrl = () => "x".repeat(600 * 1024);
  s.file.files = [file("huge")];
  await s.file.fire("change"); await flush();
  assert.match(s.doc.querySelector("#prompt-error").textContent, /huge를 줄이지 못해/);
  s.doc._dataUrl = null;
  await s.dialog.fire("paste", { clipboardData: { files: [file("t", "text/plain")] } });
  const pasted = await s.dialog.fire("paste", { clipboardData: { files: [file("c"), file("d"), file("e"), file("f")] }, preventDefault() { this.defaultPrevented = true; } });
  await flush();
  assert.equal(pasted.defaultPrevented, true);
  assert.match(s.doc.querySelector("#prompt-error").textContent, /4장까지/);
  assert.equal(s.doc.querySelectorAll("#p-images .img-thumb").length, 4);
  assert.equal(s.doc.querySelector("#p-images .img-add"), null);
  await s.dialog.fire("paste", {});
  s.doc.querySelector("#p-title").value = "New";
  s.doc.querySelector("#p-tags").value = "a, a, b";
  await s.form.fire("submit", { submitter: { value: "cancel" } });
  await s.form.fire("submit", { submitter: { value: "save" }, preventDefault() {} });
  assert.equal(s.calls[0][0], "/v1/prompts");
  assert.deepEqual(s.calls[0][1].prompt.tags, ["a", "b"]);
  assert.equal(s.calls[0][1].prompt.images.length, 4);
  assert.equal(s.state.imageTab, "slide");
  assert.equal(s.state.selected, "prompt:n");
  s.state.imageTab = "slide";
  s.v.openPromptEditor(p);
  assert.equal(s.doc.querySelector("#prompt-dialog-back span").textContent, "슬라이드");
  assert.equal(s.doc.querySelector("#p-tags").value, "x, y");
  assert.equal(s.doc.querySelector("#p-count").value, "3");
  await s.form.fire("submit", { submitter: { value: "save" }, preventDefault() {} });
  assert.equal(s.calls.at(-2)[1].expected_revision, 2);
  // A slow shrink lands after another prompt opened: the old list is not touched.
  let release;
  globalThis.createImageBitmap = () => new Promise((r) => { release = () => r({ width: 10, height: 10, close() {} }); });
  s.v.openPromptEditor(null);
  s.file.files = [file("slow"), file("later")];
  const pending = s.file.fire("change");
  await flush(2);
  s.v.openPromptEditor(null);
  release(); await pending; await flush();
  assert.equal(s.doc.querySelectorAll("#p-images .img-thumb").length, 0);
  for (const [m, re] of [["secret_detected", /키나 토큰/], ["invalid_request", /40자까지/], ["revision_conflict", /먼저 고쳤어요/], ["x", /\(x\)/]]) {
    const f = await views({ prompts: [p], requests: { "/v1/prompts/a": new Error(m) } });
    f.v.openPromptEditor(p);
    await f.form.fire("submit", { submitter: { value: "save" }, preventDefault() {} });
    assert.match(f.doc.querySelector("#prompt-error").textContent, re);
  }
});

test("AI naming fills only empty fields and steps aside when the dialog or prompt changed", async () => {
  class FakeImage { set src(v) { setImmediate(() => { if (v === "bad") this.onerror(); else { this.naturalWidth = 1600; this.naturalHeight = 900; this.onload(); } }); } }
  globalThis.Image = FakeImage;
  globalThis.createImageBitmap = async () => ({ width: 10, height: 10, close() {} });
  const p = P("a", { tags: ["음식"] });
  const s = await views({ prompts: [p] });
  const $ = (id) => s.doc.querySelector(id);
  s.v.openPromptEditor(null);
  const ai = $("#p-title-ai");
  let focused = false; $("#p-prompt").focus = () => { focused = true; };
  await ai.click();
  assert.equal($("#prompt-auto").textContent, "프롬프트를 먼저 넣어 주세요.");
  assert.equal(focused, true);
  $("#p-prompt").value = "a sunset";
  s.file.files = [{ name: "a", type: "image/png" }];
  await s.file.fire("change"); await flush();
  $("#p-negative").value = "keep";
  s.state.assist = { status: "done", output: JSON.stringify({ title: "노을", tags: ["풍경", "하늘"], negative: "blur", size: "1024×1024" }) };
  s.dialog.open = true;
  await ai.click(); await flush();
  assert.equal($("#p-title").value, "노을");
  assert.equal($("#p-tags").value, "풍경, 하늘");
  assert.equal($("#p-negative").value, "keep");
  assert.equal($("#p-size").value, "1920×1080");
  assert.match(s.assists[0][1], /Prefer existing categories: 음식/);
  assert.match($("#prompt-auto").textContent, /AI가 이름과 빈 칸을 채웠어요/);
  assert.equal(ai.disabled, false);
  // String tags, and a title typed while waiting is kept.
  $("#p-tags").value = ""; $("#p-size").value = "";
  s.state.assist = () => { $("#p-title").value = "내가 쓴 이름"; return { status: "done", output: JSON.stringify({ title: "AI", tags: "a, b", size: "1:1" }) }; };
  await ai.click(); await flush();
  assert.equal($("#p-title").value, "내가 쓴 이름");
  assert.equal($("#p-tags").value, "a, b");
  for (const [assist, re] of [[{ status: "done", output: "{}" }, /이름을 정하지 못했어요/], [{ status: "failed", error: "busy" }, /오류 busy/], [new Error("net"), /AI가 답하지 못했어요 \(net\)/]]) {
    s.state.assist = assist;
    await ai.click(); await flush();
    assert.match($("#prompt-auto").textContent, re);
  }
  s.state.assist = () => { $("#p-prompt").value = "changed"; return { status: "done", output: "{}" }; };
  await ai.click(); await flush();
  assert.match($("#prompt-auto").textContent, /프롬프트가 바뀌어/);
  $("#p-prompt").value = "a sunset";
  s.state.assist = () => { s.dialog.open = false; return { status: "done", output: JSON.stringify({ title: "late" }) }; };
  await ai.click(); await flush();
  assert.notEqual($("#p-title").value, "late");
  // An image that will not load gives no size; the AI's size is used.
  s.dialog.open = true;
  s.v.openPromptEditor(null);
  $("#p-prompt").value = "x";
  globalThis.createImageBitmap = async () => ({ width: 10, height: 10, close() {} });
  s.doc._dataUrl = () => "bad";
  s.file.files = [{ name: "b", type: "image/png" }];
  await s.file.fire("change"); await flush();
  s.dialog.open = true;
  s.state.assist = { status: "done", output: JSON.stringify({ title: "t", size: "512×512" }) };
  await ai.click(); await flush();
  assert.equal($("#p-size").value, "512×512");
});
