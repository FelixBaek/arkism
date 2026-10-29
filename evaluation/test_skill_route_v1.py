"""스킬 추천 평가 v1의 라벨 해석·채점·라우트 호출 모양을 네트워크 없이 검증한다."""
import importlib.util
import json
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("route_eval", ROOT / "skill-route-v1/route_eval.py")
route_eval = importlib.util.module_from_spec(spec)
spec.loader.exec_module(route_eval)


def run(selected, offered=(), provider="jev", degraded=(), latency=10):
    return {"selected": selected, "offered": list(offered), "provider": provider, "degraded": list(degraded), "latency_ms": latency}


class ScoreTests(unittest.TestCase):
    def test_fit_and_no_fit_questions_are_scored_apart(self):
        labels = [{"id": "q1", "expected_skill_ids": ["a"]}, {"id": "q2", "expected_skill_ids": ["a", "b"]},
                  {"id": "q3", "expected_skill_ids": ["c"]}, {"id": "q4", "expected_skill_ids": []},
                  {"id": "q5", "expected_skill_ids": []}, {"id": "q6", "expected_skill_ids": ["a"]}]
        runs = [{"id": "q1", **run("a", ["a", "x"], latency=10)}, {"id": "q2", **run("x", ["x", "b"], latency=20)},
                {"id": "q3", **run("", ["x"], provider="rules", degraded=["jev_uncertain"], latency=30)},
                {"id": "q4", **run("", latency=40)}, {"id": "q5", **run("x", ["x"], latency=50)}]
        got = route_eval.score(labels, runs)
        self.assertEqual((got["labeled"], got["scored"], got["with_fit"], got["without_fit"]), (6, 5, 3, 2))  # q6 has no run
        self.assertEqual(got["pick_accuracy"], round(1 / 3, 4))
        self.assertEqual(got["wrong_pick_rate"], round(1 / 3, 4))
        self.assertEqual(got["withheld_rate"], round(1 / 3, 4))
        self.assertEqual(got["candidate_recall"], round(2 / 3, 4))
        self.assertEqual((got["abstain_accuracy"], got["unneeded_pick_rate"]), (0.5, 0.5))
        self.assertEqual((got["latency_ms_p50"], got["latency_ms_p95"]), (30, 50))
        self.assertEqual(got["providers"], {"jev": 4, "rules": 1})
        self.assertEqual(got["degraded"], {"jev_uncertain": 1})
        self.assertNotIn("query", json.dumps(got))

    def test_empty_sets_give_no_ratio(self):
        got = route_eval.score([], [])
        self.assertIsNone(got["pick_accuracy"])
        self.assertIsNone(got["latency_ms_p50"])

    def test_parse_answer(self):
        self.assertEqual(route_eval.parse_answer("2, b 2", ["a", "b"]), ["b"])
        self.assertEqual(route_eval.parse_answer("-", ["a"]), [])
        self.assertIsNone(route_eval.parse_answer("s", ["a"]))


class RouterTests(unittest.TestCase):
    def test_route_maps_versions_to_skill_ids_by_suitability(self):
        with tempfile.TemporaryDirectory() as directory:
            token = pathlib.Path(directory) / "token"
            token.write_text("synthetic\n")
            router = route_eval.Router("http://hub/", token, {"workspace_id": "w"}, "codex")
            calls = []

            def fake(path, payload=None):
                calls.append((path, payload))
                if path == "/v1/skills/route":
                    return {"selected_id": "skill-2", "provider": "jev", "judgments": [{"id": "skill-1", "suitability": 1}, {"id": "skill-2", "suitability": 3}]}
                return {"skill_id": "agent/" + path.split("/")[3].split("?")[0]}

            router._call = fake
            got = router.route("pdf 읽어 줘")
            self.assertEqual((got["selected"], got["offered"]), ("agent/skill-2", ["agent/skill-2", "agent/skill-1"]))
            self.assertEqual(calls[0][1]["runtime"], "codex")
            self.assertEqual(calls[0][1]["purpose"], "skills")
            self.assertEqual(len(calls), 3)  # each version looked up once
            self.assertTrue(calls[1][0].endswith("?workspace_id=w"))


if __name__ == "__main__":
    unittest.main()
