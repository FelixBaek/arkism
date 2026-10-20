//! Connecting to an Arkism server the way one logs in with ssh: the user types the SSH address
//! (`user@host`, optionally `:port`) and nothing else. Over that login the app asks the server
//! for its connection details (`arkism connect-info`: API address, owner token, scope) and
//! forwards the API port to this PC (`ssh -N -L`). The UI then talks to `http://127.0.0.1:PORT`
//! as it would to the built-in Arkism. The forward is restarted on the same port if it drops.
//!
//! Authentication is whatever the user's ssh already does (keys, agent, ~/.ssh/config), or a
//! password the user types. After a login works the password goes to the OS credential store
//! (Windows Credential Manager, macOS Keychain, Linux Secret Service), never to a file, so the
//! next launch connects without asking. ssh gets it through SSH_ASKPASS, which runs this app
//! again in askpass mode (see `askpass`). A host key must be
//! reviewed and pinned before target authentication, so a password only reaches a pinned host.

use crate::lifecycle::Gate;
use crate::ssh_exec::{self, Process};
use crate::ssh_identity::{Pin, Store};
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc::Receiver;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

// The arkism program on the server. `ark` is the server install's wrapper that loads arkism.env
// (the server's scope); a plain `arkism` on PATH covers other installs.
const SERVER_BIN: &str = match option_env!("ARKISM_SERVER_BIN") {
    Some(value) => value,
    None => "/srv/arkism/bin/arkism",
};

#[derive(Clone, Debug, PartialEq)]
pub struct Target {
    /// `host` or `user@host`, exactly as ssh takes it (a ~/.ssh/config alias works too).
    pub host: String,
    pub port: Option<u16>,
    /// 인증 키 파일: a private key to use instead of ssh's defaults (agent, ~/.ssh/id_*).
    pub key: Option<std::path::PathBuf>,
    /// Arkism 경로: the remote arkism program, tried before the usual places.
    pub bin: Option<String>,
    /// 비밀번호: log in with this instead of keys. Never written anywhere.
    pub password: Option<Password>,
}

/// A login password; `{:?}` never prints it.
#[derive(Clone, PartialEq)]
pub struct Password(String);

impl std::fmt::Debug for Password {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Password(***)")
    }
}

// ssh runs SSH_ASKPASS (this app) with the prompt; the password reaches it in this variable,
// set only on the ssh child, never on the app itself.
const ASKPASS_ENV: &str = "ARKISM_SSH_ASKPASS";

/// Askpass mode: when ssh started this program to ask for the password, prints it and returns
/// the exit code. `None` means a normal app launch.
pub fn askpass() -> Option<i32> {
    let password = std::env::var(ASKPASS_ENV).ok()?;
    let prompt = std::env::args().nth(1).unwrap_or_default();
    Some(match askpass_answer(&prompt, &password) {
        Some(a) => {
            use std::io::Write;
            let mut out = std::io::stdout();
            if out.write_all(a.as_bytes()).and_then(|_| out.write_all(b"\n")).is_ok() {
                0
            } else {
                1
            }
        }
        None => 1,
    })
}

/// Only password prompts get the password; a yes/no question (an unknown or changed host) gets no.
fn askpass_answer<'a>(prompt: &str, password: &'a str) -> Option<&'a str> {
    let p = prompt.to_ascii_lowercase();
    (!p.contains("yes/no") && !p.contains("passphrase")).then_some(password)
}

/// Turns a pinned, non-batch ssh command into a password login answered by `askpass`.
pub(crate) fn password_login(command: &mut std::process::Command, password: &Password) {
    // One try per login, so a wrong password does not count three times against the server.
    command.args([
        "-o",
        "PreferredAuthentications=password,keyboard-interactive",
        "-o",
        "PubkeyAuthentication=no",
        "-o",
        "NumberOfPasswordPrompts=1",
    ]);
    if let Ok(exe) = std::env::current_exe() {
        command.env("SSH_ASKPASS", exe);
    }
    // OpenSSH 8.4+ honours `force`; older ones (Windows 10's 8.1) use askpass when DISPLAY is set and there is no tty.
    command
        .env("SSH_ASKPASS_REQUIRE", "force")
        .env(ASKPASS_ENV, &password.0);
    if std::env::var_os("DISPLAY").is_none() {
        command.env("DISPLAY", "arkism:0");
    }
}

