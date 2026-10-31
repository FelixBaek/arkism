"""v3 fault-window comparison over two projected runs; no service or database needed."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


def _load(name, file):
    spec = importlib.util.spec_from_file_location(name, HERE / file)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


T = _load("sys05_adapter_tests", "test_measurement_adapter.py")
C = _load("sys05_compare_v3", "compare_v3.py")
A, G, ANCHOR = T.A, T.G, T.ANCHOR
FAULT = {"kind": "repair_block", "offset_ns": 200, "duration_ns": 300}


def run(mode, seed=3, serving=None):
    """One synthetic run with a delete at 210 and republication at 270; async withholds."""
    serving = serving or ("withheld" if mode == "async" else "behind")
    _, _, raw = T.inputs()
    cfg = G.parse(T.inputs()[0])
    cfg.update(mode=mode, run_id="v3-" + mode, fault_schedule=[FAULT], seed=seed)
    raw_config = G.canonical(cfg)
    workload = G.generate(raw_config)
    update, read = (G.parse(line) for line in workload.splitlines())
    raw["config_sha256"], raw["workload_sha256"] = G.sha(raw_config), G.sha(workload)
    for row, request in zip(raw["ingress"], (update, read)):
        row["request_id"] = request["request_id"]
    for row, request in zip(raw["requests"], (update, read)):
        row["request_id"] = request["request_id"]
    witness = {"fault_id": "delete-race", "deleted_ns": ANCHOR + 210, "observed_ns": ANCHOR + 240,
               "withheld": serving == "withheld", "serving": serving,
               "served_published_ns": ANCHOR + 35 if serving == "servable" else None,
               "republished_ns": ANCHOR + 270}
    raw["fault_witnesses"] = [witness]
    if mode == "async":
        raw["repairs"] = [{"repair_id": "delete-race", "start_ns": ANCHOR + 215, "end_ns": ANCHOR + 260,
                           "published_ns": ANCHOR + 270, "validation_sha256": "5" * 64, "hold_witnessed_ns": ANCHOR + 240}]
    return raw_config, workload, raw


def write(root, name, raw_config, workload, raw):
    d = Path(root, name)
    d.mkdir()
    ledger, observations = A.project(raw_config, workload, raw)
    for file, data in (("config.json", raw_config), ("workload.jsonl", workload), ("ledger.jsonl", ledger),
                       ("observations.json", observations), ("raw-run.json", json.dumps(raw).encode())):
        Path(d, file).write_bytes(data)
    return str(d)


class CompareV3Tests(unittest.TestCase):
    def compare(self, left, right):
        with tempfile.TemporaryDirectory() as root:
            return C.compare(write(root, "a", *left), write(root, "b", *right))

    def test_outage_and_stale_windows_make_the_pair_comparable(self):
        result = self.compare(run("async"), run("sync"))
        self.assertTrue(result["same_common_inputs"])
        self.assertTrue(result["comparison_ready"])
        self.assertEqual(result["superiority_conclusion"], "not_evaluated")
        self.assertEqual(result["modes"]["async"]["fault"], {
            "covered": True, "observed_state": "withheld", "state_scope": "one_look_at_observed_ns",
            "deleted_ns": 210, "observed_ns": 240, "republished_ns": 270, "delete_to_republish_ns": 60,
            "withheld_interval_ns": 55, "pre_hold_unwitnessed_ns": 5})
        sync = result["modes"]["sync"]["fault"]
        self.assertEqual((sync["observed_state"], sync["withheld_interval_ns"]), ("behind", None))

    def test_a_mode_that_kept_serving_reports_a_stale_window(self):
        result = self.compare(run("async"), run("sync", serving="servable"))
        self.assertTrue(result["comparison_ready"])
        self.assertEqual(result["modes"]["sync"]["fault"]["observed_state"], "servable")

    def test_v2_compare_stays_not_ready_for_the_same_pair(self):
        with tempfile.TemporaryDirectory() as root:
            dirs = [write(root, n, *run(m)) for n, m in (("a", "async"), ("b", "sync"))]
            read = lambda d: [Path(d, n).read_bytes() for n in C.NAMES]
            self.assertFalse(C.S.compare(read(dirs[0]), read(dirs[1]))["comparison_ready"])

    def test_uncovered_faults_are_not_comparable(self):
        cases = {
            "no witness": ("sync", lambda r: r.pop("fault_witnesses")),
            "two witnesses": ("sync", lambda r: r["fault_witnesses"].append(copy.deepcopy(r["fault_witnesses"][0]))),
            "served after delete": ("sync", lambda r: r["fault_witnesses"][0].update(serving="servable", served_published_ns=ANCHOR + 220)),
            "servable without time": ("sync", lambda r: r["fault_witnesses"][0].update(serving="servable")),
            "behind with time": ("sync", lambda r: r["fault_witnesses"][0].update(served_published_ns=ANCHOR + 35)),
            "unknown serving": ("sync", lambda r: r["fault_witnesses"][0].update(serving="maybe")),
            "withheld flag without withheld serving": ("sync", lambda r: r["fault_witnesses"][0].update(withheld=True)),
            "behind with repair": ("sync", lambda r: r.update(repairs=run("async")[2]["repairs"])),
            "delete before fault": ("sync", lambda r: r["fault_witnesses"][0].update(deleted_ns=ANCHOR + 150)),
            "republished before seen": ("sync", lambda r: r["fault_witnesses"][0].update(republished_ns=ANCHOR + 230)),
            "withheld without repair": ("async", lambda r: r.update(repairs=[])),
            "withheld but served": ("async", lambda r: r["fault_witnesses"][0].update(served_published_ns=ANCHOR + 35)),
            "repair ends elsewhere": ("async", lambda r: r["fault_witnesses"][0].update(republished_ns=ANCHOR + 280)),
            "hold before delete": ("async", lambda r: r["repairs"][0].update(start_ns=ANCHOR + 205)),
            "look before hold": ("async", lambda r: r["fault_witnesses"][0].update(observed_ns=ANCHOR + 212)),
            "other repair id": ("async", lambda r: r["repairs"][0].update(repair_id="other")),
            "state changed between looks": ("sync", lambda r: r["fault_witnesses"][0].update(serving="unknown")),
            "servable with repair": ("sync", lambda r: (r["fault_witnesses"][0].update(serving="servable", served_published_ns=ANCHOR + 35),
                                                        r.update(repairs=run("async")[2]["repairs"]))),
            "missing witness key": ("sync", lambda r: r["fault_witnesses"][0].pop("served_published_ns")),
        }
        for name, (mode, edit) in cases.items():
            left, right = run("async"), run("sync")
            edit((left if mode == "async" else right)[2])
            with self.subTest(name):
                result = self.compare(left, right)
                self.assertFalse(result["comparison_ready"])
                self.assertFalse(result["modes"][mode]["fault"]["covered"])

    def test_a_raw_run_from_another_run_is_refused(self):
        left, right = run("async"), run("sync")
        with tempfile.TemporaryDirectory() as root:
            a, b = write(root, "a", *left), write(root, "b", *right)
            other = copy.deepcopy(right[2])
            other["requests"][0]["completed_ns"] += 1
            Path(b, "raw-run.json").write_text(json.dumps(other))
            with self.assertRaises(C.G.LoadError):  # C loads its own copy of the generator
                C.compare(a, b)

    def test_different_inputs_are_not_comparable(self):
        result = self.compare(run("async"), run("sync", seed=4))
        self.assertFalse(result["same_common_inputs"])
        self.assertFalse(result["comparison_ready"])

    def test_same_mode_is_not_comparable(self):
        self.assertFalse(self.compare(run("sync"), run("sync"))["comparison_ready"])


if __name__ == "__main__":
    unittest.main()
