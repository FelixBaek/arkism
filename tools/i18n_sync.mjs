#!/usr/bin/env node
// Keeps the desktop UI dictionaries (desktop/ui/i18n-<code>.js) in step with the Korean source. No dependencies.
//
//   node tools/i18n_sync.mjs                    report what is missing or stale per language (exit 1 if anything is)
//   node tools/i18n_sync.mjs --todo FILE        write the missing keys as a JSON file to translate
//   node tools/i18n_sync.mjs --apply FILE       merge translations from FILE ({ "<code>": { "strings": {ko: text}, "patterns": {...} } })
//   node tools/i18n_sync.mjs --fallback         fill every key still missing in a non-English language with the English text,
//                                               listed under "fallback" in that dictionary (see guide/development.md)
//
// English is the reference and is never filled automatically: a new Korean string needs its English first.
// Writing rewrites the non-English files in English key order and drops keys English no longer has;
// new English entries are appended as one Object.assign block, like the hand-written ones.
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const UI = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "desktop", "ui");
const require = createRequire(import.meta.url);
const { extract, stale, HANGUL } = require(path.join(UI, "i18n-extract.js"));
const { DICTIONARIES } = require(path.join(UI, "i18n.js"));
const KINDS = ["strings", "patterns"];
const placeholders = (s) => [...s.matchAll(/\{(\d+)\}/g)].map((m) => Number(m[1])).sort((x, y) => x - y).join(",");
const file = (code) => path.join(UI, `i18n-${code}.js`);

export function load(codes = DICTIONARIES) {
  const sandbox = { window: {} };
  for (const code of codes) vm.runInNewContext(fs.readFileSync(file(code), "utf8"), sandbox, { filename: file(code) });
  return sandbox.window.ArkismI18nDicts;
}

// What each language lacks. English is measured against the extracted Korean, the others against English.
export function report(dicts, source = extract(UI)) {
  const en = dicts.en, out = {};
  out.en = { missing: { strings: [...source.statics.keys()].filter((k) => !Object.hasOwn(en.strings, k)),
    patterns: [...source.templates.keys()].filter((k) => !Object.hasOwn(en.patterns, k)) }, extra: stale(en, UI), fallback: 0 };
  for (const code of Object.keys(dicts).filter((c) => c !== "en")) {
    const d = dicts[code], r = { missing: {}, extra: {}, fallback: (d.fallback || []).length };
    for (const kind of KINDS) {
      r.missing[kind] = Object.keys(en[kind]).filter((k) => !Object.hasOwn(d[kind], k));
      r.extra[kind] = Object.keys(d[kind]).filter((k) => !Object.hasOwn(en[kind], k));
    }
    out[code] = r;
  }
  return out;
}

// One translation is usable when it keeps the placeholders and has no Korean left.
export function problem(ko, text) {
  if (typeof text !== "string" || !text.trim()) return "empty";
  if (HANGUL.test(text)) return "still has Korean";
  if (placeholders(ko) !== placeholders(text)) return `placeholders ${placeholders(ko) || "none"} expected`;
  return "";
}

// Merges translations and, with fallback, English for the rest. Returns the dictionaries to write and the rejected entries.
export function merge(dicts, given = {}, { fallback = false } = {}) {
  const en = dicts.en, rejected = [], next = {};
  const addEN = { strings: {}, patterns: {} };
  for (const kind of KINDS) for (const [ko, text] of Object.entries(given.en?.[kind] || {})) {
    if (Object.hasOwn(en[kind], ko)) continue;
    const why = problem(ko, text);
    if (why) rejected.push(`en ${ko}: ${why}`); else { addEN[kind][ko] = text; en[kind][ko] = text; }
  }
  for (const code of Object.keys(dicts).filter((c) => c !== "en")) {
    const d = dicts[code], fb = new Set(d.fallback || []), out = { strings: {}, patterns: {} };
    for (const kind of KINDS) for (const ko of Object.keys(en[kind])) {
      const offered = given[code]?.[kind]?.[ko];
      const why = offered === undefined ? "missing" : problem(ko, offered);
      if (!why) { out[kind][ko] = offered; fb.delete(ko); continue; }
      if (offered !== undefined) rejected.push(`${code} ${ko}: ${why}`);
      if (Object.hasOwn(d[kind], ko)) out[kind][ko] = fb.has(ko) ? en[kind][ko] : d[kind][ko]; // a fallback follows English edits
      else if (fallback) { out[kind][ko] = en[kind][ko]; fb.add(ko); }
    }
    out.fallback = [...fb].filter((ko) => KINDS.some((kind) => Object.hasOwn(out[kind], ko)));
    next[code] = out;
  }
  return { next, addEN, rejected };
}