/// Parses what the user typed. Anything ssh could read as an option or a second word is refused.
pub fn parse_target(input: &str) -> Result<Target, String> {
    let s = input
        .trim()
        .strip_prefix("ssh ")
        .map(str::trim)
        .unwrap_or(input.trim());
    let (host, port) = match s.rsplit_once(':') {
        Some((h, p)) if !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()) => {
            let port = p
                .parse::<u16>()
                .ok()
                .filter(|p| *p != 0)
                .ok_or("ssh_address_invalid")?;
            (h, Some(port))
        }
        Some(_) => return Err("ssh_address_invalid".into()),
        None => (s, None),
    };
    let (user, name) = match host.split_once('@') {
        Some((u, n)) => (Some(u), n),
        None => (None, host),
    };
    let word = |w: &str| {
        !w.is_empty()
            && w.len() <= 253
            && w.as_bytes()[0].is_ascii_alphanumeric()
            && w.bytes()
                .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
    };
    if !word(name) || user.is_some_and(|u| !word(u)) {
        return Err("ssh_address_invalid".into());
    }
    Ok(Target {
        host: host.to_string(),
        port,
        key: None,
        bin: None,
        password: None,
    })
}

/// The connect form, like ssh itself: host (`host`, `user@host` or a ~/.ssh/config alias, may
/// carry `:port`), and optional user, port, identity file and arkism path. Blank fields leave the
/// choice to ssh (~/.ssh/config, ssh-agent, the current user, port 22).
pub struct Form {
    pub host: String,
    pub user: Option<String>,
    pub port: Option<u16>,
    pub key: Option<String>,
    pub bin: Option<String>,
    /// "password" logs in with `password`; anything else uses keys (the default).
    pub auth: Option<String>,
    pub password: Option<String>,
}

fn home() -> Option<std::path::PathBuf> {
    std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" })
        .map(std::path::PathBuf::from)
}

pub fn target_from_form(form: &Form) -> Result<Target, String> {
    target_with(form, saved_password)
}

/// `saved` looks up the stored password when the password field is left empty.
fn target_with(form: &Form, saved: impl Fn(&Target) -> Option<String>) -> Result<Target, String> {
    let blank = |v: &Option<String>| {
        v.as_deref()
            .map(str::trim)
            .filter(|v| !v.is_empty())
            .map(str::to_string)
    };
    let host = form.host.trim();
    let mut t = match blank(&form.user) {
        Some(user) if !host.contains('@') => parse_target(&format!("{user}@{host}"))?,
        _ => parse_target(host)?,
    };
    if let Some(p) = form.port {
        if p == 0 {
            return Err("ssh_port_invalid".into());
        }
        t.port = Some(p);
    }
    if form.auth.as_deref() == Some("password") {
        // Taken as typed: spaces can be part of a password.
        let pw = form
            .password
            .clone()
            .filter(|p| !p.is_empty())
            .or_else(|| saved(&t))
            .ok_or("ssh_password_missing")?;
        t.password = Some(Password(pw));
    } else if let Some(k) = blank(&form.key) {
        let path = match k.strip_prefix("~/").or_else(|| k.strip_prefix("~\\")) {
            Some(rest) => home().ok_or("ssh_key_missing")?.join(rest),
            None => std::path::PathBuf::from(&k),
        };
        if !path.is_absolute() || !path.is_file() {
            return Err("ssh_key_missing".into());
        }
        t.key = Some(path);
    }
    if let Some(b) = blank(&form.bin) {
        let ok = b.starts_with('/')
            && b.len() <= 512
            && b.bytes()
                .all(|c| c.is_ascii_alphanumeric() || b"/._-".contains(&c));
        if !ok {
            return Err("arkism_path_invalid".into());
        }
        t.bin = Some(b);
    }
    Ok(t)
}

/// Private keys in ~/.ssh, for the key-file picker: files with a matching `.pub` or named id_*.
pub fn list_keys() -> Vec<String> {
    let Some(home) = home() else {
        return Vec::new();
    };
    let dir = home.join(".ssh");
    let Ok(entries) = std::fs::read_dir(&dir) else {
        return Vec::new();
    };
    let mut keys: Vec<String> = entries
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.is_file() && p.extension().is_none_or(|x| x != "pub"))
        .filter(|p| {
            let name = p
                .file_name()
                .and_then(std::ffi::OsStr::to_str)
                .unwrap_or("");
            !matches!(
                name,
                "config" | "known_hosts" | "known_hosts.old" | "authorized_keys"
            ) && (name.starts_with("id_")
                || p.with_extension("pub").is_file()
                || std::path::PathBuf::from(format!("{}.pub", p.display())).is_file())
        })
        .map(|p| p.display().to_string())
        .collect();
    keys.sort();
    keys
}

