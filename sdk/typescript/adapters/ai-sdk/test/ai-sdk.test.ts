// Offline tests of the adapter: a fake fetch stands in for the server. They skip when the "ai" package is not installed.
import { test } from "node:test";
import assert from "node:assert/strict";
import { ArkismClient, RECALL_HEADER } from "../../../src/index.ts";

let adapter: typeof import("../src/index.ts") | undefined;
try {
  adapter = await import("../src/index.ts");
} catch (e) {
  if ((e as { code?: string }).code !== "ERR_MODULE_NOT_FOUND") throw e;
}
const skip = adapter ? false : "the ai package is not installed next to the adapter";

type Call = { url: string; body: unknown };

function fake(answers: unknown[]) {
  const calls: Call[] = [];
  const fetchFn = (async (url: URL, init: RequestInit) => {
    calls.push({ url: String(url), body: init.body ? JSON.parse(String(init.body)) : undefined });
    return new Response(JSON.stringify(answers[Math.min(calls.length - 1, answers.length - 1)]), { status: 200 });
  }) as unknown as typeof fetch;
  return { calls, client: new ArkismClient({ baseUrl: "http://x", fetch: fetchFn }) };
}

const scope = { workspace_id: "w", project_id: "p" };
const hits = { items: [{ kind: "procedural", text: "Run staging first" }] };
const user = (text: string) => ({ role: "user" as const, content: [{ type: "text" as const, text }] });

test("middleware recalls for the last user message and puts the memories first", { skip }, async () => {
  const { calls, client } = fake([hits, { items: [] }]);
  const mw = adapter!.arkismMemory(client, scope, { limit: 3 });
  const params = { prompt: [{ role: "system" as const, content: "Be brief." }, user("old"), { role: "assistant" as const, content: [{ type: "text" as const, text: "ok" }] }, user("how do I deploy?")] };
  const out = await mw.transformParams!({ type: "generate", params, model: {} } as never);
  assert.deepEqual(out.prompt[0], { role: "system", content: `${RECALL_HEADER}\n- [procedural] Run staging first` });
  assert.equal(out.prompt.length, 5);
  assert.deepEqual(calls[0], { url: "http://x/v1/search", body: { limit: 3, scope, query: "how do I deploy?" } });
  const same = await mw.transformParams!({ type: "stream", params, model: {} } as never);
  assert.equal(same, params, "nothing matched: prompt unchanged");
});

test("captureMessages sends a transcript once, appending to the session document", { skip }, async () => {
  const { calls, client } = fake([{ job_id: "j" }]);
  await adapter!.captureMessages(client, scope, [{ role: "system", content: "x" }, user("I prefer pnpm"), { role: "assistant", content: "Noted." }], { sessionId: "s1" });
  assert.equal(await adapter!.captureMessages(client, scope, [{ role: "system", content: "x" }]), undefined);
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0], { url: "http://x/v1/memories/ingest", body: { scope, async: true, items: [
    { content: "user: I prefer pnpm\nassistant: Noted.", context: "agent conversation", document_id: "s1", update_mode: "append" }] } });
});

test("tools recall approved memories and propose candidates", { skip }, async () => {
  const { calls, client } = fake([hits, { items: [] }, { candidate_ids: ["c1"] }]);
  const tools = adapter!.arkismTools(client, scope);
  const opts = { toolCallId: "t", messages: [] } as never;
  assert.equal(await tools.recall_memory.execute!({ query: "deploy" }, opts), `${RECALL_HEADER}\n- [procedural] Run staging first`);
  assert.equal(await tools.recall_memory.execute!({ query: "none" }, opts), "No matching memories.");
  assert.match(String(await tools.remember.execute!({ text: "Uses pnpm" }, opts)), /candidate/);
  const capture = calls[2].body as { event: { type: string; origin_product: string }; claims: Array<{ text: string }> };
  assert.deepEqual([calls[2].url, capture.event.type, capture.event.origin_product, capture.claims[0].text],
    ["http://x/v1/capture", "explicit_remember", "ai-sdk-adapter", "Uses pnpm"]);
});
