# arkism-langchain

Arkism memory for agents built on `langchain-core` (0.3 or later), including
graphs whose nodes take and return its messages. It is a thin layer over
`arkism-client`; every request goes through that client.

| Piece | What it does |
|---|---|
| `ArkismRetriever(client=, scope=, limit=8)` | A retriever: approved memories matching the query as `Document`s (`page_content` is the memory text; `id`, `kind`, `score`, `created_at` in metadata). |
| `recall_message(client, scope, query, limit=8)` | A `SystemMessage` with the matching memories under a header that marks them as data, or `None` when nothing matched. Put it before the conversation. |
| `capture_messages(client, scope, messages, session_id=None)` | Sends the conversation (one `role: text` line per message, system messages left out) to `POST /v1/memories/ingest`. The server extracts memory candidates; none is used until a person approves it. The same `session_id` appends to one document. |
| `memory_tools(client, scope, limit=8)` | Two tools: `recall_memory` (search approved memories) and `remember` (propose one memory; it waits as a candidate for a person's review). |

```python
from arkism_client import ArkismClient
from arkism_langchain import capture_messages, memory_tools, recall_message

client = ArkismClient("http://127.0.0.1:8787", token=os.environ["ARKISM_TOKEN"])
scope = {"workspace_id": "my-team", "project_id": "my-app"}

memory = recall_message(client, scope, question)
model = chat_model.bind_tools(memory_tools(client, scope))
reply = model.invoke(([memory] if memory else []) + history + [HumanMessage(question)])
capture_messages(client, scope, history + [HumanMessage(question), reply], session_id="chat-42")
```

Model output never becomes an active memory here: `remember` and
`capture_messages` only create candidates, and a person approves them in the
review queue.

Tests (offline, against the SDK's local fake server; they skip when
`langchain-core` is not installed):

```bash
python3 -m pip install --require-hashes -r sdk/python/adapters/langchain/requirements-test.txt
python3 -m unittest discover -s sdk/python/adapters/langchain/tests
```