// The credential store entry: one per login (`user@host:port`).
const KEYRING_SERVICE: &str = "Arkism SSH";

fn account(t: &Target) -> String {
    format!("{}:{}", t.host, t.port.unwrap_or(22))
}

fn keyring_entry(t: &Target) -> Option<keyring::Entry> {
    keyring::Entry::new(KEYRING_SERVICE, &account(t)).ok()
}

fn saved_password(t: &Target) -> Option<String> {
    keyring_entry(t)?.get_password().ok().filter(|p| !p.is_empty())
}

/// After a login works: keep the password for the next launch, or drop an old one once the
/// same login uses a key. Best effort; without a credential store the app just asks again.
fn remember(t: &Target) {
    let Some(entry) = keyring_entry(t) else {
        return;
    };
    let _ = match &t.password {
        Some(Password(pw)) => entry.set_password(pw),
        None => entry.delete_credential(),
    };
}

/// 연결 끊기 forgets the open login's saved password.
pub fn forget_current() {
    let target = TUNNEL
        .lock()
        .ok()
        .and_then(|g| g.as_ref().map(|t| t.target.clone()));
    if let Some(entry) = target.as_ref().and_then(keyring_entry) {
        let _ = entry.delete_credential();
    }
}

/// Host aliases from ~/.ssh/config, for the host picker. Patterns (`*`, `?`, `!`) are skipped.
pub fn list_hosts() -> Vec<String> {
    let Some(text) = home().and_then(|h| std::fs::read_to_string(h.join(".ssh").join("config")).ok()) else { return Vec::new() };
    config_hosts(&text)
}

fn config_hosts(text: &str) -> Vec<String> {
    let mut hosts: Vec<String> = text
        .lines()
        .filter_map(|l| {
            let l = l.trim();
            let (key, rest) = l.split_once(|c: char| c.is_whitespace() || c == '=')?;
            key.eq_ignore_ascii_case("host").then_some(rest)
        })
        .flat_map(|rest| rest.split(|c: char| c.is_whitespace() || c == '=').filter(|w| !w.is_empty()).map(str::to_string).collect::<Vec<_>>())
        .filter(|w| !w.contains(['*', '?', '!']))
        .collect();
    hosts.sort();
    hosts.dedup();
    hosts
}

/// The ssh program: Git for Windows' OpenSSH when present (Windows' own can hang on pipes),
/// otherwise `ssh` on PATH.
pub fn ssh_program() -> std::ffi::OsString {
    if cfg!(windows) {
        let git = std::path::PathBuf::from(
            std::env::var_os("ProgramFiles").unwrap_or_else(|| "C:/Program Files".into()),
        )
        .join("Git/usr/bin/ssh.exe");
        if git.is_file() {
            return git.into_os_string();
        }
    }
    "ssh".into()
}

#[derive(serde::Deserialize)]
struct Info {
    api: String,
    token: String,
    #[serde(default)]
    workspace_id: String,
    #[serde(default)]
    project_id: String,
}

#[derive(serde::Serialize)]
pub struct Connected {
    pub server: String,
    pub token: String,
    pub workspace_id: String,
    pub project_id: String,
    pub ssh_host: String,
    pub ssh_port: Option<u16>,
    pub ssh_key: Option<String>,
    #[serde(skip_serializing)]
    pub pin: Pin,
}

/// Turns ssh's exit and message into one code the UI explains.
fn ssh_error(target: &Target, code: Option<i32>, stderr: &str) -> String {
    let s = stderr.to_ascii_lowercase();
    if s.contains("permission denied") || s.contains("too many authentication failures") {
        if target.password.is_some() {
            "ssh_password_rejected"
        } else {
            "ssh_auth_failed"
        }
        .into()
    } else if s.contains("host key verification failed")
        || s.contains("remote host identification has changed")
    {
        "ssh_host_key_changed".into()
    } else if code == Some(255)
        || s.contains("could not resolve")
        || s.contains("timed out")
        || s.contains("connection refused")
    {
        "ssh_unreachable".into()
    } else if code == Some(127) {
        "arkism_not_found".into()
    } else if s.contains("usage: arkism command") {
        "arkism_too_old".into()
    } else {
        format!(
            "arkism_connect_failed: {}",
            stderr.trim().lines().last().unwrap_or("")
        )
    }
}

