import json
import unittest
from recall_inject_audit import summarize


def line(**values):
    """Build a synthetic metrics record."""
    return json.dumps({"ts": "2026-10-17T10:00:00Z", **values})


class RecallInjectAuditTests(unittest.TestCase):
    """TC-BASE-006: injection sizes are counted per hook without session ids."""
    def test_sizes_per_hook(self):
        """Count chars, sessions and over-limit blocks per hook."""
        result = summarize([
            line(event="inject", hook="SessionStart", session="s1", chars=3900),
            line(event="inject", hook="SessionStart", session="s2", chars=4100),
            line(event="inject", hook="UserPromptSubmit", session="s1", chars=800),
            line(event="recall", hook="UserPromptSubmit", injected=0, captured=0),
            line(event="recall", hook="UserPromptSubmit", injected=4, captured=1),
            line(event="turn_completed"), "not json",
        ])
        start = result["inject_chars"]["SessionStart"]
        self.assertEqual((start["n"], start["sessions"], start["over_session_max"]), (2, 2, 1))
        self.assertEqual(result["recall_items"]["UserPromptSubmit"]["empty"], 1)
        self.assertEqual(result["skipped_lines"], 1)
        self.assertNotIn("s1", json.dumps(result))

    def test_since_drops_older_rows(self):
        """Rows before the cutoff are not counted."""
        result = summarize([line(event="inject", hook="SessionStart", session="s", chars=1,
                                 ts="2026-10-01T00:00:00Z")], "2026-10-02T00:00:00Z")
        self.assertEqual(result["inject_chars"], {})

    def test_bool_counters_ignored(self):
        """TC-BASE-010: JSON booleans are not counted as integers."""
        result = summarize([line(event="inject", hook="SessionStart", session="s", chars=True),
                            line(event="recall", hook="UserPromptSubmit", injected=False)])
        self.assertEqual((result["inject_chars"], result["recall_items"]), ({}, {}))


if __name__ == "__main__":
    unittest.main()
