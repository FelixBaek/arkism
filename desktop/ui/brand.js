// Brand marks for models, providers, messengers and MCP servers (img/brand: LobeHub icons + Phosphor logos, both MIT), and what
// kind of input a model takes. One place, so every picker and row shows the same logo for the same name.
(() => {
  // [pattern on a model / provider / messenger name, file, coloured?]
  const MARKS = [
    [/^(anthropic|claude|opus|sonnet|haiku|fable)/, "claude-color", true],
    [/^(openai|gpt|o\d|codex|chatgpt)/, "openai", false],
    [/^deepseek/, "deepseek-color", true],
    [/^(glm|zhipu|chatglm)/, "zhipu-color", true],
    [/^(z\.?ai|zcode)/, "zai", false],
    [/^(gemini|google|antigravity)/, "gemini-color", true],
    [/^(grok|xai)/, "grok", false],
    [/^(qwen|alibaba)/, "qwen-color", true],
    [/^(kimi|moonshot)/, "kimi-color", true],
    [/^(mistral|codestral|pixtral)/, "mistral-color", true],
    [/^minimax/, "minimax-color", true],
    [/^ollama/, "ollama", false],
    [/^openrouter/, "openrouter", false],
    [/^(meta|llama)/, "meta-color", true],
    [/^(nous|hermes)/, "nousresearch", false],
    [/^opencode/, "opencode", false],
    [/^(discord|디스코드)/, "discord-color", true],
    [/^(telegram|텔레그램)/, "telegram-color", true],
    [/^(slack|슬랙)/, "slack-logo", false],
    [/^github(?!.*copilot)/, "github", false],
    [/^figma/, "figma-color", true],
    [/^notion/, "notion", false],
    [/^cloudflare/, "cloudflare-color", true]
  ];
  const find = (name) => {
    const n = String(name || "").trim().toLowerCase().replace(/^antigravity-/, "");
    return MARKS.find(([rx]) => rx.test(n)) || null;
  };
  // An element for the name's logo (16 px by CSS), or null when there is none. Single-colour logos are masks over
  // currentColor so they follow the theme; coloured ones are plain images.
  function mark(name, cls = "brand-mark") {
    const n = String(name || "").trim().toLowerCase();
    if (/^(arkism|arkism 앱)$/.test(n)) { const img = document.createElement("img"); img.className = cls; img.src = "img/arkism-triangle.svg"; img.alt = ""; return img; }
    const hit = find(n);
    if (!hit) return null;
    const [, file, colored] = hit;
    if (colored) { const img = document.createElement("img"); img.className = cls; img.src = `img/brand/${file}.svg`; img.alt = ""; return img; }
    const s = document.createElement("span");
    s.className = `${cls} brand-mono`;
    s.style.setProperty("--brand", `url("img/brand/${file}.svg")`);
    s.setAttribute("aria-hidden", "true");
    return s;
  }
  // shortcut: the provider list has no input kinds, so they come from the model name; move to the server's model
  // list once it reports them. Unknown names count as text only, so the 이미지 모델 row still shows for them.
  const VISION = [/^(claude|opus|sonnet|haiku|fable|gemini|antigravity-gemini|grok|kimi|llava|pixtral)/, /^gpt-?[4-9]/, /^o\d/, /vision|-vl\b|vl-/, /glm-?[\d.]+-?v\b/];
  function kind(model) {
    const m = String(model || "").trim().toLowerCase();
    if (!m || m === "auto") return "";
    if (/embed|bge-|^e5-|nomic/.test(m)) return "embed";
    if (VISION.some((r) => r.test(m))) return "vision";
    return "text";
  }
  const KIND_LABEL = { text: "텍스트 전용", vision: "비전", embed: "임베딩" };
  const kindLabel = (model) => KIND_LABEL[kind(model)] || "";
  window.ArkismBrand = { mark, kind, kindLabel };
})();
