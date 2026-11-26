"""사람 라벨 회상 세트 v2의 추출·익명화·라벨 입력 해석을 네트워크 없이 검증한다."""
import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parent


def load(relative):
    spec = importlib.util.spec_from_file_location(relative.replace("/", "_").replace(".py", ""), ROOT / relative)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def entry(text, **extra):
    row = {"type": "user", "message": {"content": text}, "sessionId": "sess-1", "timestamp": "2026-10-27T01:02:03Z", "cwd": "/home/x/repo"}
    row.update(extra)
    return row


class ExtractTests(unittest.TestCase):
    extract = load("recall-quality-v2/extract_queries.py")

    def test_anonymize_masks_addresses_paths_and_tokens(self):
        home = str(pathlib.Path.home())
        text = f"메일 a.b@x.io 전화 010-1234-5678 주소 https://x.io/p?q=1 ip 10.0.0.1 경로 {home}/repo 키 abcdefghijklmnopqrstuvwxyz0123456789"
        self.assertEqual(self.extract.anonymize(text), "메일 [email] 전화 [phone] 주소 [url] ip [ip] 경로 ~/repo 키 [token]")

    def test_usable_keeps_typed_prose_only(self):
        self.assertEqual(self.extract.usable(entry("이 함수는 왜 느려?")), "이 함수는 왜 느려?")
        for bad in (entry("짧다"), entry("<pasted>x</pasted> 긴 질문입니다"), entry("/commit 해 줘"), entry("코드 ```x``` 설명해 줘"),
                    entry("질문은 질문인데", isSidechain=True), entry("시스템 알림입니다 길게", promptSource="system"),
                    {"type": "user", "message": {"content": [{"type": "tool_result"}]}}, entry("x" * 401)):
            self.assertIsNone(self.extract.usable(bad), bad)

    def test_collect_dedupes_and_sample_puts_questions_first(self):
        with tempfile.TemporaryDirectory() as directory:
            project = pathlib.Path(directory) / "p1"
            project.mkdir()
            lines = [entry("배포는 어떻게 해?"), entry("배포는 어떻게 해?"), entry("배포는  어떻게 해?"), entry("오늘 할 일을 정리한다 길게 써 본다"),
                     entry("테스트가 왜 실패하지?", cwd="/home/x/other")]
            (project / "s.jsonl").write_text("\n".join(json.dumps(l, ensure_ascii=False) for l in lines) + "\n", encoding="utf-8")
            rows = self.extract.collect(pathlib.Path(directory))
            self.assertEqual([r["query"] for r in rows], ["배포는 어떻게 해?", "오늘 할 일을 정리한다 길게 써 본다", "테스트가 왜 실패하지?"])
            self.assertEqual(rows[0]["day"], "2026-10-27")
            self.assertNotIn("/home/x", json.dumps(rows))
            chosen = self.extract.sample(rows, limit=2, seed=1)
            self.assertEqual([r["id"] for r in chosen], ["q-0001", "q-0002"])
            self.assertTrue(all(self.extract.QUESTION.search(r["query"]) for r in chosen))
            self.assertEqual(self.extract.sample(rows, 2, 1), chosen)  # deterministic


    def test_single_project_fills_spare_slots_with_questions_first(self):
        rows = [{"query": f"question {i}?", "project": "local"} for i in range(9)]
        rows.append({"query": "plain statement", "project": "local"})
        result = self.extract.sample(rows, 8, seed=1)
        self.assertEqual(len({row["query"] for row in result}), 8)
        self.assertTrue(all(row["query"].endswith("?") for row in result))

    @unittest.skipUnless(os.name == "nt", "Windows home directory spelling")
    def test_windows_home_is_masked_with_mixed_case_and_forward_slashes(self):
        home = pathlib.Path.home().as_posix().swapcase()
        self.assertEqual(self.extract.anonymize(f"Read {home}/private/file?"), "Read ~/private/file?")

    def test_empty_transcripts_preserve_previous_questions(self):
        with tempfile.TemporaryDirectory() as directory:
            out = pathlib.Path(directory) / "queries.jsonl"
            out.write_bytes(b"previous\n")
            with self.assertRaises(SystemExit):
                self.extract.main(["--transcripts", directory, "--out", str(out)])
            self.assertEqual(out.read_bytes(), b"previous\n")


