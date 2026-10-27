"""사람 라벨 회상 세트 v2의 추출·익명화·라벨 입력 해석을 네트워크 없이 검증한다."""
import importlib.util
import json
import pathlib
import tempfile
import unittest

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


class LabelTests(unittest.TestCase):
    label = load("recall-quality-v2/label.py")

    def test_parse_answer_maps_ranks_and_keeps_ids(self):
        shown = ["m1", "m2", "m3"]
        self.assertEqual(self.label.parse_answer("2 1 m9 2", shown), ["m2", "m1", "m9"])
        self.assertEqual(self.label.parse_answer("-", shown), [])
        self.assertIsNone(self.label.parse_answer("s", shown))
        self.assertEqual(self.label.parse_answer("7", shown), ["7"])  # out of range: taken as an id, not dropped silently

    def test_label_row_has_the_form_and_private_mode(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "labels.jsonl"
            self.label.append_jsonl(path, {"id": "q-0001", "query": "q", "expected_memory_ids": [], "labeler": "x", "labeled_at": "t", "note": "", "shown": []})
            row = self.label.read_jsonl(path)[0]
            self.assertEqual(set(row), {"id", "query", "expected_memory_ids", "labeler", "labeled_at", "note", "shown"})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)


if __name__ == "__main__":
    unittest.main()
