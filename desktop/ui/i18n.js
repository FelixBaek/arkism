// 화면 언어: 한국어 원문 그대로 두거나 영어로 바꿔 보여 준다.
// 화면 코드는 한국어 문구를 그대로 쓰고, 영어일 때만 이 파일이 DOM에 들어간 글자를 사전(i18n-en.js)으로 바꾼다.
//   - 정확한 문장 사전 + `{0}` 자리 표시가 있는 패턴(템플릿 문자열에서 뽑음)
//   - MutationObserver가 새로 그려진 글자·title·placeholder·aria-label을 한 번씩 바꾸고 원문을 기억해 한국어로 되돌릴 수 있다
//   - data-no-i18n이 붙은 요소 아래(기억 본문·스킬 설명 같은 사용자 데이터)와 code·textarea·편집 칸은 건드리지 않는다
//   - confirm·alert·prompt 문구도 바꾼다
// 언어 선택은 look.js(설정 › 모양)가 저장한다. 저장한 값이 없으면 navigator.language가 ko로 시작할 때만 한국어.
(function (root) {
  "use strict";
  const HANGUL = /[가-힣ㄱ-ㆎ]/;
  const collapse = (s) => s.replace(/\s+/g, " ").trim();
  const PARTICLE = /[을를이가은는에의로으와과도만까께한처보부랑예요]/;
  const escapeRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

  function compile(dict) {
    const exact = new Map();
    const loose = new Map();
    for (const [k, v] of Object.entries(dict.strings || {})) {
      if (typeof v !== "string" || !v) continue;
      exact.set(k, v);
      const c = collapse(k);
      if (!loose.has(c)) loose.set(c, v);
      // "· 지원 안 함"처럼 구분자로 시작하는 문구는 구분자 없이도 찾는다 (줄을 " · "로 나눠 번역할 때).
      const bare = /^[·—]\s+/;
      if (bare.test(c) && bare.test(v) && !loose.has(c.replace(bare, ""))) loose.set(c.replace(bare, ""), v.replace(bare, ""));
    }
    const patterns = [];
    const entries = Object.entries(dict.patterns || {});
    for (const [k, v] of [...entries]) if (/^[·—] /.test(k) && /^[·—] /.test(v) && !(dict.patterns || {})[k.slice(2)]) entries.push([k.slice(2), v.slice(2)]);
    for (const [k, v] of entries) {
      if (typeof v !== "string" || !v) continue;
      const parts = k.split(/\{(\d+)\}/);
      let src = "^";
      const order = [];
      const edges = []; // per capture: [Hangul right before it, Hangul right after it] in the pattern (no space between)
      let literal = 0;
      let need = "";
      parts.forEach((p, n) => {
        if (n % 2) {
          src += "([\\s\\S]*?)"; order.push(Number(p));
          const before = parts[n - 1].slice(-1), after = (parts[n + 1] || "").slice(0, 1);
          edges.push([HANGUL.test(before), HANGUL.test(after) && !PARTICLE.test(after)]);
          return;
        }
        const lit = collapse(p);
        literal += lit.replace(/\s/g, "").length;
        if (lit.length > need.length) need = lit;
        src += p.split(/\s+/).map(escapeRe).join("\\s+");
      });
      if (!HANGUL.test(k)) continue;
      // 패턴에 " · "가 있으면 자리 표시에는 " · "가 들어가지 않는다: "{0} · {1}명"이 "A · 6단계 · 협업 워커 5명"을 엉뚱하게 자르지 않게.
      const lits = parts.filter((_, n) => n % 2 === 0).join("\u0000");
      const seps = [" · ", " — ", " / "].filter((x) => lits.includes(x));
      const hl = (lits.match(/[가-힣]/g) || []).length;
      patterns.push({ re: new RegExp(src + "$"), order, edges, seps, hl, en: v, literal, need });
    }
    // 글자가 많이 고정된 패턴부터: "{0}개"보다 "스킬 {0}개를 옮길까요?"가 먼저.
    patterns.sort((a, b) => b.literal - a.literal);
    return { exact, loose, patterns };
  }

  // A capture must hold a whole word: "{0}건" may match "3건" but not "조건", "{0}개{1}" not "개선".
  // Before a Korean particle (를·에서·로…) a word may end in Hangul ("디자이너를"); before a unit (개·번·시…) it may not.
  // Empty captures are fine ("{0} 빠짐{1}" for "code-review 빠짐").
  function fits(p, m) {
    return p.edges.every(([before, unit], n) => {
      const c = m[n + 1];
      if (!c) return true;
      if (p.seps.some((x) => c.includes(x))) return false;
      if (before && HANGUL.test(c[0])) return false;
      if (unit && /[\s가-힣ㄱ-ㆎ]/.test(c[c.length - 1])) return false;
      return true;
    });
  }

  const SEPARATORS = ["\n", " · ", " → ", " — ", " - ", " / ", ": ", " ("];

  function makeTranslator(dict) {
    const { exact, loose, patterns } = compile(dict);
    const cache = new Map();
    function fromPattern(core, depth) {
      const flat = collapse(core);
      let first = null;
      for (const p of patterns) {
        if (p.need && !flat.includes(p.need)) continue;
        const m = p.re.exec(core);
        if (!m || !fits(p, m)) continue;
        const caps = [];
        p.order.forEach((idx, n) => { caps[idx] = depth < 3 ? whole(m[n + 1], depth + 1) : m[n + 1]; });
        // 번역 못 한 한국어 자리 표시가 남으면: 한 글자짜리 패턴("{0}건")이거나 자리 표시가 " · "로 이어진 여러 말이면 버린다.
        if (caps.some((c) => HANGUL.test(c) && (p.hl < 2 || /\s[·—/]\s|: /.test(c)))) continue;
        const r = p.en.replace(/\{(\d+)\}/g, (_, i) => caps[Number(i)] ?? "");
        // 한글이 남으면 다음 패턴도 본다: "{0} · {1}명"보다 조각마다 맞는 패턴이 나을 수 있다.
        if (!HANGUL.test(r)) return r;
        if (first === null) first = r;
      }
      return first;
    }
    // "빠진 곳 있음 10"처럼 끝에 숫자만 붙은 말
    function fromNumber(core, depth) {
      const m = /^(.*?[^\s\d.,])\s+(\d[\d.,]*[%KkM]?)$/.exec(core);
      if (!m || depth >= 3) return null;
      const head = whole(m[1], depth + 1);
      return head === m[1] ? null : head + " " + m[2];
    }
    // 자리 표시·조각은 끝까지 번역될 때만 바꾼다. 반쯤 바뀐 문장(대개 사용자 데이터)은 원문 그대로 둔다.
    const whole = (x, depth) => { const t = tr(x, depth); return HANGUL.test(t) ? x : t; };
    const hangulCount = (x) => (x == null ? Infinity : (x.match(/[가-힣ㄱ-ㆎ]/g) || []).length);
    function fromParts(core, depth) {
      if (depth >= 3) return null;
      for (const sep of SEPARATORS) {
        if (!core.includes(sep)) continue;
        const bits = core.split(sep);
        // " (" 뒤의 닫는 괄호는 마지막 조각과 함께 다룬다.
        const one = (b, n) => (sep === " (" && n > 0 && b.endsWith(")") ? "(" + whole(b.slice(0, -1), depth + 1) + ")" : whole(b, depth + 1));
        const out = [];
        // 조각을 앞에서부터 가장 길게 묶어 번역한다: "A에서 써요 · 누르면 뺄 예정 · PC 연결 안 됨"은 앞 두 조각이 한 문장.
        // 한 조각으로 안 되는 것만 뒤 조각과 묶어 본다.
        for (let i = 0; i < bits.length;) {
          const single = one(bits[i], i);
          let j = i + 1;
          if (HANGUL.test(single)) {
            for (j = bits.length - (i === 0 ? 1 : 0); j > i + 1; j--) {
              const run = bits.slice(i, j).join(sep);
              const t = tr(run, depth + 1);
              if (t !== run && !HANGUL.test(t)) { out.push(t); break; }
            }
          }
          if (j === i + 1) out.push(single);
          i = j;
        }
        const joined = out.map((b, n) => (sep === " (" && n > 0 && b.startsWith("(") ? b.slice(1) : b)).join(sep);
        if (joined !== core) return joined;
      }
      return null;
    }
    function tr(s, depth = 0) {
      if (typeof s !== "string" || !HANGUL.test(s)) return s;
      const key = depth + "\u0000" + s; // 깊이마다 따로: 깊은 곳에서는 덜 번역된다
      const hit = cache.get(key);
      if (hit !== undefined) return hit;
      const lead = /^\s*/.exec(s)[0];
      const rest = s.slice(lead.length), trail = rest.slice(rest.trimEnd().length);
      const core = s.slice(lead.length, s.length - trail.length);
      let r = exact.get(core) ?? loose.get(collapse(core));
      if (r == null) {
        const a = fromPattern(core, depth);
        const b = a != null && !HANGUL.test(a) ? null : fromParts(core, depth) ?? fromNumber(core, depth);
        r = b != null && hangulCount(b) <= hangulCount(a) ? b : a;
      }
      r = r == null ? s : lead + r + trail;
      if (cache.size > 20000) cache.clear();
      cache.set(key, r);
      return r;
    }
    return { translate: (s) => tr(s, 0), patterns, exact };
  }

  // Node(테스트)에서는 번역기만 쓴다.
  if (typeof window === "undefined" && typeof module !== "undefined" && module.exports) {
    module.exports = { makeTranslator, compile, HANGUL };
    if (typeof document === "undefined") return;
  }

  let translator = makeTranslator({});
  const translate = (text) => translator.translate(text);
  let dictionaryTask = null;
  let languageSequence = 0;
  async function englishDictionary() {
    if (!dictionaryTask) dictionaryTask = import("./i18n-en.js").then(() => {
      translator = makeTranslator(root.ArkismI18nEN);
    }).catch((error) => { dictionaryTask = null; throw error; });
    return dictionaryTask;
  }
  const ATTRS = ["title", "placeholder", "aria-label"];
  const FIELD = /^(INPUT|TEXTAREA|SELECT)$/;
  const SKIP = "[data-i18n-id],[data-no-i18n],script,style,code,textarea,[contenteditable=''],[contenteditable='true']";
  const textOrig = new WeakMap(); // Text → { o: 원문, t: 번역 }
  const attrOrig = new WeakMap(); // Element → { [attr]: { o, t } }
  let lang = "ko";

  const skipped = (el) => !el || !!el.closest(SKIP);

  function doText(node) {
    const cur = node.data;
    const rec = textOrig.get(node);
    if (rec && cur === rec.t) return;
    if (!HANGUL.test(cur) || skipped(node.parentElement)) return;
    const t = translate(cur);
    textOrig.set(node, { o: cur, t });
    if (t !== cur) node.data = t;
  }
  function doAttr(el, name) {
    const cur = el.getAttribute(name);
    if (cur == null) return;
    let recs = attrOrig.get(el);
    const rec = recs && recs[name];
    if (rec && cur === rec.t) return;
    // textarea·code의 placeholder·title은 번역한다. 사용자 값이 든 입력 칸(raw)도 값만 그대로 두고 placeholder·aria-label은 화면 문구다.
    if (!HANGUL.test(cur) || (el.closest("[data-no-i18n]") && !FIELD.test(el.tagName))) return;
    const t = translate(cur);
    if (!recs) attrOrig.set(el, (recs = {}));
    recs[name] = { o: cur, t };
    if (t !== cur) el.setAttribute(name, t);
  }
  function walk(start, fn) {
    if (start.nodeType === 3) { fn(start); return; }
    if (start.nodeType !== 1 && start.nodeType !== 9 && start.nodeType !== 11) return;
    if (start.nodeType === 1) fn(start);
    if (start.nodeType === 1 && start.matches(SKIP)) return;
    const w = document.createTreeWalker(start, 5 /* ELEMENT | TEXT */, {
      // 건너뛸 요소는 자기 속성만 다루고 안쪽 글자는 그대로 둔다.
      acceptNode: (n) => (n.nodeType === 1 && n.matches(SKIP) ? (fn(n), 2 /* REJECT */) : 1)
    });
    for (let n = w.nextNode(); n; n = w.nextNode()) fn(n);
  }
  function apply(node) {
    if (node.nodeType === 3) doText(node);
    else for (const a of ATTRS) if (node.hasAttribute(a)) doAttr(node, a);
  }
  function restore(node) {
    if (node.nodeType === 3) {
      const rec = textOrig.get(node);
      if (rec && node.data === rec.t) node.data = rec.o;
      textOrig.delete(node);
      return;
    }
    const recs = attrOrig.get(node);
    if (!recs) return;
    for (const [a, rec] of Object.entries(recs)) if (node.getAttribute(a) === rec.t) node.setAttribute(a, rec.o);
    attrOrig.delete(node);
  }

  const observer = new MutationObserver((records) => {
    for (const r of records) {
      if (r.type === "childList") for (const n of r.addedNodes) walk(n, apply);
      else if (r.type === "characterData") doText(r.target);
      else if (r.type === "attributes") doAttr(r.target, r.attributeName);
    }
  });

  // Stable message IDs are used by the shell; legacy screens retain the DOM adapter.
  const messages = new Map();
  function registerMessages(entries) {
    for (const [id, original] of Object.entries(entries)) {
      if (messages.has(id) && messages.get(id) !== original) throw new Error("Duplicate message ID: " + id);
      messages.set(id, original);
    }
  }
  function bindMessage(node, id) {
    if (!messages.has(id)) throw new Error("Unknown message ID: " + id);
    node.setAttribute("data-i18n-id", id);
    node.textContent = lang === "en" ? translate(messages.get(id)) : messages.get(id);
    return node;
  }
  function updateMessages() {
    for (const node of document.querySelectorAll("[data-i18n-id]")) bindMessage(node, node.getAttribute("data-i18n-id"));
  }

  let titleOrig = null;
  async function setLang(next) {
    next = next === "en" ? "en" : "ko";
    const sequence = ++languageSequence;
    if (next === "en") {
      try { await englishDictionary(); }
      catch (error) { if (sequence === languageSequence) document.documentElement.lang = lang; console.warn("English dictionary unavailable", error); return false; }
      if (sequence !== languageSequence) return false;
    }
    if (next === lang) return true;
    lang = next;
    document.documentElement.lang = lang;
    if (lang === "en") {
      walk(document.documentElement, apply);
      if (HANGUL.test(document.title)) { titleOrig = document.title; document.title = translate(document.title); }
      observer.observe(document.documentElement, { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ATTRS });
    } else {
      observer.disconnect();
      walk(document.documentElement, restore);
      if (titleOrig) { document.title = titleOrig; titleOrig = null; }
    }
    updateMessages();
  }

  // 대화 상자 문구
  for (const name of ["confirm", "alert", "prompt"]) {
    const native = root[name];
    if (typeof native !== "function") continue;
    root[name] = function (message, ...rest) {
      return native.call(root, lang === "en" && typeof message === "string" ? translate(message) : message, ...rest);
    };
  }

  const wanted = () => (root.ArkismLook && root.ArkismLook.get().lang) || "ko";
  root.addEventListener("arkism-look", () => { void setLang(wanted()); });
  const initialLanguage = setLang(wanted());

  // 화면 코드가 직접 쓰는 것: translate(s)는 data-no-i18n 안에 넣을 화면 문구용, locale()은 날짜·시각 형식용.
  root.ArkismI18n = { ready: () => initialLanguage, registerMessages, bindMessage, translate: (s) => (lang === "en" ? translate(s) : s), lang: () => lang, locale: () => (lang === "en" ? "en-US" : "ko-KR"), setLang };
})(typeof window !== "undefined" ? window : globalThis);
