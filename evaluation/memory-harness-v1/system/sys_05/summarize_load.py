"""Offline SYS-05 ledger reconciliation; reported observations are not service proof."""
import importlib.util
import json
from pathlib import Path
import sys


def _load_generator():
    path = Path(__file__).resolve().with_name("generate_workload.py")
    spec = importlib.util.spec_from_file_location("sys05_generate", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


try:
    G = _load_generator()
except Exception:
    if __name__ == "__main__":
        print('{"error":"internal_error","path":"$"}')
        sys.exit(2)
    raise RuntimeError("sys05_dependency_error") from None
STAGES = ("accepted_ns", "durable_ns", "applied_ns", "visible_ns")
STATUSES = ("success", "rejected", "timeout", "failed", "pending")


def distribution(values):
    values = sorted(values)
    count = len(values)
    total = sum(values)
    return {"n": count, "minimum": values[0] if count else None,
            "maximum": values[-1] if count else None,
            "mean": {"quotient": total // count, "remainder": total % count,
                     "denominator": count} if count else None,
            "p50": values[(50 * count + 99) // 100 - 1] if count else None,
            "p95": values[(95 * count + 99) // 100 - 1] if count else None,
            "p99": values[(99 * count + 99) // 100 - 1] if count else None,
            "percentile_definition": "nearest_rank_ceil_pN"}


def _within(value, low, high, path):
    if value is not None:
        G.require(low <= value <= high, "invalid_time", path)


def _index(rows, key, path):
    result = {}
    for row in rows:
        identity = row[key]
        G.require(identity not in result, "duplicate_record", path)
        result[identity] = row
    return result


def _check_attempts(row, received, horizon, retry_limit):
    attempts = row["attempts"]
    G.require(1 <= len(attempts) <= retry_limit + 1, "invalid_attempts", "$/outcomes/attempts")
    previous_end = received
    for index, attempt in enumerate(attempts):
        G.require(attempt["attempt"] == index + 1 and previous_end is not None,
                  "invalid_attempts", "$/outcomes/attempts")
        _within(attempt["started_ns"], previous_end, horizon, "$/outcomes/attempts/started_ns")
        _within(attempt["ended_ns"], attempt["started_ns"], horizon, "$/outcomes/attempts/ended_ns")
        last = index == len(attempts) - 1
        if attempt["status"] == "pending":
            G.require(last and attempt["ended_ns"] is None, "invalid_attempts", "$/outcomes/attempts")
        elif attempt["ended_ns"] is None:
            G.require(last and attempt["status"] == "timeout", "invalid_attempts", "$/outcomes/attempts")
        previous_end = attempt["ended_ns"]
    G.require(attempts[-1]["status"] == row["status"] and row["completed_ns"] == previous_end,
              "conflicting_result", "$/outcomes/attempts")
    if row["status"] == "success":
        G.require(row["completed_ns"] is not None, "invalid_attempts", "$/outcomes/completed_ns")


def _check_outcome(row, request, received, horizon, config):
    _check_attempts(row, received, horizon, config["retry_limit"])
    previous = received
    absent = False
    for stage in STAGES:
        value = row[stage]
        if value is None:
            absent = True
        else:
            G.require(not absent, "stage_gap", "$/outcomes/" + stage)
            _within(value, previous, horizon, "$/outcomes/" + stage)
            previous = value
    if row["status"] == "rejected":
        G.require(all(row[x] is None for x in STAGES), "conflicting_result", "$/outcomes/status")
    if row["status"] == "success":
        G.require(row["accepted_ns"] is not None, "stage_gap", "$/outcomes/accepted_ns")
    if row["completed_ns"] is not None and row["accepted_ns"] is not None:
        G.require(row["accepted_ns"] <= row["completed_ns"], "invalid_time", "$/outcomes/accepted_ns")

    for later in ("applied_ns", "visible_ns"):
        if row["processing_started_ns"] is not None and row[later] is not None:
            G.require(row["processing_started_ns"] <= row[later], "invalid_time", "$/outcomes/" + later)
    if request["operation"] == "read":
        G.require(all(row[x] is None for x in STAGES[1:]), "not_applicable", "$/outcomes")
    else:
        G.require(row["read_at_ns"] is None and row["snapshot_ns"] is None,
                  "not_applicable", "$/outcomes/snapshot_ns")
    for name in ("enqueued_ns", "processing_started_ns", "read_at_ns"):
        _within(row[name], received, horizon, "$/outcomes/" + name)
    if row["enqueued_ns"] is not None:
        G.require(row["accepted_ns"] is not None and row["enqueued_ns"] >= row["accepted_ns"],
                  "stage_gap", "$/outcomes/enqueued_ns")
    if row["processing_started_ns"] is not None:
        G.require(row["enqueued_ns"] is not None and row["processing_started_ns"] >= row["enqueued_ns"],
                  "stage_gap", "$/outcomes/processing_started_ns")
    if row["snapshot_ns"] is not None:
        G.require(row["read_at_ns"] is not None and row["snapshot_ns"] <= row["read_at_ns"]
                  and row["read_at_ns"] - row["snapshot_ns"] <= G.MAX_I64,
                  "invalid_snapshot", "$/outcomes/snapshot_ns")
    if row["completed_ns"] is not None and row["read_at_ns"] is not None:
        G.require(row["read_at_ns"] <= row["completed_ns"], "invalid_time", "$/outcomes/read_at_ns")


def _reconcile(workload, ledger, outcomes, horizon, config, allow_incomplete):
    expected = _index(workload, "request_id", "$/workload")
    received = {}
    for line in ledger:
        row = line["request"]
        identity = row["request_id"]
        G.require(identity not in received, "duplicate_record", "$/ledger")
        G.require(identity in expected, "extra_record", "$/ledger")
        G.require(row == expected[identity], "request_mismatch", "$/ledger/request")
        _within(line["received_ns"], row["scheduled_ns"], horizon, "$/ledger/received_ns")
        received[identity] = line["received_ns"]
    results = _index(outcomes, "request_id", "$/outcomes")
    G.require(set(results) <= set(received), "extra_record", "$/outcomes")
    missing_received = sorted(set(expected) - set(received))
    missing_results = sorted(set(received) - set(results))
    G.require(allow_incomplete or not (missing_received or missing_results), "incomplete", "$")
    for identity, row in results.items():
        _check_outcome(row, expected[identity], received[identity], horizon, config)
    return expected, received, results, missing_received, missing_results


def _queue(samples, start, horizon):
    last = start
    for row in samples:
        _within(row["at_ns"], last, horizon, "$/queue_samples/at_ns")
        last = row["at_ns"]
        if row["depth"] == 0:
            G.require(row["oldest_age_ns"] == 0, "invalid_backlog", "$/queue_samples")
    def values(field):
        known = [x[field] for x in samples if x[field] is not None]
        return {"distribution": distribution(known), "unknown_samples": len(samples) - len(known),
                "last": samples[-1][field] if samples else None}
    return {"samples": len(samples), "missing_coverage": not bool(samples),
            "depth": values("depth"), "oldest_age_ns": values("oldest_age_ns")}


def _costs(samples, start, horizon):
    last = start
    for row in samples:
        _within(row["at_ns"], last, horizon, "$/cost_samples/at_ns")
        last = row["at_ns"]
    result = {}
    for name in G.contract()["cost_dimensions"]:
        values = [row[name] for row in samples if row[name] is not None]
        result[name] = {"distribution": distribution(values), "unknown_samples": len(samples) - len(values),
                        "last": samples[-1][name] if samples else None,
                        "missing_coverage": not bool(samples)}
    return result


def _repairs_v1(rows, start, horizon):
    _index(rows, "repair_id", "$/repair_intervals")
    intervals = []
    output = []
    for row in rows:
        _within(row["blocked_ns"], start, horizon, "$/repair_intervals/blocked_ns")
        _within(row["validated_ns"], row["blocked_ns"], horizon, "$/repair_intervals/validated_ns")
        G.require((row["validated_ns"] is None) == (row["validation_sha256"] is None),
                  "missing_validation", "$/repair_intervals/validation_sha256")
        if row["republished_ns"] is not None:
            G.require(row["validated_ns"] is not None, "missing_validation", "$/repair_intervals/republished_ns")
            _within(row["republished_ns"], row["validated_ns"], horizon, "$/repair_intervals/republished_ns")
        end = row["republished_ns"] if row["republished_ns"] is not None else horizon
        intervals.append((row["blocked_ns"], end))
        output.append({"repair_id": row["repair_id"], "observed_outage_ns": end - row["blocked_ns"],
                       "still_blocked": row["republished_ns"] is None})
    union = 0
    right = start
    for left, end in sorted(intervals):
        union += max(0, end - max(left, right))
        right = max(right, end)
    return {"intervals": output, "reported_events": len(rows), "union_observed_outage_ns": union,
            "no_interval_is_not_proof_no_repair": True}



def _repair_v2(row, start, horizon):
    first = row["observed_from_ns"]
    bound = row["observed_through_ns"]
    _within(first, start, horizon, "$/repair_intervals/observed_from_ns")
    _within(bound, first, horizon, "$/repair_intervals/observed_through_ns")
    origin = row["blocked_ns"]
    if origin is not None:
        G.require(origin <= first, "invalid_time", "$/repair_intervals/blocked_ns")
        G.require(bound - origin <= G.MAX_I64, "time_overflow", "$/repair_intervals/blocked_ns")
    validation = row["validated_ns"]
    G.require((validation is None) == (row["validation_sha256"] is None),
              "missing_validation", "$/repair_intervals/validation_sha256")
    if validation is not None:
        _within(validation, origin if origin is not None else G.MIN_I64, bound,
                "$/repair_intervals/validated_ns")
    end = row["republished_ns"]
    if row["end_state"] == "republished":
        G.require(end is not None and validation is not None, "missing_validation", "$/repair_intervals/republished_ns")
        _within(end, max(start, validation), horizon, "$/repair_intervals/republished_ns")
        G.require(end == bound, "invalid_time", "$/repair_intervals/observed_through_ns")
    else:
        G.require(end is None, "conflicting_result", "$/repair_intervals/republished_ns")
    full = end - origin if end is not None and origin is not None else None
    overlap = max(0, min(end, horizon) - max(origin, start)) if full is not None else None
    complete = (origin is not None and first == max(origin, start)
                and (end is not None or row["end_state"] == "still_blocked" and bound == horizon))
    return {"repair_id": row["repair_id"], "blocked_ns": origin, "origin_known": origin is not None,
            "observed_from_ns": first, "observed_through_ns": bound, "end_state": row["end_state"],
            "full_interval_ns": full, "full_observed_lower_bound_ns": bound - origin if origin is not None else None,
            "interval_window_overlap_ns": overlap, "observed_window_outage_ns": bound - first,
            "still_blocked_at_bound": True if row["end_state"] == "still_blocked" else False if end is not None else None,
            "coverage_complete": complete, "incomplete": not complete}


def _repairs_v2(rows, start, horizon):
    _index(rows, "repair_id", "$/repair_intervals")
    output = [_repair_v2(row, start, horizon) for row in rows]
    union = 0
    right = start
    for row in sorted(output, key=lambda x: (x["observed_from_ns"], x["observed_through_ns"])):
        left, end = row["observed_from_ns"], row["observed_through_ns"]
        union += max(0, end - max(left, right))
        right = max(right, end)
    complete = bool(output) and all(row["coverage_complete"] for row in output)
    return {"intervals": output, "reported_events": len(rows), "union_observed_outage_ns": union if output else None,
            "coverage_complete": complete, "incomplete": not complete,
            "unknown_origin_count": sum(not row["origin_known"] for row in output),
            "unknown_end_count": sum(row["end_state"] == "unknown" for row in output),
            "no_interval_is_not_proof_no_repair": True, "input_profile": "sys05-observations-v2"}


def _repair_summary(observations, start, horizon):
    if observations["protocol"] == "sys05-observations-v2":
        return _repairs_v2(observations["repair_intervals"], start, horizon)
    result = _repairs_v1(observations["repair_intervals"], start, horizon)
    result.update(coverage_complete=False, incomplete=True, input_profile="sys05-observations-v1",
                  legacy_limit="No explicit observation bound/end witness or carry-in provenance")
    return result


def _processing_completeness(results):
    unknown = sum(row["processing_started_ns"] is None
                  and (row["applied_ns"] is not None or row["visible_ns"] is not None)
                  for row in results.values())
    return {"unknown_processing_rows": unknown, "complete": unknown == 0,
            "missing_is_known_zero": False}


def _background(rows):
    result = _index(rows, "kind", "$/background_coverage")
    G.require(set(result) == {"privacy", "embedding"}, "incomplete_background", "$/background_coverage")
    for row in rows:
        if row["completed"] is not None:
            G.require(row["eligible"] is not None and row["completed"] <= row["eligible"],
                      "conflicting_coverage", "$/background_coverage")
        if row["eligible"] == 0:
            G.require(row["completed"] == 0 and row["max_wait_ns"] is None
                      and row["starvation_observed"] is None,
                      "conflicting_coverage", "$/background_coverage")
        if row["starvation_observed"] is False:
            G.require(row["eligible"] is not None and row["eligible"] > 0
                      and row["completed"] is not None and row["max_wait_ns"] is not None,
                      "incomplete_background", "$/background_coverage")
    return result


def _request_metrics(expected, received, results, config):
    counts = {status: sum(row["status"] == status for row in results.values()) for status in STATUSES}
    completed = [row["completed_ns"] - received[key] for key, row in results.items()
                 if row["completed_ns"] is not None]
    success = [row["completed_ns"] - received[key] for key, row in results.items() if row["status"] == "success"]
    queue_delay = [row["processing_started_ns"] - row["enqueued_ns"] for row in results.values()
                   if row["processing_started_ns"] is not None]
    reads = [key for key in received if expected[key]["operation"] == "read"]
    ages = [results[key]["read_at_ns"] - results[key]["snapshot_ns"] for key in reads
            if key in results and results[key]["snapshot_ns"] is not None]
    quality_true = sum(key in results and results[key]["quality_passed"] is True for key in reads)
    quality_false = sum(key in results and results[key]["quality_passed"] is False for key in reads)
    stages = {"received": {"observed": len(received), "applicable": len(expected)}}
    for name in STAGES:
        applicable = [key for key in received if name == "accepted_ns" or expected[key]["operation"] != "read"]
        stages[name.removesuffix("_ns")] = {"observed": sum(key in results and results[key][name] is not None
                                                          for key in applicable), "applicable": len(applicable)}
    return {"status_counts": counts, "logical_request_denominator": len(expected),
            "received_denominator": len(received), "outcome_rows": len(results),
            "unobserved_received_outcomes": len(received) - len(results), "stages": stages,
            "unobserved_update_stage_gaps": {name: sum(key in results and results[key]["accepted_ns"] is not None
                for key in received if expected[key]["operation"] != "read") - sum(
                key in results and results[key][name + "_ns"] is not None for key in received
                if expected[key]["operation"] != "read") for name in ("durable", "applied", "visible")},
            "completed_response_latency_ns": distribution(completed), "success_latency_ns": distribution(success),
            "unobserved_completion_count": len(received) - len(completed),
            "queue_delay_ns": distribution(queue_delay),
            "queue_delay_unknown": len(received) - len(queue_delay),
            "attempt_count": sum(len(row["attempts"]) for row in results.values()),
            "retry_count": sum(len(row["attempts"]) - 1 for row in results.values()),
            "fallback_true": sum(row["fallback"] is True for row in results.values()),
            "fallback_unknown": len(received) - sum(row["fallback"] is not None for row in results.values()),
            "freshness": {"allowed_ns": config["allowed_freshness_ns"], "read_denominator": len(reads),
                          "age_ns": distribution(ages), "unknown": len(reads) - len(ages),
                          "violations": sum(age > config["allowed_freshness_ns"] for age in ages)},
            "quality": {"criterion_sha256": config["quality_criterion_sha256"], "true": quality_true,
                        "false": quality_false, "unknown": len(reads) - quality_true - quality_false}}


def summarize(raw_config, raw_workload, raw_ledger, raw_observations, allow_incomplete=False):
    G.require(type(allow_incomplete) is bool, "invalid_type", "$/allow_incomplete")
    config = G.load_config(raw_config)
    G.require(raw_workload == G.generate(raw_config), "workload_mismatch", "$/workload")
    workload = G.parse_jsonl(raw_workload, "request")
    ledger = G.parse_jsonl(raw_ledger, "ledger")
    G.require(bool(ledger), "missing_ledger", "$/ledger")
    observations = G.parse(raw_observations)
    G.require(type(observations) is dict, "invalid_fields", "$")
    schema = "observations_v1" if observations.get("protocol") == "sys05-observations-v1" else "observations"
    G.validate(observations, G.contract()["schemas"][schema])
    G.require(observations["config_sha256"] == G.sha(raw_config), "config_mismatch", "$/config_sha256")
    horizon = observations["observed_until_ns"]
    G.require(horizon >= config["start_ns"] + config["duration_ns"]
              and 0 < horizon - config["start_ns"] <= G.MAX_I64, "invalid_time", "$/observed_until_ns")
    expected, received, results, missing_received, missing_results = _reconcile(
        workload, ledger, observations["outcomes"], horizon, config, allow_incomplete)
    metrics = _request_metrics(expected, received, results, config)
    metrics["throughput"] = {"success_numerator": metrics["status_counts"]["success"],
                             "all_planned_request_denominator": len(expected),
                             "elapsed_ns": horizon - config["start_ns"], "rate_not_fabricated": True}
    complete = not missing_received and not missing_results
    fresh = metrics["freshness"]
    quality = metrics["quality"]
    repair = _repair_summary(observations, config["start_ns"], horizon)
    processing = _processing_completeness(results)
    profile = "reported_window_v1" if observations["protocol"] == "sys05-observations-v1" else "explicit_window_witness_v2"
    repair_gate = profile == "reported_window_v1" or repair["coverage_complete"]
    ready = (processing["complete"] and repair_gate and complete and fresh["read_denominator"] > 0 and fresh["unknown"] == 0
             and fresh["violations"] == 0 and quality["false"] == 0 and quality["unknown"] == 0)
    normalized = [{key: value for key, value in row.items() if key != "config_sha256"} for row in workload]
    return {"protocol": "sys05-summary-v2", "contract_sha256": G.CONTRACT_SHA256,
            "config_sha256": G.sha(raw_config), "workload_sha256": G.sha(raw_workload),
            "ledger_sha256": G.sha(raw_ledger), "observations_sha256": G.sha(raw_observations),
            "common_input_sha256": G.common_input_sha(config),
            "normalized_request_sequence_sha256": G.sha(G.canonical(normalized)),
            "group": {key: config[key] for key in ("mode", "scenario", "cache", "run_id")},
            "origin": observations["origin"], "service_execution_authenticated": False,
            "reconciliation": {"complete": complete, "missing_received_ids": missing_received,
                               "missing_outcome_ids": missing_results}, "metrics": metrics,
            "backlog": _queue(observations["queue_samples"], config["start_ns"], horizon),
            "costs": _costs(observations["cost_samples"], config["start_ns"], horizon),
            "repair": repair, "processing_evidence": processing,
            "observation_input_profile": observations["protocol"],
            "comparison_profile": profile,
            "comparison_scope": "legacy_within_window_reported_only" if profile == "reported_window_v1" else "reported_explicit_window_witness_only",
            "v2_repair_coverage_ready": profile == "explicit_window_witness_v2" and repair["coverage_complete"],
            "B_requires_comparison_profile": "explicit_window_witness_v2", "B_execution_ready": False,
            "background": _background(observations["background_coverage"]),
            "comparison_input_ready": ready, "superiority_conclusion": "not_evaluated",
            "whole_card_accepted": False}


def compare(left_inputs, right_inputs):
    left = summarize(*left_inputs)
    right = summarize(*right_inputs)
    G.require(left["comparison_profile"] == right["comparison_profile"], "comparison_profile_mismatch", "$")
    same = all(left[key] == right[key] for key in ("common_input_sha256",
               "normalized_request_sequence_sha256", "origin"))
    ready = (same and left["metrics"]["throughput"]["elapsed_ns"] == right["metrics"]["throughput"]["elapsed_ns"]
             and left["group"]["mode"] != right["group"]["mode"]
             and left["comparison_input_ready"] and right["comparison_input_ready"])
    return {"protocol": "sys05-comparison-eligibility-v2", "same_common_inputs": same,
            "comparison_ready": ready, "superiority_conclusion": "not_evaluated",
            "comparison_profile": left["comparison_profile"], "comparison_scope": left["comparison_scope"],
            "B_requires_comparison_profile": "explicit_window_witness_v2", "B_execution_ready": False,
            "execution_authenticated": False}


def main():
    parser = G.Parser(description=__doc__)
    for name in ("config", "workload", "ledger", "observations", "out"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--allow-incomplete", action="store_true")
    args = parser.parse_args()
    result = summarize(G.read_raw(args.config), G.read_raw(args.workload), G.read_raw(args.ledger),
                       G.read_raw(args.observations), args.allow_incomplete)
    G.publish_new(args.out, G.canonical(result) + b"\n")
    print(json.dumps({"protocol": "sys05-summary-written-v2", "complete": result["reconciliation"]["complete"],
                      "service_execution_authenticated": False}, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(G.cli_main(main))
