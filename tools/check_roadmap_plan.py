"""Validate the pinned research baseline and the roadmap's single-writer plan.

This checks preparation, not card completion or experiment results. External lab
bindings and active writer reservations must be checked again before dispatch.
"""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath


BASELINE_PATH = "evaluation/memory-harness-v1/contracts/implementation_contract.json"
BASELINE_SHA256 = "04463d838f397772743c5740a346230eb8e9db60db8dddc824d52e1f083790ef"
PLAN_PATH = "guide/roadmap/ownership.json"
EXPECTED = {
    **{f"R{i}": 151 for i in range(1, 9)},
    **{f"DYN-{i:02}": 152 for i in range(1, 6)},
    **{f"SYS-{i:02}": 152 for i in range(1, 7)},
    **{f"EXP-{i:02}": 153 for i in range(1, 5)},
    **{f"CRW-{i:02}": 154 for i in range(1, 8)}, "FLOW-01": 154,
    **{f"RTE-{i:02}": 155 for i in range(1, 9)},
    **{f"CAP-{i:02}": 156 for i in range(1, 8)},
    **{f"PSY-{i:02}": 157 for i in range(1, 6)}, "MEAS-WL": 157, "PSY-TEAM-01": 157,
    **{f"ASSOC-{i:02}": 158 for i in range(1, 4)},
    **{f"STRUCT-{i:02}": 158 for i in range(1, 4)},
    **{f"ADAPT-{i:02}": 158 for i in range(1, 5)},
}
OPTIONAL = {key for key, issue in EXPECTED.items() if issue == 158} | {"DYN-04", "FLOW-01", "MEAS-WL", "PSY-TEAM-01"}
FORBIDDEN = {".git", ".private", ".gitignore", ".dockerignore", ".env", ".env.example"}


class PlanError(ValueError):
    """Only fixed reason codes cross the CLI boundary; never private JSON."""


def require(ok, code):
    if not ok:
        raise PlanError(code)


def object_pairs(pairs):
    out = {}
    for key, value in pairs:
        require(key not in out, "duplicate_json_key")
        out[key] = value
    return out


def reject_constant(_value):
    raise PlanError("nonfinite_json_number")


def decode(data):
    try:
        return json.loads(data.decode("utf-8"), object_pairs_hook=object_pairs,
                          parse_constant=reject_constant)
    except (UnicodeError, json.JSONDecodeError, RecursionError):
        raise PlanError("invalid_json") from None


def read_asset(root, name):
    current = root
    for part in PurePosixPath(name).parts:
        current = current / part
        require(not current.is_symlink(), "asset_symlink")
    try:
        require(current.is_file(), "asset_missing")
        require(current.stat().st_size <= 4_000_000, "asset_too_large")
        return current.read_bytes()
    except OSError:
        raise PlanError("asset_unreadable") from None


def array(value, code):
    require(isinstance(value, list), code)
    return value


def ids(value, code):
    value = array(value, code)
    require(all(isinstance(x, str) and x for x in value), code)
    require(len(value) == len(set(value)), code)
    return value


def relative(value):
    require(isinstance(value, str) and bool(value), "invalid_owned_path")
    require(not any(c in value for c in ("\\", ":", "\x00", "\n", "\r")), "unsafe_owned_path")
    parts = value.split("/")
    require(not value.startswith("/") and all(x not in ("", ".", "..") for x in parts), "unsafe_owned_path")
    require(not any(x in FORBIDDEN or x.startswith(".env.") for x in parts), "private_owned_path")
    require("*" not in value or value.endswith("/**") and "*" not in value[:-3], "invalid_owned_glob")
    return value


def location(item, roots):
    require(isinstance(item, dict), "invalid_location")
    require(isinstance(item.get("root"), str) and item["root"] in roots, "unknown_root")
    return item["root"], relative(item.get("path"))


def overlaps(left, right):
    if left[0] != right[0]:
        return False
    a, b = left[1].removesuffix("/**"), right[1].removesuffix("/**")
    return a == b or a.startswith(b + "/") or b.startswith(a + "/")


def check_roots(roots):
    require(isinstance(roots, dict) and roots.get("repo") == {"kind": "repository", "path": "."}, "invalid_repo_root")
    for name, root in roots.items():
        require(isinstance(name, str) and name.isidentifier() and isinstance(root, dict), "invalid_root")
        if name != "repo":
            require(root == {"kind": "external-private", "dispatch_required": True}, "private_root_binding")


