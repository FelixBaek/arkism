// Explicit allowlists keep credentials out of exports. Skill export contains reviewed drafts.
window.ArkismPreferenceTransfer = {
  export(read) {
    const values = {};
    for (const key of ["look", "designGroup", "skillSort", "skillDir", "gitHost", "gitRepo", "routing.policyOpen"]) {
      const value = read("arkism." + key);
      if (value !== null && value !== undefined) values[key] = key === "routing.policyOpen" || key === "look" ? JSON.parse(value) : value;
    }
    const prefs = JSON.parse(read("arkism.prefs") || "{}");
    for (const key of ["agentsOff", "categories", "comfyUrl"]) if (Object.hasOwn(prefs, key)) values[key] = prefs[key];
    return JSON.stringify({ product: "arkism-preferences", version: 1, values }, null, 2);
  },
  exportSkills(read, identity) {
    const envelope = JSON.parse(this.exportMemory(() => null, identity));
    const saved = JSON.parse(read("arkism.skillSuggest") || '{"items":[],"dismissed":[]}');
    const similar = JSON.parse(read("arkism.similarDismissed") || "[]");
    const name = v => typeof v === "string" && /^[a-z0-9][a-z0-9-]{0,63}$/.test(v);
    const text = (v, max) => { if (typeof v !== "string" || v.length > max) throw new Error("invalid_draft"); return v; };
    if (!saved || !Array.isArray(saved.items) || saved.items.length > 3 || !Array.isArray(saved.dismissed) || saved.dismissed.length > 5000 || saved.dismissed.some(v => !name(v))) throw new Error("invalid_suggestions");
    if (!Array.isArray(similar) || similar.length > 5000 || similar.some(v => typeof v !== "string" || !v || v.length > 513 || /\p{Cc}/u.test(v))) throw new Error("invalid_similar");
    const items = saved.items.map(item => {
      if (!item || !name(item.name) || typeof item.skill_md !== "string" || !item.skill_md.trim()) throw new Error("invalid_draft");
      if (item.examples !== undefined && (!Array.isArray(item.examples) || item.examples.some(v => typeof v !== "string"))) throw new Error("invalid_examples");
      return { name: item.name, title: text(item.title ?? "", 2000), why: text(item.why ?? "", 10000), skill_md: text(item.skill_md, 20000), examples: text((item.examples || []).join("\n"), 20000) };
    });
    envelope.product = "arkism-skill-preferences";
    envelope.values = { items, dismissed: [...new Set(saved.dismissed)], similarDismissed: [...new Set(similar)] };
    const output = JSON.stringify(envelope, null, 2);
    if (new TextEncoder().encode(output).length > 1048576) throw new Error("file_too_large");
    return output;
  },
  exportMemory(read, identity) {
    const keys = ["workspace_id", "project_id", "user_id", "agent_id"];
    if (!identity || identity.role !== "human" || typeof identity.principal_id !== "string" || !identity.principal_id || !identity.scope || typeof identity.server !== "string") throw new Error("identity_required");
    const scope = Object.fromEntries(keys.map(k => [k, identity.scope[k] || ""]));
    if (Object.values(scope).some(v => typeof v !== "string")) throw new Error("invalid_scope");
    const prefs = JSON.parse(read("arkism.prefs") || "{}");
    const kept = JSON.parse(read("arkism.keptMemories") || "[]");
    if (!Array.isArray(kept) || kept.length > 5000 || kept.some(id => typeof id !== "string" || !id || id === "." || id === ".." || id.length > 256 || /\p{Cc}/u.test(id))) throw new Error("invalid_kept");
    const text = JSON.stringify({ product: "arkism-memory-preferences", version: 1, source: "legacy-unscoped", server: identity.server,
      principal: identity.principal_id, scope, values: { collect: prefs.collectUnused !== false, kept: [...new Set(kept)] } }, null, 2);
    if (new TextEncoder().encode(text).length > 262144) throw new Error("file_too_large");
    return text;
  },
  // Imports accept only what the matching export writes, check every value, and change nothing on any error.
  import(text, read, write) {
    const data = JSON.parse(text);
    if (!data || data.product !== "arkism-preferences" || data.version !== 1 || !data.values || typeof data.values !== "object") throw new Error("not_preferences");
    const v = data.values, out = {}, short = (x, max = 256) => typeof x === "string" && x.length <= max && !/\p{Cc}/u.test(x);
    if (v.look !== undefined) {
      const fonts = ["sm", "md", "lg", "xl"], zoom = Number(v.look?.zoom);
      if (!["system", "dark", "light"].includes(v.look?.theme) || !fonts.includes(v.look?.font) || !(zoom >= 0.7 && zoom <= 1.6)) throw new Error("invalid_look");
      out["arkism.look"] = JSON.stringify({ theme: v.look.theme, font: v.look.font, zoom });
    }
    if (v.designGroup !== undefined) { if (!["product", "purpose"].includes(v.designGroup)) throw new Error("invalid_design_group"); out["arkism.designGroup"] = v.designGroup; }
    for (const key of ["skillSort", "skillDir", "gitHost", "gitRepo"]) {
      if (v[key] === undefined) continue;
      if (!short(v[key])) throw new Error("invalid_" + key);
      out["arkism." + key] = v[key];
    }
    if (v["routing.policyOpen"] !== undefined) {
      const o = v["routing.policyOpen"];
      if (!o || typeof o !== "object" || Object.values(o).some(x => typeof x !== "boolean")) throw new Error("invalid_policy_open");
      out["arkism.routing.policyOpen"] = JSON.stringify({ routing: !!o.routing, failover: !!o.failover });
    }
    const prefs = JSON.parse(read("arkism.prefs") || "{}");
    if (v.agentsOff !== undefined) {
      if (!Array.isArray(v.agentsOff) || v.agentsOff.length > 64 || v.agentsOff.some(x => !short(x, 64))) throw new Error("invalid_agents");
      prefs.agentsOff = [...new Set(v.agentsOff)];
    }
    if (v.categories !== undefined) {
      const okRule = r => Array.isArray(r) && r.length === 2 && ["name", "path", "desc"].includes(r[0]) && short(r[1], 200);
      if (!Array.isArray(v.categories) || v.categories.length > 64 || v.categories.some(c => !c || !short(c.id, 64) || !short(c.name, 60) || !short(c.icon, 40) || !Array.isArray(c.rules) || c.rules.length > 64 || !c.rules.every(okRule))) throw new Error("invalid_categories");
      prefs.categories = v.categories.map(c => ({ id: c.id, name: c.name, icon: c.icon, rules: c.rules.map(r => [r[0], r[1]]) }));
    }
    if (v.comfyUrl !== undefined) {
      if (v.comfyUrl !== "" && !(short(v.comfyUrl, 512) && /^https?:\/\/[^\s]+$/.test(v.comfyUrl))) throw new Error("invalid_comfy_url");
      prefs.comfyUrl = v.comfyUrl;
    }
    out["arkism.prefs"] = JSON.stringify(prefs);
    for (const [key, value] of Object.entries(out)) write(key, value);
    return Object.keys(out).length;
  },
  // A memory or skill file belongs to one account: it is refused for any other principal or scope.
  checkOwner(data, product, identity) {
    if (!data || data.product !== product || data.version !== 1 || !data.values) throw new Error("not_" + product);
    if (!identity || identity.role !== "human" || data.principal !== identity.principal_id) throw new Error("other_account");
    for (const k of ["workspace_id", "project_id", "user_id", "agent_id"]) if ((data.scope?.[k] || "") !== (identity.scope?.[k] || "")) throw new Error("other_scope");
  },
  importMemory(text, identity, read, write) {
    const data = JSON.parse(text);
    this.checkOwner(data, "arkism-memory-preferences", identity);
    const { collect, kept } = data.values;
    if (typeof collect !== "boolean" || !Array.isArray(kept) || kept.length > 5000 || kept.some(id => typeof id !== "string" || !id || id.length > 256 || /\p{Cc}/u.test(id))) throw new Error("invalid_memory_values");
    const prefs = JSON.parse(read("arkism.prefs") || "{}");
    const current = JSON.parse(read("arkism.keptMemories") || "[]");
    prefs.collectUnused = collect;
    write("arkism.prefs", JSON.stringify(prefs));
    write("arkism.keptMemories", JSON.stringify([...new Set([...(Array.isArray(current) ? current : []), ...kept])].slice(0, 5000)));
    return kept.length;
  },
  importSkills(text, identity, read, write) {
    const data = JSON.parse(text);
    this.checkOwner(data, "arkism-skill-preferences", identity);
    const { items, dismissed, similarDismissed } = data.values;
    const name = x => typeof x === "string" && /^[a-z0-9][a-z0-9-]{0,63}$/.test(x);
    const str = (x, max) => typeof x === "string" && x.length <= max;
    if (!Array.isArray(items) || items.length > 3 || items.some(i => !i || !name(i.name) || !str(i.title, 2000) || !str(i.why, 10000) || !str(i.skill_md, 20000) || !i.skill_md.trim() || !str(i.examples, 20000))) throw new Error("invalid_drafts");
    if (!Array.isArray(dismissed) || dismissed.length > 5000 || dismissed.some(x => !name(x))) throw new Error("invalid_dismissed");
    if (!Array.isArray(similarDismissed) || similarDismissed.length > 5000 || similarDismissed.some(x => !str(x, 513) || !x || /\p{Cc}/u.test(x))) throw new Error("invalid_similar");
    write("arkism.skillSuggest", JSON.stringify({ items: items.map(i => ({ name: i.name, title: i.title, why: i.why, skill_md: i.skill_md, examples: i.examples.split("\n").filter(Boolean) })), dismissed: [...new Set(dismissed)] }));
    const current = JSON.parse(read("arkism.similarDismissed") || "[]");
    write("arkism.similarDismissed", JSON.stringify([...new Set([...(Array.isArray(current) ? current : []), ...similarDismissed])].slice(0, 5000)));
    return items.length;
  }
};
