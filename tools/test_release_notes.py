import unittest

import release_notes as r

UI = "Popup drags\n\nWhy: x\nVerified: y\n\n제목: 팝업 끌기\n이유: x\n검증: y\n"
STUDY = UI.replace("Popup drags", "Memory map stays still") + "Task: PSY-02\nResearch: changes what the map shows\n"
NOT_STUDY = UI + "Task: PSY-02\nResearch: none\n"


class ReleaseNotesTest(unittest.TestCase):
    def test_groups_and_both_languages(self):
        text = r.notes([
            ("a" * 40, UI, ["desktop/ui/popup-drag.js", "desktop/ui/app.css"]),
            ("b" * 40, STUDY, ["desktop/ui/memory-bubbles.js"]),
            ("c" * 40, "Old style subject\n\nbody", ["internal/crewrun/nodes.go", "desktop/ui/x.js", "internal/a.go"]),
            ("d" * 40, NOT_STUDY, [".github/workflows/release.yml"]),
        ], "Arkism 0.1.3")
        self.assertEqual(text.splitlines()[0], "# Arkism 0.1.3")
        research, app, engine, build = (text.index(h) for h in (
            "## 연구 변경 · Research changes", "## 앱 화면 · App", "## 엔진·서버 · Engine & server",
            "## 빌드·배포·도구 · Build, release & tools"))
        self.assertTrue(research < app < engine < build)
        self.assertIn("- **팝업 끌기** · Memory map stays still (PSY-02) `bbbbbbb`", text)
        self.assertIn("- **팝업 끌기** · Popup drags `aaaaaaa`", text)
        self.assertIn("- Old style subject `ccccccc`", text)
        self.assertNotIn("## 문서", text)

    def test_empty_range(self):
        self.assertIn("바뀐 점 없음 · No changes", r.notes([], "Arkism"))


if __name__ == "__main__":
    unittest.main()
