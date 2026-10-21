"""Exercise the Unix runtime script offline, with real checksums and fake builds."""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import sysconfig
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "desktop/installer/prepare-runtime.sh"
PAYLOADS = {"pg": b"synthetic postgres source\n", "vector": b"synthetic vector source\n"}
PROGRAMS = ("postgres", "pg_ctl", "initdb", "psql", "pg_dump", "pg_restore")
FAKE_TOOL = r'''
import json, os, pathlib, sys
tool = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
root = pathlib.Path(os.environ["RUNTIME_TEST_ROOT"])
pg = pathlib.Path(os.environ["RUNTIME_TEST_PG"])
with (root / "events.jsonl").open("a") as log:
    log.write(json.dumps({"tool": tool, "args": args}) + "\n")
if tool == "curl":
    urls = json.loads(os.environ["RUNTIME_TEST_URLS"])
    key = next((key for key, url in urls.items() if url == args[-1]), None)
    if key is None:
        sys.exit("unexpected download URL")
    target = pathlib.Path(args[args.index("-o") + 1])
    target.resolve().relative_to(root)
    data = (root / (key + ".payload")).read_bytes()
    if os.environ.get("RUNTIME_TEST_BAD") == key:
        data += b"synthetic corruption"
    target.write_bytes(data)
    if os.environ.get("RUNTIME_TEST_CURL_FAIL") == key:
        sys.exit("synthetic transfer failure after partial output")
elif tool == "tar":
    source = pathlib.Path(args[args.index("-C") + 1])
    name = pathlib.Path(args[1]).name
    versions = json.loads(os.environ["RUNTIME_TEST_VERSIONS"])
    folder = ("postgresql-" + versions["PG_VERSION"] if name == "pg.tar.bz2"
              else "pgvector-" + versions["VECTOR_VERSION"])
    source = source / folder
    source.mkdir()
    configure = source / "configure"
    configure.write_text(pathlib.Path(sys.argv[0]).read_text())
    configure.chmod(0o700)
elif tool == "make" and "install" in args:
    if pathlib.Path.cwd().name.startswith("postgresql-"):
        for folder in ("bin", "lib", "share"):
            (pg / folder).mkdir(parents=True, exist_ok=True)
        for name in ("postgres", "pg_ctl", "initdb", "psql", "pg_dump", "pg_restore", "pg_config"):
            (pg / "bin" / name).write_bytes(b"synthetic installed program")
        (pg / "share" / "fixture.txt").write_bytes(b"synthetic runtime data")
    else:
        library = "vector.dylib" if sys.platform == "darwin" else "vector.so"
        (pg / "lib" / library).write_bytes(b"synthetic installed extension")
'''


