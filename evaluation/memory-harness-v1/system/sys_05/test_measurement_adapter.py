"""Projection of a raw B run onto the unchanged A-v2 summary; no service or database needed."""
import copy
import hashlib
import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("sys05_measurement_adapter", HERE / "measurement_adapter.py")
A = importlib.util.module_from_spec(spec)
spec.loader.exec_module(A)
S, G = A.S, A.G
ANCHOR = 5_000


def inputs():
    cfg = {"allowed_freshness_ns": 400, "arrival_schedule": [
        {"count": 1, "offset_ns": i * 100, "operation": op, "spacing_ns": 0} for i, op in enumerate(["update", "read"])],
        "batch_limit": 2, "cache": "cold", "concurrency_limit": 1, "corpus_sha256": "3" * 64,
        "deadline_ns": 100, "duration_ns": 1000, "fault_schedule": [], "mode": "explicit-sync",
        "model_sha256": "2" * 64, "protocol": "sys05-load-config-v1", "quality_criterion_sha256": "1" * 64,
        "queue_admission_limit": 3, "retry_limit": 2, "run_id": "adapter-unit", "scenario": "normal",
        "seed": 3, "start_ns": 0}
    raw_config = G.canonical(cfg)
    workload = G.generate(raw_config)
    update, read = (G.parse(line) for line in workload.splitlines())
    at = lambda ns: ANCHOR + ns
    raw = {"protocol": "sys05-b5-raw-run-v1", "anchor_unix_ns": ANCHOR,
           "config_sha256": hashlib.sha256(raw_config).hexdigest(), "workload_sha256": hashlib.sha256(workload).hexdigest(),
           "ingress": [{"request_id": r["request_id"], "received_ns": at(r["scheduled_ns"])} for r in (update, read)],
           "requests": [
               {"request_id": update["request_id"], "op": "update", "http_status": 200, "completed_ns": at(40),
                "raw_entry_accepted_ns": at(10), "raw_entry_started_ns": at(12), "durable_witness_ns": at(20),
                "applied_witness_ns": at(30), "visible_witness_ns": at(35)},
               {"request_id": read["request_id"], "op": "read", "http_status": 200, "completed_ns": at(150),
                "search_observation_status": "selected", "snapshot_as_of_ns": at(35), "quality_passed": True}],
           "queue_samples": [{"at_unix_ns": at(900), "depth": 0}], "costs_at_unix_ns": at(900),
           "costs": [{"name": n, "bytes": 1} for n in A.COSTS], "repairs": [], "observed_until_unix_ns": at(1000)}
    return raw_config, workload, raw


def summary(raw_config, workload, raw):
    ledger, observations = A.project(raw_config, workload, raw)
    return S.summarize(raw_config, workload, ledger, observations), G.parse(observations)


class AdapterTests(unittest.TestCase):
    def test_real_shape_reconciles_and_keeps_times_relative(self):
        result, observations = summary(*inputs())
        self.assertTrue(result["reconciliation"]["complete"])
        update = observations["outcomes"][0]
        self.assertEqual((update["accepted_ns"], update["durable_ns"], update["visible_ns"]), (10, 20, 35))
        self.assertEqual(observations["outcomes"][1]["snapshot_ns"], 35)

    def test_missing_durable_readback_keeps_only_the_admission_prefix(self):
        raw_config, workload, raw = inputs()
        raw["requests"][0]["durable_witness_ns"] = None
        result, observations = summary(raw_config, workload, raw)
        update = observations["outcomes"][0]
        self.assertEqual(update["accepted_ns"], 10)
        self.assertEqual((update["durable_ns"], update["applied_ns"], update["visible_ns"]), (None, None, None))
        self.assertTrue(result["reconciliation"]["complete"])

    def test_repair_needs_a_hold_seen_inside_its_interval(self):
        raw_config, workload, raw = inputs()
        repair = {"repair_id": "r", "start_ns": ANCHOR + 200, "end_ns": ANCHOR + 260, "published_ns": ANCHOR + 270,
                  "validation_sha256": "5" * 64, "hold_witnessed_ns": ANCHOR + 240}
        raw["repairs"] = [repair]
        result, _ = summary(raw_config, workload, raw)
        self.assertTrue(result["v2_repair_coverage_ready"])
        for held in (ANCHOR + 199, ANCHOR + 271, None):
            raw["repairs"] = [dict(repair, hold_witnessed_ns=held)]
            with self.subTest(held=held), self.assertRaises(G.LoadError):
                A.project(raw_config, workload, raw)

    def test_altered_runs_are_refused(self):
        for name, edit in {
            "extra ingress": lambda r: r["ingress"].append(dict(r["ingress"][0], request_id="x" * 64)),
            "duplicate ingress": lambda r: r["ingress"].append(copy.deepcopy(r["ingress"][0])),
            "relabeled read": lambda r: r["requests"][1].update(op="update"),
            "background op": lambda r: r["requests"][1].update(op="privacy"),
            "swapped config": lambda r: r.update(config_sha256="0" * 64),
        }.items():
            raw_config, workload, raw = inputs()
            edit(raw)
            with self.subTest(name), self.assertRaises(G.LoadError):
                A.project(raw_config, workload, raw)


if __name__ == "__main__":
    unittest.main()