/// `ssh HOST arkism connect-info`, trying the server install's wrapper first.
pub fn connect_info_for(target: &Target, store: &Store) -> Result<(String, String), String> {
    let pin = store.required(target)?;
    let info = connect_info(target, store, &pin)?;
    Ok((info.api, info.workspace_id))
}

fn connect_info(target: &Target, store: &Store, pin: &Pin) -> Result<Info, String> {
    let first = target
        .bin
        .as_deref()
        .map(|b| format!("{b} "))
        .unwrap_or_default();
    let remote = format!(
        "sh -c 'for b in {first}/srv/arkism/bin/ark {SERVER_BIN} arkism; do if command -v \"$b\" >/dev/null 2>&1; then exec \"$b\" connect-info; fi; done; exit 127'"
    );
    let mut command = store.command(target, pin)?;
    command
        .args(["-T", "-o", "ClearAllForwardings=yes"])
        .arg(&target.host)
        .arg(remote);
    let out = ssh_exec::run(command, Vec::new(), 65536, Duration::from_secs(20))?;
    if out.code != 0 {
        return Err(ssh_error(
            target,
            Some(out.code),
            &String::from_utf8_lossy(&out.err),
        ));
    }
    let info: Info = serde_json::from_slice(&out.out)
        .map_err(|_| "arkism_connect_failed: bad reply".to_string())?;
    if info.token.is_empty() || info.token.chars().any(char::is_whitespace) {
        return Err("arkism_connect_failed: bad reply".into());
    }
    Ok(info)
}

/// The server-side `host:port` the API listens on, from connect-info's `http://host:port`.
fn remote_endpoint(api: &str) -> Result<(String, u16), String> {
    let rest = api
        .strip_prefix("http://")
        .ok_or("arkism_connect_failed: bad reply")?;
    let hostport = rest.split('/').next().unwrap_or("");
    let (h, p) = hostport
        .rsplit_once(':')
        .ok_or("arkism_connect_failed: bad reply")?;
    let port = p
        .parse::<u16>()
        .map_err(|_| "arkism_connect_failed: bad reply")?;
    let h = h.trim_start_matches('[').trim_end_matches(']');
    let ok = !h.is_empty()
        && h.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b".-:".contains(&b));
    if !ok {
        return Err("arkism_connect_failed: bad reply".into());
    }
    Ok((h.to_string(), port))
}

struct Tunnel {
    child: Process,
    errors: Receiver<Result<Vec<u8>, String>>,
    target: Target,
    store: Store,
    pin: Pin,
    forward: String,
}

static TUNNEL: Mutex<Option<Tunnel>> = Mutex::new(None);
static GENERATION: AtomicU64 = AtomicU64::new(0);

/// Side operations use the same explicit port and identity as the active tunnel.
/// A different requested host still requires its own approved record.
pub fn target_for_host(host: &str) -> Result<Target, String> {
    if let Ok(guard) = TUNNEL.lock() {
        if let Some(tunnel) = &*guard {
            if tunnel.target.host == host {
                return Ok(tunnel.target.clone());
            }
        }
    }
    parse_target(host)
}

fn spawn_forward(
    target: &Target,
    forward: &str,
    store: &Store,
    pin: &Pin,
) -> Result<(Process, Receiver<Result<Vec<u8>, String>>), String> {
    let mut command = store.command(target, pin)?;
    command
        .args(["-N", "-o", "ExitOnForwardFailure=yes", "-L", forward])
        .arg(&target.host);
    let mut child = Process::spawn(command)?;
    drop(child.input());
    drop(child.output());
    let errors = ssh_exec::read_pipe(child.errors().ok_or("ssh_process_failed")?, 16384);
    Ok((child, errors))
}

