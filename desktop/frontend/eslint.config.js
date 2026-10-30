// Lint for the desktop UI scripts and test tools; run `npm run lint` (eslint runs from desktop/, nothing is bundled).
const js = require("@eslint/js");
const globals = require("globals");
const promise = require("eslint-plugin-promise");

// ask.js sets these on window; the other UI files call them as bare globals.
const app = { ...globals.browser, ask: "readonly", askText: "readonly", askTyped: "readonly" };

module.exports = [
  { ignores: ["ui-dist/**", "frontend/node_modules/**", "src-tauri/**"] },
  { files: ["ui/**/*.{js,mjs}", "tests/*.cjs", "frontend/*.{js,cjs}"], ...js.configs.recommended },
  // A leading underscore marks a name the codebase keeps unused on purpose (`catch (_)`, `(_, i) =>`).
  { files: ["ui/**/*.{js,mjs}", "tests/*.cjs", "frontend/*.{js,cjs}"], rules: { "no-unused-vars": ["error", { argsIgnorePattern: "^_", varsIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_" }] } },
  // Promise misuse in the UI: a rejection nobody handles is a failure the user never sees.
  // A two-callback .then(ok, fail) counts as handled (the UI's usual form); always-return and param-names only judge style.
  { files: ["ui/**/*.{js,mjs}"], ...promise.configs["flat/recommended"] },
  { files: ["ui/**/*.{js,mjs}"], rules: { "promise/catch-or-return": ["error", { allowThen: true }], "promise/always-return": "off", "promise/param-names": "off" } },
  // Classic <script> files share one page; their CommonJS guard lets node tests load them too.
  { files: ["ui/**/*.js"], languageOptions: { sourceType: "script", globals: { ...app, module: "readonly" } } },
  { files: ["ui/**/*.mjs", "ui/app.js", "ui/crew.js"], languageOptions: { sourceType: "module", globals: app } },
  { files: ["ui/**/*.test.js", "ui/testdata/**/*.{js,cjs}", "ui/i18n-extract.js", "tests/*.cjs", "frontend/*.{js,cjs}"], languageOptions: { sourceType: "commonjs", globals: globals.node } },
  { files: ["ui/testdata/preview/*.mjs"], languageOptions: { globals: globals.node } },
  // page.evaluate callbacks run inside the browser page.
  { files: ["tests/*.browser.cjs"], languageOptions: { globals: { ...globals.node, ...globals.browser } } },
];
