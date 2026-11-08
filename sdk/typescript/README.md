# arkism-client

Typed client for the Arkism memory server API, generated from `api/openapi.yaml`.
No runtime dependencies; Node 20 or later (or any runtime with `fetch`).

```bash
npm install arkism-client
```

```ts
import { ArkismClient } from "arkism-client";

const client = new ArkismClient({ baseUrl: "http://127.0.0.1:8080", token: process.env.ARKISM_TOKEN });
const scope = { workspace_id: "default", project_id: "my-project" };

const lines = await client.recallText(scope, "what did we decide about the cache?");
await client.remember(scope, "The cache keeps entries for one hour."); // stays a candidate until a person approves it
```

Each API route is one method named by its `operationId`; `request(method, path, query, body, headers)`
reaches any route. Retries, errors and the helper methods are described in
[guide/development.md](https://github.com/FelixBaek/arkism/blob/main/guide/development.md#client-libraries).

License: MIT.
