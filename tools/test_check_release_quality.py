"""Exact-commit release gates: wrong evidence, reruns, API failures and wiring."""
import json
import io
import contextlib
import os
import subprocess
import sys
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

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

    def test_other_branch_failure_cannot_override_main_success(self):
        result = self.check({"checks.yml": [run(), run(run_id=11, head_branch="feature", conclusion="failure")]})
        self.assertEqual(result["workflows"][0]["run_id"], 10)

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

    def fetch(self, pages):
        return gate.validate_run_inventory(pages, "checks.yml")

    def test_fetch_rejects_invalid_identifiers_before_external_io(self):
        cases = [("owner/repo/extra", SHA, "checks.yml"),
                 (REPOSITORY, "--option", "checks.yml"),
                 (REPOSITORY, SHA, "other.yml"),
                 (REPOSITORY, SHA, "../checks.yml"),
                 (REPOSITORY, SHA, "--header=bad")]
        for repository, sha, workflow in cases:
            with self.subTest(repository=repository, sha=sha, workflow=workflow), \
                    patch.object(gate, "github_auth") as auth, \
                    patch.object(gate, "request_page") as request:
                with self.assertRaises(gate.GateError):
                    gate.fetch_runs(repository, sha, workflow, 30)
                auth.assert_not_called()
                request.assert_not_called()

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

    def test_credential_lookup_is_fixed_and_never_logs_diagnostics(self):
        for outcome in [subprocess.CompletedProcess([], 1, API_DIAGNOSTIC, API_DIAGNOSTIC),
                        subprocess.CompletedProcess([], 0, "", API_DIAGNOSTIC),
                        subprocess.TimeoutExpired("gh", 30, output=API_DIAGNOSTIC),
                        FileNotFoundError(API_DIAGNOSTIC)]:
            with self.subTest(outcome=type(outcome).__name__):
                kwargs = {"side_effect": outcome} if isinstance(outcome, Exception) else {"return_value": outcome}
                with patch.dict(os.environ, {"GH_TOKEN": "", "GITHUB_TOKEN": ""}), \
                        patch.object(gate.subprocess, "run", **kwargs) as process:
                    with self.assertRaises(gate.GateError) as error:
                        gate.github_auth(30)
                    self.assertNotIn(API_DIAGNOSTIC, str(error.exception))
                    self.assertEqual(process.call_args.args[0], ["gh", "auth", "token", "--hostname", "github.com"])

    def test_environment_auth_avoids_processes_and_rejects_header_whitespace(self):
        with patch.dict(os.environ, {"GH_TOKEN": "test-auth"}), \
                patch.object(gate.subprocess, "run") as process:
            self.assertEqual(gate.github_auth(30), "test-auth")
            process.assert_not_called()
        with patch.dict(os.environ, {"GH_TOKEN": "test\nauth"}):
            with self.assertRaises(gate.GateError):
                gate.github_auth(30)

    def test_api_errors_and_invalid_json_are_not_leaked_or_accepted(self):
        for outcome in (OSError(API_DIAGNOSTIC), ValueError(API_DIAGNOSTIC)):
            with self.subTest(outcome=type(outcome).__name__), \
                    patch.object(gate, "build_opener") as opener:
                opener.return_value.open.side_effect = outcome
                with self.assertRaises(gate.GateError) as error:
                    gate.request_page("https://api.github.com/", "test-auth", 30)
                self.assertNotIn(API_DIAGNOSTIC, str(error.exception))
        response_bytes = io.BytesIO(API_DIAGNOSTIC.encode())
        response_bytes.status = 200
        with patch.object(gate, "build_opener") as opener:
            opener.return_value.open.return_value = response_bytes
            with self.assertRaises(gate.GateError):
                gate.request_page("https://api.github.com/", "test-auth", 30)

    def test_http_requests_use_fixed_https_origin_get_auth_and_no_redirects(self):
        response_bytes = io.BytesIO(json.dumps(response([])[0]).encode())
        response_bytes.status = 200
        with patch.object(gate, "build_opener") as opener:
            opener.return_value.open.return_value = response_bytes
            result = gate.request_page("https://api.github.com/repos/example/arkism", "test-auth", 30)
        self.assertEqual(result["total_count"], 0)
        self.assertIsInstance(opener.call_args.args[0], gate.NoApiRedirect)
        self.assertIsNone(opener.call_args.args[0].redirect_request(None, None, None, None, None, None))
        request = opener.return_value.open.call_args.args[0]
        self.assertEqual(request.get_method(), "GET")
        self.assertEqual(request.get_header("Authorization"), "Bearer test-auth")
        self.assertTrue(request.full_url.startswith("https://api.github.com/"))

    def test_http_fetch_paginates_with_exact_query_and_bounded_requests(self):
        pages = [{"total_count": 2, "workflow_runs": [run()]},
                 {"total_count": 2, "workflow_runs": [run(run_id=11)]}]
        with patch.object(gate, "github_auth", return_value="test-auth"), \
                patch.object(gate, "request_page", side_effect=pages) as fetch:
            result = gate.fetch_runs(REPOSITORY, SHA, "checks.yml", 30)
        self.assertEqual(len(result), 2)
        for number, call in enumerate(fetch.call_args_list, 1):
            url, auth, timeout = call.args
            parsed = urlsplit(url)
            self.assertEqual((parsed.scheme, parsed.netloc), ("https", "api.github.com"))
            self.assertEqual(parsed.path, "/repos/" + REPOSITORY + "/actions/workflows/checks.yml/runs")
            self.assertEqual(parse_qs(parsed.query), {"head_sha": [SHA], "event": ["push"], "branch": ["main"],
                                                   "per_page": ["100"], "page": [str(number)]})
            self.assertEqual(auth, "test-auth")
            self.assertGreater(timeout, 0)
            self.assertLessEqual(timeout, 30)

    def test_http_fetch_rejects_empty_intermediate_pages_and_page_limit(self):
        for pages in [[{"total_count": 1, "workflow_runs": []}],
                      [{"total_count": 1001, "workflow_runs": [run(run_id=i + 1)]}
                       for i in range(gate.MAX_PAGES)]]:
            with self.subTest(pages=len(pages)), \
                    patch.object(gate, "github_auth", return_value="test-auth"), \
                    patch.object(gate, "request_page", side_effect=pages):
                with self.assertRaises(gate.GateError):
                    gate.fetch_runs(REPOSITORY, SHA, "checks.yml", 30)

    def test_http_response_size_is_bounded(self):
        response_bytes = io.BytesIO(b"x" * (gate.MAX_PAGE_BYTES + 1))
        response_bytes.status = 200
        with patch.object(gate, "build_opener") as opener:
            opener.return_value.open.return_value = response_bytes
            with self.assertRaisesRegex(gate.GateError, "size limit"):
                gate.request_page("https://api.github.com/", "test-auth", 30)

    def test_cli_returns_verified_metadata_and_hides_provider_errors(self):
        args = [gate.__file__, "--repository", REPOSITORY, "--sha", SHA, "--timeout-seconds", "0"]
        output = io.StringIO()
        pages = [response([run(w)])[0] for w in gate.WORKFLOWS]
        with patch.object(sys, "argv", args), patch.object(gate, "github_auth", return_value="test-auth"), \
                patch.object(gate, "request_page", side_effect=pages), contextlib.redirect_stdout(output):
            result = gate.main()
        self.assertEqual(result, 0)
        self.assertEqual(json.loads(output.getvalue())["commit"], SHA)
        error = io.StringIO()
        with patch.object(sys, "argv", args), patch.object(gate, "github_auth", return_value="test-auth"), \
                patch.object(gate, "build_opener") as opener, contextlib.redirect_stderr(error):
            opener.return_value.open.side_effect = OSError(API_DIAGNOSTIC)
            result = gate.main()
        self.assertEqual(result, 1)
        self.assertNotIn(API_DIAGNOSTIC, error.getvalue())

    def test_workflow_gates_packaging_and_rechecks_before_release(self):
        workflow = (Path(gate.__file__).resolve().parents[1] / ".github/workflows/release.yml").read_text()
        validate = workflow.split("\n  validate:\n")[1].split("\n  windows:\n")[0]
        self.assertIn("if: github.event_name == 'push'", validate)
        self.assertIn('run: python3 tools/check_release_quality.py', validate)
        self.assertIn('actions: read', validate)
        self.assertIn('RELEASE_SHA: ${{ github.sha }}', validate)
        windows = workflow.split("\n  windows:\n")[1].split("\n  release:\n")[0]
        self.assertIn("needs: validate", windows)
        self.assertNotIn("\n  unix:\n", workflow)
        self.assertNotIn("macos-", workflow)
        self.assertNotIn("build-unix.sh", workflow)
        release = workflow.split("\n  release:\n")[1]
        self.assertIn("needs: [windows]", release)
        self.assertIn("name: windows", release)
        self.assertNotIn("merge-multiple: true", release)
        recheck = release.index("--timeout-seconds 0")
        self.assertLess(recheck, release.index("- name: Draft tag release"))
        self.assertLess(recheck, release.index("- name: Publish the verified initial release"))
        self.assertIn('actions: read', release)
        self.assertIn('RELEASE_SHA: ${{ github.sha }}', release)

    def test_initial_publication_is_manual_and_inherits_read_permission(self):
        root = Path(gate.__file__).resolve().parents[1]
        initial = (root / '.github/workflows/initial-release.yml').read_text()
        events = initial.split('\non:\n')[1].split('\npermissions:\n')[0]
        self.assertIn('workflow_dispatch:', events)
        self.assertNotIn('push:', events)
        self.assertIn('run: test "$GITHUB_REF" = refs/heads/main', initial)
        quality = initial.split('\n  quality:\n')[1].split('\n  packages:\n')[0]
        self.assertIn('contents: read', quality)
        self.assertIn('actions: read', quality)
        self.assertIn('ref: ${{ github.sha }}', quality)
        self.assertIn('run: python3 tools/check_release_quality.py', quality)
        self.assertIn('RELEASE_SHA: ${{ github.sha }}', quality)
        self.assertNotIn('gh api', quality)
        packages = initial.split('\n  packages:\n')[1]
        self.assertIn('actions: read', packages)
        self.assertIn('contents: write', packages)
        self.assertIn('publish_initial: true', packages)
        release = (root / '.github/workflows/release.yml').read_text()
        self.assertEqual(release.count("if: github.event_name == 'push' || inputs.publish_initial"), 2)
        self.assertIn("github.event_name == 'workflow_dispatch' && inputs.publish_initial", release)


if __name__ == "__main__":
    unittest.main()
