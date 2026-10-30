"""Separate v2 synthetic corpus. Original v1/40 corpus remains immutable externally."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("sys05_followup_summary", HERE / "summarize_load.py")
S = importlib.util.module_from_spec(spec)
spec.loader.exec_module(S)
G = S.G


def encode(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()


def config():
    return {"allowed_freshness_ns": 10, "arrival_schedule": [
        {"count": 1, "offset_ns": i * 100, "operation": op, "spacing_ns": 0}
        for i, op in enumerate(["read", "update", "read", "update", "privacy", "embedding"])],
        "batch_limit": 2, "cache": "cold", "concurrency_limit": 1, "corpus_sha256": "3" * 64,
        "deadline_ns": 100, "duration_ns": 1000, "fault_schedule": [], "mode": "explicit-sync",
        "model_sha256": "2" * 64, "protocol": "sys05-load-config-v1", "quality_criterion_sha256": "1" * 64,
        "queue_admission_limit": 3, "retry_limit": 2, "run_id": "independent-handwritten",
        "scenario": "normal", "seed": 19, "start_ns": 1000}


def valid_inputs(cfg=None):
    raw = encode(cfg or config())
    workload = G.generate(raw)
    requests = [json.loads(line) for line in workload.splitlines()]
    ledger = [{"protocol": "sys05-ledger-v1", "request": row, "received_ns": row["scheduled_ns"]} for row in requests]
    statuses = ["success", "success", "timeout", "failed", "pending", "success"]
    completed = [1020, 1150, None, 1320, None, 1510]
    rows = []
    for i, status in enumerate(statuses):
        at = requests[i]["scheduled_ns"]
        rows.append({"accepted_ns": at + 1, "applied_ns": {1: 1125, 5: 1505}.get(i),
            "attempts": [{"attempt": 1, "ended_ns": completed[i], "started_ns": at, "status": status}],
            "completed_ns": completed[i], "durable_ns": {1: 1102, 4: 1402, 5: 1502}.get(i),
            "enqueued_ns": {1: 1102, 3: 1302, 4: 1402, 5: 1502}.get(i),
            "fallback": None if i in [2, 4] else i == 3,
            "processing_started_ns": {1: 1105, 3: 1305, 5: 1503}.get(i),
            "quality_passed": True if i == 0 else None, "read_at_ns": 1010 if i == 0 else None,
            "request_id": requests[i]["request_id"], "snapshot_ns": 1004 if i == 0 else None,
            "status": status, "visible_ns": {1: 1140, 5: 1506}.get(i)})
    observations = {"background_coverage": [
        {"completed": 0, "eligible": 1, "kind": "privacy", "max_wait_ns": 1000, "starvation_observed": True},
        {"completed": 1, "eligible": 1, "kind": "embedding", "max_wait_ns": 2, "starvation_observed": False}],
        "config_sha256": hashlib.sha256(raw).hexdigest(), "cost_samples": [], "observed_until_ns": 2200,
        "origin": "synthetic_fixture", "outcomes": rows, "protocol": "sys05-observations-v2",
        "queue_samples": [{"at_ns": 2100, "depth": 2, "oldest_age_ns": 2000},
                          {"at_ns": 2200, "depth": None, "oldest_age_ns": None}],
        "repair_intervals": [repair(900, 1000, 1260, "republished", 1220, 1260),
                             dict(repair(1240, 1240, 2200, "still_blocked"), repair_id="b")]}
    dimensions = ["state_bytes", "receipt_bytes", "ledger_bytes", "checkpoint_bytes", "replay_bytes",
                  "source_bytes", "log_bytes", "db_bytes", "rss_bytes"]
    observations["cost_samples"] = [dict({name: i + 1 for i, name in enumerate(dimensions)}, at_ns=2100),
                                     dict({name: None for name in dimensions}, at_ns=2200)]
    return [raw, workload, ledger, observations]


def repair(origin, first, bound, state, validated=None, republished=None):
    return {"repair_id": "a", "blocked_ns": origin, "observed_from_ns": first,
            "observed_through_ns": bound, "end_state": state, "validated_ns": validated,
            "republished_ns": republished, "validation_sha256": "4" * 64 if validated is not None else None}


def packed(parts):
    raw, workload, ledger, observations = parts
    return raw, workload, b"".join(encode(row) + b"\n" for row in ledger), encode(observations)


def summarized(parts):
    return S.summarize(*packed(parts))


class LoadV2Tests(unittest.TestCase):
    def reject(self, action, code=None):
        with self.assertRaises(G.LoadError) as result:
            action()
        if code:
            self.assertEqual(result.exception.code, code)

    def test_valid_separate_six_request_corpus_literal_accounting(self):
        summary = summarized(valid_inputs())
        metrics = summary["metrics"]
        self.assertEqual(summary["protocol"], "sys05-summary-v2")
        self.assertEqual(metrics["status_counts"], {"success": 3, "rejected": 0, "timeout": 1, "failed": 1, "pending": 1})
        self.assertEqual(metrics["logical_request_denominator"], 6)
        self.assertEqual(metrics["unobserved_update_stage_gaps"], {"durable": 1, "applied": 2, "visible": 2})
        self.assertEqual([metrics["stages"][key]["observed"] for key in ["received", "accepted", "durable", "applied", "visible"]], [6, 6, 3, 2, 2])
        self.assertTrue(summary["processing_evidence"]["complete"])
        self.assertFalse(summary["whole_card_accepted"])

    def test_completed_success_distribution_literals_and_full_denominator(self):
        metrics = summarized(valid_inputs())["metrics"]
        self.assertEqual(metrics["completed_response_latency_ns"]["n"], 4)
        self.assertEqual(metrics["completed_response_latency_ns"]["mean"], {"quotient": 25, "remainder": 0, "denominator": 4})
        self.assertEqual(metrics["success_latency_ns"]["mean"], {"quotient": 26, "remainder": 2, "denominator": 3})
        self.assertEqual(metrics["completed_response_latency_ns"]["p99"], 50)
        self.assertEqual(metrics["unobserved_completion_count"], 2)
        self.assertEqual(metrics["throughput"]["all_planned_request_denominator"], 6)

    def test_original_invalid_positive_literal_is_now_negative(self):
        # Exact contradictory pending row from original author fixture; never exempt it.
        row = {"accepted_ns": 41, "durable_ns": 42, "applied_ns": 45, "visible_ns": None,
               "enqueued_ns": 41, "processing_started_ns": 48, "status": "pending", "completed_ns": None,
               "read_at_ns": None, "snapshot_ns": None,
               "attempts": [{"attempt": 1, "started_ns": 40, "ended_ns": None, "status": "pending"}]}
        self.reject(lambda: S._check_outcome(row, {"operation": "update"}, 40, 200, {"retry_limit": 2}), "invalid_time")

    def test_processing_applied_visible_each_observed_pair(self):
        for processing, applied, visible in [(1126, 1125, 1140), (1141, 1125, 1140), (1125, 1125, 1124)]:
            with self.subTest(processing=processing, applied=applied, visible=visible):
                parts = valid_inputs(); parts[3]["outcomes"][1].update(processing_started_ns=processing, applied_ns=applied, visible_ns=visible)
                self.reject(lambda: summarized(parts), "invalid_time")

    def test_processing_equal_boundary_and_missing_not_zero(self):
        parts = valid_inputs(); parts[3]["outcomes"][1]["processing_started_ns"] = 1125
        self.assertTrue(summarized(parts)["processing_evidence"]["complete"])
        parts[3]["outcomes"][1]["processing_started_ns"] = None
        result = summarized(parts)
        self.assertEqual(result["processing_evidence"]["unknown_processing_rows"], 1)
        self.assertFalse(result["processing_evidence"]["complete"])
        self.assertFalse(result["comparison_input_ready"])
        self.assertIsNone(parts[3]["outcomes"][1]["processing_started_ns"])

    def test_carry_in_actual_origin_full360_window260(self):
        parts = valid_inputs(); parts[3]["repair_intervals"] = [repair(900, 1000, 1260, "republished", 1220, 1260)]
        result = summarized(parts)["repair"]; row = result["intervals"][0]
        self.assertEqual((row["blocked_ns"], row["full_interval_ns"], row["interval_window_overlap_ns"], row["observed_window_outage_ns"]), (900, 360, 260, 260))
        self.assertEqual(result["union_observed_outage_ns"], 260)
        self.assertTrue(result["coverage_complete"])
        self.assertEqual(parts[3]["repair_intervals"][0]["blocked_ns"], 900)

    def test_repair_union_overlap_not_sum_and_queue_separate(self):
        result = summarized(valid_inputs())
        self.assertEqual(result["repair"]["union_observed_outage_ns"], 1200)
        self.assertEqual([r["observed_window_outage_ns"] for r in result["repair"]["intervals"]], [260, 960])
        self.assertEqual(result["metrics"]["queue_delay_ns"]["p99"], 3)
        self.assertTrue(result["repair"]["coverage_complete"])

    def test_unknown_origin_null_full_not_zero(self):
        parts = valid_inputs(); parts[3]["repair_intervals"] = [repair(None, 1000, 1260, "republished", 1220, 1260)]
        result = summarized(parts)["repair"]; row = result["intervals"][0]
        self.assertIsNone(row["full_interval_ns"])
        self.assertIsNone(row["interval_window_overlap_ns"])
        self.assertIsNone(row["full_observed_lower_bound_ns"])
        self.assertEqual(row["observed_window_outage_ns"], 260)
        self.assertTrue(result["incomplete"])
        self.assertEqual(result["unknown_origin_count"], 1)

    def test_unknown_end_bound_not_extended_to_horizon(self):
        parts = valid_inputs(); parts[3]["repair_intervals"] = [repair(900, 1000, 1250, "unknown")]
        result = summarized(parts)["repair"]; row = result["intervals"][0]
        self.assertEqual((row["full_observed_lower_bound_ns"], row["observed_window_outage_ns"]), (350, 250))
        self.assertIsNone(row["full_interval_ns"])
        self.assertIsNone(row["still_blocked_at_bound"])
        self.assertEqual(result["union_observed_outage_ns"], 250)
        self.assertTrue(result["incomplete"])

    def test_ongoing_known_bound_and_missing_coverage(self):
        parts = valid_inputs(); parts[3]["repair_intervals"] = [repair(900, 1000, 2200, "still_blocked")]
        row = summarized(parts)["repair"]["intervals"][0]
        self.assertIsNone(row["full_interval_ns"])
        self.assertEqual(row["full_observed_lower_bound_ns"], 1300)
        self.assertTrue(row["coverage_complete"])
        parts[3]["repair_intervals"] = []
        self.assertTrue(summarized(parts)["repair"]["incomplete"])
        self.assertIsNone(summarized(parts)["repair"]["union_observed_outage_ns"])

    def test_witness_gap_preserved_distinct_from_mathematical_overlap(self):
        parts = valid_inputs(); parts[3]["repair_intervals"] = [repair(900, 1050, 1260, "republished", 1220, 1260)]
        row = summarized(parts)["repair"]["intervals"][0]
        self.assertEqual((row["interval_window_overlap_ns"], row["observed_window_outage_ns"]), (260, 210))
        self.assertTrue(row["incomplete"])

    def test_future_out_of_window_end_bound_and_validation_reject(self):
        cases = [repair(2300, 1000, 1260, "republished", 1220, 1260),
                 repair(900, 1000, 2300, "still_blocked"), repair(800, 900, 950, "republished", 920, 950),
                 repair(900, 1000, 1260, "republished", None, 1260),
                 repair(900, 1000, 1260, "unknown", 1220, 1260),
                 repair(900, 1000, 1250, "republished", 1220, 1260),
                 repair(900, 1000, 1260, "republished", 899, 1260),
                 repair(G.MIN_I64, 1000, 1260, "republished", 1220, 1260)]
        for index, row in enumerate(cases):
            with self.subTest(case=index):
                parts = valid_inputs(); parts[3]["repair_intervals"] = [row]
                self.reject(lambda: summarized(parts))

    def test_zero_and_negative_origin_are_known_not_missing(self):
        for origin in [0, -100]:
            with self.subTest(origin=origin):
                parts = valid_inputs(); parts[3]["repair_intervals"] = [repair(origin, 1000, 1260, "republished", 1220, 1260)]
                row = summarized(parts)["repair"]["intervals"][0]
                self.assertTrue(row["origin_known"])
                self.assertEqual(row["full_interval_ns"], 1260 - origin)
                self.assertEqual(row["observed_window_outage_ns"], 260)

    def test_v1_closed_input_supported_but_not_fabricated_v2_provenance(self):
        parts = valid_inputs(); parts[3]["protocol"] = "sys05-observations-v1"
        parts[3]["repair_intervals"] = [{"repair_id": "a", "blocked_ns": 1200, "validated_ns": 1220,
                                        "republished_ns": 1260, "validation_sha256": "4" * 64}]
        result = summarized(parts)
        self.assertFalse(result["repair"]["coverage_complete"])
        self.assertEqual(result["repair"]["input_profile"], "sys05-observations-v1")
        self.assertFalse(result["comparison_input_ready"])

    def test_seed_exact_repeat_domains_and_crossmode_same_requests(self):
        cfg = config(); first = G.generate(encode(cfg)); self.assertEqual(first, G.generate(encode(cfg)))
        first_row = json.loads(first.splitlines()[0])
        self.assertEqual(first_row["request_id"], "a3e268a9640942f5e2b7abf1e62e4a48a441f275af662548aa111711c4aafc65")
        cfg.update(mode="snapshot", run_id="other"); other = G.generate(encode(cfg))
        def normalized(raw):
            return [{k: v for k, v in json.loads(line).items() if k != "config_sha256"} for line in raw.splitlines()]
        self.assertEqual(normalized(first), normalized(other))
        cfg["seed"] += 1; self.assertNotEqual(normalized(first), normalized(G.generate(encode(cfg))))

    def test_eight_scenarios_explicit_plan_not_service(self):
        for scenario in ["normal", "burst", "overload", "restart", "publish_delay", "store_failure", "cold", "warm"]:
            with self.subTest(scenario=scenario):
                cfg = config(); cfg["scenario"] = scenario
                if scenario in ["restart", "publish_delay", "store_failure"]:
                    cfg["fault_schedule"] = [{"kind": scenario, "offset_ns": 20, "duration_ns": 10}]
                if scenario == "warm": cfg["cache"] = "warm"
                self.assertEqual(len(G.parse_jsonl(G.generate(encode(cfg)), "request")), 6)

    def test_strict_json_duplicate_nonfinite_float_unicode_integer(self):
        for raw in [b'{"a":1,"a":2}', b'NaN', b'Infinity', b'1.0', b'9223372036854775808', b'"\\ud800"', b'\xff', b'{}{}']:
            with self.subTest(raw=raw): self.reject(lambda: G.parse(raw))
        self.reject(lambda: G.parse(b'[' * 33 + b'0' + b']' * 33))
        self.reject(lambda: G.parse(encode("가" * 1366)))

    def test_config_all_required_bools_unknown_and_schedule(self):
        for key in ["seed", "mode", "deadline_ns", "retry_limit", "batch_limit", "concurrency_limit", "queue_admission_limit", "allowed_freshness_ns", "quality_criterion_sha256", "model_sha256", "corpus_sha256"]:
            with self.subTest(missing=key):
                cfg = config(); del cfg[key]; self.reject(lambda: G.generate(encode(cfg)))
        for key, value in [("seed", True), ("duration_ns", False), ("unknown", 3), ("start_ns", G.MAX_I64)]:
            with self.subTest(key=key):
                cfg = config(); cfg[key] = value; self.reject(lambda: G.generate(encode(cfg)))
        cfg = config(); cfg["arrival_schedule"][0]["count"] = 100001
        self.reject(lambda: G.generate(encode(cfg)))

    def test_missing_duplicate_extra_mutated_ledger_and_outcomes(self):
        for kind in ["empty", "missing", "duplicate", "extra", "mutated", "duplicate_outcome", "extra_outcome"]:
            with self.subTest(kind=kind):
                parts = valid_inputs()
                if kind == "empty": parts[2].clear()
                elif kind == "missing": parts[2].pop()
                elif kind == "duplicate": parts[2].append(copy.deepcopy(parts[2][0]))
                elif kind == "extra":
                    row = copy.deepcopy(parts[2][0]); row["request"]["request_id"] = "f" * 64; parts[2].append(row)
                elif kind == "mutated": parts[2][0]["request"]["payload_sha256"] = "f" * 64
                elif kind == "duplicate_outcome": parts[3]["outcomes"].append(copy.deepcopy(parts[3]["outcomes"][0]))
                else: parts[3]["outcomes"][0]["request_id"] = "f" * 64
                self.reject(lambda: summarized(parts))

    def test_explicit_incomplete_keeps_all_denominators(self):
        parts = valid_inputs(); missing = parts[3]["outcomes"].pop()["request_id"]
        self.reject(lambda: summarized(parts), "incomplete")
        result = S.summarize(*packed(parts), allow_incomplete=True)
        self.assertEqual(result["reconciliation"]["missing_outcome_ids"], [missing])
        self.assertEqual(result["metrics"]["logical_request_denominator"], 6)
        self.assertFalse(result["comparison_input_ready"])

    def test_retry_counts_separate_and_overlap_reject(self):
        parts = valid_inputs(); row = parts[3]["outcomes"][3]
        row["attempts"].append({"attempt": 2, "started_ns": 1321, "ended_ns": 1340, "status": "failed"})
        row["completed_ns"] = 1340
        metrics = summarized(parts)["metrics"]
        self.assertEqual((metrics["attempt_count"], metrics["retry_count"], metrics["logical_request_denominator"]), (7, 1, 6))
        row["attempts"][1]["started_ns"] = 1319
        self.reject(lambda: summarized(parts), "invalid_time")

    def test_freshness_unknown_violation_future_and_quality(self):
        result = summarized(valid_inputs())["metrics"]
        self.assertEqual(result["freshness"]["unknown"], 1)
        self.assertEqual(result["quality"]["unknown"], 1)
        parts = valid_inputs(); parts[3]["outcomes"][0]["snapshot_ns"] = 990
        self.assertEqual(summarized(parts)["metrics"]["freshness"]["violations"], 1)
        parts[3]["outcomes"][0]["snapshot_ns"] = 1011
        self.reject(lambda: summarized(parts), "invalid_snapshot")

    def test_backlog_oldest_cost9_and_background_unknown(self):
        result = summarized(valid_inputs())
        self.assertEqual(result["backlog"]["oldest_age_ns"]["distribution"]["maximum"], 2000)
        self.assertEqual(result["backlog"]["depth"]["unknown_samples"], 1)
        self.assertEqual(len(result["costs"]), 9)
        for name, row in result["costs"].items():
            with self.subTest(cost=name):
                self.assertEqual(row["distribution"]["n"], 1); self.assertEqual(row["unknown_samples"], 1); self.assertIsNone(row["last"])
        self.assertTrue(result["background"]["privacy"]["starvation_observed"])
        parts = valid_inputs(); parts[3]["background_coverage"][1].update(eligible=None, completed=None, max_wait_ns=None, starvation_observed=None)
        self.assertIsNone(summarized(parts)["background"]["embedding"]["starvation_observed"])

    def test_comparison_same_inputs_quality_freshness_and_provenance(self):
        left = valid_inputs(); cfg = config(); cfg.update(mode="snapshot", run_id="other"); right = valid_inputs(cfg)
        for parts in [left, right]: parts[3]["outcomes"][2].update(read_at_ns=1210, snapshot_ns=1205, quality_passed=True)
        self.assertTrue(S.compare(packed(left), packed(right))["comparison_ready"])
        self.assertEqual(S.compare(packed(left), packed(right))["superiority_conclusion"], "not_evaluated")
        for key, value in [("allowed_freshness_ns", 20), ("model_sha256", "f" * 64), ("quality_criterion_sha256", "f" * 64), ("cache", "warm")]:
            with self.subTest(key=key):
                other = copy.deepcopy(cfg); other[key] = value; modified = valid_inputs(other)
                self.assertFalse(S.compare(packed(left), packed(modified))["comparison_ready"])
        right[3]["outcomes"][1]["processing_started_ns"] = None
        self.assertFalse(S.compare(packed(left), packed(right))["comparison_ready"])

    def test_percentiles_no_samples_are_null_and_exact_mean(self):
        empty = S.distribution([])
        self.assertEqual(empty["n"], 0)
        for key in ["minimum", "maximum", "mean", "p50", "p95", "p99"]: self.assertIsNone(empty[key])
        values = S.distribution(list(range(1, 22)))
        self.assertEqual((values["p50"], values["p95"], values["p99"]), (11, 20, 21))

    def test_exclusive_publication_symlink_and_actual_cli(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); parts = packed(valid_inputs())
            for name, raw in zip(["config", "workload", "ledger", "observations"], parts): (root / name).write_bytes(raw)
            output = root / "summary"
            command = [sys.executable, str(HERE / "summarize_load.py")]
            for name in ["config", "workload", "ledger", "observations"]: command += ["--" + name, str(root / name)]
            command += ["--out", str(output)]
            result = subprocess.run(command, cwd=root, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stdout)
            self.assertEqual(json.loads(output.read_bytes())["protocol"], "sys05-summary-v2")
            before = output.read_bytes(); result = subprocess.run(command, cwd=root, capture_output=True)
            self.assertEqual(result.returncode, 2); self.assertEqual(output.read_bytes(), before)
            alias = root / "alias"; alias.symlink_to(output)
            self.reject(lambda: G.read_raw(str(alias)), "io_error")
            self.reject(lambda: G.publish_new("relative", b"data"), "unsafe_path")


    def test_signed_epoch_known_zero_and_minimum(self):
        cfg = config()
        cfg["start_ns"] = G.MIN_I64
        rows = G.parse_jsonl(G.generate(encode(cfg)), "request")
        self.assertEqual(rows[0]["scheduled_ns"], G.MIN_I64)
        cfg["start_ns"] = 0
        self.assertEqual(G.parse_jsonl(G.generate(encode(cfg)), "request")[0]["scheduled_ns"], 0)

    def test_safe_publication_exclusive_and_symlink(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); output = root / "result.json"
            G.publish_new(str(output), b"safe")
            self.assertEqual(G.read_raw(str(output)), b"safe")
            self.reject(lambda: G.publish_new(str(output), b"overwrite"), "io_error")
            link = root / "link"; link.symlink_to(output)
            self.reject(lambda: G.read_raw(str(link)), "io_error")
            folder = root / "folder"; folder.mkdir(); alias = root / "alias"; alias.symlink_to(folder)
            self.reject(lambda: G.publish_new(str(alias / "new"), b"data"), "io_error")
            self.assertFalse((folder / "new").exists())
            self.reject(lambda: G.publish_new("relative", b"data"), "unsafe_path")
            self.assertEqual(sorted(x.name for x in root.iterdir()), ["alias", "folder", "link", "result.json"])

    def test_dependency_failure_cli_has_fixed_error_no_traceback(self):
        with tempfile.TemporaryDirectory() as temporary:
            script = Path(temporary) / "summarize_load.py"
            script.write_bytes((HERE / "summarize_load.py").read_bytes())
            result = subprocess.run([sys.executable, str(script)], capture_output=True)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(json.loads(result.stdout), {"error": "internal_error", "path": "$"})
            self.assertEqual(result.stderr, b"")

    def test_contract_tampering_cli_fails_before_publication(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name in ["generate_workload.py", "load_contract.json"]:
                (root / name).write_bytes((HERE / name).read_bytes())
            (root / "load_contract.json").write_bytes(b"{}")
            (root / "config").write_bytes(encode(config()))
            result = subprocess.run([sys.executable, str(root / "generate_workload.py"),
                                     "--config", str(root / "config"), "--out", str(root / "output")],
                                    cwd=root, capture_output=True)
            self.assertEqual((result.returncode, json.loads(result.stdout)["error"]), (2, "contract_mismatch"))
            self.assertFalse((root / "output").exists())
            self.assertEqual(result.stderr, b"")

    def test_concurrent_exclusive_publication_one_winner(self):
        import concurrent.futures
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "published"
            def publish(value):
                try:
                    G.publish_new(str(output), value)
                    return "created"
                except G.LoadError as error:
                    return error.code
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as workers:
                results = list(workers.map(publish, [b"alpha", b"beta"]))
            self.assertEqual(sorted(results), ["created", "io_error"])
            self.assertIn(output.read_bytes(), [b"alpha", b"beta"])
            self.assertEqual([x.name for x in output.parent.iterdir()], ["published"])

    def test_nonregular_input_and_missing_file_fixed_cli_error(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.reject(lambda: G.read_raw(str(root)), "unsafe_path")
            missing = root / "private-missing-config"
            result = subprocess.run([sys.executable, str(HERE / "generate_workload.py"),
                                     "--config", str(missing), "--out", str(root / "out")],
                                    cwd=root, capture_output=True)
            self.assertEqual((result.returncode, json.loads(result.stdout)["error"]), (2, "io_error"))
            self.assertNotIn(str(root).encode(), result.stdout + result.stderr)
            self.assertNotIn(b"Traceback", result.stdout + result.stderr)
            self.assertFalse((root / "out").exists())

    def test_preserved_same_time_order_payload_hash_and_byte_labels(self):
        cfg = config(); cfg["arrival_schedule"] = [{"count": 5, "offset_ns": 0, "operation": "read", "spacing_ns": 0}]
        rows = G.parse_jsonl(G.generate(encode(cfg)), "request")
        self.assertEqual([r["request_id"] for r in rows], sorted(r["request_id"] for r in rows))
        self.assertEqual(rows[0]["payload_sha256"], hashlib.sha256(("sys05-payload-v1\0" + "3" * 64 + "\0" + rows[0]["request_id"]).encode()).hexdigest())
        cfg["mode"] = "가" * 85; self.assertEqual(len(G.parse_jsonl(G.generate(encode(cfg)), "request")), 5)
        cfg["mode"] += "가"; self.reject(lambda: G.generate(encode(cfg)), "invalid_value")
        parts = valid_inputs(); parts[3]["outcomes"][0].update(read_at_ns=1000, snapshot_ns=G.MIN_I64)
        self.reject(lambda: summarized(parts), "invalid_snapshot")

    def test_preserved_fault_cache_and_final_arrival_guards(self):
        for mode in ["missing_fault", "cache", "late_arrival", "fault_duration"]:
            with self.subTest(mode=mode):
                cfg = config()
                if mode == "missing_fault": cfg["scenario"] = "restart"
                elif mode == "cache": cfg["scenario"] = "warm"
                elif mode == "late_arrival": cfg["arrival_schedule"][0]["offset_ns"] = 1000
                else: cfg["fault_schedule"] = [{"kind": "restart", "offset_ns": 999, "duration_ns": 2}]
                self.reject(lambda: G.generate(encode(cfg)))

    def test_preserved_attempt_status_number_pending_and_retry_limits(self):
        for mode in ["number", "pending_middle", "retry_limit", "boolean", "status"]:
            with self.subTest(mode=mode):
                parts = valid_inputs(); row = parts[3]["outcomes"][0]
                if mode == "number": row["attempts"][0]["attempt"] = 2
                elif mode == "pending_middle": row["attempts"].insert(0, {"attempt": 1, "started_ns": 1000, "ended_ns": None, "status": "pending"})
                elif mode == "retry_limit": row["attempts"] *= 4
                elif mode == "boolean": row["attempts"][0]["attempt"] = True
                else: row["status"] = "failed"
                self.reject(lambda: summarized(parts))

    def test_preserved_stage_prefix_read_applicability_and_success_acceptance(self):
        for field, value in [("accepted_ns", None), ("durable_ns", 1002), ("accepted_ns", 1021),
                             ("read_at_ns", 1021), ("completed_ns", 2300)]:
            with self.subTest(field=field, value=value):
                parts = valid_inputs(); parts[3]["outcomes"][0][field] = value
                self.reject(lambda: summarized(parts))

    def test_preserved_input_binding_closed_observation_and_missing_receipt(self):
        for mode in ["workload", "config", "unknown", "boolean", "origin", "empty_ledger"]:
            with self.subTest(mode=mode):
                parts = valid_inputs()
                if mode == "workload": parts[1] += b"\n"
                elif mode == "config": parts[3]["config_sha256"] = "f" * 64
                elif mode == "unknown": parts[3]["unknown"] = 1
                elif mode == "boolean": parts[3]["observed_until_ns"] = True
                elif mode == "origin": parts[3]["origin"] = "actual_verified"
                else: parts[2].clear()
                self.reject(lambda: S.summarize(*packed(parts), allow_incomplete=True))
        parts = valid_inputs(); missing = parts[2].pop()["request"]["request_id"]; parts[3]["outcomes"].pop()
        result = S.summarize(*packed(parts), allow_incomplete=True)
        self.assertEqual(result["reconciliation"]["missing_received_ids"], [missing])
        self.assertEqual(result["metrics"]["logical_request_denominator"], 6)

    def test_preserved_backlog_background_and_unknown_coverage_guards(self):
        for mode in ["zero_depth", "missing_background", "background_overcount", "false_starvation_missing"]:
            with self.subTest(mode=mode):
                parts = valid_inputs()
                if mode == "zero_depth": parts[3]["queue_samples"][0].update(depth=0, oldest_age_ns=None)
                elif mode == "missing_background": parts[3]["background_coverage"].pop()
                elif mode == "background_overcount": parts[3]["background_coverage"][0]["completed"] = 2
                else: parts[3]["background_coverage"][0].update(starvation_observed=False, max_wait_ns=None)
                self.reject(lambda: summarized(parts))
        parts = valid_inputs(); parts[3]["queue_samples"] = []
        self.assertTrue(summarized(parts)["backlog"]["missing_coverage"])
        parts[3]["cost_samples"] = []
        self.assertTrue(summarized(parts)["costs"]["rss_bytes"]["missing_coverage"])

    def test_preserved_comparison_window_corpus_quality_and_closed_repair(self):
        left = valid_inputs(); cfg = config(); cfg.update(mode="snapshot", run_id="other"); right = valid_inputs(cfg)
        for parts in [left, right]: parts[3]["outcomes"][2].update(read_at_ns=1210, snapshot_ns=1205, quality_passed=True)
        self.assertTrue(S.compare(packed(left), packed(right))["comparison_ready"])
        right[3]["observed_until_ns"] = 2201
        self.assertFalse(S.compare(packed(left), packed(right))["comparison_ready"])
        right[3]["observed_until_ns"] = 2200; right[3]["outcomes"][0]["quality_passed"] = False
        self.assertFalse(S.compare(packed(left), packed(right))["comparison_ready"])
        cfg["corpus_sha256"] = "f" * 64
        self.assertFalse(S.compare(packed(left), packed(valid_inputs(cfg)))["comparison_ready"])
        for value in [True, "unknown"]:
            with self.subTest(blocked=value):
                parts = valid_inputs(); parts[3]["repair_intervals"][0]["blocked_ns"] = value
                self.reject(lambda: summarized(parts))

    def test_preserved_cli_fixed_errors_without_private_path(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            result = subprocess.run([sys.executable, str(HERE / "generate_workload.py"), "--private-arg", str(root)], capture_output=True)
            self.assertEqual((result.returncode, json.loads(result.stdout)["error"]), (2, "invalid_cli"))
            self.assertNotIn(str(root).encode(), result.stdout + result.stderr)
            self.assertNotIn(b"Traceback", result.stdout + result.stderr)


    def test_explicit_profile_no_v2_downgrade_and_mixed_reject(self):
        left = valid_inputs(); right_config = config(); right_config.update(mode="snapshot", run_id="other"); right = valid_inputs(right_config)
        for parts in [left, right]: parts[3]["outcomes"][2].update(read_at_ns=1210, snapshot_ns=1205, quality_passed=True)
        right[3]["protocol"] = "sys05-observations-v1"
        right[3]["repair_intervals"] = [{"repair_id": "a", "blocked_ns": 1200, "validated_ns": 1220, "republished_ns": 1260, "validation_sha256": "4" * 64}]
        result = summarized(right)
        self.assertEqual(result["comparison_profile"], "reported_window_v1")
        self.assertEqual(result["comparison_scope"], "legacy_within_window_reported_only")
        self.assertTrue(result["comparison_input_ready"])
        self.assertFalse(result["v2_repair_coverage_ready"])
        self.assertFalse(result["B_execution_ready"])
        self.reject(lambda: S.compare(packed(left), packed(right)), "comparison_profile_mismatch")
        left[3]["repair_intervals"][0]["blocked_ns"] = None
        unknown = summarized(left)
        self.assertEqual(unknown["comparison_profile"], "explicit_window_witness_v2")
        self.assertFalse(unknown["comparison_input_ready"])


if __name__ == "__main__":
    unittest.main()
