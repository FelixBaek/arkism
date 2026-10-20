import importlib.util
import pathlib
import subprocess
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("public_push", pathlib.Path(__file__).with_name("check_public_push.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class PublicPushTests(unittest.TestCase):
    """Exercise public audit contracts using isolated synthetic fixtures."""
    def setUp(self):
        """Create an isolated public Git baseline for each test."""
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name) / "repo"
        self.root.mkdir()
        self.git("init", "-q")
        self.git("config", "user.name", "Test")
        self.git("config", "user.email", "test@example.invalid")
        self.write("app.txt", "public\n")
        self.commit("public base")
        self.base = self.git("rev-parse", "HEAD").decode().strip()
        self.patterns = [MODULE.re.compile("PRIVATE_SENTINEL")]

    def git(self, *args):
        """Run a test Git command in the temporary repository."""
        return subprocess.check_output(["git", "-C", str(self.root), *args], stderr=subprocess.PIPE)

    def write(self, name, text):
        """Write a synthetic fixture without touching production data."""
        p = self.root / name
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def commit(self, message):
        """Record the staged synthetic fixture."""
        self.git("add", "-A")
        self.git("commit", "-qm", message)

    def check(self):
        """Audit the test branch against its initial public baseline."""
        return MODULE.check(self.root, self.base, self.patterns)[0]

    def test_clean_public_branch(self):
        """Verify clean public branch."""
        self.write("app.txt", "new public content")
        self.commit("public change")
        self.assertEqual(self.check(), [])

    def test_deleted_private_file_remains_blocked_in_history(self):
        """Verify deleted private file remains blocked in history."""
        self.write(".private/notes.md", "internal")
        self.commit("add notes")
        self.git("rm", "-r", ".private")
        self.commit("delete notes")
        self.assertTrue(any("forbidden path" in x for x in self.check()))

    def test_deleted_content_and_commit_messages_scanned_without_echoing_content(self):
        """Verify deleted content and commit messages scanned without echoing content."""
        self.write("temporary.txt", "PRIVATE_SENTINEL")
        self.commit("PRIVATE_SENTINEL")
        self.git("rm", "temporary.txt")
        self.commit("remove")
        result = self.check()
        self.assertTrue(any("commit message" in x for x in result))
        self.assertTrue(any("temporary.txt" in x for x in result))
        self.assertNotIn("PRIVATE_SENTINEL", "\n".join(result))

    def test_index_blob_checked_when_worktree_is_cleaned(self):
        """Verify index blob checked when worktree is cleaned."""
        self.write("app.txt", "PRIVATE_SENTINEL")
        self.git("add", "app.txt")
        self.write("app.txt", "public")
        self.assertTrue(any("index:app.txt" in x for x in self.check()))

    def test_unstaged_content_checked(self):
        """Verify unstaged content checked."""
        self.write("app.txt", "PRIVATE_SENTINEL")
        self.assertTrue(any("worktree:app.txt" in x for x in self.check()))

    def test_forbidden_names_at_any_depth(self):
        """Verify forbidden names at any depth."""
        for name in (".gitignore", ".dockerignore", ".env.example", "held-out/task.json"):
            self.write("nested/" + name, "public")
        self.git("add", "-f", "nested")
        self.assertEqual(sum("forbidden path" in x for x in self.check()), 4)

    def test_unrelated_public_base_rejected(self):
        """Verify unrelated public base rejected."""
        self.git("checkout", "--orphan", "unrelated")
        self.git("rm", "-rf", ".")
        self.write("other.txt", "public")
        self.commit("different root")
        with self.assertRaises(ValueError):
            self.check()

    def test_symlink_target_checked_without_reading_external_file(self):
        """Verify symlink target checked without reading external file."""
        outside = pathlib.Path(self.temp.name) / "outside"
        outside.write_text("PRIVATE_SENTINEL")
        (self.root / "link").symlink_to(outside)
        self.git("add", "link")
        self.assertEqual(self.check(), [])


    def test_merge_side_branch_history_checked(self):
        """Verify merge side branch history checked."""
        self.git("checkout", "-qb", "side")
        self.write("temporary.txt", "PRIVATE_SENTINEL")
        self.commit("temporary")
        self.git("rm", "temporary.txt")
        self.commit("remove")
        side = self.git("rev-parse", "HEAD").decode().strip()
        self.git("checkout", "-qb", "public", self.base)
        self.write("public.txt", "public")
        self.commit("public")
        self.git("merge", "--no-ff", "-m", "merge side", side)
        self.assertTrue(any("temporary.txt" in x for x in self.check()))

    def test_cli_refuses_missing_or_internal_denylist(self):
        """Verify cli refuses missing or internal denylist."""
        import os
        import sys
        env = dict(os.environ)
        env.pop("ARKISM_PUBLIC_DENYLIST", None)
        script = str(pathlib.Path(__file__).with_name("check_public_push.py"))
        args = [sys.executable, script, "--repo", str(self.root), "--base", self.base]
        result = subprocess.run(args, capture_output=True, env=env)
        self.assertEqual(result.returncode, 2)
        self.write("denylist.txt", "PRIVATE_SENTINEL")
        result = subprocess.run(args + ["--denylist", str(self.root / "denylist.txt")],
                                capture_output=True, env=env)
        self.assertEqual(result.returncode, 2)
        self.assertNotIn(b"PRIVATE_SENTINEL", result.stdout + result.stderr)


    def test_license_and_environment_files_checked(self):
        """Verify license and environment files checked."""
        self.write("LICENSE", "PRIVATE_SENTINEL")
        self.write(".env", "public")
        self.write(".env.local", "public")
        self.git("add", "-A")
        result = self.check()
        self.assertTrue(any("LICENSE" in x and "denylist" in x for x in result))
        self.assertEqual(sum("forbidden path" in x for x in result), 2)

    def test_private_path_is_not_echoed(self):
        """Verify private path is not echoed."""
        self.write("PRIVATE_SENTINEL.txt", "public")
        self.commit("file")
        result = self.check()
        self.assertTrue(any("[private-path]" in x for x in result))
        self.assertNotIn("PRIVATE_SENTINEL", "\n".join(result))


    def test_exact_reviewed_public_blob_allowance(self):
        """Verify exact reviewed public blob allowance."""
        self.write("LICENSE", "PRIVATE_SENTINEL copyright")
        self.commit("public copyright")
        self.base = self.git("rev-parse", "HEAD").decode().strip()
        blob = self.git("rev-parse", "HEAD:LICENSE").decode().strip()
        self.assertEqual(MODULE.check(self.root, self.base, self.patterns, [blob])[0], [])
        self.write("LICENSE", "PRIVATE_SENTINEL copyright plus private addition")
        self.assertTrue(MODULE.check(self.root, self.base, self.patterns, [blob])[0])
        self.commit("changed license")
        self.assertTrue(MODULE.check(self.root, self.base, self.patterns, [blob])[0])

    def test_nonbase_blob_cannot_be_allowed(self):
        """Verify nonbase blob cannot be allowed."""
        self.write("later.txt", "PRIVATE_SENTINEL")
        self.commit("later")
        blob = self.git("rev-parse", "HEAD:later.txt").decode().strip()
        with self.assertRaises(ValueError):
            MODULE.check(self.root, self.base, self.patterns, [blob])

    def test_blob_allowance_does_not_exempt_paths_or_commit_messages(self):
        """Verify blob allowance does not exempt paths or commit messages."""
        blob = self.git("rev-parse", "HEAD:app.txt").decode().strip()
        self.write(".private/notes", "public\n")
        self.commit("PRIVATE_SENTINEL")
        findings = MODULE.check(self.root, self.base, self.patterns, [blob])[0]
        self.assertTrue(any("forbidden path" in x for x in findings))
        self.assertTrue(any("commit message" in x for x in findings))


    def test_author_and_committer_identity_are_scanned(self):
        """Verify author and committer identity are scanned."""
        import os
        for identity in ("GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
                         "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"):
            with self.subTest(identity=identity):
                self.git("reset", "--hard", self.base)
                self.write("app.txt", identity)
                self.git("add", "-A")
                env = dict(os.environ)
                env[identity] = "PRIVATE_SENTINEL@example.invalid"
                subprocess.check_call(["git", "-C", str(self.root), "commit", "-qm", "public change"],
                                      env=env)
                findings = self.check()
                self.assertTrue(any("identities" in item for item in findings))
                self.assertNotIn("PRIVATE_SENTINEL", "\n".join(findings))

    def test_option_like_base_and_git_arguments_are_rejected(self):
        """Verify option like base and git arguments are rejected."""
        from unittest.mock import patch
        with patch.object(MODULE.subprocess, "check_output") as run:
            for base in ("--help", "-c", "--output=private", "main;echo", "main\u00e9"):
                with self.assertRaises(ValueError):
                    MODULE.check(self.root, base, self.patterns)
            with self.assertRaises(ValueError):
                MODULE.git(self.root, "rev-parse", "--output=private")
            run.assert_not_called()


    def test_exact_public_identity_pair_is_scoped_to_metadata(self):
        """Allow only the reviewed pair, not mismatched emails or private messages."""
        name, email = "PRIVATE_SENTINEL", "public@example.invalid"
        self.git("config", "user.name", name)
        self.git("config", "user.email", email)
        self.write("app.txt", "public update")
        self.commit("public update")
        allow = [(name, email)]
        self.assertEqual(MODULE.check(self.root, self.base, self.patterns, (), allow)[0], [])
        self.assertTrue(MODULE.check(self.root, self.base, self.patterns, (),
                                    [(name, "other@example.invalid")])[0])
        self.write("app.txt", "PRIVATE_SENTINEL")
        self.commit("PRIVATE_SENTINEL")
        result = MODULE.check(self.root, self.base, self.patterns, (), allow)[0]
        self.assertTrue(any("commit message" in item for item in result))
        self.assertTrue(any("app.txt" in item for item in result))


if __name__ == "__main__":
    unittest.main()