class RuntimeDownloadTests(unittest.TestCase):
    def fixture(self, print_sha=None, bad=None, curl_fail=None):
        temporary = tempfile.TemporaryDirectory(prefix="runtime download test ")
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        cache, stage, commands = root / "cache", root / "stage", root / "commands"
        commands.mkdir()
        (stage / "runtime/pgsql").mkdir(parents=True)
        (stage / "runtime/pgsql/previous.txt").write_bytes(b"previous valid runtime")
        (stage / "keep.txt").write_bytes(b"unrelated stage file")
        source = SCRIPT.read_text()
        versions = dict(re.findall(r"^(PG_VERSION|VECTOR_VERSION)=([^\s]+)$", source, re.M))
        self.assertEqual(set(versions), {"PG_VERSION", "VECTOR_VERSION"})
        for key, data in PAYLOADS.items():
            (root / (key + ".payload")).write_bytes(data)
            variable = "PG_SHA256" if key == "pg" else "VECTOR_SHA256"
            # Only the two pins are rebound in a private script copy. The real
            # sha256sum/shasum and the entire download/build/staging flow run.
            source, count = re.subn(r"^" + variable + r"=[0-9a-f]{64}$",
                                    variable + "=" + hashlib.sha256(data).hexdigest(),
                                    source, flags=re.M)
            self.assertEqual(count, 1)
        script = root / "prepare-runtime.sh"
        script.write_text(source)
        for tool in ("curl", "tar", "make", "otool", "install_name_tool"):
            fake = commands / tool
            fake.write_text("#!" + sys.executable + "\n" + FAKE_TOOL)
            fake.chmod(0o700)
        system = subprocess.check_output(["uname", "-s"], text=True).strip()
        machine = subprocess.check_output(["uname", "-m"], text=True).strip()
        pg = cache / ("postgres-" + versions["PG_VERSION"] + "-pgvector-" +
                      versions["VECTOR_VERSION"] + "-" + system + "-" + machine) / "pgsql"
        urls = {
            "pg": "https://ftp.postgresql.org/pub/source/v" + versions["PG_VERSION"] +
                  "/postgresql-" + versions["PG_VERSION"] + ".tar.bz2",
            "vector": "https://github.com/pgvector/pgvector/archive/refs/tags/v" +
                      versions["VECTOR_VERSION"] + ".tar.gz",
        }
        env = {"PATH": str(commands) + os.pathsep + os.environ["PATH"],
               "HOME": str(root), "LANG": "C", "RUNTIME_TEST_ROOT": str(root),
               "RUNTIME_TEST_PG": str(pg), "RUNTIME_TEST_URLS": json.dumps(urls),
               "RUNTIME_TEST_VERSIONS": json.dumps(versions)}
        # Portable CI Python needs its own shared library. Keep the fixture's
        # clean environment; do not inherit arbitrary host library paths.
        if sys.platform.startswith("linux") and sysconfig.get_config_var("Py_ENABLE_SHARED"):
            # sysconfig LIBDIR can retain the build machine's prefix in a
            # relocated setup-python distribution. Use the deployed prefix.
            library = sysconfig.get_config_var("INSTSONAME") or sysconfig.get_config_var("LDLIBRARY")
            candidates = (Path(sys.base_prefix) / "lib", Path(sys.base_prefix) / "lib64",
                          Path(sysconfig.get_config_var("LIBDIR")))
            directory = next((p for p in candidates if (p / library).is_file()), None)
            if directory is None:
                raise RuntimeError("active Python shared library is unavailable")
            env["LD_LIBRARY_PATH"] = str(directory)
        for key, value in (("ARKISM_PRINT_SHA", print_sha), ("RUNTIME_TEST_BAD", bad),
                           ("RUNTIME_TEST_CURL_FAIL", curl_fail)):
            if value is not None:
                env[key] = value
        return root, script, cache, stage, env, urls

    def run_script(self, fixture):
        root, script, cache, stage, env, _ = fixture
        result = subprocess.run([shutil.which("bash"), str(script), str(cache), str(stage)],
                                env=env, text=True, capture_output=True, timeout=15)
        log = root / "events.jsonl"
        events = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
        return result, events

    @staticmethod
    def stage_files(stage):
        return {path.relative_to(stage).as_posix(): None if path.is_dir() else path.read_bytes()
                for path in stage.rglob("*")}

    def assert_download_stopped(self, fixture):
        before = self.stage_files(fixture[3])
        result, events = self.run_script(fixture)
        self.assertNotEqual(result.returncode, 0,
                            result.stdout + " observed tools: " + str([e["tool"] for e in events]))
        self.assertEqual(self.stage_files(fixture[3]), before)
        self.assertTrue(all(event["tool"] == "curl" for event in events), events)
        self.assertNotIn("built-in runtime staged:", result.stdout)
        return result, events

    def test_mismatch_never_extracts_builds_or_stages_with_diagnostic_flag(self):
        for artifact in PAYLOADS:
            for flag in (None, "0", "1"):
                with self.subTest(artifact=artifact, print_sha=flag):
                    fixture = self.fixture(print_sha=flag, bad=artifact)
                    result, events = self.assert_download_stopped(fixture)
                    self.assertIn("checksum mismatch for " + fixture[5][artifact], result.stderr)
                    observed = hashlib.sha256(PAYLOADS[artifact] + b"synthetic corruption").hexdigest()
                    self.assertIn(observed, result.stderr)
                    self.assertEqual(len(events), 1 if artifact == "pg" else 2)
                    if flag is not None:
                        self.assertIn("sha256 ", result.stderr)

    def test_curl_partial_failure_never_reaches_checksum_extract_build_or_stage(self):
        for artifact in PAYLOADS:
            for flag in (None, "0", "1"):
                with self.subTest(artifact=artifact, print_sha=flag):
                    fixture = self.fixture(print_sha=flag, curl_fail=artifact)
                    result, events = self.assert_download_stopped(fixture)
                    self.assertIn("synthetic transfer failure", result.stderr)
                    self.assertNotIn("checksum mismatch", result.stderr)
                    self.assertNotIn("sha256 ", result.stderr)
                    self.assertEqual(len(events), 1 if artifact == "pg" else 2)

    def test_matching_pins_preserve_build_output_and_cached_staging(self):
        for flag in (None, "0", "1"):
            with self.subTest(print_sha=flag):
                fixture = self.fixture(print_sha=flag)
                result, events = self.run_script(fixture)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("built-in runtime staged:", result.stdout)
                self.assertEqual(result.stderr, "")
                downloads = [event for event in events if event["tool"] == "curl"]
                self.assertEqual([e["args"][-1] for e in downloads], list(fixture[5].values()))
                self.assertTrue(all(e["args"][:4] == ["-fsSL", "--retry", "3", "-o"] for e in downloads))
                self.assertEqual(sum(e["tool"] == "tar" for e in events), 2)
                self.assertEqual(sum(e["tool"] == "configure" for e in events), 1)
                self.assertEqual(sum(e["tool"] == "make" for e in events), 4)
                stage = fixture[3]
                self.assertEqual(sorted(p.name for p in (stage / "runtime/pgsql/bin").iterdir()), sorted(PROGRAMS))
                self.assertNotIn("runtime/pgsql/previous.txt", self.stage_files(stage))
                self.assertEqual((stage / "keep.txt").read_bytes(), b"unrelated stage file")
                staged = self.stage_files(stage)
                (fixture[0] / "events.jsonl").write_text("")
                result, events = self.run_script(fixture)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(events, [])
                self.assertEqual(self.stage_files(stage), staged)


if __name__ == "__main__":
    unittest.main()
