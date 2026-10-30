//! 계정 추가 › 로그인: opens a terminal window on this PC that runs an agent CLI's own sign-in (it opens the
//! browser itself), then waits for the CLI to write its login file. Only fixed commands run; nothing the page
//! sends reaches a shell except the account folder the app made itself (quoted).
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};

/// The sign-in command of each CLI the app can log in, and the file it writes when done (in its config folder).
pub fn known(cli: &str) -> Option<(&'static [&'static str], &'static str)> {
    Some(match cli {
        "claude" => (&["claude", "auth", "login"], ".credentials.json"),
        "codex" => (&["codex", "login"], "auth.json"),
        // Gemini CLI has no login subcommand: on its first run it asks how to sign in.
        "gemini" => (&["gemini"], "oauth_creds.json"),
        _ => return None,
    })
}

/// The variable that points a CLI at another config folder (a second account), as the host's account profiles read it.
pub fn folder_env(cli: &str) -> Option<&'static str> {
    match cli {
        "claude" => Some("CLAUDE_CONFIG_DIR"),
        "codex" => Some("CODEX_HOME"),
        _ => None,
    }
}

/// The CLI's default config folder. shortcut: ignores CLAUDE_CONFIG_DIR/CODEX_HOME set for the app itself; read them if users report it.
pub fn default_folder(home: &Path, cli: &str) -> PathBuf {
    home.join(format!(".{cli}"))
}

fn sh_quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', "'\\''"))
}

/// The shell line a macOS or Linux terminal runs: the sign-in, then a line saying the window can close.
pub fn script(cli: &str, folder: Option<(&str, &Path)>) -> Option<String> {
    let (cmd, _) = known(cli)?;
    let env = folder.map(|(k, d)| format!("{k}={} ", sh_quote(&d.to_string_lossy()))).unwrap_or_default();
    Some(format!("{env}{}; echo; echo 'Arkism: 로그인이 끝나면 이 창을 닫아도 돼요.'; read -r _", cmd.join(" ")))
}

/// Opens the terminal window. `folder`: (variable, folder) for a second account.
pub fn open(cli: &str, folder: Option<(&str, &Path)>) -> Result<(), String> {
    let (cmd, _) = known(cli).ok_or("login_cli_unknown")?;
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        // `start` gives the CLI its own console window; /k keeps it open to show how the sign-in ended.
        let mut c = std::process::Command::new("cmd");
        c.args(["/c", "start", "Arkism", "cmd", "/k"]).args(cmd.iter());
        if let Some((k, d)) = folder {
            c.env(k, d);
        }
        c.creation_flags(0x0800_0000);
        return c.spawn().map(|_| ()).map_err(|_| "login_terminal_missing".into());
    }
    #[cfg(not(windows))]
    {
        let _ = cmd;
        let line = script(cli, folder).ok_or("login_cli_unknown")?;
        #[cfg(target_os = "macos")]
        {
            let quoted = line.replace('\\', "\\\\").replace('"', "\\\"");
            return std::process::Command::new("osascript")
                .args(["-e", &format!("tell application \"Terminal\" to do script \"{quoted}\""), "-e", "tell application \"Terminal\" to activate"])
                .spawn()
                .map(|_| ())
                .map_err(|_| "login_terminal_missing".into());
        }
        #[cfg(not(target_os = "macos"))]
        {
            for (prog, flag) in [("x-terminal-emulator", "-e"), ("gnome-terminal", "--"), ("konsole", "-e"), ("xfce4-terminal", "-x"), ("xterm", "-e")] {
                let mut c = std::process::Command::new(prog);
                c.args([flag, "sh", "-c", &line]);
                if let Some((k, d)) = folder {
                    c.env(k, d);
                }
                if c.spawn().is_ok() {
                    return Ok(());
                }
            }
            Err("login_terminal_missing".into())
        }
    }
}

/// Whether `file` was written at or after `since`.
pub fn written_since(file: &Path, since: SystemTime) -> bool {
    std::fs::metadata(file).and_then(|m| m.modified()).is_ok_and(|t| t >= since)
}

/// Waits up to `timeout` for the login file, checking every two seconds.
pub fn wait(file: &Path, since: SystemTime, timeout: Duration) -> bool {
    let deadline = std::time::Instant::now() + timeout;
    while std::time::Instant::now() < deadline {
        if written_since(file, since) {
            return true;
        }
        std::thread::sleep(Duration::from_secs(2));
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_known_clis_and_fixed_commands() {
        assert!(known("claude").is_some() && known("codex").is_some() && known("gemini").is_some());
        assert!(known("rm -rf /").is_none() && script("x; ls", None).is_none());
        assert!(script("codex", None).unwrap().starts_with("codex login; "));
        assert_eq!(folder_env("gemini"), None);
    }

    #[test]
    fn folder_is_quoted_for_the_shell() {
        let s = script("claude", Some(("CLAUDE_CONFIG_DIR", Path::new("/Users/a b/it's")))).unwrap();
        assert!(s.starts_with("CLAUDE_CONFIG_DIR='/Users/a b/it'\\''s' claude auth login; "), "{s}");
    }

    #[test]
    fn login_file_counts_only_after_start() {
        let dir = std::env::temp_dir().join(format!("arkism-login-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("auth.json");
        let before = SystemTime::now() - Duration::from_secs(5);
        assert!(!written_since(&file, before));
        std::fs::write(&file, "{}").unwrap();
        assert!(written_since(&file, before));
        assert!(!written_since(&file, SystemTime::now() + Duration::from_secs(60)));
        let _ = std::fs::remove_dir_all(&dir);
    }
}
