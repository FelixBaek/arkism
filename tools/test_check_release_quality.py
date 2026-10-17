"""Exact-commit release gates: wrong evidence, reruns, API failures and wiring."""
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import check_release_quality as gate


REPOSITORY = "example/arkism"
SHA = "a" * 40
API_DIAGNOSTIC = "provider diagnostic that must not reach release logs"


def run(workflow="checks.yml", run_id=10, **changes):
    value = {"id": run_id, "run_attempt": 1, "head_sha": SHA,
             "event": "push", "head_branch": "main",
             "path": f".github/workflows/{workflow}",
             "repository": {"full_name": REPOSITORY},
             "head_repository": {"full_name": REPOSITORY},
             "status": "completed", "conclusion": "success"}
    value.update(changes)
    return value


def response(runs):
    return [{"total_count": len(runs), "workflow_runs": runs}]


class ReleaseQualityTests(unittest.TestCase):
    def check(self, by_workflow):
        with patch.object(gate, "fetch_runs", side_effect=lambda repo, sha, workflow, timeout:
                          by_workflow.get(workflow, [run(workflow)])):
            return gate.verify(REPOSITORY, SHA, timeout_seconds=0)

    def test_both_exact_main_push_runs_are_required(self):
        result = self.check({})
        self.assertEqual(result["status"], "verified")
        self.assertEqual(result["commit"], SHA)
        self.assertEqual([item["workflow"] for item in result["workflows"]], list(gate.WORKFLOWS))
        for workflow in gate.WORKFLOWS:
            with self.subTest(workflow=workflow), self.assertRaises(gate.GateError):
                self.check({workflow: []})

    def test_other_commit_branch_event_workflow_and_repository_do_not_count(self):
        wrong = [{"head_sha": "b" * 40}, {"head_branch": "feature"},
                 {"event": "pull_request"}, {"event": "workflow_dispatch"},
                 {"path": ".github/workflows/impostor.yml"},
                 {"repository": {"full_name": "other/arkism"}},
                 {"head_repository": {"full_name": "fork/arkism"}},
                 {"head_repository": None}]
        for changes in wrong:
            evidence = {"checks.yml": [run(**changes)]}
            with self.subTest(changes=changes), self.assertRaises(gate.GateError):
                self.check(evidence)

    def test_latest_failed_run_cannot_reuse_older_success(self):
        for conclusion in ("failure", "cancelled", "skipped", "neutral", "timed_out",
                           "action_required", "stale", None, API_DIAGNOSTIC):
            with self.subTest(conclusion=conclusion):
                evidence = {"checks.yml": [run(run_id=11, conclusion=conclusion), run()]}
                with self.assertRaises(gate.GateError) as error:
                    self.check(evidence)
                self.assertNotIn(API_DIAGNOSTIC, str(error.exception))

    def test_completed_success_after_failure_can_pass(self):
        result = self.check({"checks.yml": [run(conclusion="failure"), run(run_id=11)]})
        self.assertEqual(result["workflows"][0]["run_id"], 11)

    def test_latest_pending_run_and_rerun_block_release(self):
        for status in gate.PENDING:
            evidence = {"checks.yml": [run(), run(run_id=11, status=status, conclusion=None)]}
            with self.subTest(status=status), self.assertRaises(gate.GateError):
                self.check(evidence)
        evidence = {"checks.yml": [run(run_attempt=2, conclusion="failure")]}
        with self.assertRaises(gate.GateError):
            self.check(evidence)
        evidence = {"checks.yml": [run(status="unknown", conclusion="success")]}
        with self.assertRaises(gate.GateError):
            self.check(evidence)
        for status in (None, [], {"unexpected": API_DIAGNOSTIC}):
            evidence = {"checks.yml": [run(status=status)]}
            with self.subTest(status=status), self.assertRaises(gate.GateError):
                self.check(evidence)

    def test_second_check_observes_a_failed_rerun(self):
        self.assertEqual(self.check({})["status"], "verified")
        evidence = {"sonarcloud.yml": [run("sonarcloud.yml", run_attempt=2, conclusion="failure")]}
        with self.assertRaises(gate.GateError):
            self.check(evidence)

    def test_run_attempt_is_mandatory(self):
        for attempt in (None, 0, -1, True, "2"):
            evidence = {"checks.yml": [run(run_attempt=attempt)]}
            with self.subTest(attempt=attempt), self.assertRaises(gate.GateError):
                self.check(evidence)

    def test_waits_for_pending_then_reads_fresh_results(self):
        calls = []
        def fetch(repo, sha, workflow, timeout):
            calls.append(workflow)
            return [] if len(calls) <= 2 else [run(workflow)]
        with patch.object(gate, "fetch_runs", side_effect=fetch), \
                patch.object(gate.time, "sleep") as sleep:
            result = gate.verify(REPOSITORY, SHA, timeout_seconds=20, poll_seconds=1)
        self.assertEqual(result["status"], "verified")
        self.assertEqual(calls, [*gate.WORKFLOWS, *gate.WORKFLOWS])
        sleep.assert_called_once()

    def test_wait_deadline_expires_without_another_poll(self):
        with patch.object(gate, "fetch_runs", return_value=[]), \
                patch.object(gate.time, "monotonic", side_effect=[0, 0, 0, 21]), \
                patch.object(gate.time, "sleep") as sleep:
            with self.assertRaisesRegex(gate.GateError, "timed out"):
                gate.verify(REPOSITORY, SHA, timeout_seconds=20)
        sleep.assert_not_called()

    def test_invalid_arguments_never_call_github(self):
        for repo, sha, timeout, poll in [("../bad", SHA, 0, 1),
                                        (REPOSITORY, "abc", 0, 1),
                                        (REPOSITORY, SHA, -1, 1),
                                        (REPOSITORY, SHA, 1801, 1),
                                        (REPOSITORY, SHA, 0, 0)]:
            with self.subTest(repo=repo, sha=sha, timeout=timeout, poll=poll), \
                    patch.object(gate, "fetch_runs") as fetch:
                with self.assertRaises(gate.GateError):
                    gate.verify(repo, sha, timeout, poll)
                fetch.assert_not_called()

    def fetch(self, pages, returncode=0):
        body = "\n".join(json.dumps(p) for p in pages) if isinstance(pages, list) else json.dumps(pages)
        result = subprocess.CompletedProcess([], returncode, body, API_DIAGNOSTIC)
        with patch.object(gate.subprocess, "run", return_value=result):
            return gate.fetch_runs(REPOSITORY, SHA, "checks.yml", 30)

    def test_fetch_rejects_invalid_cli_arguments_before_starting_a_process(self):
        cases = [("owner/repo/extra", SHA, "checks.yml"),
                 (REPOSITORY, "--option", "checks.yml"),
                 (REPOSITORY, SHA, "other.yml"),
                 (REPOSITORY, SHA, "../checks.yml"),
                 (REPOSITORY, SHA, "--header=bad")]
        for repository, sha, workflow in cases:
            with self.subTest(repository=repository, sha=sha, workflow=workflow), \
                    patch.object(gate.subprocess, "run") as process:
                with self.assertRaises(gate.GateError):
                    gate.fetch_runs(repository, sha, workflow, 30)
                process.assert_not_called()

    def test_all_pages_are_read_including_newer_failure(self):
        pages = [{"total_count": 2, "workflow_runs": [run()]},
                 {"total_count": 2, "workflow_runs": [run(run_id=11, conclusion="failure")]}]
        runs = self.fetch(pages)
        with self.assertRaises(gate.GateError):
            self.check({"checks.yml": runs})

    def test_incomplete_duplicate_or_malformed_inventory_is_rejected(self):
        valid = response([run()])
        broken = [None, {}, [], [None], [{"total_count": 1, "workflow_runs": []}],
                  [{"total_count": True, "workflow_runs": []}],
                  [{"total_count": 0, "workflow_runs": {}}],
                  [{"total_count": 1, "workflow_runs": [None]}],
                  [{"total_count": 1, "workflow_runs": [{"id": True}]}],
                  [{"total_count": 2, "workflow_runs": [run(), run()]}],
                  [*valid, {"total_count": 2, "workflow_runs": [run(run_id=11)]}]]
        for value in broken:
            with self.subTest(value=value), self.assertRaises(gate.GateError):
                self.fetch(value)
        self.assertEqual(self.fetch(response([])), [])

    def test_api_errors_and_invalid_json_are_not_leaked_or_accepted(self):
        for outcome in [subprocess.CompletedProcess([], 1, API_DIAGNOSTIC, API_DIAGNOSTIC),
                        subprocess.CompletedProcess([], 0, API_DIAGNOSTIC, API_DIAGNOSTIC),
                        subprocess.TimeoutExpired("gh", 30, output=API_DIAGNOSTIC),
                        FileNotFoundError(API_DIAGNOSTIC)]:
            with self.subTest(outcome=type(outcome).__name__):
                kwargs = {"side_effect": outcome} if isinstance(outcome, Exception) else {"return_value": outcome}
                with patch.object(gate.subprocess, "run", **kwargs):
                    with self.assertRaises(gate.GateError) as error:
                        gate.fetch_runs(REPOSITORY, SHA, "checks.yml", 30)
                    self.assertNotIn(API_DIAGNOSTIC, str(error.exception))

    @unittest.skipIf(os.name == "nt", "Unix workflow CLI fixture uses an executable shebang")
    def test_cli_executes_paginated_read_only_query_and_hides_api_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / "fixture.json"
            fixture.write_text(json.dumps({w: response([run(w)]) for w in gate.WORKFLOWS}))
            executable = root / "gh"
            executable.write_text(
                "#!/usr/bin/env python3\nimport json,os,sys\n"
                "with open(os.environ['GATE_CALLS'],'a') as f: f.write(json.dumps(sys.argv[1:])+'\\n')\n"
                "if os.environ.get('GATE_FAIL'):\n"
                f" print('{API_DIAGNOSTIC}',file=sys.stderr);sys.exit(1)\n"
                "endpoint=next(x for x in sys.argv if x.endswith('/runs'))\n"
                "for page in json.load(open(os.environ['GATE_FIXTURE']))[endpoint.split('/')[-2]]: print(json.dumps(page))\n")
            executable.chmod(0o700)
            calls = root / "calls.jsonl"
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                   "GATE_FIXTURE": str(fixture), "GATE_CALLS": str(calls)}
            command = [sys.executable, gate.__file__, "--repository", REPOSITORY,
                       "--sha", SHA, "--timeout-seconds", "0"]
            result = subprocess.run(command, env=env, capture_output=True, text=True, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["commit"], SHA)
            requests = [json.loads(line) for line in calls.read_text().splitlines()]
            self.assertEqual(len(requests), 2)
            for request in requests:
                self.assertIn("--paginate", request)
                self.assertEqual(request[request.index("--method") + 1], "GET")
                self.assertIn("head_sha=" + SHA, request)
                self.assertIn("event=push", request)
                self.assertIn("branch=main", request)
            env["GATE_FAIL"] = "1"
            failure = subprocess.run(command, env=env, capture_output=True, text=True, check=False)
            self.assertNotEqual(failure.returncode, 0)
            self.assertNotIn(API_DIAGNOSTIC, failure.stdout + failure.stderr)

    def test_workflow_gates_packaging_and_rechecks_before_release(self):
        workflow = (Path(gate.__file__).resolve().parents[1] / ".github/workflows/release.yml").read_text()
        validate = workflow.split("\n  validate:\n")[1].split("\n  windows:\n")[0]
        self.assertIn("if: github.event_name == 'push'", validate)
        self.assertIn('run: python3 tools/check_release_quality.py', validate)
        self.assertIn('actions: read', validate)
        self.assertIn('RELEASE_SHA: ${{ github.sha }}', validate)
        for job, following in [("windows", "unix"), ("unix", "release")]:
            section = workflow.split(f"\n  {job}:\n")[1].split(f"\n  {following}:\n")[0]
            self.assertIn("needs: validate", section)
        release = workflow.split("\n  release:\n")[1]
        recheck = release.index("--timeout-seconds 0")
        self.assertLess(recheck, release.index("- name: Draft tag release"))
        self.assertLess(recheck, release.index("- name: Publish the verified initial release"))
        self.assertIn('actions: read', release)
        self.assertIn('RELEASE_SHA: ${{ github.sha }}', release)


if __name__ == "__main__":
    unittest.main()