def check_cards(cards, roots):
    require(isinstance(cards, list) and len(cards) == len(EXPECTED), "card_set_mismatch")
    seen = {}
    for card in cards:
        require(isinstance(card, dict), "invalid_card")
        key = card.get("id")
        require(isinstance(key, str) and key in EXPECTED and key not in seen, "card_set_mismatch")
        require(type(card.get("issue")) is int and card["issue"] == EXPECTED[key], "card_issue_mismatch")
        require(type(card.get("conditional")) is bool and card["conditional"] == (key in OPTIONAL), "card_condition_mismatch")
        require(card.get("owner") == key, "card_owner_mismatch")
        require(card.get("dispatch_required") is True, "dispatch_required")
        require(card.get("status") in ("planned", "in_progress", "verified", "held", "rejected"), "invalid_card_status")
        require(isinstance(card.get("condition"), str) and bool(card["condition"]), "condition_missing")
        for field in ("owns", "consumes", "shared_updates"):
            for item in array(card.get(field), "invalid_card_paths"):
                location(item, roots)
        for field in ("depends_on", "entry_gates", "completion_gates"):
            ids(card.get(field), "invalid_card_gates")
        require(card["entry_gates"] and card["completion_gates"], "card_gates_missing")
        seen[key] = card
    require(set(seen) == set(EXPECTED), "card_set_mismatch")
    return seen


def check_gates(items, cards):
    gates = {}
    for item in array(items, "invalid_gates"):
        require(isinstance(item, dict), "invalid_gate")
        key = item.get("id")
        require(isinstance(key, str) and key and key not in gates, "duplicate_gate")
        require(item.get("kind") in ("acceptance", "preparation", "external_review"), "invalid_gate_kind")
        require(isinstance(item.get("condition"), str) and bool(item["condition"]), "gate_condition_missing")
        gates[key] = item
    require("R0.acceptance" in gates, "preparation_gate_missing")
    edges = {}
    for key, gate in gates.items():
        links = ids(gate.get("depends_on"), "invalid_gate_dependencies")
        for rule in array(gate.get("requires_if_selected", []), "invalid_selection_gates"):
            require(isinstance(rule, dict) and isinstance(rule.get("selection"), str) and rule["selection"], "invalid_selection_gate")
            require(isinstance(rule.get("gate"), str), "invalid_selection_gate")
            links = [*links, rule["gate"]]
        require(all(x in gates and x != key for x in links), "unknown_or_self_gate")
        edges[key] = links
    check_acyclic(edges)
    card_edges = {key: [p for p in card["depends_on"] if p in cards]
                  for key, card in cards.items()}
    check_acyclic(card_edges)
    for card in cards.values():
        for field in ("entry_gates", "completion_gates", "depends_on"):
            require(all(x in gates for x in card[field]), "unknown_card_gate")
    return gates


def check_acyclic(edges):
    visited, visiting = set(), set()

    def visit(key):
        require(key not in visiting, "dependency_cycle")
        if key in visited:
            return
        visiting.add(key)
        for parent in edges[key]:
            visit(parent)
        visiting.remove(key)
        visited.add(key)

    for key in edges:
        visit(key)


