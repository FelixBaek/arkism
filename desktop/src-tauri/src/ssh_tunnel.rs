//! Connecting to an Arkism server the way one logs in with ssh: the user types the SSH address
//! (`user@host`, optionally `:port`) and nothing else. Over that login the app asks the server
//! for its connection details (`arkism connect-info`: API address, owner token, scope) and
//! forwards the API port to this PC (`ssh -N -L`). The UI then talks to `http://127.0.0.1:PORT`
//! as it would to the built-in Arkism. The forward is restarted on the same port if it drops.
//!
//! Authentication is whatever the user's ssh already does (keys, agent, ~/.ssh/config); the app
//! never sees a password. New host keys are accepted on first use, like answering "yes" once.

use std::io::Read;
use std::net::{TcpListener, TcpStream};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Mutex;
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
}

/// Parses what the user typed. Anything ssh could read as an option or a second word is refused.
pub fn parse_target(input: &str) -> Result<Target, String> {
    let s = input.trim().strip_prefix("ssh ").map(str::trim).unwrap_or(input.trim());
    let (host, port) = match s.rsplit_once(':') {
        Some((h, p)) if !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()) => {
            let port = p.parse::<u16>().ok().filter(|p| *p != 0).ok_or("ssh_address_invalid")?;
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
        !w.is_empty() && w.len() <= 253 && w.as_bytes()[0].is_ascii_alphanumeric() && w.bytes().all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
    };
    if !word(name) || user.is_some_and(|u| !word(u)) {
        return Err("ssh_address_invalid".into());
    }
    Ok(Target { host: host.to_string(), port, key: None, bin: None })
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
}

fn home() -> Option<std::path::PathBuf> {
    std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" }).map(std::path::PathBuf::from)
}

pub fn target_from_form(form: &Form) -> Result<Target, String> {
    let blank = |v: &Option<String>| v.as_deref().map(str::trim).filter(|v| !v.is_empty()).map(str::to_string);
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
    if let Some(k) = blank(&form.key) {
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
        let ok = b.starts_with('/') && b.len() <= 512 && b.bytes().all(|c| c.is_ascii_alphanumeric() || b"/._-".contains(&c));
        if !ok {
            return Err("arkism_path_invalid".into());
        }
        t.bin = Some(b);
    }
    Ok(t)
}

/// Private keys in ~/.ssh, for the key-file picker: files with a matching `.pub` or named id_*.
pub fn list_keys() -> Vec<String> {
    let Some(home) = home() else { return Vec::new() };
    let dir = home.join(".ssh");
    let Ok(entries) = std::fs::read_dir(&dir) else { return Vec::new() };
    let mut keys: Vec<String> = entries
        .flatten()
        .map(|e| e.path())
        .filter(|p| p.is_file() && p.extension().is_none_or(|x| x != "pub"))
        .filter(|p| {
            let name = p.file_name().and_then(std::ffi::OsStr::to_str).unwrap_or("");
            !matches!(name, "config" | "known_hosts" | "known_hosts.old" | "authorized_keys") && (name.starts_with("id_") || p.with_extension("pub").is_file() || std::path::PathBuf::from(format!("{}.pub", p.display())).is_file())
        })
        .map(|p| p.display().to_string())
        .collect();
    keys.sort();
    keys
}

/// The ssh program: Git for Windows' OpenSSH when present (Windows' own can hang on pipes),
/// otherwise `ssh` on PATH.
pub fn ssh_program() -> std::ffi::OsString {
    if cfg!(windows) {
        let git = std::path::PathBuf::from(std::env::var_os("ProgramFiles").unwrap_or_else(|| "C:/Program Files".into())).join("Git/usr/bin/ssh.exe");
        if git.is_file() {
            return git.into_os_string();
        }
    }
    "ssh".into()
}

fn ssh(target: &Target) -> Command {
    let mut cmd = Command::new(ssh_program());
    cmd.args(["-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"]);
    if let Some(port) = target.port {
        cmd.arg("-p").arg(port.to_string());
    }
    if let Some(key) = &target.key {
        cmd.arg("-i").arg(key).args(["-o", "IdentitiesOnly=yes"]);
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        cmd.creation_flags(0x0800_0000);
    }
    cmd
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
}

