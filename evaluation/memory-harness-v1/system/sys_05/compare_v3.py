"""Compare two projected SYS-05 B runs under the v3 fault-window rule.

The v2 summary and compare stay unchanged. v2 counts a run without a repair interval as
incomplete, and sync never withholds, so a sync run can never be compared. v3 asks each run
for an explicit fault witness instead: the delete, one look at whether a read could select the
snapshot (bracketed by two agreeing observations), and the republication. A withheld look also
needs the run's complete v2 repair interval, starting after the delete and containing the look;
the span before it is reported as unwitnessed. A look that was not withheld is "behind" (the
last applied update is not published, or no snapshot is available, so a read gets none) or "servable" with a
publication time before the delete. The state is claimed only at the look; the
delete-to-republication span is the window common to both modes. No superiority is judged.
Only a withheld look is cross-checked against the service's repair interval; behind and servable
looks are checked for order only, so execution stays unauthenticated.
"""
import importlib.util
import json
from pathlib import Path
import sys


def _load(name, file):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).resolve().with_name(file))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


A = _load("sys05_measurement_adapter", "measurement_adapter.py")
S, G = A.S, A.G
NAMES = ("config.json", "workload.jsonl", "ledger.jsonl", "observations.json")
WITNESS = ("fault_id", "deleted_ns", "observed_ns", "withheld", "serving", "served_published_ns", "republished_ns")


def _uncovered(reason):
    return {"covered": False, "reason": reason}


def _fault(summary, raw, config):
    """Return the one fault window of a run, or why it is not covered.

    The serving state is one look at observed_ns, bracketed by two observations that agree; it is
    not claimed for the whole window. Only the delete-to-republication span is common to both modes.
    """
    faults, witnesses = config["fault_schedule"], raw.get("fault_witnesses")
    if (not isinstance(witnesses, list) or len(faults) != 1 or len(witnesses) != 1
            or faults[0]["kind"] != "repair_block" or not isinstance(witnesses[0], dict)
            or set(witnesses[0]) != set(WITNESS)):
        return _uncovered("fault_witness_shape")
    w, anchor = witnesses[0], raw["anchor_unix_ns"]
    deleted, seen, back = (w[k] - anchor for k in ("deleted_ns", "observed_ns", "republished_ns"))
    start = config["start_ns"] + faults[0]["offset_ns"]
    if not start <= deleted <= seen <= back <= config["start_ns"] + config["duration_ns"]:
        return _uncovered("fault_window_order")
    repair, serving, served = summary["repair"], w["serving"], w["served_published_ns"]
    if (serving not in ("withheld", "behind", "servable") or w["withheld"] != (serving == "withheld")
            or (served is None) == (serving == "servable")):
        return _uncovered("serving_witness_mismatch")
    out = {"covered": True, "observed_state": serving, "state_scope": "one_look_at_observed_ns",
           "deleted_ns": deleted, "observed_ns": seen, "republished_ns": back,
           "delete_to_republish_ns": back - deleted, "withheld_interval_ns": None, "pre_hold_unwitnessed_ns": None}
    if serving == "withheld":
        rows = repair["intervals"] if repair.get("input_profile") == "sys05-observations-v2" else []
        if (len(rows) != 1 or not repair["coverage_complete"] or rows[0]["end_state"] != "republished"
                or rows[0]["repair_id"] != w["fault_id"]
                or not deleted <= rows[0]["blocked_ns"] <= seen <= rows[0]["observed_through_ns"] == back):
            return _uncovered("withheld_without_matching_repair")
        out.update(withheld_interval_ns=back - rows[0]["blocked_ns"], pre_hold_unwitnessed_ns=rows[0]["blocked_ns"] - deleted)
    elif repair["reported_events"] != 0:
        return _uncovered("repair_without_withhold")
    elif serving == "servable" and served - anchor > deleted:
        return _uncovered("served_after_delete")
    return out


def _ready_without_repair(summary):
    """The v2 readiness gates other than repair coverage and the run's own last observation time.

    summarize() already refuses a run observed for less than the planned window."""
    m = summary["metrics"]
    fresh, quality = m["freshness"], m["quality"]
    return (summary["reconciliation"]["complete"] and summary["processing_evidence"]["complete"]
            and fresh["read_denominator"] > 0 and fresh["unknown"] == 0 and fresh["violations"] == 0
            and quality["false"] == 0 and quality["unknown"] == 0)


def _side(directory):
    # The same guarded reader as the other SYS-05 tools: fixed file names, no symlinks, bounded size.
    inputs = [G.read_raw(str(Path(directory, name))) for name in NAMES]
    raw_bytes = G.read_raw(str(Path(directory, "raw-run.json")))
    # The witness must come from the very run that was projected: re-project and match bytes.
    ledger, observations = A.project(inputs[0], inputs[1], G.parse(raw_bytes))
    G.require(ledger == inputs[2] and observations == inputs[3], "config_mismatch", "$/raw-run.json")
    summary = S.summarize(*inputs)
    raw = json.loads(raw_bytes)
    config = json.loads(inputs[0])
    fault = _fault(summary, raw, config)
    m = summary["metrics"]
    return summary, {
        "mode": summary["group"]["mode"], "ready": _ready_without_repair(summary) and fault["covered"],
        "fault": fault, "status_counts": m["status_counts"],
        "success_latency_ns": m["success_latency_ns"], "queue_delay_ns": m["queue_delay_ns"],
        "freshness_age_ns": m["freshness"]["age_ns"],
        "costs_last": {name: cost["last"] for name, cost in summary["costs"].items()},
    }


def compare(left_dir, right_dir):
    left, a = _side(left_dir)
    right, b = _side(right_dir)
    G.require(left["comparison_profile"] == right["comparison_profile"] == "explicit_window_witness_v2",
              "comparison_profile_mismatch", "$")
    same = all(left[key] == right[key] for key in ("common_input_sha256", "normalized_request_sequence_sha256", "origin"))
    ready = same and a["mode"] != b["mode"] and a["ready"] and b["ready"]
    return {"protocol": "sys05-comparison-eligibility-v3", "v2_compare_unchanged": True,
            "same_common_inputs": same, "comparison_ready": ready,
            "comparison_scope": "planned_window_with_explicit_fault_witness",
            "superiority_conclusion": "not_evaluated", "execution_authenticated": False,
            "modes": {a["mode"]: a, b["mode"]: b}}


def main():
    args = sys.argv[1:]
    G.require(len(args) == 2, "invalid_fields", "$/argv")
    print(json.dumps(compare(*args), sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(G.cli_main(main))
