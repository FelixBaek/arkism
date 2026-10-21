"""Sign and independently verify a Windows release. Never publishes or generates keys.

ARKISM_UPDATE_PUBLIC_KEY is base64 of the complete minisign public-key file,
matching Tauri's public-key encoding. Secret keys are local files; the optional
password comes from ARKISM_UPDATE_SIGNING_PASSWORD and is sent only over stdin.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.parse import urlsplit


class ReleaseError(Exception):
    pass


def run_minisign(arguments, password=None):
    try:
        subprocess.run([os.environ.get("MINISIGN", "minisign"), *map(str, arguments)],
                       input=((password or "") + "\n").encode(),
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                       check=True, timeout=120)
    except (OSError, subprocess.SubprocessError) as exc:
        raise ReleaseError("minisign signing or verification failed") from exc


def public_key():
    try:
        data = base64.b64decode(os.environ["ARKISM_UPDATE_PUBLIC_KEY"].strip(), validate=True)
        lines = data.decode("ascii").splitlines()
        raw = base64.b64decode(lines[1], validate=True)
        if len(lines) != 2 or not lines[0].startswith("untrusted comment:") or len(raw) != 42 or raw[:2] != b"Ed":
            raise ValueError()
        return data
    except (KeyError, ValueError, UnicodeError, IndexError) as exc:
        raise ReleaseError("a pinned Tauri-encoded minisign public key is required") from exc


def validate(version, feed, channel):
    parts = version.split(".")
    if (len(parts) != 3 or any(not re.fullmatch(r"0|[1-9][0-9]*", p) or int(p) > 2**32-1 for p in parts)
            or channel not in ("stable", "beta")):
        raise ReleaseError("invalid version or channel")
    url = urlsplit(feed)
    if (url.scheme != "https" or not url.hostname or url.username or url.password
            or url.query or url.fragment or feed.endswith("/")):
        raise ReleaseError("an HTTPS feed directory without credentials, query or fragment is required")


def names(version, channel):
    installer = f"Arkism-{version}-setup.exe"
    manifest = "latest.json" if channel == "stable" else "latest-beta.json"
    return installer, manifest, f"Arkism-{version}-windows.sbom.spdx.json"


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def require_file(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size == 0:
        raise ReleaseError(f"missing, empty or linked release input: {path.name}")


def verify_signature(path, signature, key, version=None):
    lines = signature.read_text(encoding="ascii").splitlines()
    if len(lines) != 4 or not lines[0].startswith("untrusted comment:") or not lines[2].startswith("trusted comment:"):
        raise ReleaseError("invalid minisign signature envelope")
    run_minisign(["-V", "-H", "-q", "-p", key, "-m", path, "-x", signature])
    # Only authenticated comments may bind the announced file/version.
    if version is not None:
        fields = lines[2].removeprefix("trusted comment: ").split("\t")
        if (fields.count(f"file:{path.name}") != 1 or fields.count(f"version:{version}") != 1
                or sum(f.startswith("file:") for f in fields) != 1
                or sum(f.startswith("version:") for f in fields) != 1):
            raise ReleaseError("signed file or version mismatch")


def sign_file(path, key, version, public):
    signature = path.with_name(path.name + ".minisig")
    # Modern Tauri recognizes the authenticated `version:` comment field.
    comment = f"file:{path.name}\tversion:{version}"
    run_minisign(["-S", "-s", key, "-m", path, "-x", signature, "-t", comment],
                 os.environ.get("ARKISM_UPDATE_SIGNING_PASSWORD"))
    verify_signature(path, signature, public, version)
    return signature


def check_sbom(path):
    require_file(path)
    try:
        sbom = json.loads(path.read_bytes())
        if not sbom.get("spdxVersion", "").startswith("SPDX-") or not sbom.get("packages"):
            raise ValueError()
    except (ValueError, AttributeError) as exc:
        raise ReleaseError("a nonempty Syft SPDX SBOM is required") from exc


def sign(directory, version, feed, channel, key):
    validate(version, feed, channel)
    installer, manifest, sbom = names(version, channel)
    key = Path(key).resolve(strict=True)
    if key.is_relative_to(directory.resolve()):
        raise ReleaseError("private signing key must be outside the release directory")
    if os.name != "nt" and key.stat().st_mode & 0o077:
        raise ReleaseError("private signing key must have mode 0600")
    require_file(directory / installer)
    check_sbom(directory / sbom)
    with tempfile.TemporaryDirectory(prefix="arkism-verify-key-") as tmp:
        public = Path(tmp) / "public.key"
        public.write_bytes(public_key())
        signature = sign_file(directory / installer, key, version, public)
        wrapped = base64.b64encode(signature.read_bytes()).decode("ascii")
        (directory / (installer + ".sig")).write_text(wrapped + "\n", encoding="ascii")
        checksum = digest(directory / installer)
        (directory / (installer + ".sha256")).write_text(f"{checksum}  {installer}\n", encoding="ascii")
        document = dict(version=version, file=installer, sha256=checksum, channel=channel, signature=wrapped)
        (directory / manifest).write_text(json.dumps(document, separators=(",", ":")) + "\n", encoding="utf-8")
        # The same installer and .sig work with tauri-plugin-updater. Its static
        # manifest has a different layout, provided explicitly for migration.
        tauri = {"version": version, "platforms": {"windows-x86_64": {
            "url": f"{feed}/{installer}", "signature": wrapped}}}
        (directory / "latest-tauri.json").write_text(json.dumps(tauri, separators=(",", ":")) + "\n", encoding="utf-8")
        for name in (manifest, "latest-tauri.json", sbom):
            sign_file(directory / name, key, version, public)
        files = sorted(p for p in directory.iterdir() if p.name not in ("SHA256SUMS", "SHA256SUMS.minisig"))
        for path in files:
            require_file(path)
            if not re.fullmatch(r"[A-Za-z0-9._-]+", path.name):
                raise ReleaseError("unsafe release filename")
        (directory / "SHA256SUMS").write_text("".join(f"{digest(p)}  {p.name}\n" for p in files), encoding="ascii")
        sign_file(directory / "SHA256SUMS", key, version, public)
    verify(directory, version, feed, channel)


def verify_inventory(directory, required, public, version):
    for name in required | {"SHA256SUMS", "SHA256SUMS.minisig"}:
        require_file(directory / name)
    if {p.name for p in directory.iterdir()} != required | {"SHA256SUMS", "SHA256SUMS.minisig"}:
        raise ReleaseError("unexpected release assets")
    verify_signature(directory / "SHA256SUMS", directory / "SHA256SUMS.minisig", public, version)
    sums = {}
    for line in (directory / "SHA256SUMS").read_text(encoding="ascii").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9._-]+)", line)
        if not match or match[2] in sums:
            raise ReleaseError("invalid or duplicate checksum entry")
        sums[match[2]] = match[1]
    if set(sums) != required:
        raise ReleaseError("checksum inventory is incomplete")
    for name, expected in sums.items():
        if digest(directory / name) != expected:
            raise ReleaseError(f"checksum mismatch: {name}")
    return sums


def verify(directory, version, feed, channel):
    validate(version, feed, channel)
    installer, manifest, sbom = names(version, channel)
    required = {installer, installer + ".sha256", installer + ".minisig", installer + ".sig",
                manifest, manifest + ".minisig", "latest-tauri.json", "latest-tauri.json.minisig",
                sbom, sbom + ".minisig"}
    # Provenance, when present, is checked by `gh attestation verify` in CI and
    # is signed into SHA256SUMS too. Never accept arbitrary extra release assets.
    if (directory / "provenance.sigstore.json").exists():
        required.add("provenance.sigstore.json")
    with tempfile.TemporaryDirectory(prefix="arkism-verify-key-") as tmp:
        public = Path(tmp) / "public.key"
        public.write_bytes(public_key())
        sums = verify_inventory(directory, required, public, version)
        for name in (installer, manifest, "latest-tauri.json", sbom):
            verify_signature(directory / name, directory / (name + ".minisig"), public, version)
        document = json.loads((directory / manifest).read_bytes())
        signature = base64.b64encode((directory / (installer + ".minisig")).read_bytes()).decode("ascii")
        if document != dict(version=version, file=installer, sha256=sums[installer], channel=channel, signature=signature):
            raise ReleaseError("signed manifest does not describe the release")
        if (directory / (installer + ".sig")).read_text(encoding="ascii").strip() != signature:
            raise ReleaseError("Tauri signature wrapper mismatch")
        tauri = json.loads((directory / "latest-tauri.json").read_bytes())
        if tauri != {"version": version, "platforms": {"windows-x86_64": {
                "url": f"{feed}/{installer}", "signature": signature}}}:
            raise ReleaseError("Tauri manifest does not describe the release")
        check_sbom(directory / sbom)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("sign", "verify", "check-key"))
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--version")
    parser.add_argument("--feed")
    parser.add_argument("--channel", choices=("stable", "beta"), default="stable")
    parser.add_argument("--key", type=Path)
    args = parser.parse_args()
    try:
        if args.mode == "check-key":
            public_key()
        elif not all((args.directory, args.version, args.feed)):
            parser.error("--directory, --version and --feed are required")
        elif args.mode == "sign":
            if args.key:
                sign(args.directory, args.version, args.feed, args.channel, args.key)
            else:
                # Environment-protected GitHub signing job only. The build job
                # has no access to this secret; deletion also runs on failure.
                secret = os.environ.pop("ARKISM_UPDATE_SIGNING_KEY", "")
                if not secret:
                    raise ReleaseError("signing key is required")
                with tempfile.TemporaryDirectory(prefix="arkism-signing-key-") as tmp:
                    key = Path(tmp) / "private.key"
                    key.write_text(secret, encoding="ascii")
                    key.chmod(0o600)
                    sign(args.directory, args.version, args.feed, args.channel, key)
        else:
            verify(args.directory, args.version, args.feed, args.channel)
    except (ReleaseError, OSError, ValueError) as exc:
        # Do not print subprocess output, key material, or parsed manifest data.
        parser.exit(1, f"Windows release verification failed: {type(exc).__name__}\n")
    print("Windows release verification passed.")


if __name__ == "__main__":
    main()