/// Turns ssh's exit and message into one code the UI explains.
fn ssh_error(code: Option<i32>, stderr: &str) -> String {
    let s = stderr.to_ascii_lowercase();
    if s.contains("permission denied") || s.contains("too many authentication failures") {
        "ssh_auth_failed".into()
    } else if s.contains("host key verification failed") || s.contains("remote host identification has changed") {
        "ssh_host_key_changed".into()
    } else if code == Some(255) || s.contains("could not resolve") || s.contains("timed out") || s.contains("connection refused") {
        "ssh_unreachable".into()
    } else if code == Some(127) {
        "arkism_not_found".into()
    } else if s.contains("usage: arkism command") {
        "arkism_too_old".into()
    } else {
        format!("arkism_connect_failed: {}", stderr.trim().lines().last().unwrap_or(""))
    }
}

/// `ssh HOST arkism connect-info`, trying the server install's wrapper first.
pub fn connect_info_for(target: &Target) -> Result<(String, String), String> {
    let info = connect_info(target)?;
    Ok((info.api, info.workspace_id))
}

fn connect_info(target: &Target) -> Result<Info, String> {
    let first = target.bin.as_deref().map(|b| format!("{b} ")).unwrap_or_default();
    let remote = format!(
        "sh -c 'for b in {first}/srv/arkism/bin/ark {SERVER_BIN} arkism; do if command -v \"$b\" >/dev/null 2>&1; then exec \"$b\" connect-info; fi; done; exit 127'"
    );
    let out = ssh(target)
        .arg("-T")
        .arg(&target.host)
        .arg(remote)
        .stdin(Stdio::null())
        .output()
        .map_err(|_| "ssh_missing".to_string())?;
    if !out.status.success() {
        return Err(ssh_error(out.status.code(), &String::from_utf8_lossy(&out.stderr)));
    }
    let info: Info = serde_json::from_slice(&out.stdout).map_err(|_| "arkism_connect_failed: bad reply".to_string())?;
    if info.token.is_empty() || info.token.chars().any(char::is_whitespace) {
        return Err("arkism_connect_failed: bad reply".into());
    }
    Ok(info)
}

/// The server-side `host:port` the API listens on, from connect-info's `http://host:port`.
fn remote_endpoint(api: &str) -> Result<(String, u16), String> {
    let rest = api.strip_prefix("http://").ok_or("arkism_connect_failed: bad reply")?;
    let hostport = rest.split('/').next().unwrap_or("");
    let (h, p) = hostport.rsplit_once(':').ok_or("arkism_connect_failed: bad reply")?;
    let port = p.parse::<u16>().map_err(|_| "arkism_connect_failed: bad reply")?;
    let h = h.trim_start_matches('[').trim_end_matches(']');
    let ok = !h.is_empty() && h.bytes().all(|b| b.is_ascii_alphanumeric() || b".-:".contains(&b));
    if !ok {
        return Err("arkism_connect_failed: bad reply".into());
    }
    Ok((h.to_string(), port))
}

struct Tunnel {
    child: Child,
    target: Target,
    forward: String,
}

static TUNNEL: Mutex<Option<Tunnel>> = Mutex::new(None);
// Bumped by every connect and by stop(); a watcher whose number is old quits.
static GENERATION: AtomicU64 = AtomicU64::new(0);

fn spawn_forward(target: &Target, forward: &str) -> Result<Child, String> {
    ssh(target)
        .args(["-N", "-o", "ExitOnForwardFailure=yes", "-L", forward])
        .arg(&target.host)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|_| "ssh_missing".to_string())
}

/// Waits until ssh listens on the local port (it binds only after logging in), or fails with why it quit.
fn wait_ready(child: &mut Child, local: u16) -> Result<(), String> {
    let deadline = Instant::now() + Duration::from_secs(20);
    while Instant::now() < deadline {
        if let Ok(Some(status)) = child.try_wait() {
            let mut err = String::new();
            if let Some(mut e) = child.stderr.take() {
                let _ = e.read_to_string(&mut err);
            }
            return Err(ssh_error(status.code(), &err));
        }
        if TcpStream::connect_timeout(&([127, 0, 0, 1], local).into(), Duration::from_millis(300)).is_ok() {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(150));
    }
    let _ = child.kill();
    let _ = child.wait();
    Err("ssh_unreachable".into())
}

fn free_port() -> Result<u16, String> {
    let l = TcpListener::bind("127.0.0.1:0").map_err(|e| e.to_string())?;
    Ok(l.local_addr().map_err(|e| e.to_string())?.port())
}

/// Restarts the forward on the same local port whenever ssh exits (sleep, network change),
/// so the address the UI holds keeps working.
fn watch(generation: u64) {
    std::thread::spawn(move || loop {
        std::thread::sleep(Duration::from_secs(3));
        if GENERATION.load(Ordering::SeqCst) != generation {
            return;
        }
        let Ok(mut guard) = TUNNEL.lock() else { return };
        let Some(t) = guard.as_mut() else { return };
        if matches!(t.child.try_wait(), Ok(None)) {
            continue;
        }
        if let Ok(child) = spawn_forward(&t.target, &t.forward) {
            t.child = child;
        }
    });
}

