"""Offline tests of the adapter against the SDK's local fake server. They skip when langchain-core is not installed."""

import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SDK = os.path.join(HERE, "..", "..", "..")
sys.path[:0] = [os.path.join(HERE, ".."), SDK, os.path.join(SDK, "tests")]

from arkism_client import RECALL_HEADER, ArkismClient  # noqa: E402
from test_client import SCOPE, FakeServer  # noqa: E402

try:
    from langchain_core.messages import AIMessage, HumanMessage, SystemMessage  # noqa: E402

    import arkism_langchain  # noqa: E402
except ImportError:
    arkism_langchain = None

HITS = {"items": [{"id": "m1", "kind": "procedural", "text": "Run staging first", "score": 0.9}, {"kind": "semantic"}]}


@unittest.skipIf(arkism_langchain is None, "langchain-core is not installed")
class LangchainAdapterTest(unittest.TestCase):
    def serve(self, replies):
        server = FakeServer([(200, r, None) for r in replies])
        self.addCleanup(server.close)
        return server, ArkismClient(server.url)

    def test_retriever_and_recall_message(self):
        server, client = self.serve([HITS, HITS, {"items": []}])
        docs = arkism_langchain.ArkismRetriever(client=client, scope=SCOPE, limit=3).invoke("deploy")
        self.assertEqual([(d.page_content, d.metadata) for d in docs], [("Run staging first", {"id": "m1", "kind": "procedural", "score": 0.9})])
        self.assertEqual(server.calls[0][1:4:2], ("/v1/search", {"limit": 3, "scope": SCOPE, "query": "deploy"}))
        msg = arkism_langchain.recall_message(client, SCOPE, "deploy")
        self.assertEqual(msg.content, RECALL_HEADER + "\n- [procedural] Run staging first")
        self.assertIsNone(arkism_langchain.recall_message(client, SCOPE, "nothing"))
        self.assertIsNone(arkism_langchain.recall_message(client, SCOPE, ""))
        self.assertEqual(len(server.calls), 3)

    def test_capture_messages(self):
        server, client = self.serve([{"job_id": "j"}])
        messages = [SystemMessage(content="x"), HumanMessage(content="I prefer pnpm"),
                    AIMessage(content=[{"type": "text", "text": "Noted."}, {"type": "tool_use", "id": "t"}])]
        arkism_langchain.capture_messages(client, SCOPE, messages, session_id="s1")
        self.assertIsNone(arkism_langchain.capture_messages(client, SCOPE, [SystemMessage(content="x")]))
        self.assertEqual(len(server.calls), 1)
        self.assertEqual(server.calls[0][1:4:2], ("/v1/memories/ingest", {"scope": SCOPE, "async": True, "items": [
            {"content": "human: I prefer pnpm\nai: Noted.", "context": "agent conversation", "document_id": "s1", "update_mode": "append"}]}))

    def test_tools(self):
        server, client = self.serve([HITS, {"items": []}, {"candidate_ids": ["c1"]}])
        recall, remember = arkism_langchain.memory_tools(client, SCOPE)
        self.assertEqual((recall.name, remember.name), ("recall_memory", "remember"))
        self.assertEqual(recall.invoke({"query": "deploy"}), RECALL_HEADER + "\n- [procedural] Run staging first")
        self.assertEqual(recall.invoke({"query": "none"}), "No matching memories.")
        self.assertIn("candidate", remember.invoke({"text": "Uses pnpm"}))
        body = server.calls[2][3]
        self.assertEqual((server.calls[2][1], body["event"]["type"], body["event"]["origin_product"], body["claims"][0]["text"]),
                         ("/v1/capture", "explicit_remember", "langchain-adapter", "Uses pnpm"))


if __name__ == "__main__":
    unittest.main()
