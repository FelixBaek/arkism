const test = require("node:test"), assert = require("node:assert/strict");

test("loadFailure tells an unreachable server from each kind of answer", async () => {
  const { loadFailure } = await import("./modules/appApi.mjs");
  const cases = [
    [{ code: "memory_list_invalid" }, true, "서버 답을 읽지 못했어요"],
    [{ status: 401 }, true, "서버가 접속을 거절했어요"],
    [{ code: "access_denied" }, true, "서버가 접속을 거절했어요"],
    [{ status: 503, code: "dependency_unavailable" }, true, "서버에 문제가 생겼어요"],
    [{ status: 404 }, true, "서버가 요청을 받지 않았어요"],
    [new Error("Failed to fetch"), false, "서버 연결이 끊겼어요"],
    [undefined, false, "서버 연결이 끊겼어요"],
  ];
  for (const [error, reached, title] of cases) {
    const got = loadFailure(error);
    assert.equal(got.reached, reached, title);
    assert.equal(got.title, title);
  }
  assert.equal(loadFailure({ code: "x_invalid" }).code, "x_invalid");
});

test("binary requests return the file's bytes from the app bridge, and an error stays a coded error", async () => {
  const { createAppApi } = await import("./modules/appApi.mjs");
  const sent = [];
  const replies = [{ status: 200, body: "AQID" }, { status: 413, body: "{\"code\":\"payload_too_large\"}" }, { status: 200, body: "{\"ok\":1}" }];
  const deps = { state: { server: "http://s", token: "t", scope: {} }, trimSlash: (s) => s, readJSON: (t) => { try { return JSON.parse(t); } catch (_) { return null; } },
    tauri: { invoke: async (cmd, a) => { sent.push(a); return replies.shift(); } } };
  const api = createAppApi(deps);
  assert.deepEqual([...await api.request("/v1/wiki/export", { binary: true })], [1, 2, 3]);
  assert.equal(sent[0].binary, true);
  await assert.rejects(api.request("/v1/wiki/export", { binary: true }), (e) => e.code === "payload_too_large" && e.status === 413);
  assert.deepEqual(await api.request("/v1/x"), { ok: 1 });
  assert.equal("binary" in sent[2], false); // plain calls keep the old command shape
});

test("a form upload goes to the app bridge as multipart bytes with its boundary, and a browser posts the same bytes", async () => {
  const { createAppApi } = await import("./modules/appApi.mjs");
  const calls = [];
  const deps = { state: { server: "http://s", token: "t", scope: {} }, trimSlash: (s) => s, readJSON: (t) => { try { return JSON.parse(t); } catch (_) { return null; } },
    tauri: { invoke: async (cmd, body, o) => { calls.push([cmd, body, o]); return { status: 200, body: "{\"files\":[]}" }; } } };
  const form = new FormData();
  form.append("metadata", "{\"scope\":{}}");
  form.append("file", new Blob(["hello"]), "a.md");
  assert.deepEqual(await createAppApi(deps).request("/v1/files/memories/batch", { form }), { files: [] });
  const [cmd, body, o] = calls[0], text = new TextDecoder().decode(body), type = o.headers["x-arkism-content-type"];
  assert.equal(cmd, "api_upload");
  assert.ok(body instanceof Uint8Array && /^multipart\/form-data; boundary=/.test(type), type);
  assert.ok(text.indexOf('name="metadata"') < text.indexOf('filename="a.md"') && text.includes("hello"), "metadata first, then the file");
  assert.equal(o.headers["x-arkism-base"], "http://s");
  const fetched = [];
  const saved = global.fetch;
  global.fetch = async (url, init) => { fetched.push([url, init]); return { status: 413, text: async () => "{\"code\":\"artifact_too_large\"}" }; };
  try { await assert.rejects(createAppApi({ ...deps, tauri: null }).request("/v1/files/memories/batch", { form }), (e) => e.code === "artifact_too_large"); }
  finally { global.fetch = saved; }
  assert.equal(fetched[0][0], "http://s/v1/files/memories/batch");
  assert.match(fetched[0][1].headers["Content-Type"], /^multipart\/form-data; boundary=/);
});
