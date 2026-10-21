// Build-only dependencies live here, outside Tauri's asset directory.
const fs = require("node:fs"), path = require("node:path"), os = require("node:os"), crypto = require("node:crypto");
const esbuild = require(process.env.ARKISM_ESBUILD_MODULE || "esbuild");
const desktop = path.resolve(__dirname, ".."), ui = path.join(desktop, "ui");
const dest = path.resolve(process.env.ARKISM_UI_OUTPUT || path.join(desktop, "ui-dist"));
const marker = ".arkism-frontend-output";

async function build() {
  if (esbuild.version !== "0.28.2") throw Error("Use the pinned esbuild 0.28.2");
  if (fs.existsSync(dest) && !fs.existsSync(path.join(dest, marker))) throw Error("Refusing to replace an unowned output directory: " + dest);
  const stage = fs.mkdtempSync(path.join(os.tmpdir(), "arkism-frontend-"));
  try {
    fs.writeFileSync(path.join(stage, marker), "arkism-frontend-v1\n");
    const result = await esbuild.build({ entryPoints: [path.join(ui, "entry.mjs")], outdir: path.join(stage, "assets"), entryNames: "entry", chunkNames: "[name]-[hash]", bundle: true, splitting: true, format: "esm", platform: "browser", target: "es2022", minify: true, legalComments: "eof", metafile: true, logLevel: "warning" });
    // Copy product assets only. No tests, source scripts or node_modules ship.
    for (const entry of fs.readdirSync(ui, { withFileTypes: true })) {
      if (entry.isDirectory() && ["fonts", "img"].includes(entry.name) || entry.isFile() && /\.(css|ico|png|svg)$/.test(entry.name)) fs.cpSync(path.join(ui, entry.name), path.join(stage, entry.name), { recursive: true });
    }
    fs.copyFileSync(path.join(ui, "look.js"), path.join(stage, "look.js"));
    fs.writeFileSync(path.join(stage, "index.html"), fs.readFileSync(path.join(ui, "index.html"), "utf8").replace('src="entry.mjs"', 'src="assets/entry.js"'));
    const inputs = Object.keys(result.metafile.inputs).map(name => ({ path: path.relative(desktop, path.resolve(name)).split(path.sep).join("/"), sha256: crypto.createHash("sha256").update(fs.readFileSync(name)).digest("hex") })).sort((a, b) => a.path.localeCompare(b.path));
    fs.writeFileSync(path.join(stage, "build-manifest.json"), JSON.stringify({ esbuild: esbuild.version, inputs, outputBytes: Object.values(result.metafile.outputs).reduce((n, o) => n + o.bytes, 0) }, null, 2) + "\n");
    if (fs.existsSync(dest)) fs.rmSync(dest, { recursive: true });
    fs.mkdirSync(path.dirname(dest), { recursive: true });
    fs.cpSync(stage, dest, { recursive: true });
    console.log(`Frontend: ${inputs.length} module inputs; production assets in ${dest}`);
  } finally { fs.rmSync(stage, { recursive: true, force: true }); }
}
build().catch(error => { console.error(error); process.exitCode = 1; });
