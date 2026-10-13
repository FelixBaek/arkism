use std::io::{Read, Write};
use std::process::{Command, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

const LIMIT: usize = 256 * 1024;
// The arkism binary on the server (build-time ARKISM_SERVER_BIN).
const SERVER_BIN: &str = match option_env!("ARKISM_SERVER_BIN") {
    Some(value) => value,
    None => "/srv/arkism/bin/arkism",
};
fn valid(host: &str, origin: &str, client: &str) -> bool {
    !host.is_empty()
        && host.len() <= 253
        && host.as_bytes()[0].is_ascii_alphanumeric()
        && host
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-@".contains(&b))
        && matches!(origin, "claude_code" | "codex" | "zcode")
        && matches!(client, "server" | "pc")
}

// Same bounded owner-SSH transport; the server accepts only registered profiles.
pub fn skill_execution(
    host: &str,
    request: serde_json::Value,
) -> Result<serde_json::Value, String> {
    if !valid(host, "codex", "server") {
        return Err("skill_execution_invalid".into());
    }
    let raw = serde_json::to_vec(&request).map_err(|_| "skill_execution_invalid")?;
    if raw.len() > 8192 {
        return Err("skill_execution_invalid".into());
    }
    execute(
        ssh_command(
            host,
            &format!("{SERVER_BIN} skill-execution"),
        ),
        raw,
        Duration::from_secs(25),
    )
    .map_err(|_| "skill_execution_unavailable".to_string())
}

// Git screen: the server reads the ARKISM_GIT_ORG repositories and makes bounded GitHub changes
// (arkism git). A first clone of a large repository can take about a minute.
pub fn git(host: &str, request: serde_json::Value) -> Result<serde_json::Value, String> {
    if !valid(host, "codex", "server") {
        return Err("git_invalid_request".into());
    }
    let raw = serde_json::to_vec(&request).map_err(|_| "git_invalid_request")?;
    if raw.len() > 65536 {
        return Err("git_invalid_request".into());
    }
    execute(
        ssh_command(host, &format!("{SERVER_BIN} git")),
        raw,
        Duration::from_secs(180),
    )
}

fn ssh_command(host: &str, remote: &str) -> Command {
    let git_ssh = std::path::PathBuf::from(
        std::env::var_os("ProgramFiles").unwrap_or_else(|| "C:/Program Files".into()),
    )
    .join("Git/usr/bin/ssh.exe");
    let mut cmd = Command::new(if cfg!(windows) && git_ssh.is_file() {
        git_ssh.as_os_str()
    } else {
        std::ffi::OsStr::new("ssh")
    });
    cmd.args([
        "-T",
        "-o",
        "BatchMode=yes",
        "-o",
        "ConnectTimeout=5",
        "-o",
        "ServerAliveInterval=5",
        "-o",
        "ServerAliveCountMax=2",
        host,
        remote,
    ]);
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        cmd.creation_flags(0x08000000);
    }
    cmd
}