class LabelTests(unittest.TestCase):
    label = load("recall-quality-v2/label.py")

    def test_independent_labels_keep_separate_decisions_topics_and_rankings(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            queries = root / "queries.jsonl"
            for key in ("closed", "open"):
                self.label.append_jsonl(queries, {"id": key, "query": key})
            first, second = root / "team.jsonl", root / "team.txt"
            args = ["label", "--blind", "--queries", str(queries), "--labels"]
            with mock.patch("builtins.input", side_effect=[
                    "해보지 않음", "topic: first only", "m1", "", "m2", "", "m3", ""]), \
                    mock.patch("builtins.print") as output:
                self.label.main([*args, str(first)])
                self.label.main([*args, str(second)])
                self.assertNotIn(mock.call("  Previously recorded expectation: first only"), output.call_args_list)
                self.label.main([*args, str(first)])
                output.assert_any_call("  Previously recorded expectation: first only")
            search = mock.Mock()
            search.hits.return_value = []
            for labels in (first, second):
                self.label.run(mock.Mock(labels=labels), search)
            for labels, expected in ((first, ["open"]), (second, ["closed", "open"])):
                result, = root.glob(labels.name + ".runs-*.jsonl")
                self.assertEqual([row["id"] for row in self.label.read_jsonl(result)], expected)

    def test_failed_search_preserves_last_complete_run(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            args = mock.Mock(labels=root / "labels.jsonl")
            for query_id in ("a", "b"):
                self.label.append_jsonl(args.labels, {"id": query_id, "query": query_id})
            search = mock.Mock()
            search.hits.side_effect = [[{"id": "one"}], [], [], TimeoutError()]
            self.label.run(args, search)
            out, = root.glob("runs-*.jsonl")
            self.assertEqual(self.label.read_jsonl(out), [{"id": "a", "returned": ["one"]}, {"id": "b", "returned": []}])
            original = out.read_bytes()
            with self.assertRaises(TimeoutError):
                self.label.run(args, search)
            args.labels.write_bytes(b"")
            with self.assertRaisesRegex(ValueError, "no labeled queries"):
                self.label.run(args, search)
            self.assertEqual(out.read_bytes(), original)
            self.assertEqual(set(root.iterdir()), {out, args.labels})

    def test_parse_answer_maps_ranks_and_keeps_ids(self):
        shown = ["m1", "m2", "m3"]
        self.assertEqual(self.label.parse_answer("2 1 m9 2", shown), ["m2", "m1", "m9"])
        self.assertEqual(self.label.parse_answer("-", shown), [])
        self.assertIsNone(self.label.parse_answer("s", shown))
        self.assertEqual(self.label.parse_answer("7", shown), ["7"])  # out of range: taken as an id, not dropped silently
        self.assertEqual(self.label.parse_answer("００２ 01", shown), ["m2", "m1"])
        for blank in ("", "  ", ", ,"):
            self.assertIsNone(self.label.parse_answer(blank, shown))
        for literal in ("²", "9" * 5000):
            self.assertEqual(self.label.parse_answer(literal, shown), [literal])

    def test_full_text_control_preserves_default_run_and_uses_same_queries(self):
        import io
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            labels, token = root / "labels.jsonl", root / "token"
            token.write_text("test", encoding="utf-8")
            self.label.append_jsonl(labels, {"id": "a", "query": "question"})
            args = ["run", "--labels", str(labels), "--token-file", str(token)]
            with mock.patch.object(self.label.urllib.request, "urlopen", side_effect=[
                io.BytesIO(b'{"items":[{"id":"one"}]}'), io.BytesIO(b'{"items":[]}')]) as requests:
                self.label.main(args)
                self.label.main([*args, "--fts-only"])
            normal, baseline = [json.loads(call.args[0].data) for call in requests.call_args_list]
            self.assertEqual(baseline, {**normal, "modes": ["fts"], "expand": False, "rerank": False})
            files = sorted(root.glob("runs-*.jsonl"))
            self.assertEqual(len(files), 2)
            for file in files:
                expected = [] if file.stem.endswith("-fts") else ["one"]
                self.assertEqual(self.label.read_jsonl(file), [{"id": "a", "returned": expected, "requested_k": 10}])

    def test_label_row_has_the_form_and_private_mode(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "labels.jsonl"
            self.label.append_jsonl(path, {"id": "q-0001", "query": "q", "expected_memory_ids": [], "labeler": "x", "labeled_at": "t", "note": "", "shown": []})
            row = self.label.read_jsonl(path)[0]
            self.assertEqual(set(row), {"id", "query", "expected_memory_ids", "labeler", "labeled_at", "note", "shown"})
            if os.name == "nt":
                descriptor = subprocess.check_output(
                    ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
                     "[IO.File]::GetAccessControl($env:RECALL_TEST_FILE).GetSecurityDescriptorSddlForm('Access')"],
                    env={**os.environ, "RECALL_TEST_FILE": str(path)}, text=True,
                ).strip()
                self.assertIn(descriptor.split("D:", 1)[1], ("P(A;;FA;;;OW)(A;;FA;;;SY)", "P(A;;FA;;;SY)(A;;FA;;;OW)"))
            else:
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_blind_topic_stays_unscored_until_independent_id_is_given(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            queries, labels = root / "queries.jsonl", root / "labels.jsonl"
            queries.write_text('{"id":"q1","query":"why?"}\n', encoding="utf-8")
            args = ["label", "--blind", "--queries", str(queries), "--labels", str(labels)]
            with mock.patch.object(self.label, "Search", side_effect=AssertionError("offline")), \
                    mock.patch("builtins.print") as output, \
                    mock.patch("builtins.input", side_effect=["topic: release reason", "topic: release reason", "memory-1", ""]):
                self.label.main(args)
                self.label.main(args)
                self.assertFalse(labels.exists())
                self.assertEqual(len(self.label.read_jsonl(root / "expectations.jsonl")), 1)
                self.label.main(args)
                output.assert_any_call("  Previously recorded expectation: release reason")
                self.assertEqual(self.label.read_jsonl(labels)[0]["expected_memory_ids"], ["memory-1"])

    def test_no_experience_closes_query_without_a_label_and_skip_defers(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            queries, labels = root / "queries.jsonl", root / "labels.jsonl"
            for query_id in ("q1", "q2", "q3", "q3"):
                self.label.append_jsonl(queries, {"id": query_id, "query": query_id})
            args = ["label", "--blind", "--queries", str(queries), "--labels", str(labels)]
            with mock.patch.object(self.label, "Search", side_effect=AssertionError("offline")), \
                    mock.patch("builtins.print"), mock.patch("builtins.input", side_effect=[
                        "해보지않음", "s", "해보지 않음", "-", ""]) as prompt:
                self.label.main(args)
                self.assertFalse(labels.exists())
                self.label.main(args)
                self.label.main(args)
                self.assertEqual(prompt.call_count, 5)
            entries = self.label.read_jsonl(root / "dispositions.jsonl")
            self.assertEqual([(e["id"], e["disposition"]) for e in entries],
                             [("q1", "no_experience"), ("q3", "no_experience")])
            self.assertTrue(all("expected_memory_ids" not in e for e in entries))
            self.assertEqual([(e["id"], e["expected_memory_ids"]) for e in self.label.read_jsonl(labels)],
                             [("q2", [])])

    def test_no_required_memory_is_not_a_negative_label(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            queries, labels = root / "queries.jsonl", root / "labels.jsonl"
            for key in ("optional", "negative"):
                self.label.append_jsonl(queries, {"id": key, "query": key})
            args = ["label", "--blind", "--queries", str(queries), "--labels", str(labels)]
            with mock.patch.object(self.label, "Search", side_effect=AssertionError("offline")), \
                    mock.patch("builtins.print"), mock.patch("builtins.input", side_effect=["필수 기억 없음", "-", ""]) as ask:
                self.label.main(args)
                self.label.main(args)
                self.assertEqual(ask.call_count, 3)
            self.assertEqual(self.label.read_jsonl(root / "dispositions.jsonl")[0]["disposition"], "no_required_memory")
            self.assertEqual([(r["id"], r["expected_memory_ids"]) for r in self.label.read_jsonl(labels)], [("negative", [])])

    def test_blind_labeling_stays_offline_and_records_explicit_answers(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = pathlib.Path(directory)
            queries, labels = directory / "queries.jsonl", directory / "labels.jsonl"
            queries.write_text('{"id":"q1","query":"positive"}\n{"id":"q2","query":"skip"}\n{"id":"q3","query":"negative"}\n', encoding="utf-8")
            with mock.patch.object(self.label, "Search", side_effect=AssertionError("search must remain blind")), \
                    mock.patch("builtins.input", side_effect=["memory-1", "", "", "-", ""]), \
                    mock.patch("builtins.print"):
                self.assertEqual(self.label.main(["label", "--blind", "--queries", str(queries),
                                                  "--labels", str(labels), "--token-file", str(directory / "absent")]), 0)
            rows = self.label.read_jsonl(labels)
            self.assertEqual([(r["id"], r["expected_memory_ids"]) for r in rows],
                             [("q1", ["memory-1"]), ("q3", [])])
            self.assertTrue(all(r["label_mode"] == "blind" and not r["shown"] for r in rows))


if __name__ == "__main__":
    unittest.main()
