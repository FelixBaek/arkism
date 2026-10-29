// Stylelint for the desktop UI stylesheets; `npm run lint` runs it after eslint. Only rules that catch real mistakes, no formatting.
module.exports = {
  rules: {
    "no-duplicate-selectors": true,
    "declaration-block-no-duplicate-properties": [true, { ignore: ["consecutive-duplicates-with-different-syntaxes"] }],
  },
};
