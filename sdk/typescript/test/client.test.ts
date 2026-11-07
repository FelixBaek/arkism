// Offline tests of the generated client: a fake fetch stands in for the server.
import { test } from "node:test";
import assert from "node:assert/strict";
import { ArkismClient, ArkismError, RECALL_HEADER, retryAfterMs } from "../src/index.ts";

type Call = { url: string; method: string; headers: Record<string, string>; body: unknown };

function fake(answers: Array<{ status: number; body?: unknown; headers?: Record<string, string> }>) {
  const calls: Call[] = [];
  const fetchFn = (async (url: URL, init: RequestInit) => {
    calls.push({ url: String(url), method: String(init.method), headers: init.headers as Record<string, string>, body: init.body ? JSON.parse(String(init.body)) : undefined });
    const a = answers[Math.min(calls.length - 1, answers.length - 1)];
    return new Response(a.body === undefined ? "" : JSON.stringify(a.body), { status: a.status, headers: a.headers });
  }) as unknown as typeof fetch;
  return { calls, fetchFn };
}

const scope = { workspace_id: "w", project_id: "p" };

test("retries 429 and 503 honoring Retry-After, bounded by maxDelayMs", async () => {
  const { calls, fetchFn } = fake([{ status: 429, headers: { "Retry-After": "2" } }, { status: 503, headers: { "Retry-After": "120" } }, { status: 200, body: { ok: true } }]);
  const waits: number[] = [];
  const client = new ArkismClient({ baseUrl: "http://x/", token: "t", fetch: fetchFn, retry: { maxDelayMs: 5000, sleep: async (ms) => { waits.push(ms); } } });
  assert.deepEqual(await client.request("GET", "/v1/ping"), { ok: true });
  assert.equal(calls.length, 3);
  assert.deepEqual(waits, [2000, 5000]);
  assert.equal(calls[0].headers.Authorization, "Bearer t");
});

test("without Retry-After the wait doubles; after the last retry the error surfaces", async () => {
  const { calls, fetchFn } = fake([{ status: 503, body: { code: "busy" } }]);
  const waits: number[] = [];
  const client = new ArkismClient({ baseUrl: "http://x", fetch: fetchFn, retry: { retries: 3, baseDelayMs: 10, sleep: async (ms) => { waits.push(ms); } } });
  await assert.rejects(client.request("GET", "/v1/ping"), (e: unknown) => e instanceof ArkismError && e.status === 503 && (e.body as { code: string }).code === "busy");
  assert.equal(calls.length, 4);
  assert.deepEqual(waits, [10, 20, 40]);
});

test("other errors and retries: 0 are not retried", async () => {
  for (const [status, retries] of [[500, 2], [429, 0]] as const) {
    const { calls, fetchFn } = fake([{ status }]);
    const client = new ArkismClient({ baseUrl: "http://x", fetch: fetchFn, retry: { retries, sleep: async () => {} } });
    await assert.rejects(client.request("GET", "/v1/ping"), ArkismError);
    assert.equal(calls.length, 1);
  }
});

test("POST is retried only when turned away (Retry-After) or with retryUnsafe", async () => {
  const bare = fake([{ status: 503, body: { code: "dependency_unavailable" } }]);
  const noSleep = { sleep: async () => {} };
  await assert.rejects(new ArkismClient({ baseUrl: "http://x", fetch: bare.fetchFn, retry: noSleep }).request("POST", "/v1/capture", undefined, {}), ArkismError);
  assert.equal(bare.calls.length, 1);
  const turned = fake([{ status: 429, headers: { "Retry-After": "1" } }, { status: 200, body: { ok: true } }]);
  assert.deepEqual(await new ArkismClient({ baseUrl: "http://x", fetch: turned.fetchFn, retry: noSleep }).request("POST", "/v1/capture", undefined, {}), { ok: true });
  assert.equal(turned.calls.length, 2);
  const optIn = fake([{ status: 503 }, { status: 200, body: { ok: true } }]);
  assert.deepEqual(await new ArkismClient({ baseUrl: "http://x", fetch: optIn.fetchFn, retry: { ...noSleep, retryUnsafe: true } }).request("POST", "/v1/search", undefined, {}), { ok: true });
  assert.equal(optIn.calls.length, 2);
});

