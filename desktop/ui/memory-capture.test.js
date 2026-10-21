// Execute the actual dialog and restore handlers with a small DOM/API stand-in.
const test = require("node:test");
const assert = require("node:assert/strict");
const moduleTask = import("./modules/memoryDetail.mjs");

async function setup(item) {
  const notices = [], captures = [];
  let reloads = 0;
  function el(tag, className, text) {
    return {tag, className, text, children: [], listeners: {},
      append(...children) { this.children.push(...children); },
      addEventListener(name, fn) { this.listeners[name] = fn; },
      close() { this.closed = true; }, showModal() {}, remove() {}};
  }
  const body = el("body");
  const context = { state: {selected: "before", scope: {}},
    el, button: (text, cls) => el("button", cls, text),
    notice: text => notices.push(text),
    request: async (route, options) => { captures.push(JSON.parse(options.body)); return {items: [item]}; },
    reloadMemories: async () => { reloads++; }
  };
  globalThis.document = {body};
  globalThis.ask = async () => true;
  const { createMemoryDetail } = await moduleTask;
  Object.assign(context, createMemoryDetail(context));
  return {context, body, notices, captures, reloads: () => reloads};
}

for (const [name, item, expected] of [
  ["accepted", {status: "accepted", memory_id: "m1"}, "saved"],
  ["candidate", {status: "candidate", candidate_id: "c1"}, "pending"],
  ["merged candidate", {status: "merged", candidate_id: "c1"}, "duplicate"],
  ["merged source without candidate", {status: "merged", reason: "duplicate_source"}, "duplicate"]
]) {
  test("manual Add reports " + name + " accurately", async () => {
    const h = await setup(item);
    h.context.openMemoryFix(null);
    const dialog = h.body.children[0], form = dialog.children[0];
    form.children[1].children[0].children[0].value = "a lasting preference";
    form.children[1].children[1].children[0].value = "user";
    await form.listeners.submit({submitter: {value: "ok"}, preventDefault() {}});
    assert.equal(h.captures.length, 1);
    assert.equal(h.captures[0].claims[0].text, "a lasting preference");
    assert.equal(dialog.closed, true);
    assert.equal(h.reloads(), 1);
    assert.equal(h.context.state.selected, null);
    const notice = h.notices[0];
    if (expected === "saved") assert.equal(notice, "저장했어요.");
    if (expected === "pending") assert.match(notice, /^검토 대기에/);
    if (expected === "duplicate") assert.match(notice, /^이미 수집한/);
  });

  test("restore reports " + name + " accurately", async () => {
    const h = await setup(item), memory = {id: "m1", revision: 2, text: "current", kind: "user"};
    await h.context.revertMemory(memory, "previous", {});
    assert.equal(h.captures.length, 1);
    assert.deepEqual(h.captures[0].claims[0].supersedes, {collection: "memories", id: memory.id, revision: memory.revision});
    assert.equal(h.reloads(), 1);
    const notice = h.notices[0];
    if (expected === "saved") assert.equal(notice, "이전 내용으로 되돌렸어요.");
    if (expected === "pending") assert.match(notice, /^검토 대기에/);
    if (expected === "duplicate") assert.match(notice, /^이미 수집한/);
  });
}
