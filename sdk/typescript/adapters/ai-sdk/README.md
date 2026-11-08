# arkism-ai-sdk

Arkism memory for agents built on the [`ai`](https://www.npmjs.com/package/ai)
package (version 5 or later). It is a thin layer over `arkism-client`; every
request goes through that client.

| Piece | What it does |
|---|---|
| `arkismMemory(client, scope, {limit})` | Middleware for `wrapLanguageModel`. Before each call it recalls approved memories for the last user message and puts them first in the prompt as a system message marked as data. Nothing is added when nothing matched. |
| `captureMessages(client, scope, messages, {sessionId, context})` | Sends the conversation (one `role: text` line per message, system messages left out) to `POST /v1/memories/ingest`. The server extracts memory candidates; none is used until a person approves it. The same `sessionId` appends to one document. |
| `arkismTools(client, scope, {limit})` | Two tools: `recall_memory` (search approved memories) and `remember` (propose one memory; it waits as a candidate for a person's review). |

```ts
import { generateText, wrapLanguageModel, stepCountIs } from "ai";
import { ArkismClient } from "arkism-client";
import { arkismMemory, arkismTools, captureMessages } from "arkism-ai-sdk";

const client = new ArkismClient({ baseUrl: "http://127.0.0.1:8787", token: process.env.ARKISM_TOKEN });
const scope = { workspace_id: "my-team", project_id: "my-app" };

const result = await generateText({
  model: wrapLanguageModel({ model: yourModel, middleware: arkismMemory(client, scope) }),
  tools: arkismTools(client, scope),
  stopWhen: stepCountIs(5),
  messages,
});
await captureMessages(client, scope, [...messages, ...result.response.messages], { sessionId: "chat-42" });
```

Model output never becomes an active memory here: `remember` and
`captureMessages` only create candidates, and a person approves them in the
review queue.

Tests (offline, fake server answers; they skip when `ai` is not installed
next to the adapter, so install the pinned versions first; the install links
the in-repo `arkism-client`, so nothing has to be published first):

```bash
npm ci --prefix sdk/typescript/adapters/ai-sdk --ignore-scripts
node --experimental-strip-types --test sdk/typescript/adapters/ai-sdk/test/*.test.ts
```