fn wait_ready(
    target: &Target,
    child: &mut Process,
    errors: &Receiver<Result<Vec<u8>, String>>,
    local: u16,
) -> Result<(), String> {
    let deadline = Instant::now() + Duration::from_secs(20);
    while Instant::now() < deadline {
        if let Some(code) = child.status()? {
            child.stop()?;
            let bytes = errors
                .recv_timeout(Duration::from_secs(1))
                .map_err(|_| "ssh_process_failed")??;
            return Err(ssh_error(
                target,
                Some(code),
                &String::from_utf8_lossy(&bytes),
            ));
        }
        if let Ok(result) = errors.try_recv() {
            result?;
        }
        if TcpStream::connect_timeout(&([127, 0, 0, 1], local).into(), Duration::from_millis(300))
            .is_ok()
        {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(150));
    }
    child.stop()?;
    Err("ssh_unreachable".into())
}

fn free_port() -> Result<u16, String> {
    let l = TcpListener::bind("127.0.0.1:0").map_err(|e| e.to_string())?;
    Ok(l.local_addr().map_err(|e| e.to_string())?.port())
}

/// Restarts the forward on the same local port whenever ssh exits (sleep, network change),
/// so the address the UI holds keeps working.
fn watch(generation: u64, gate: Arc<Gate>) {
    std::thread::spawn(move || loop {
        std::thread::sleep(Duration::from_secs(3));
        if GENERATION.load(Ordering::SeqCst) != generation {
            return;
        }
        // Count only a probe/reconnect operation, not an idle tunnel's lifetime. An update
        // lease cannot pass a reconnect already in flight, and does not change its endpoint.
        let Ok(_activity) = gate.activity() else {
            continue;
        };
        let Ok(mut guard) = TUNNEL.lock() else { return };
        if GENERATION.load(Ordering::SeqCst) != generation {
            return;
        }
        let Some(t) = guard.as_mut() else { return };
        if matches!(t.child.status(), Ok(None)) {
            continue;
        }
        if t.child.stop().is_err() {
            continue;
        }
        if let Ok((child, errors)) = spawn_forward(&t.target, &t.forward, &t.store, &t.pin) {
            t.child = child;
            t.errors = errors;
        }
    });
}

pub fn stop(gate: &Arc<Gate>) -> Result<(), String> {
    let _activity = gate.activity()?;
    stop_inner()
}

/// Terminal app-exit teardown; normal disconnects must enter the lifecycle gate above.
pub(crate) fn shutdown() {
    let _ = stop_inner();
}

fn stop_inner() -> Result<(), String> {
    GENERATION.fetch_add(1, Ordering::SeqCst);
    let mut guard = TUNNEL.lock().map_err(|_| "ssh_process_failed")?;
    if let Some(tunnel) = guard.as_mut() {
        tunnel.child.stop()?;
    }
    *guard = None;
    Ok(())
}

