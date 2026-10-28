"""Exercise the roadmap gate with a pinned contract and isolated mutated plans."""
import copy
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

import check_roadmap_plan as gate


ROOT = pathlib.Path(__file__).resolve().parent.parent
PLAN_PATH = pathlib.Path("guide/roadmap/ownership.json")
CONTRACT_PATH = pathlib.Path("evaluation/memory-harness-v1/contracts/implementation_contract.json")


class RoadmapPlanTests(unittest.TestCase):
    """Keep every rejection fixture separate from the repository's real bytes."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name) / "repo"
        self.root.mkdir()
        self.plan = json.loads((ROOT / PLAN_PATH).read_text(encoding="utf-8"))
        self.contract = (ROOT / CONTRACT_PATH).read_bytes()
        self.contract_path = self.root / CONTRACT_PATH
        self.contract_path.parent.mkdir(parents=True)
        self.contract_path.write_bytes(self.contract)
        self.write_plan()
        # A rejection counts only after the unchanged fixture passed. This
        # prevents an unrelated invalid plan from making every mutant pass.
        self.assertIsInstance(gate.validate(self.root), dict)

    def write_plan(self):
        """Publish only a test-owned manifest, leaving the real plan untouched."""
        path = self.root / PLAN_PATH
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(self.plan, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    def card(self, name):
        """Select a declared card without relying on its list position."""
        return next(card for card in self.plan["cards"] if card["id"] == name)

    def rejected(self):
        """Require a fixed reason code, never a private input in diagnostics."""
        self.write_plan()
        with self.assertRaises(gate.PlanError) as caught:
            gate.validate(self.root)
        self.assertRegex(str(caught.exception), r"\A[a-z][a-z0-9_]*\Z")
        self.assertNotIn("PRIVATE_SENTINEL", str(caught.exception))

    def test_real_plan_and_exact_contract_are_accepted(self):
        """A gate that rejects every fixture cannot satisfy preparation."""
        result = gate.validate(self.root)
        self.assertIsInstance(result, dict)
        self.assertTrue(result)
        self.assertEqual(self.contract_path.read_bytes(), self.contract)

    def test_one_extra_newline_in_contract_is_rejected(self):
        """Logical JSON equivalence cannot replace the pinned raw bytes."""
        self.contract_path.write_bytes(self.contract + b"\n")
        self.rejected()

    def test_missing_contract_is_rejected(self):
        """An absent prerequisite is not successful validation."""
        self.contract_path.unlink()
        self.rejected()

    def test_manifest_cannot_repin_a_changed_contract(self):
        """A caller cannot rewrite both content and the expected digest."""
        import hashlib
        changed = self.contract + b"\n"
        self.contract_path.write_bytes(changed)
        self.plan["baseline_contract"]["sha256"] = hashlib.sha256(changed).hexdigest()
        self.rejected()

    def test_contract_symlink_is_rejected_even_with_matching_bytes(self):
        """A matching external target does not authorize a path escape."""
        target = pathlib.Path(self.temp.name) / "outside.json"
        target.write_bytes(self.contract)
        self.contract_path.unlink()
        self.contract_path.symlink_to(target)
        self.rejected()

    def test_contract_parent_symlink_is_rejected(self):
        """Checking only the leaf cannot protect an aliased contract root."""
        target = pathlib.Path(self.temp.name) / "external-contracts"
        target.mkdir()
        (target / self.contract_path.name).write_bytes(self.contract)
        self.contract_path.unlink()
        self.contract_path.parent.rmdir()
        self.contract_path.parent.symlink_to(target, target_is_directory=True)
        self.rejected()

    def test_missing_manifest_is_rejected(self):
        """Missing metadata is never accepted as an empty plan."""
        (self.root / PLAN_PATH).unlink()
        with self.assertRaises(gate.PlanError):
            gate.validate(self.root)

    def test_duplicate_manifest_key_is_rejected(self):
        """Last-key-wins JSON cannot silently choose the governing schema."""
        raw = json.dumps(self.plan)
        (self.root / PLAN_PATH).write_text('{"schema_version":1,' + raw[1:], encoding="utf-8")
        with self.assertRaises(gate.PlanError):
            gate.validate(self.root)

    def test_manifest_symlink_is_rejected(self):
        """A manifest outside the test repository is not governing input."""
        target = pathlib.Path(self.temp.name) / "external-plan.json"
        target.write_text(json.dumps(self.plan), encoding="utf-8")
        path = self.root / PLAN_PATH
        path.unlink()
        path.symlink_to(target)
        with self.assertRaises(gate.PlanError):
            gate.validate(self.root)

    def test_root_array_is_rejected(self):
        """Shallow schema corruption produces a controlled rejection."""
        self.plan = []
        self.rejected()

    def test_boolean_schema_version_is_not_an_integer_version(self):
        """Python's bool subclassing int must not admit true as version1."""
        self.plan["schema_version"] = True
        self.rejected()

    def test_unknown_top_level_field_is_rejected(self):
        """Unrecognized authority-bearing metadata is not ignored."""
        self.plan["unexpected_override"] = "PRIVATE_SENTINEL"
        self.rejected()

    def test_missing_required_manifest_section_is_rejected(self):
        """The graph cannot pass without its ownership declarations."""
        self.plan.pop("preparation")
        self.rejected()

    def test_duplicate_card_is_rejected(self):
        """Exact set coverage includes identity uniqueness."""
        self.plan["cards"].append(copy.deepcopy(self.card("R1")))
        self.rejected()

    def test_missing_card_is_rejected(self):
        """A missing required card cannot disappear from the denominator."""
        self.plan["cards"] = [card for card in self.plan["cards"] if card["id"] != "R1"]
        self.rejected()

    def test_unknown_card_cannot_replace_a_required_card(self):
        """The right count alone is insufficient coverage."""
        self.card("R1")["id"] = "UNKNOWN-01"
        self.rejected()

    def test_wrong_issue_mapping_is_rejected(self):
        """A card cannot silently move to a different roadmap stage."""
        self.card("R1")["issue"] = 152
        self.rejected()

    def test_required_card_cannot_be_marked_optional(self):
        """Conditional selection cannot remove required baseline work."""
        self.card("R1")["conditional"] = True
        self.rejected()

    def test_optional_card_cannot_be_marked_required(self):
        """A plan edit cannot silently turn optional research into a mandate."""
        self.card("ASSOC-01")["conditional"] = False
        self.rejected()

    def test_string_conditional_is_rejected(self):
        """String false is not a typed boolean."""
        self.card("R1")["conditional"] = "false"
        self.rejected()

    def test_fixed_baseline_file_ownership_cannot_drift(self):
        """R1–R8 retain the original contract's exact file boundary."""
        self.card("R1")["owns"][0]["path"] = "internal/memoryharness/other.go"
        self.rejected()

    def test_duplicate_writers_are_rejected(self):
        """A consumer cannot become a second owner of a shared type."""
        self.card("CAP-01")["owns"].append(copy.deepcopy(self.card("R1")["owns"][0]))
        self.rejected()

    def test_directory_glob_cannot_hide_a_second_writer(self):
        """A directory reservation overlaps a descendant owned elsewhere."""
        self.card("CAP-01")["owns"].append({"root": "repo", "path": "research-fixture/**"})
        self.card("CAP-02")["owns"].append({"root": "repo", "path": "research-fixture/child.json"})
        self.rejected()

    def test_unknown_root_is_rejected(self):
        """An undeclared external storage root grants no write scope."""
        self.card("CAP-01")["owns"][0]["root"] = "unknown_private_root"
        self.rejected()

    def test_unsafe_owned_paths_are_rejected_without_echoing_them(self):
        """Traversal, absolute and Windows paths cannot enter dispatch."""
        original = copy.deepcopy(self.plan)
        for path in ("../PRIVATE_SENTINEL", "safe/../PRIVATE_SENTINEL", "/PRIVATE_SENTINEL",
                     "C:/PRIVATE_SENTINEL", "\\\\host\\PRIVATE_SENTINEL", "safe\\PRIVATE_SENTINEL"):
            with self.subTest(path_kind=path.split("PRIVATE_SENTINEL")[0]):
                self.plan = copy.deepcopy(original)
                self.card("CAP-01")["owns"][0]["path"] = path
                self.rejected()

    def test_private_root_cannot_publish_a_machine_specific_binding(self):
        """Actual private dispatch paths stay outside the public plan."""
        self.plan["roots"]["crew_lab"]["path"] = "/PRIVATE_SENTINEL/private-lab"
        self.rejected()

    def test_repository_root_must_remain_relative_and_local(self):
        """The plan cannot redirect repository writes to a private machine path."""
        self.plan["roots"]["repo"]["path"] = "/PRIVATE_SENTINEL/repository"
        self.rejected()

    def test_unknown_dependency_is_rejected(self):
        """No dependency is satisfied by an unknown name."""
        self.card("R3")["depends_on"].append("UNKNOWN-01")
        self.rejected()

    def test_card_dependency_cycle_is_rejected(self):
        """Mutually waiting cards cannot form a dispatchable plan."""
        self.card("R1")["depends_on"] = ["R2"]
        self.card("R2")["depends_on"] = ["R1"]
        self.rejected()

    def test_unknown_entry_gate_is_rejected(self):
        """A card cannot claim a nonexistent acceptance prerequisite."""
        self.card("R1")["entry_gates"] = ["UNKNOWN.acceptance"]
        self.rejected()

    def test_duplicate_gate_is_rejected(self):
        """Repeated gate IDs must not use last-writer-wins semantics."""
        self.plan["gates"].append(copy.deepcopy(self.plan["gates"][0]))
        self.rejected()

    def test_gate_dependency_cycle_is_rejected(self):
        """Substage edges must also remain acyclic."""
        first = next(item for item in self.plan["gates"] if item["id"] == "R1")
        second = next(item for item in self.plan["gates"] if item["id"] == "R2")
        first["depends_on"], second["depends_on"] = ["R2"], ["R1"]
        self.rejected()

    def test_unregistered_shared_update_is_rejected(self):
        """Shared edits need a named integration owner, not merely a path."""
        self.card("CAP-07")["shared_updates"][0]["path"] = "internal/unknown-shared.go"
        self.rejected()

    def test_consumer_cannot_forge_the_owner_of_a_known_path(self):
        """A known owner name does not confer ownership of another card's file."""
        self.card("R2")["consumes"][0]["owner"] = "R3"
        self.rejected()

    def test_malformed_nested_shapes_have_controlled_errors(self):
        """Damaged inner records must not escape as Python indexing errors."""
        original = copy.deepcopy(self.plan)
        corruptions = (
            ("cards", None), ("roots", []), ("gates", [None]),
            ("preparation", {"owner": "R0", "writes": None}),
            ("shared_files", [None]), ("dispatch_contract", []),
        )
        for key, value in corruptions:
            with self.subTest(section=key):
                self.plan = copy.deepcopy(original)
                self.plan[key] = value
                self.rejected()
        for field, value in (("owns", [None]), ("consumes", [None]),
                             ("shared_updates", [None]), ("entry_gates", True), ("owner", None)):
            with self.subTest(card_field=field):
                self.plan = copy.deepcopy(original)
                self.card("R2")[field] = value
                self.rejected()

    def test_future_work_rejects_an_unknown_root(self):
        """Deferred collection files still need a declared private root."""
        self.card("PSY-04")["future_owns"][0]["root"] = "unknown_private_root"
        self.rejected()

    def test_future_work_rejects_path_traversal(self):
        """A future authorization does not permit an unsafe path."""
        self.card("PSY-04")["future_owns"][0]["path"] = "../PRIVATE_SENTINEL"
        self.rejected()

    def test_future_work_needs_an_existing_authorization_gate(self):
        """Misspelled permission gates cannot authorize later collection."""
        self.card("PSY-04")["future_owns"][0]["requires_gate"] = "UNKNOWN.authorization"
        self.rejected()

    def test_future_work_cannot_replace_authorization_with_baseline_acceptance(self):
        """A completed parser is not permission to collect participant data."""
        self.card("PSY-04")["future_owns"][0]["requires_gate"] = "R1"
        self.rejected()

    def test_future_collection_needs_its_own_authorization_not_another_review(self):
        """Another external review is not this study's collection consent."""
        self.card("PSY-04")["future_owns"][0]["requires_gate"] = "PAPER-01"
        self.rejected()

    def test_future_work_cannot_claim_a_current_preparation_file(self):
        """Current preparation and deferred execution must remain separable."""
        future = self.card("PSY-04")["future_owns"][0]
        current = self.card("PSY-04")["owns"][0]
        future["root"], future["path"] = current["root"], current["path"]
        self.rejected()

    def test_cli_returns_success_for_the_pinned_fixture(self):
        """Run the actual CLI rather than replacing its validator."""
        result = subprocess.run([sys.executable, str(ROOT / "tools/check_roadmap_plan.py"),
                                 "--root", str(self.root)], capture_output=True, check=False)
        self.assertEqual(result.returncode, 0, result.stderr.decode(errors="replace"))

    def test_cli_reports_a_fixed_error_and_nonzero_exit(self):
        """An unsafe input must not leak its supplied path in diagnostics."""
        self.card("CAP-01")["owns"][0]["path"] = "/PRIVATE_SENTINEL/private-lab"
        self.write_plan()
        result = subprocess.run([sys.executable, str(ROOT / "tools/check_roadmap_plan.py"),
                                 "--root", str(self.root)], capture_output=True, check=False)
        self.assertEqual(result.returncode, 1)
        self.assertNotIn(b"PRIVATE_SENTINEL", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
