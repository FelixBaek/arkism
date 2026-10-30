"""Real minisign round trips and release refusal under tampering. No product executes."""
import base64
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import release_artifacts as release


class WindowsReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="arkism-release-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.dist = self.root / "dist"
        self.dist.mkdir()
        self.key = self.root / "test.key"
        self.pub = self.root / "test.pub"
        mini = os.environ.get("MINISIGN") or shutil.which("minisign")
        if not mini:
            self.fail("Install minisign with tools/install-minisign.sh and set MINISIGN before running this suite")
        subprocess.run([mini, "-G", "-W", "-s", str(self.key), "-p", str(self.pub)],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.key.chmod(0o600)
        self.env = patch.dict(os.environ, MINISIGN=mini,
            ARKISM_UPDATE_PUBLIC_KEY=base64.b64encode(self.pub.read_bytes()).decode())
        self.env.start()
        self.addCleanup(self.env.stop)
        self.exe = self.dist / "Arkism-1.2.3-setup.exe"
        self.exe.write_bytes(b"inert Windows installer fixture\n")
        (self.dist / "Arkism-1.2.3-windows.sbom.spdx.json").write_text(json.dumps({
            "spdxVersion": "SPDX-2.3", "packages": [{"name": "inert Windows fixture"}]}))
        for name in release.server_bundles("1.2.3"):
            self.server_bundle(self.dist / name, release.SERVER_FILES)

    @staticmethod
    def server_bundle(path, files):
        with tarfile.open(path, "w:gz") as tar:
            for name in sorted(files):
                info = tarfile.TarInfo(name)
                info.size = len(b"inert\n")
                tar.addfile(info, io.BytesIO(b"inert\n"))

    def sign(self):
        release.sign(self.dist, "1.2.3", "https://example.test/feed", "stable", self.key)

    def verify(self):
        release.verify(self.dist, "1.2.3", "https://example.test/feed", "stable")

    def test_real_signatures_cover_every_release_asset(self):
        self.sign()
        self.verify()
        tauri = json.loads((self.dist / "latest-tauri.json").read_bytes())
        encoded = tauri["platforms"]["windows-x86_64"]["signature"]
        self.assertEqual(base64.b64decode(encoded), (self.dist / (self.exe.name + ".minisig")).read_bytes())
        # Tauri's wrapper has no other platform; key generation is test-only.
        self.assertEqual(list(tauri["platforms"]), ["windows-x86_64"])
        self.assertFalse(any("PRIVATE" in p.read_text(errors="ignore") for p in self.dist.iterdir()))

    def test_every_tampered_asset_blocks_release(self):
        self.sign()
        for path in list(self.dist.iterdir()):
            with self.subTest(asset=path.name):
                original = path.read_bytes()
                path.write_bytes(original + b"tampered")
                with self.assertRaises((release.ReleaseError, ValueError)):
                    self.verify()
                path.write_bytes(original)
        self.verify()

    def test_server_bundles_are_signed_for_their_own_name_and_checked_for_contents(self):
        self.sign()
        amd64, arm64 = (self.dist / n for n in release.server_bundles("1.2.3"))
        release.verify_signature(amd64, amd64.with_name(amd64.name + ".minisig"), self.pub, "1.2.3")
        # A signed amd64 bundle cannot stand in for the arm64 one.
        shutil.copy(amd64.with_name(amd64.name + ".minisig"), arm64.with_name(arm64.name + ".minisig"))
        shutil.copy(amd64, arm64)
        with self.assertRaises(release.ReleaseError):
            self.verify()
        for files in (release.SERVER_FILES | {"evil"}, release.SERVER_FILES - {"memoryd"}):
            with self.subTest(files=sorted(files)):
                for p in self.dist.glob("*.minisig"):
                    p.unlink()
                self.server_bundle(arm64, files)
                with self.assertRaises(release.ReleaseError):
                    self.sign()
        arm64.unlink()
        with self.assertRaises(release.ReleaseError):
            self.sign()

    def test_feed_and_installer_replacement_cannot_forge_publisher(self):
        self.sign()
        self.exe.write_bytes(b"attacker installer")
        manifest = self.dist / "latest.json"
        data = json.loads(manifest.read_bytes())
        data["sha256"] = release.digest(self.exe)
        data["version"] = "9.9.9"
        manifest.write_text(json.dumps(data))
        sums = self.dist / "SHA256SUMS"
        sums.write_text("".join(f"{release.digest(p)}  {p.name}\n" for p in sorted(self.dist.iterdir())
                                if p.name not in ("SHA256SUMS", "SHA256SUMS.minisig")))
        # Even a feed with matching replacement hashes cannot forge this signature.
        with self.assertRaises(release.ReleaseError):
            self.verify()

    def test_wrong_key_unsigned_release_and_missing_sbom_are_rejected(self):
        with self.assertRaises(release.ReleaseError):
            self.verify()
        self.sign()
        with patch.dict(os.environ, ARKISM_UPDATE_PUBLIC_KEY=""):
            with self.assertRaises(release.ReleaseError):
                self.verify()
        other = self.root / "other.pub"
        subprocess.run([os.environ["MINISIGN"], "-G", "-W", "-s", str(self.root / "other.key"), "-p", str(other)],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        with patch.dict(os.environ, ARKISM_UPDATE_PUBLIC_KEY=base64.b64encode(other.read_bytes()).decode()):
            with self.assertRaises(release.ReleaseError):
                self.verify()
        (self.dist / "Arkism-1.2.3-windows.sbom.spdx.json").unlink()
        with self.assertRaises(release.ReleaseError):
            self.sign()

    def test_wrong_version_channel_or_feed_and_extra_files_block_release(self):
        self.sign()
        for version, feed, channel in [("9.9.9", "https://example.test/feed", "stable"),
                ("1.2.3", "http://example.test/feed", "stable"),
                ("1.2.3", "https://example.test/other", "stable"),
                ("1.2.3", "https://example.test/feed", "beta")]:
            with self.subTest(version=version, feed=feed, channel=channel):
                with self.assertRaises(release.ReleaseError):
                    release.verify(self.dist, version, feed, channel)
        (self.dist / "unexpected.exe").write_bytes(b"unverified")
        with self.assertRaises(release.ReleaseError):
            self.verify()

    def test_signed_inventory_cannot_omit_or_duplicate_files(self):
        self.sign()
        sums = self.dist / "SHA256SUMS"
        for text in ["", sums.read_text() + sums.read_text().splitlines()[0] + "\n"]:
            sums.write_text(text)
            release.sign_file(sums, self.key, "1.2.3", self.pub)
            with self.assertRaises(release.ReleaseError):
                self.verify()

    def test_cli_failure_does_not_leak_key_or_password(self):
        secret = "password-must-not-reach-logs"
        env = dict(os.environ, ARKISM_UPDATE_SIGNING_PASSWORD=secret)
        result = subprocess.run(["python3", release.__file__, "verify", "--directory", str(self.dist),
                                 "--version", "1.2.3", "--feed", "https://example.test/feed"],
                                env=env, text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(secret, result.stdout + result.stderr)
        self.assertNotIn(str(self.key), result.stdout + result.stderr)

    def test_feed_publisher_refuses_unsigned_bundle_before_mutation(self):
        feed = self.root / "feed"
        env = dict(os.environ, ARKISM_RELEASE_DIR=str(self.dist), ARKISM_FEED_DIR=str(feed),
                   ARKISM_UPDATE_FEED="https://example.test/feed", ARKISM_UPDATE_CHANNEL="stable")
        script = Path(release.__file__).resolve().parents[1] / "desktop/publish-update.sh"
        command = ["bash", str(script), "1.2.3"]
        result = subprocess.run(command, env=env, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(feed.exists())
        self.sign()
        result = subprocess.run(command, env=env, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        before = (feed / "latest.json").read_bytes()
        result = subprocess.run(command, env=env, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((feed / "latest.json").read_bytes(), before)
        self.assertFalse(any(p.is_dir() and p.name.startswith(".publish.") for p in feed.iterdir()))


if __name__ == "__main__":
    unittest.main()
