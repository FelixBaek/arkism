"""The remote server update script (desktop/src-tauri/src/server_update.sh) against a fake host.

Everything runs in a temporary folder: fake sudo, id and systemctl come first on PATH, so the
script never touches a real service or a real install.
"""
import io
import os
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "desktop/src-tauri/src/server_update.sh"
FILES = ["arkism", "arkism-crew", "memoryd", "memory-principal"]

FAKES = {
    # FAKE_SUDO=ok allows passwordless sudo; anything else fails like `sudo -n` without rights.
    "sudo": '[ "${FAKE_SUDO:-}" = ok ] || exit 1\n[ "$1" = -n ] && shift\nexec "$@"\n',
    "id": 'echo "${FAKE_UID:-1000}"\n',
    "systemctl": r'''case "$1" in
list-units) [ -n "${FAKE_UNIT:-}" ] && echo "$FAKE_UNIT loaded active running Arkism API";;
show) echo "{ path=$FAKE_DIR/memoryd ; argv[]=$FAKE_DIR/memoryd --serve ; }";;
restart) echo "$2" >> "$FAKE_STATE/restarts"; "$FAKE_DIR/memoryd" > "$FAKE_STATE/health";;
esac
''',
}

# The bundled arkism: `version` and a server-check reading what the restarted "memoryd" reported.
ARKISM = r'''case "$1" in
version) echo "{v}";;
server-check) h=$(cat "$FAKE_STATE/health"); [ "$h" != broken ] && {{ [ "$3" = "" ] || [ "$h" = "$3" ]; }};;
esac
'''


def program(text):
    return "#!/bin/sh\n" + text


def bundle(version, health=None, extra=None):
    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode="w:gz") as tar:
        for name in FILES + ([extra] if extra else []):
            body = ARKISM.format(v=version) if name == "arkism" else f"echo {health or version}\n"
            data = program(body).encode()
            info = tarfile.TarInfo(name)
            info.size, info.mode = len(data), 0o755
            tar.addfile(info, io.BytesIO(data))
    return out.getvalue()


class ServerUpdateScript(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.addCleanup(subprocess.run, ["rm", "-rf", str(self.root)])
        self.bin, self.dir, self.state = (self.root / n for n in ("fakebin", "srv/bin", "state"))
        for d in (self.bin, self.dir, self.state):
            d.mkdir(parents=True)
        for name, text in FAKES.items():
            (self.bin / name).write_text(program(text))
            (self.bin / name).chmod(0o755)
        for name in FILES:
            (self.dir / name).write_text(program("echo 0.1.5\n"))
            (self.dir / name).chmod(0o755)
        self.env = {"PATH": f"{self.bin}:/usr/bin:/bin", "FAKE_DIR": str(self.dir),
                    "FAKE_STATE": str(self.state), "FAKE_SUDO": "ok", "FAKE_UNIT": "arkism-api.service"}

    def run_update(self, data, version="0.1.7", **env):
        return subprocess.run(["sh", str(SCRIPT), version, str(self.dir / "arkism")], input=data,
                              capture_output=True, env={**self.env, **env}, timeout=120)

    def installed(self):
        return {name: (self.dir / name).read_text() for name in FILES}

    def backups(self):
        return sorted(self.dir.glob(".backup-*"))

    def test_installs_backs_up_restarts_and_checks(self):
        r = self.run_update(bundle("0.1.7"))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn(b"ok 0.1.7", r.stdout)
        self.assertIn("echo 0.1.7", self.installed()["memoryd"])
        (backup,) = self.backups()
        self.assertIn("echo 0.1.5", (backup / "memoryd").read_text())
        self.assertEqual((self.state / "restarts").read_text(), "arkism-api.service\n")
        self.assertEqual(list(self.dir.glob(".update-*")), [])

    def test_a_new_server_that_does_not_come_up_is_rolled_back(self):
        before = self.installed()
        r = self.run_update(bundle("0.1.7", health="broken"))
        self.assertEqual(r.returncode, 3)
        self.assertIn(b"health_failed_rolled_back", r.stderr)
        self.assertEqual(self.installed(), before)
        self.assertEqual((self.state / "restarts").read_text().count("\n"), 2)

    @unittest.skipIf(os.geteuid() == 0, "root writes a read-only folder anyway")
    def test_without_write_or_sudo_nothing_changes(self):
        before = self.installed()
        self.dir.chmod(0o555)
        self.addCleanup(self.dir.chmod, 0o755)
        r = self.run_update(bundle("0.1.7"), FAKE_SUDO="no")
        self.assertEqual(r.returncode, 3)
        self.assertTrue(r.stderr.startswith(b"needs_write"), r.stderr)
        self.assertEqual(self.installed(), before)

    def test_a_writable_folder_still_needs_sudo_to_restart(self):
        r = self.run_update(bundle("0.1.7"), FAKE_SUDO="no")
        self.assertIn(b"needs_sudo_restart: arkism-api.service", r.stderr)
        self.assertEqual(self.backups(), [])

    def test_root_needs_no_sudo(self):
        r = self.run_update(bundle("0.1.7"), FAKE_SUDO="no", FAKE_UID="0")
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_no_service_stops_before_changing_anything(self):
        r = self.run_update(bundle("0.1.7"), FAKE_UNIT="")
        self.assertTrue(r.stderr.startswith(b"no_service"), r.stderr)
        self.assertEqual(self.backups(), [])

    def test_unexpected_bundle_contents_are_refused(self):
        before = self.installed()
        for data, version in ((bundle("0.1.7", extra="evil"), "0.1.7"), (bundle("0.1.6"), "0.1.7")):
            r = self.run_update(data, version)
            self.assertTrue(r.stderr.startswith(b"bad_archive"), r.stderr)
        self.assertEqual(self.installed(), before)
        self.assertEqual(self.backups(), [])
        self.assertEqual(list(self.dir.glob(".update-*")), [])


if __name__ == "__main__":
    unittest.main()