def check_writers(plan, cards, roots, contract):
    writers = []
    preparation = plan.get("preparation")
    require(isinstance(preparation, dict) and preparation.get("owner") == "R0", "preparation_owner")
    for item in array(preparation.get("writes"), "preparation_paths"):
        writers.append(("R0", location(item, roots)))
    shared = {}
    for item in array(plan.get("shared_files"), "shared_paths"):
        place = location(item, roots)
        require(item.get("owner") == "INTEGRATION" and place not in shared, "shared_owner")
        requesters = ids(item.get("requesters"), "shared_requesters")
        require(requesters and all(x in cards for x in requesters), "shared_requesters")
        shared[place] = requesters
        writers.append(("INTEGRATION", place))
    for key, card in cards.items():
        owned = [location(x, roots) for x in card["owns"]]
        require(owned and len(owned) == len(set(owned)), "duplicate_owned_path")
        writers.extend((key, p) for p in owned)
        future = [location(item, roots) for item in array(card.get("future_owns", []), "invalid_future_paths")]
        require(len(future) == len(set(future)), "duplicate_future_path")
        require(not any(overlaps(p, q) for p in future for q in owned), "current_future_overlap")
        writers.extend((key, p) for p in future)
        for item in card["shared_updates"]:
            place = location(item, roots)
            require(item.get("owner") == "INTEGRATION" and key in shared.get(place, []), "invalid_shared_update")
            ids(item.get("after"), "shared_update_gates")
    for i, (owner, path) in enumerate(writers):
        for other, place in writers[i + 1:]:
            require(owner == other or not overlaps(path, place), "writer_overlap")
    for card in cards.values():
        for item in card["consumes"]:
            owner = item.get("owner")
            require(isinstance(owner, str) and owner in {*cards, "R0", "INTEGRATION"}, "unknown_consumer_owner")
            place = location(item, roots)
            require(any(who == owner and overlaps(place, path) for who, path in writers), "consumer_owner_mismatch")
    for key, names in contract["file_ownership"].items():
        require(key in cards, "baseline_owner_missing")
        actual = [item["path"] for item in cards[key]["owns"] if item["root"] == "repo"]
        require(actual == names and len(cards[key]["owns"]) == len(names), "baseline_owner_drift")


def validate(root):
    try:
        return validate_plan(root)
    except PlanError:
        raise
    except (OSError, ValueError, TypeError, KeyError, RecursionError):
        raise PlanError("invalid_plan_shape") from None


def validate_plan(root):
    root = Path(root).resolve()
    plan = decode(read_asset(root, PLAN_PATH))
    require(isinstance(plan, dict) and type(plan.get("schema_version")) is int and plan["schema_version"] == 1, "plan_version")
    require(set(plan) == {"schema_version", "plan_id", "baseline_contract", "roots", "preparation", "shared_files",
                          "cards", "gates", "handoff_rules", "dispatch_contract"}, "unknown_plan_field")
    baseline = plan.get("baseline_contract")
    require(isinstance(baseline, dict) and baseline.get("path") == BASELINE_PATH and baseline.get("sha256") == BASELINE_SHA256, "baseline_pin_changed")
    raw = read_asset(root, BASELINE_PATH)
    require(hashlib.sha256(raw).hexdigest() == BASELINE_SHA256, "baseline_bytes_changed")
    contract = decode(raw)
    require(contract.get("contract_id") == "arkism-offline-b0-v1" and len(contract.get("objects", {})) == 27, "baseline_contract_shape")
    roots = plan.get("roots")
    check_roots(roots)
    dispatch = plan.get("dispatch_contract")
    require(isinstance(dispatch, dict) and set(dispatch) == {"required_fields", "private_root_rules", "writer_rules"}, "invalid_dispatch_contract")
    fields = ids(dispatch.get("required_fields"), "invalid_dispatch_fields")
    require(set(fields) == {"task_id", "assigned_agent", "source_commit", "owned_paths", "root_bindings",
                            "accepted_predecessor_evidence"}, "invalid_dispatch_fields")
    for field in ("private_root_rules", "writer_rules"):
        require(ids(dispatch.get(field), "invalid_dispatch_rules"), "invalid_dispatch_rules")
    require(ids(plan.get("handoff_rules"), "invalid_handoff_rules"), "invalid_handoff_rules")
    cards = check_cards(plan.get("cards"), roots)
    gates = check_gates(plan.get("gates"), cards)
    for card in cards.values():
        for item in array(card.get("future_owns", []), "invalid_future_paths"):
            location(item, roots)
            required = item.get("requires_gate")
            require(required == card["id"] + ".execution_authorized" and required in gates and
                    gates[required]["kind"] == "external_review", "future_authorization_gate")
    check_writers(plan, cards, roots, contract)
    for card in cards.values():
        for item in card["shared_updates"]:
            require(all(x in gates for x in item["after"]), "unknown_shared_update_gate")
    return {"status": "verified", "cards": len(cards), "gates": len(gates), "baseline_sha256": BASELINE_SHA256,
            "scope": "preparation_only_not_card_acceptance"}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    args = parser.parse_args(argv)
    try:
        print(json.dumps(validate(args.root), sort_keys=True))
        return 0
    except (PlanError, OSError, TypeError, KeyError, RecursionError) as error:
        code = str(error) if isinstance(error, PlanError) else "invalid_plan_shape"
        print("roadmap_preparation_failed: " + code)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