pub fn stop() {
    GENERATION.fetch_add(1, Ordering::SeqCst);
    if let Ok(mut guard) = TUNNEL.lock() {
        if let Some(mut t) = guard.take() {
            let _ = t.child.kill();
            let _ = t.child.wait();
        }
    }
}

pub fn connect(target: Target) -> Result<Connected, String> {
    let info = connect_info(&target)?;
    let (rhost, rport) = remote_endpoint(&info.api)?;
    stop();
    let local = free_port()?;
    let forward = format!("127.0.0.1:{local}:{rhost}:{rport}");
    let mut child = spawn_forward(&target, &forward)?;
    wait_ready(&mut child, local)?;
    let generation = GENERATION.fetch_add(1, Ordering::SeqCst) + 1;
    *TUNNEL.lock().map_err(|e| e.to_string())? = Some(Tunnel { child, target: target.clone(), forward });
    watch(generation);
    Ok(Connected {
        server: format!("http://127.0.0.1:{local}"),
        token: info.token,
        workspace_id: if info.workspace_id.is_empty() { "default".into() } else { info.workspace_id },
        project_id: if info.project_id.is_empty() { "default".into() } else { info.project_id },
        ssh_key: target.key.map(|k| k.display().to_string()),
        ssh_host: target.host,
        ssh_port: target.port,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn addresses_parse_like_ssh() {
        assert_eq!(parse_target("alex@example.com").unwrap(), Target { host: "alex@example.com".into(), port: None, key: None, bin: None });
        assert_eq!(parse_target(" ssh myserver ").unwrap(), Target { host: "myserver".into(), port: None, key: None, bin: None });
        assert_eq!(parse_target("root@10.0.0.5:2222").unwrap(), Target { host: "root@10.0.0.5".into(), port: Some(2222), key: None, bin: None });
        for bad in ["", "-oProxyCommand=x", "a b", "host:", "host:0", "host:99999", "@host", "user@", "x;rm"] {
            assert!(parse_target(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn form_fields_work_like_ssh() {
        let f = |host: &str, user: Option<&str>, port: Option<u16>, key: Option<&str>, bin: Option<&str>| {
            target_from_form(&Form { host: host.into(), user: user.map(Into::into), port, key: key.map(Into::into), bin: bin.map(Into::into) })
        };
        assert_eq!(f("h", Some("root"), None, None, None).unwrap().host, "root@h");
        assert_eq!(f("alex@h", Some("root"), None, None, None).unwrap().host, "alex@h");
        assert_eq!(f("h:22", None, Some(2200), None, None).unwrap().port, Some(2200));
        assert_eq!(f("h", Some(" "), None, Some("  "), None).unwrap().key, None);
        assert_eq!(f("h", None, None, Some("/no/such/key"), None).unwrap_err(), "ssh_key_missing");
        assert_eq!(f("h", None, Some(0), None, None).unwrap_err(), "ssh_port_invalid");
        assert_eq!(f("h", None, None, None, Some("/opt/arkism/bin/arkism")).unwrap().bin.as_deref(), Some("/opt/arkism/bin/arkism"));
        assert_eq!(f("h", None, None, None, Some("x;rm")).unwrap_err(), "arkism_path_invalid");
        assert!(f("h", Some("-oX"), None, None, None).is_err());
    }

    #[test]
    fn api_address_gives_the_remote_end_of_the_forward() {
        assert_eq!(remote_endpoint("http://127.0.0.1:8091").unwrap(), ("127.0.0.1".into(), 8091));
        assert_eq!(remote_endpoint("http://localhost:9000/").unwrap(), ("localhost".into(), 9000));
        assert!(remote_endpoint("https://x:1").is_err());
        assert!(remote_endpoint("http://x").is_err());
        assert!(remote_endpoint("http://a b:1").is_err());
    }

    #[test]
    fn ssh_failures_get_one_code() {
        assert_eq!(ssh_error(Some(255), "alex@h: Permission denied (publickey)."), "ssh_auth_failed");
        assert_eq!(ssh_error(Some(255), "ssh: Could not resolve hostname x"), "ssh_unreachable");
        assert_eq!(ssh_error(Some(127), ""), "arkism_not_found");
        assert_eq!(ssh_error(Some(2), "usage: arkism COMMAND [ARGS]\ncommands:"), "arkism_too_old");
    }
}