const j = (x) => JSON.stringify(x);
export function render(code, d, language) {
  const body = KINDS.map((kind) => ` "${kind}": {\n` + Object.entries(d[kind]).map(([k, v]) => ` ${j(k)}: ${j(v)}`).join(",\n") + "\n }");
  if (d.fallback.length) body.push(` "fallback": [\n` + d.fallback.map((k) => ` ${j(k)}`).join(",\n") + "\n ]");
  return `// ${language} UI dictionary for i18n.js: the same keys as i18n-en.js (Korean source → ${language}).\n`
    + "// i18n.test.js checks it has every key with the same placeholders; keep it in step with node tools/i18n_sync.mjs.\n"
    + `(window.ArkismI18nDicts ||= {})["${code}"] = {\n` + body.join(",\n") + "\n};\n";
}
const languageName = (code) => /^\/\/ (.+?) UI dictionary/.exec(fs.readFileSync(file(code), "utf8"))?.[1] || code;
function appendEnglish(addEN) {
  let text = "";
  for (const kind of KINDS) if (Object.keys(addEN[kind]).length)
    text += `Object.assign(window.ArkismI18nEN.${kind}, {\n` + Object.entries(addEN[kind]).map(([k, v]) => `  ${j(k)}: ${j(v)}`).join(",\n") + " });\n";
  if (text) fs.appendFileSync(file("en"), text);
}

function todo(dicts, rep) {
  const out = {};
  for (const [code, r] of Object.entries(rep)) for (const kind of KINDS) for (const ko of r.missing[kind]) {
    ((out[code] ||= {})[kind] ||= {})[ko] = code === "en" ? "" : dicts.en[kind][ko]; // English shown as the meaning to translate
  }
  for (const code of Object.keys(dicts).filter((c) => c !== "en")) for (const ko of dicts[code].fallback || []) {
    const kind = Object.hasOwn(dicts.en.strings, ko) ? "strings" : "patterns";
    ((out[code] ||= {})[kind] ||= {})[ko] = dicts.en[kind][ko];
  }
  return out;
}

function main(argv) {
  const arg = (name) => { const i = argv.indexOf(name); return i < 0 ? null : argv[i + 1]; };
  const dicts = load();
  let rep = report(dicts);
  if (argv.includes("--fallback") || arg("--apply")) {
    const given = arg("--apply") ? JSON.parse(fs.readFileSync(arg("--apply"), "utf8")) : {};
    const { next, addEN, rejected } = merge(dicts, given, { fallback: argv.includes("--fallback") });
    for (const r of rejected) console.error("rejected: " + r);
    appendEnglish(addEN);
    for (const [code, d] of Object.entries(next)) {
      const text = render(code, d, languageName(code));
      if (text !== fs.readFileSync(file(code), "utf8")) { fs.writeFileSync(file(code), text); console.log(`wrote i18n-${code}.js`); }
    }
    rep = report(load());
  }
  if (arg("--todo")) { fs.writeFileSync(arg("--todo"), JSON.stringify(todo(load(), rep), null, 1) + "\n"); console.log(`wrote ${arg("--todo")}`); }
  let bad = 0;
  for (const [code, r] of Object.entries(rep)) {
    const n = (o) => o.strings.length + o.patterns.length, miss = n(r.missing), extra = n(r.extra);
    bad += miss + extra;
    if (miss || extra || r.fallback) console.log(`${code}: ${miss} missing, ${extra} stale, ${r.fallback} English fallback`);
    for (const kind of KINDS) for (const k of [...r.missing[kind].slice(0, 5)]) console.log(`  missing ${kind}: ${k}`);
  }
  if (!bad) console.log("All UI languages cover every Korean string.");
  return bad ? 1 : 0;
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) process.exitCode = main(process.argv.slice(2));
