import json
import unittest
from capture_metrics_audit import summarize


def line(**values):
    """Build a synthetic timestamped metrics record."""
    return json.dumps({"ts": "2026-10-14T14:00:00Z",
                       "event": "session_processor_pending", **values})


class MetricsAuditTests(unittest.TestCase):
    """Exercise public audit contracts using isolated synthetic fixtures."""
    def test_retries_are_occurrences_not_candidates(self):
        """Verify retries are occurrences not candidates."""
        result = summarize([line(error="evidence_missing", session="one")] * 3 +
                           [line(error="evidence_missing", session="two")])
        group = result["errors"][0]
        self.assertEqual(group["occurrences"], 4)
        self.assertEqual(group["distinct_logged_session_identifiers"], 2)
        self.assertEqual(result["count_unit"], "log_occurrences")

    def test_cutoff_uses_timezones_not_lexical_order(self):
        """Verify cutoff uses timezones not lexical order."""
        result = summarize([
            line(error="evidence_missing", ts="2026-10-14T23:00:00+09:00"),
            line(error="evidence_missing", ts="2026-10-14T14:00:01Z"),
        ], "2026-10-14T14:00:00Z")
        self.assertEqual(result["errors"][0]["occurrences"], 1)

    def test_no_untrusted_values_are_emitted(self):
        """Verify no untrusted values are emitted."""
        secret = "private-session-and-body"
        result = summarize([
            line(error=secret, session=secret, text=secret),
            line(event="session_evidence_missing", field=secret, text=secret),
            line(event="session_evidence_missing", field="check:not_run"),
        ])
        self.assertNotIn(secret, json.dumps(result))
        self.assertEqual(result["missing_evidence_fields"],
                         {"check:not_run": 1, "other": 1})

    def test_malformed_records_and_parked_events(self):
        """Verify malformed records and parked events."""
        result = summarize(["{", "[]", line(event=["untrusted"]), line(ts="bad", error="evidence_missing"),
                            line(event="session_processor_parked",
                                 error="evidence_missing")])
        self.assertEqual(result["malformed_lines"], 2)
        self.assertEqual(result["invalid_event_timestamps"], 1)
        self.assertEqual(result["errors"][0]["event"], "session_processor_parked")

    def test_naive_cutoff_rejected(self):
        """Verify naive cutoff rejected."""
        with self.assertRaises(ValueError):
            summarize([], "2026-10-14T14:00:00")


if __name__ == "__main__":
    unittest.main()
