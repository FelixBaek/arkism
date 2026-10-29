//! 서버 업데이트 (SSH): fetches the server bundle with the app's own version from the release,
//! checks its signature with the compiled-in update key, and sends it over the same SSH login
//! to server_update.sh, which backs up, replaces, restarts, checks /healthz and rolls back.
//! The server needs no minisign; it only receives bytes this app has already authenticated.
use crate::{ssh_identity::Store, ssh_tunnel::{self, Target}, update_manifest};
use std::time::Duration;

const SCRIPT: &str = include_str!("server_update.sh");
const BUNDLE_MAX_BYTES: u64 = 256 << 20;

#[derive(serde::Serialize, Debug, PartialEq)]
pub struct Done {
    pub version: String,
    pub steps: Vec<String>,
}

pub fn bundle_name(version: &str, arch: &str) -> String {
    format!("arkism-server-{version}-linux-{arch}.tar.gz")
}

/// `uname -m` on the server, as the release names it.
pub fn arch(uname: &str) -> Result<&'static str, String> {
    match uname.trim() {
        "x86_64" | "amd64" => Ok("amd64"),
        "aarch64" | "arm64" => Ok("arm64"),
        _ => Err("server_arch_unsupported".into()),
    }
}

/// The app follows `.../releases/latest/download`; the server bundle must be this exact release.
pub fn bundle_url(feed: &str, version: &str, file: &str) -> String {
    match feed.strip_suffix("/releases/latest/download") {
        Some(repo) => format!("{repo}/releases/download/v{version}/{file}"),
        None => format!("{feed}/{file}"),
    }
}

fn quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\\''"))
}

/// The remote command line: the script as `sh -c` text, then the version and the form's arkism path.
pub fn remote_command(version: &str, hint: Option<&str>) -> String {
    format!("sh -c {} arkism-server-update {} {}", quote(SCRIPT), quote(version), quote(hint.unwrap_or("")))
}

/// The script's stdout: `step ...` lines and a final `ok <version>`.
pub fn read_result(out: &[u8], want: &str) -> Result<Done, String> {
    let text = String::from_utf8_lossy(out);
    let steps = text.lines().filter_map(|l| l.strip_prefix("step ")).map(str::to_string).collect();
    match text.lines().last().and_then(|l| l.strip_prefix("ok ")) {
        Some(v) if v == want => Ok(Done { version: v.to_string(), steps }),
        _ => Err("server_update_failed: no result".into()),
    }
}

fn download(client: &ureq::Agent, url: &str, limit: u64) -> Result<Vec<u8>, String> {
    let response = client.get(url).call().map_err(|e| match e {
        ureq::Error::StatusCode(404) => "server_release_missing".to_string(),
        _ => "update_feed_unreachable".to_string(),
    })?;
    update_manifest::read_bounded(response.into_body().into_reader(), limit)
}

pub fn run(target: &Target, store: &Store, feed: &str, key: &str, version: &str, step: impl Fn(&str)) -> Result<Done, String> {
    if update_manifest::parse_version(version).is_none() {
        return Err("server_update_version_invalid".into());
    }
    let uname = ssh_tunnel::run_remote(target, store, "uname -m", Vec::new(), Duration::from_secs(20))?;
    let file = bundle_name(version, arch(&String::from_utf8_lossy(&uname))?);
    step("download");
    let client = update_manifest::agent(Duration::from_secs(300));
    let url = bundle_url(feed, version, &file);
    let bundle = download(&client, &url, BUNDLE_MAX_BYTES)?;
    let signature = download(&client, &format!("{url}.minisig"), 8 << 10)?;
    step("verify");
    let signature = std::str::from_utf8(&signature).map_err(|_| "update_signature_invalid")?;
    update_manifest::verify_signed_file(&bundle, signature, key, &file, version)
        .map_err(|_| "update_signature_invalid".to_string())?;
    step("install");
    // Two 60 s health waits (new build, then rollback) plus transfer and restarts.
    let out = ssh_tunnel::run_remote(target, store, &remote_command(version, target.bin.as_deref()), bundle, Duration::from_secs(300))?;
    read_result(&out, version)
}

#[cfg(test)]
mod tests {
    use super::*;
    const KEY: &str = include_str!("../tests/fixtures/update/server-public-key-base64.txt");
    const BUNDLE: &[u8] = include_bytes!("../tests/fixtures/update/server-bundle.bin");
    const SIGNATURE: &str = include_str!("../tests/fixtures/update/server-bundle.bin.minisig");

    #[test]
    fn names_and_addresses_follow_the_release() {
        assert_eq!(bundle_name("1.2.3", arch("x86_64\n").unwrap()), "arkism-server-1.2.3-linux-amd64.tar.gz");
        assert_eq!(arch("aarch64").unwrap(), "arm64");
        assert_eq!(arch("armv7l").unwrap_err(), "server_arch_unsupported");
        assert_eq!(bundle_url("https://github.com/o/r/releases/latest/download", "1.2.3", "f.tar.gz"),
                   "https://github.com/o/r/releases/download/v1.2.3/f.tar.gz");
        assert_eq!(bundle_url("https://feed.test/arkism", "1.2.3", "f.tar.gz"), "https://feed.test/arkism/f.tar.gz");
    }

    #[test]
    fn only_the_signed_bundle_for_this_file_and_version_passes() {
        let file = "arkism-server-1.2.3-linux-amd64.tar.gz";
        update_manifest::verify_signed_file(BUNDLE, SIGNATURE, KEY, file, "1.2.3").unwrap();
        assert!(update_manifest::verify_signed_file(b"other bytes", SIGNATURE, KEY, file, "1.2.3").is_err());
        // An older signed bundle cannot be relabelled as another version or architecture.
        assert!(update_manifest::verify_signed_file(BUNDLE, SIGNATURE, KEY, file, "1.2.4").is_err());
        assert!(update_manifest::verify_signed_file(BUNDLE, SIGNATURE, KEY, "arkism-server-1.2.3-linux-arm64.tar.gz", "1.2.3").is_err());
        let app_key = include_str!("../tests/fixtures/update/public-key-base64.txt");
        assert!(update_manifest::verify_signed_file(BUNDLE, SIGNATURE, app_key, file, "1.2.3").is_err());
    }

    #[test]
    fn the_remote_command_quotes_every_argument() {
        let command = remote_command("1.2.3", Some("/opt/it's/arkism"));
        assert!(command.starts_with("sh -c '# Arkism server update"));
        assert!(command.ends_with(" arkism-server-update '1.2.3' '/opt/it'\\''s/arkism'"));
        // The script reaches the server unchanged after the remote shell removes the quoting.
        #[cfg(unix)]
        let out = std::process::Command::new("sh").arg("-c").arg(format!("printf %s {}", quote(SCRIPT))).output().unwrap();
        #[cfg(unix)]
        assert_eq!(out.stdout, SCRIPT.as_bytes());
    }

    #[test]
    fn the_result_needs_the_final_ok_for_this_version() {
        let done = read_result(b"step found /srv/arkism/bin\nstep backup /x\nstep check\nok 1.2.3\n", "1.2.3").unwrap();
        assert_eq!(done.steps, ["found /srv/arkism/bin", "backup /x", "check"]);
        assert!(read_result(b"step check\nok 1.2.2\n", "1.2.3").is_err());
        assert!(read_result(b"step replace\n", "1.2.3").is_err());
    }
}