test("retryAfterMs reads seconds and HTTP dates", () => {
  assert.equal(retryAfterMs("3"), 3000);
  assert.equal(retryAfterMs(new Date(1_000_000 + 4000).toUTCString(), 1_000_000), 4000);
  assert.equal(retryAfterMs("soon"), undefined);
  assert.equal(retryAfterMs(null), undefined);
});

test("recall, reflect and remember send the documented bodies", async () => {
  const { calls, fetchFn } = fake([{ status: 200, body: { items: [] } }]);
  const client = new ArkismClient({ baseUrl: "http://x", fetch: fetchFn });
  await client.recall(scope, "deploy steps", { limit: 5 });
  await client.reflect(scope, "why?", { max_tokens: 100 });
  await client.remember(scope, "Run staging first", { kind: "procedural", sessionId: "s1" });
  assert.deepEqual([calls[0].url, calls[0].body], ["http://x/v1/search", { limit: 5, scope, query: "deploy steps" }]);
  assert.deepEqual([calls[1].url, calls[1].body], ["http://x/v1/reflect", { max_tokens: 100, scope, query: "why?" }]);
  const capture = calls[2].body as { event: { id: string; type: string; session_id: string; occurred_at: string }; claims: Array<{ kind: string; text: string; basis: string }>; generator: object };
  assert.equal(calls[2].url, "http://x/v1/capture");
  assert.match(capture.event.id, /^sdk:/);
  assert.equal(capture.event.type, "explicit_remember");
  assert.equal(capture.event.session_id, "s1");
  assert.ok(!Number.isNaN(Date.parse(capture.event.occurred_at)));
  assert.deepEqual([capture.claims[0].kind, capture.claims[0].text, capture.claims[0].basis], ["procedural", "Run staging first", "inferred"]);
});

test("recallText formats approved hits; captureText queues one ingest item", async () => {
  const { calls, fetchFn } = fake([{ status: 200, body: { items: [{ kind: "procedural", text: "Run staging first" }, { kind: "semantic" }] } }, { status: 200, body: { items: [] } }]);
  const client = new ArkismClient({ baseUrl: "http://x", fetch: fetchFn });
  assert.equal(await client.recallText(scope, "deploy", { limit: 3 }), `${RECALL_HEADER}\n- [procedural] Run staging first`);
  assert.equal(await client.recallText(scope, "nothing"), "");
  await client.captureText(scope, "user: hi", { documentId: "s1", context: "chat" });
  await client.captureText(scope, "plain");
  assert.deepEqual(calls[0].body, { limit: 3, scope, query: "deploy" });
  assert.deepEqual([calls[2].url, calls[2].body], ["http://x/v1/memories/ingest", { scope, async: true, items: [{ content: "user: hi", context: "chat", document_id: "s1", update_mode: "append" }] }]);
  assert.deepEqual((calls[3].body as { items: unknown[] }).items, [{ content: "plain" }]);
});

test("pages follows next_cursor until empty and stops at maxPages", async () => {
  const seen: Array<string | undefined> = [];
  const client = new ArkismClient({ baseUrl: "http://x" });
  const page = async (cursor?: string) => {
    seen.push(cursor);
    return cursor === "c2" ? { items: [3], next_cursor: "" } : { items: cursor ? [2] : [1], next_cursor: cursor ? "c2" : "c1" };
  };
  const all: number[] = [];
  for await (const item of client.pages(page)) all.push(item);
  assert.deepEqual(all, [1, 2, 3]);
  assert.deepEqual(seen, [undefined, "c1", "c2"]);
  const capped: number[] = [];
  for await (const item of client.pages(page, 2)) capped.push(item);
  assert.deepEqual(capped, [1, 2]);
});

test("generated methods build paths and queries", async () => {
  const { calls, fetchFn } = fake([{ status: 200, body: { items: [], next_cursor: null, authority: {} } }]);
  const client = new ArkismClient({ baseUrl: "http://x", fetch: fetchFn });
  await client.getV1Memories({ workspace_id: "w s", project_id: "p", limit: 2 });
  assert.equal(calls[0].url, "http://x/v1/memories?workspace_id=w+s&project_id=p&limit=2");
});
