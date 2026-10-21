// Node-only helper (not loaded by index.html): pulls every Korean UI string out of desktop/ui
// so i18n.test.js can measure dictionary coverage. A small hand-written lexer, no dependencies.
//   static   — plain "..." / '...' literals and templates without ${}, plus index.html text/attributes
//   template — `...${x}...` literals, returned as a key with {0}, {1}… in place of each ${}
"use strict";
const fs = require("fs");
const path = require("path");

const HANGUL = /[가-힣ㄱ-ㆎ]/;
const REGEX_AFTER_WORD = new Set(["return", "typeof", "case", "in", "of", "new", "delete", "void", "throw", "else", "do", "yield", "await", "instanceof"]);

function decodeEscape(src, i) {
  // src[i] is the char after the backslash; returns [text, nextIndex]
  const c = src[i];
  const simple = { n: "\n", t: "\t", r: "\r", b: "\b", f: "\f", v: "\v", 0: "\0" };
  if (c === "u") {
    if (src[i + 1] === "{") { const end = src.indexOf("}", i); return [String.fromCodePoint(parseInt(src.slice(i + 2, end), 16)), end + 1]; }
    return [String.fromCharCode(parseInt(src.slice(i + 1, i + 5), 16)), i + 5];
  }
  if (c === "x") return [String.fromCharCode(parseInt(src.slice(i + 1, i + 3), 16)), i + 3];
  if (c === "\r" && src[i + 1] === "\n") return ["", i + 2];
  if (c === "\n") return ["", i + 1];
  if (Object.hasOwn(simple, c)) return [simple[c], i + 1];
  return [c, i + 1];
}

// Lex JS source; calls onString(text, line) and onTemplate(quasis[], line).
function lexJS(src, onString, onTemplate) {
  let i = 0;
  let prev = ""; // last significant token class: "" | "word" | "num" | ")" | "]" | "}" | "punct" | "str"
  let prevWord = "";
  const lineAt = (pos) => src.slice(0, pos).split("\n").length;
  const regexAllowed = () => prev === "" || prev === "punct" || (prev === "word" && REGEX_AFTER_WORD.has(prevWord));

  function readString(q) {
    const start = i; i++;
    let out = "";
    while (i < src.length && src[i] !== q) {
      if (src[i] === "\\") { const [t, n] = decodeEscape(src, i + 1); out += t; i = n; }
      else out += src[i++];
    }
    i++;
    onString(out, lineAt(start));
  }
  function readTemplate() {
    const start = i; i++;
    const quasis = [""];
    while (i < src.length && src[i] !== "`") {
      if (src[i] === "\\") { const [t, n] = decodeEscape(src, i + 1); quasis[quasis.length - 1] += t; i = n; }
      else if (src[i] === "$" && src[i + 1] === "{") {
        i += 2;
        lexUntilBrace();
        quasis.push("");
      } else quasis[quasis.length - 1] += src[i++];
    }
    i++;
    onTemplate(quasis, lineAt(start));
  }
  function readRegex() {
    i++;
    let inClass = false;
    while (i < src.length) {
      const c = src[i];
      if (c === "\\") { i += 2; continue; }
      if (c === "[") inClass = true;
      else if (c === "]") inClass = false;
      else if (c === "/" && !inClass) { i++; break; }
      else if (c === "\n") break;
      i++;
    }
    while (/[a-z]/i.test(src[i] || "")) i++;
  }
  // Lex tokens until an unmatched "}" (used for ${...}); at top level depth never goes negative.
  function lexUntilBrace(top = false) {
    let depth = 0;
    prev = "punct";
    while (i < src.length) {
      const c = src[i];
      if (c === "/" && src[i + 1] === "/") { while (i < src.length && src[i] !== "\n") i++; continue; }
      if (c === "/" && src[i + 1] === "*") { const e = src.indexOf("*/", i + 2); i = e < 0 ? src.length : e + 2; continue; }
      if (/\s/.test(c)) { i++; continue; }
      if (c === '"' || c === "'") { readString(c); prev = "str"; continue; }
      if (c === "`") { readTemplate(); prev = "str"; continue; }
      if (c === "/") {
        if (regexAllowed()) { readRegex(); prev = "str"; } else { i++; prev = "punct"; }
        continue;
      }
      if (/[A-Za-z_$À-￿]/.test(c)) {
        let w = "";
        while (i < src.length && /[\w$À-￿]/.test(src[i])) w += src[i++];
        prev = "word"; prevWord = w; continue;
      }
      if (/\d/.test(c)) { while (/[\w.]/.test(src[i] || "")) i++; prev = "num"; continue; }
      if (c === "{") { depth++; i++; prev = "punct"; continue; }
      if (c === "}") {
        if (depth === 0 && !top) { i++; return; }
        depth--; i++; prev = "}"; continue;
      }
      if (c === ")" || c === "]") { prev = c; i++; continue; }
      i++; prev = "punct";
    }
  }
  lexUntilBrace(true);
}

function collapse(s) { return s.replace(/\s+/g, " ").trim(); }

function extractHTML(src, add) {
  // Repeat until nothing changes, so a removal can't join pieces into a new block.
  let body = src, prev;
  do {
    prev = body;
    body = body.replace(/<(svg|script|style)\b[\s\S]*?<\/\1\b[^>]*>/gi, "");
  } while (body !== prev);
  for (const m of body.matchAll(/>([^<>]+)</g)) {
    const t = collapse(m[1].replace(/&nbsp;/g, " ").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&"));
    if (HANGUL.test(t)) add(t);
  }
  for (const m of body.matchAll(/\s(?:title|placeholder|aria-label|alt|label|content)="([^"]*)"/g)) {
    const t = collapse(m[1].replace(/&amp;/g, "&"));
    if (HANGUL.test(t)) add(t);
  }
}

// Returns { statics: Map<text, [file:line]>, templates: Map<key, [file:line]> }
function extract(dir = __dirname) {
  const statics = new Map();
  const templates = new Map();
  const put = (map, k, where) => { if (!map.has(k)) map.set(k, []); map.get(k).push(where); };
  const files = [
    ...fs.readdirSync(dir).filter((f) => /\.(js|mjs)$/.test(f) && !f.endsWith(".test.js") && !f.startsWith("i18n")),
    ...(fs.existsSync(path.join(dir, "modules")) ? fs.readdirSync(path.join(dir, "modules")).filter(f => f.endsWith(".mjs")).map(f => "modules/" + f) : [])
  ].sort();
  for (const f of files) {
    const src = fs.readFileSync(path.join(dir, f), "utf8");
    lexJS(src,
      (s, line) => { const t = s.trim(); if (HANGUL.test(t)) put(statics, t, `${f}:${line}`); },
      (quasis, line) => {
        if (!quasis.some((q) => HANGUL.test(q))) return;
        if (quasis.length === 1) { const t = quasis[0].trim(); put(statics, t, `${f}:${line}`); return; }
        const key = quasis.map((q, n) => (n ? `{${n - 1}}` : "") + q).join("").trim();
        put(templates, key, `${f}:${line}`);
      });
  }
  const html = path.join(dir, "index.html");
  if (fs.existsSync(html)) extractHTML(fs.readFileSync(html, "utf8"), (t) => put(statics, t, "index.html"));
  return { statics, templates };
}

module.exports = { extract, lexJS, HANGUL, collapse };

if (require.main === module) {
  const { statics, templates } = extract(process.argv[2] || __dirname);
  process.stdout.write(JSON.stringify({ statics: [...statics.keys()], templates: [...templates.keys()] }, null, 1));
}
