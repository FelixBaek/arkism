# arkism-client

Thin client for the Arkism memory server API, generated from `api/openapi.yaml`.
Standard library only; Python 3.9 or later.

```bash
pip install arkism-client
```

```python
import os
from arkism_client import ArkismClient

client = ArkismClient("http://127.0.0.1:8080", token=os.environ.get("ARKISM_TOKEN"))
scope = {"workspace_id": "default", "project_id": "my-project"}

lines = client.recall_text(scope, "what did we decide about the cache?")
client.remember(scope, "The cache keeps entries for one hour.")  # stays a candidate until a person approves it
```

Each API route is one method named by its `operationId`; `request(method, path, query, body, headers)`
reaches any route. Retries, errors and the helper methods are described in
[guide/development.md](https://github.com/FelixBaek/arkism/blob/main/guide/development.md#client-libraries).

License: MIT.
