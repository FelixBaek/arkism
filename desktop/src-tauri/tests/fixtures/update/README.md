# Inert Windows update signature fixture

`public-key-base64.txt` is a Tauri-encoded minisign **public** key for tests only.
`latest.json` describes the literal bytes `inert Windows installer fixture\n`.
The installer signature inside that manifest and `latest.json.minisig` were
produced by minisign 0.12. The temporary private test key was discarded.
No executable payload or production signing key is included.

These fixtures exercise verification in the actual Rust updater, independently
of the Python release verifier. Never use this public key in production builds.

`server-bundle.bin` stands in for a server bundle: its signature
`server-bundle.bin.minisig` (key `server-public-key-base64.txt`, minisign 0.12)
signs `file:arkism-server-1.2.3-linux-amd64.tar.gz` and `version:1.2.3`.
That private test key was discarded too.