pub fn connect(target: Target, gate: Arc<Gate>, store: Store) -> Result<Connected, String> {
    let _activity = gate.activity()?;
    let pin = store.required(&target)?;
    let info = connect_info(&target, &store, &pin)?;
    let (rhost, rport) = remote_endpoint(&info.api)?;
    let local = free_port()?;
    let forward = format!("127.0.0.1:{local}:{rhost}:{rport}");
    let (mut child, errors) = spawn_forward(&target, &forward, &store, &pin)?;
    wait_ready(&target, &mut child, &errors, local)?;
    let mut guard = TUNNEL.lock().map_err(|_| "ssh_process_failed")?;
    if let Some(previous) = guard.as_mut() {
        previous.child.stop()?;
    }
    let generation = GENERATION.fetch_add(1, Ordering::SeqCst) + 1;
    *guard = Some(Tunnel {
        child,
        errors,
        target: target.clone(),
        forward,
        store,
        pin: pin.clone(),
    });
    drop(guard);
    remember(&target);
    watch(generation, Arc::clone(&gate));
    Ok(Connected {
        server: format!("http://127.0.0.1:{local}"),
        token: info.token,
        workspace_id: if info.workspace_id.is_empty() {
            "default".into()
        } else {
            info.workspace_id
        },
        project_id: if info.project_id.is_empty() {
            "default".into()
        } else {
            info.project_id
        },
        ssh_key: target.key.map(|k| k.display().to_string()),
        ssh_host: target.host,
        ssh_port: target.port,
        pin,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn update_lifecycle_disconnect_does_not_mutate_tunnel_during_handoff() {
        let gate = Arc::new(Gate::default());
        let _lease = gate.lease().unwrap();
        let generation = GENERATION.load(Ordering::SeqCst);
        assert_eq!(stop(&gate), Err("update_runtime_busy".into()));
        assert_eq!(GENERATION.load(Ordering::SeqCst), generation);
    }

    #[test]
    fn addresses_parse_like_ssh() {
        assert_eq!(
            parse_target("alex@example.com").unwrap(),
            Target {
                host: "alex@example.com".into(),
                port: None,
                key: None,
                bin: None,
                password: None
            }
        );
        assert_eq!(
            parse_target(" ssh myserver ").unwrap(),
            Target {
                host: "myserver".into(),
                port: None,
                key: None,
                bin: None,
                password: None
            }
        );
        assert_eq!(
            parse_target("root@10.0.0.5:2222").unwrap(),
            Target {
                host: "root@10.0.0.5".into(),
                port: Some(2222),
                key: None,
                bin: None,
                password: None
            }
        );
        for bad in [
            "",
            "-oProxyCommand=x",
            "a b",
            "host:",
            "host:0",
            "host:99999",
            "@host",
            "user@",
            "x;rm",
        ] {
            assert!(parse_target(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn form_fields_work_like_ssh() {
        let f = |host: &str,
                 user: Option<&str>,
                 port: Option<u16>,
                 key: Option<&str>,
                 bin: Option<&str>| {
            target_with(
                &Form {
                    host: host.into(),
                    user: user.map(Into::into),
                    port,
                    key: key.map(Into::into),
                    bin: bin.map(Into::into),
                    auth: None,
                    password: None,
                },
                |_| None,
            )
        };
        assert_eq!(
            f("h", Some("root"), None, None, None).unwrap().host,
            "root@h"
        );
        assert_eq!(
            f("alex@h", Some("root"), None, None, None).unwrap().host,
            "alex@h"
        );
        assert_eq!(
            f("h:22", None, Some(2200), None, None).unwrap().port,
            Some(2200)
        );
        assert_eq!(f("h", Some(" "), None, Some("  "), None).unwrap().key, None);
        assert_eq!(
            f("h", None, None, Some("/no/such/key"), None).unwrap_err(),
            "ssh_key_missing"
        );
        assert_eq!(
            f("h", None, Some(0), None, None).unwrap_err(),
            "ssh_port_invalid"
        );
        assert_eq!(
            f("h", None, None, None, Some("/opt/arkism/bin/arkism"))
                .unwrap()
                .bin
                .as_deref(),
            Some("/opt/arkism/bin/arkism")
        );
        assert_eq!(
            f("h", None, None, None, Some("x;rm")).unwrap_err(),
            "arkism_path_invalid"
        );
        assert!(f("h", Some("-oX"), None, None, None).is_err());
    }

    #[test]
    fn api_address_gives_the_remote_end_of_the_forward() {
        assert_eq!(
            remote_endpoint("http://127.0.0.1:8091").unwrap(),
            ("127.0.0.1".into(), 8091)
        );
        assert_eq!(
            remote_endpoint("http://localhost:9000/").unwrap(),
            ("localhost".into(), 9000)
        );
        assert!(remote_endpoint("https://x:1").is_err());
        assert!(remote_endpoint("http://x").is_err());
        assert!(remote_endpoint("http://a b:1").is_err());
    }

    fn args(cmd: &std::process::Command) -> Vec<String> {
        cmd.get_args()
            .map(|a| a.to_string_lossy().into_owned())
            .collect()
    }

    fn env(cmd: &std::process::Command, name: &str) -> Option<String> {
        cmd.get_envs()
            .find(|(k, _)| *k == name)
            .and_then(|(_, v)| v.map(|v| v.to_string_lossy().into_owned()))
    }

    #[test]
    fn password_login_uses_askpass_not_batch_mode() {
        let form = |auth: Option<&str>, pw: Option<&str>| Form {
            host: "h".into(),
            user: None,
            port: None,
            key: Some("/no/such/key".into()),
            bin: None,
            auth: auth.map(Into::into),
            password: pw.map(Into::into),
        };
        let none = |_: &Target| None;
        assert_eq!(
            target_with(&form(Some("password"), None), none).unwrap_err(),
            "ssh_password_missing"
        );
        assert_eq!(
            target_with(&form(Some("password"), Some("")), none).unwrap_err(),
            "ssh_password_missing"
        );
        // An empty field takes the saved password for that login; a typed one wins over it.
        let saved = |t: &Target| (account(t) == "h:22").then(|| "kept".to_string());
        assert_eq!(
            target_with(&form(Some("password"), None), saved)
                .unwrap()
                .password,
            Some(Password("kept".into()))
        );
        assert_eq!(
            target_with(&form(Some("password"), Some("new")), saved)
                .unwrap()
                .password,
            Some(Password("new".into()))
        );
        // The key field is ignored in password mode, so a stale path does not block it.
        let t = target_with(&form(Some("password"), Some(" s3cret ")), none).unwrap();
        assert_eq!(
            (t.key.as_ref(), t.password.as_ref()),
            (None, Some(&Password(" s3cret ".into())))
        );
        assert!(!format!("{t:?}").contains("s3cret"));

        let mut cmd = crate::ssh_identity::Client::system().command(&t);
        password_login(&mut cmd, t.password.as_ref().unwrap());
        let a = args(&cmd).join(" ");
        assert!(!a.contains("BatchMode") && !a.contains("s3cret"), "{a}");
        assert!(
            a.contains("PreferredAuthentications=password,keyboard-interactive")
                && a.contains("NumberOfPasswordPrompts=1"),
            "{a}"
        );
        assert_eq!(env(&cmd, "SSH_ASKPASS_REQUIRE").as_deref(), Some("force"));
        assert_eq!(env(&cmd, ASKPASS_ENV).as_deref(), Some(" s3cret "));
        assert!(env(&cmd, "SSH_ASKPASS").is_some());

        let key = crate::ssh_identity::Client::system().command(&parse_target("h").unwrap());
        assert!(args(&key).join(" ").contains("BatchMode=yes"));
        assert_eq!(env(&key, ASKPASS_ENV), None);
    }

    #[test]
    fn config_hosts_lists_plain_aliases() {
        let cfg = "Host *\n  User x\nhost web db\n  HostName 10.0.0.1\nHost=gpu !bad dev-*\nHostName nope\n# Host commented\nHost web\n";
        assert_eq!(config_hosts(cfg), ["db", "gpu", "web"]);
    }

    // Touches the real credential store, so run by hand: cargo test -- --ignored keyring
    #[test]
    #[ignore]
    fn keyring_roundtrip_on_this_machine() {
        let t = Target {
            password: Some(Password("pw-test".into())),
            ..parse_target("arkism-test@example.invalid:2299").unwrap()
        };
        remember(&t);
        assert_eq!(saved_password(&t).as_deref(), Some("pw-test"));
        remember(&Target {
            password: None,
            ..t.clone()
        });
        assert_eq!(saved_password(&t), None);
    }

    #[test]
    fn askpass_answers_only_password_prompts() {
        assert_eq!(askpass_answer("alex@h's password: ", "pw"), Some("pw"));
        assert_eq!(askpass_answer("(alex@h) Password: ", "pw"), Some("pw"));
        assert_eq!(askpass_answer("암호:", "pw"), Some("pw"));
        assert_eq!(
            askpass_answer(
                "Are you sure you want to continue connecting (yes/no/[fingerprint])? ",
                "pw"
            ),
            None
        );
        assert_eq!(askpass_answer("Enter passphrase for key '/k': ", "pw"), None);
    }

    #[test]
    fn ssh_failures_get_one_code() {
        let t = parse_target("h").unwrap();
        assert_eq!(
            ssh_error(&t, Some(255), "alex@h: Permission denied (publickey)."),
            "ssh_auth_failed"
        );
        assert_eq!(
            ssh_error(&t, Some(255), "ssh: Could not resolve hostname x"),
            "ssh_unreachable"
        );
        assert_eq!(ssh_error(&t, Some(127), ""), "arkism_not_found");
        assert_eq!(
            ssh_error(&t, Some(2), "usage: arkism COMMAND [ARGS]\ncommands:"),
            "arkism_too_old"
        );
        let pw = Target {
            password: Some(Password("x".into())),
            ..t
        };
        assert_eq!(
            ssh_error(&pw, Some(255), "alex@h: Permission denied (password)."),
            "ssh_password_rejected"
        );
    }
}