fn execute(mut cmd: Command, raw: Vec<u8>, timeout: Duration) -> Result<serde_json::Value, String> {
    let mut child = cmd
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|_| "work_admin_unavailable")?;
    let mut input = child.stdin.take().ok_or("work_admin_unavailable")?;
    let output = child.stdout.take().ok_or("work_admin_unavailable")?;
    std::thread::spawn(move || {
        let _ = input.write_all(&raw);
    });
    let (tx, rx) = mpsc::channel();
    std::thread::spawn(move || {
        let mut bytes = Vec::new();
        let result = output
            .take((LIMIT + 1) as u64)
            .read_to_end(&mut bytes)
            .map(|_| bytes);
        let _ = tx.send(result);
    });
    let start = Instant::now();
    let mut received = None;
    loop {
        if let Ok(value) = rx.try_recv() {
            received = Some(value);
        }
        if received
            .as_ref()
            .is_some_and(|r| r.as_ref().map_or(true, |b| b.len() > LIMIT))
        {
            let _ = child.kill();
            let _ = child.wait();
            return Err("work_admin_response_invalid".into());
        }
        match child.try_wait() {
            Ok(Some(status)) => {
                let bytes = received
                    .unwrap_or_else(|| {
                        rx.recv_timeout(Duration::from_millis(500))
                            .unwrap_or_else(|_| {
                                Err(std::io::Error::new(std::io::ErrorKind::TimedOut, "read"))
                            })
                    })
                    .map_err(|_| "work_admin_response_invalid")?;
                if bytes.len() > LIMIT {
                    return Err("work_admin_response_invalid".into());
                }
                let value: serde_json::Value =
                    serde_json::from_slice(&bytes).map_err(|_| "work_admin_response_invalid")?;
                if !status.success() {
                    let code = value.get("error").and_then(|v| v.as_str()).unwrap_or("");
                    return Err(
                        if (code.starts_with("work_") || code.starts_with("git_"))
                            && code.len() <= 80
                            && code.bytes().all(|b| b.is_ascii_lowercase() || b == b'_')
                        {
                            code.to_string()
                        } else {
                            "work_admin_unavailable".into()
                        },
                    );
                }
                return Ok(value);
            }
            Ok(None) if start.elapsed() < timeout => std::thread::sleep(Duration::from_millis(20)),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                return Err("work_admin_timeout".into());
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn python(script: &str) -> Command {
        let mut c = Command::new(if cfg!(windows) { "python" } else { "python3" });
        c.args(["-I", "-c", script]);
        c
    }
    #[test]
    fn strict_remote_selection() {
        assert!(valid("server", "codex", "pc"));
        assert!(valid("user@host", "codex", "pc"));
        for host in ["-oProxyCommand=x", "host;id", "@host", "host\n", ""] {
            assert!(!valid(host, "codex", "pc"));
            assert_eq!(
                skill_execution(host, serde_json::json!({})),
                Err("skill_execution_invalid".into())
            );
        }
        assert_eq!(
            skill_execution(
                "server",
                serde_json::json!({"oversized": "x".repeat(8193)})
            ),
            Err("skill_execution_invalid".into())
        );
        assert!(!valid("server", "codex;id", "pc"));
        assert!(!valid("server", "codex", "other"));
    }
    #[test]
    fn pipe_limits_timeout_and_private_errors() {
        let raw = br#"{"goal":"test"}"#.to_vec();
        let value = execute(
            python("import sys;sys.stdout.write(sys.stdin.read())"),
            raw,
            Duration::from_secs(5),
        )
        .unwrap();
        assert_eq!(value["goal"], "test");
        assert_eq!(
            execute(
                python("import sys;sys.stdout.write('x'*300000)"),
                vec![],
                Duration::from_secs(5)
            ),
            Err("work_admin_response_invalid".into())
        );
        assert_eq!(
            execute(
                python("import time;time.sleep(60)"),
                vec![],
                Duration::from_millis(30)
            ),
            Err("work_admin_timeout".into())
        );
        assert_eq!(
            execute(
                python("import sys;print('{\"error\":\"private detail\"}');sys.exit(1)"),
                vec![],
                Duration::from_secs(5)
            ),
            Err("work_admin_unavailable".into())
        );
        assert_eq!(
            execute(
                python("import sys;print('{\"error\":\"work_event_changed\"}');sys.exit(1)"),
                vec![],
                Duration::from_secs(5)
            ),
            Err("work_event_changed".into())
        );
    }
    #[test]
    #[ignore = "requires isolated server fixture and an actual owner SSH connection"]
    fn owner_ssh_fixture_roundtrip() {
        let path = std::env::var("ARKISM_HANDOFF_TEST_SCENARIO").expect("isolated fixture path");
        let scenarios: serde_json::Value =
            serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
        for scenario in scenarios.as_array().unwrap() {
            let host = scenario["host"].as_str().unwrap();
            assert!(valid(host, "codex", "pc"));
            let remote = scenario["remote"].as_str().unwrap();
            let invoke = |request: &serde_json::Value| {
                execute(
                    ssh_command(host, remote),
                    serde_json::to_vec(request).unwrap(),
                    Duration::from_secs(25),
                )
            };
            let mut request = scenario["request"].clone();
            let listing = invoke(&request).unwrap();
            assert_eq!(listing["authority"], "data_only_untrusted");
            let before = scenario["before"].as_u64().unwrap() as usize;
            assert_eq!(listing["items"].as_array().unwrap().len(), before);
            let row = listing["items"]
                .as_array()
                .unwrap()
                .iter()
                .find(|row| row["event_id"] == scenario["event_id"])
                .unwrap();
            assert_eq!(row["work_id"], request["work_id"]);
            let version = row["version"].clone();
            request["event_id"] = row["event_id"].clone();
            request["action"] = "compare".into();
            let compared = invoke(&request).unwrap();
            assert_eq!(compared["version"], version);
            assert_eq!(
                compared["changes"]["goal"]["proposed"],
                "윈도우 실제 인계 검증"
            );
            let mut wrong_scope = request.clone();
            wrong_scope["scope"]["project_id"] = "foreign-project".into();
            assert_eq!(
                invoke(&wrong_scope),
                Err("work_scope_or_request_invalid".into())
            );
            request["action"] = "discard".into();
            request["version"] = "e".repeat(64).into();
            assert_eq!(invoke(&request), Err("work_event_changed".into()));
            request["version"] = version;
            let discarded = invoke(&request).unwrap();
            assert_eq!(discarded["event_id"], row["event_id"]);
            assert_eq!(discarded["status"], "discarded");
            let empty = invoke(&scenario["request"]).unwrap();
            assert_eq!(empty["items"].as_array().unwrap().len(), before - 1);
            assert!(empty["items"]
                .as_array()
                .unwrap()
                .iter()
                .all(|item| item["event_id"] != row["event_id"]));
            for (field, expected) in scenario["receipt"].as_object().unwrap() {
                assert_eq!(&discarded[field], expected);
            }
        }
    }
}
