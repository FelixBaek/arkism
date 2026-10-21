use crate::ssh_identity::Store;
use std::process::Command;
use std::time::Duration;

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
    store: &Store,
) -> Result<serde_json::Value, String> {
    if !valid(host, "codex", "server") {
        return Err("skill_execution_invalid".into());
    }
    let raw = serde_json::to_vec(&request).map_err(|_| "skill_execution_invalid")?;
    if raw.len() > 8192 {
        return Err("skill_execution_invalid".into());
    }
    execute(
        ssh_command(host, &format!("{SERVER_BIN} skill-execution"), store)?,
        raw,
        Duration::from_secs(25),
    )
    .map_err(|e| {
        if e.starts_with("ssh_") {
            e
        } else {
            "skill_execution_unavailable".into()
        }
    })
}

// Git screen: the server reads the ARKISM_GIT_ORG repositories and makes bounded GitHub changes
// (arkism git). A first clone of a large repository can take about a minute.
pub fn git(
    host: &str,
    request: serde_json::Value,
    store: &Store,
) -> Result<serde_json::Value, String> {
    if !valid(host, "codex", "server") {
        return Err("git_invalid_request".into());
    }
    let raw = serde_json::to_vec(&request).map_err(|_| "git_invalid_request")?;
    if raw.len() > 65536 {
        return Err("git_invalid_request".into());
    }
    execute(
        ssh_command(host, &format!("{SERVER_BIN} git"), store)?,
        raw,
        Duration::from_secs(180),
    )
}

fn ssh_command(host: &str, remote: &str, store: &Store) -> Result<Command, String> {
    let target = crate::ssh_tunnel::target_for_host(host)?;
    let pin = store.required(&target)?;
    let mut cmd = store.command(&target, &pin)?;
    cmd.args(["-T", "-o", "ClearAllForwardings=yes"])
        .arg(&target.host)
        .arg(remote);
    Ok(cmd)
}

fn execute(cmd: Command, raw: Vec<u8>, timeout: Duration) -> Result<serde_json::Value, String> {
    let result = crate::ssh_exec::run(cmd, raw, LIMIT, timeout).map_err(|e| -> String {
        match e.as_str() {
            "ssh_unreachable" => "work_admin_timeout".into(),
            "ssh_output_invalid" => "work_admin_response_invalid".into(),
            _ => "work_admin_unavailable".into(),
        }
    })?;
    // Host-key rejection precedes target authentication and must reach the shared review UI.
    let stderr = String::from_utf8_lossy(&result.err).to_ascii_lowercase();
    if result.code != 0
        && (stderr.contains("host key verification failed")
            || stderr.contains("remote host identification has changed"))
    {
        return Err("ssh_host_key_changed".into());
    }
    let value: serde_json::Value =
        serde_json::from_slice(&result.out).map_err(|_| "work_admin_response_invalid")?;
    if result.code != 0 {
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
    Ok(value)
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
                skill_execution(
                    host,
                    serde_json::json!({}),
                    &Store::new(std::path::Path::new("unused"))
                ),
                Err("skill_execution_invalid".into())
            );
        }
        assert_eq!(
            skill_execution(
                "server",
                serde_json::json!({"oversized": "x".repeat(8193)}),
                &Store::new(std::path::Path::new("unused"))
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
                    ssh_command(
                        host,
                        remote,
                        &Store::new(&std::path::PathBuf::from(
                            std::env::var_os("ARKISM_HANDOFF_TEST_DATA")
                                .expect("isolated approved store"),
                        )),
                    )
                    .unwrap(),
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
