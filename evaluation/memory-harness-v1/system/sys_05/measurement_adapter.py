"""Project one raw SYS-05 B measurement run onto the unchanged A-v2 ledger and observations.

The raw run is written by the Go measurement test. Every time is moved to the run anchor
(config start_ns 0); unknown values stay null and no stage time is invented.
"""
import importlib.util
from pathlib import Path
import sys


def _load_summarizer():
    path = Path(__file__).resolve().with_name("summarize_load.py")
    spec = importlib.util.spec_from_file_location("sys05_summarize", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


S = _load_summarizer()
G = S.G
COSTS = ("state_bytes", "receipt_bytes", "ledger_bytes", "checkpoint_bytes", "replay_bytes",
         "source_bytes", "log_bytes", "db_bytes", "rss_bytes")


def _rel(value, anchor):
    return None if value is None or value == 0 else value - anchor


def _status(http):
    if http == 200:
        return "success"
    return "rejected" if 400 <= http < 500 else "failed"


def _update(row, anchor):
    # Only the observed prefix of accepted <= durable <= applied <= visible is kept; nothing after a gap.
    out = dict.fromkeys(("accepted_ns", "enqueued_ns", "processing_started_ns", "durable_ns",
                         "applied_ns", "visible_ns"))
    accepted = _rel(row.get("raw_entry_accepted_ns"), anchor)
    if accepted is None:
        return out
    out.update(accepted_ns=accepted, enqueued_ns=accepted,
               processing_started_ns=_rel(row.get("raw_entry_started_ns"), anchor))
    for stage, key in (("durable_ns", "durable_witness_ns"), ("applied_ns", "applied_witness_ns"),
                       ("visible_ns", "visible_witness_ns")):
        value = _rel(row.get(key), anchor)
        if value is None:
            break
        out[stage] = value
    return out


def _outcome(row, received, anchor):
    status = _status(row["http_status"])
    completed = _rel(row["completed_ns"], anchor)
    out = {"request_id": row["request_id"], "status": status, "completed_ns": completed,
           "attempts": [{"attempt": 1, "started_ns": received, "ended_ns": completed, "status": status}],
           "fallback": None, "read_at_ns": None, "snapshot_ns": None, "quality_passed": None}
    stages = dict.fromkeys(("accepted_ns", "enqueued_ns", "processing_started_ns", "durable_ns",
                            "applied_ns", "visible_ns"))
    if row["op"] == "read":
        if status == "success":
            # The read is witnessed only by its response; its upper bound keeps age conservative.
            stages["accepted_ns"] = completed
            out["read_at_ns"] = completed
            out["fallback"] = row.get("search_observation_status") != "selected"
            out["snapshot_ns"] = _rel(row.get("snapshot_as_of_ns"), anchor)
            out["quality_passed"] = row.get("quality_passed")
    elif status == "success":
        stages = _update(row, anchor)
    out.update(stages)
    return out


def _repairs(raw, anchor):
    rows = []
    for item in raw["repairs"]:
        start, end, published = (_rel(item[k], anchor) for k in ("start_ns", "end_ns", "published_ns"))
        G.require(None not in (start, end, published) and item["validation_sha256"],
                  "incomplete", "$/repairs")
        # The origin is server-recorded; the harness must itself have seen the hold inside the interval.
        held = _rel(item["hold_witnessed_ns"], anchor)
        G.require(held is not None and start <= held <= published, "invalid_time", "$/repairs/hold_witnessed_ns")
        rows.append({"repair_id": item["repair_id"], "blocked_ns": start, "validated_ns": end,
                     "republished_ns": published, "validation_sha256": item["validation_sha256"],
                     "observed_from_ns": start, "observed_through_ns": published, "end_state": "republished"})
    return rows


def project(raw_config, raw_workload, raw):
    G.require(raw.get("protocol") == "sys05-b5-raw-run-v1", "invalid_fields", "$/protocol")
    G.require(raw["config_sha256"] == G.sha(raw_config) and raw["workload_sha256"] == G.sha(raw_workload),
              "config_mismatch", "$/config_sha256")
    expected = {row["request_id"]: row for row in G.parse_jsonl(raw_workload, "request")}
    anchor = raw["anchor_unix_ns"]
    ledger, received = [], {}
    for line in raw["ingress"]:
        identity = line["request_id"]
        G.require(identity in expected, "extra_record", "$/ingress")
        G.require(identity not in received, "duplicate_record", "$/ingress")
        received[identity] = line["received_ns"] - anchor
        ledger.append({"protocol": "sys05-ledger-v1", "request": expected[identity], "received_ns": received[identity]})
    outcomes = []
    for row in raw["requests"]:
        G.require(row["request_id"] in received, "missing_ledger", "$/requests")
        G.require(row["op"] == expected[row["request_id"]]["operation"], "request_mismatch", "$/requests/op")
        # Background operations have no projection yet; refuse rather than read them as updates.
        G.require(row["op"] in ("read", "update"), "not_applicable", "$/requests/op")
        outcomes.append(_outcome(row, received[row["request_id"]], anchor))
    costs = {c["name"]: c["bytes"] for c in raw["costs"]}
    observations = {
        "protocol": "sys05-observations-v2", "config_sha256": G.sha(raw_config),
        "origin": "reported_isolated_service", "observed_until_ns": raw["observed_until_unix_ns"] - anchor,
        "outcomes": outcomes,
        "queue_samples": [{"at_ns": s["at_unix_ns"] - anchor, "depth": s["depth"], "oldest_age_ns": None if s["depth"] else 0}
                          for s in raw["queue_samples"]],
        "cost_samples": [dict({"at_ns": raw["costs_at_unix_ns"] - anchor}, **{k: costs.get(k) for k in COSTS})],
        "repair_intervals": _repairs(raw, anchor),
        "background_coverage": [{"kind": k, "eligible": None, "completed": None, "max_wait_ns": None,
                                 "starvation_observed": None} for k in ("privacy", "embedding")],
    }
    ledger_raw = b"".join(G.canonical(line) + b"\n" for line in ledger)
    return ledger_raw, G.canonical(observations) + b"\n"


def main():
    parser = G.Parser(description=__doc__)
    for name in ("config", "workload", "raw", "ledger_out", "observations_out"):
        parser.add_argument("--" + name.replace("_", "-"), dest=name, required=True)
    args = parser.parse_args()
    raw_config, raw_workload = G.read_raw(args.config), G.read_raw(args.workload)
    ledger, observations = project(raw_config, raw_workload, G.parse(G.read_raw(args.raw)))
    G.publish_new(args.ledger_out, ledger)
    G.publish_new(args.observations_out, observations)
    print('{"protocol":"sys05-projection-written-v1","service_execution_authenticated":false}')
    return 0


if __name__ == "__main__":
    sys.exit(G.cli_main(main))
