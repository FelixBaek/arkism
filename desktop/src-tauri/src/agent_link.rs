//! 라우팅 › 에이전트 연결: points an agent on this computer at the built-in model gateway, and back.
//!
//! Each agent keeps its own login; only where it sends model requests changes, and the model becomes
//! `group/auto` (every model turned on in 모델 설정). What was there before is kept in `agent-links.json`
//! in the app's data folder and put back on disconnect.
//! - Claude Code: `~/.claude/settings.json` → `env.ANTHROPIC_BASE_URL` and `model`.
//! - Codex: `~/.codex/config.toml` → `model_provider = "arkism"`, `model`, and a `[model_providers.arkism]` table.
//! - Gemini CLI: `~/.gemini/.env` → `GOOGLE_GEMINI_BASE_URL` and `GEMINI_MODEL` (API-key sign-in only:
//!   with a Google login the CLI talks to Code Assist and ignores the base URL).
use std::fs;
use std::path::{Path, PathBuf};

pub const AGENTS: [&str; 3] = ["claude_code", "codex", "gemini"];
const MODEL: &str = "group/auto";
const MARK: &str = "# arkism-gateway";
const OFF: &str = "# arkism-gateway-off: ";

fn file(home: &Path, agent: &str) -> Option<PathBuf> {
    Some(match agent {
        "claude_code" => home.join(".claude").join("settings.json"),
        "codex" => home.join(".codex").join("config.toml"),
        "gemini" => home.join(".gemini").join(".env"),
        _ => return None,
    })
}

/// Whether each agent is installed (its folder exists) and whether it points at `url`.
pub fn status(home: &Path, url: Option<&str>) -> serde_json::Value {
    let mut out = serde_json::Map::new();
    for agent in AGENTS {
        let path = file(home, agent).expect("known agent");
        let installed = path.parent().is_some_and(Path::is_dir);
        let text = fs::read_to_string(&path).unwrap_or_default();
        let connected = match agent {
            "claude_code" => serde_json::from_str::<serde_json::Value>(&text).ok().and_then(|v| v["env"]["ANTHROPIC_BASE_URL"].as_str().map(str::to_string)).is_some_and(|u| u.starts_with("http://127.0.0.1:")) && text.contains(MODEL),
            _ => text.contains(MARK),
        };
        let current = url.is_some_and(|u| points_at(agent, &text, u));
        out.insert(agent.into(), serde_json::json!({ "installed": installed, "connected": connected, "current": connected && current }));
    }
    serde_json::Value::Object(out)
}

/// Whether the agent's file sends requests to exactly `url` (not a URL that merely starts with it).
fn points_at(agent: &str, text: &str, url: &str) -> bool {
    match agent {
        "claude_code" => serde_json::from_str::<serde_json::Value>(text).ok().is_some_and(|v| v["env"]["ANTHROPIC_BASE_URL"].as_str() == Some(url)),
        "codex" => text.lines().any(|l| l.trim() == format!("base_url = \"{url}/v1\"")),
        _ => text.lines().any(|l| l.trim() == format!("GOOGLE_GEMINI_BASE_URL={url}")),
    }
}

/// Connects (`url` Some) or disconnects (`None`) one agent. `saved` is the app's `agent-links.json`.
pub fn set(home: &Path, saved: &Path, agent: &str, url: Option<&str>) -> Result<(), String> {
    let path = file(home, agent).ok_or("agent_unknown")?;
    let text = match fs::read_to_string(&path) {
        Ok(t) => t,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => String::new(),
        Err(e) => return Err(format!("agent_settings_read: {e}")),
    };
    let saved_before = fs::read(saved).ok();
    let mut links: serde_json::Value = saved_before.as_deref().and_then(|t| serde_json::from_slice(t).ok()).unwrap_or_else(|| serde_json::json!({}));
    let next = match agent {
        "claude_code" => claude(&text, url, &mut links)?,
        "codex" => codex(&text, url)?,
        _ => gemini(&text, url),
    };
    if url.is_none() && text.is_empty() {
        return Ok(());
    }
    fs::create_dir_all(path.parent().expect("has a parent")).map_err(|e| e.to_string())?;
    if !text.is_empty() {
        // One copy of what was there before the first change, beside the file.
        let backup = path.with_extension(format!("{}.arkism-backup", path.extension().and_then(|e| e.to_str()).unwrap_or("bak")));
        if !backup.exists() {
            fs::write(&backup, &text).map_err(|e| e.to_string())?;
        }
    }
    // The saved links go first; if the agent's file can't be written, they are put back as they were.
    fs::write(saved, links.to_string()).map_err(|e| e.to_string())?;
    if let Err(e) = fs::write(&path, next) {
        let _ = match &saved_before {
            Some(b) => fs::write(saved, b),
            None => fs::remove_file(saved),
        };
        return Err(format!("agent_settings_write: {e}"));
    }
    Ok(())
}

fn claude(text: &str, url: Option<&str>, links: &mut serde_json::Value) -> Result<String, String> {
    let mut v: serde_json::Value = if text.trim().is_empty() { serde_json::json!({}) } else { serde_json::from_str(text).map_err(|_| "agent_settings_unreadable")? };
    let obj = v.as_object_mut().ok_or("agent_settings_unreadable")?;
    match url {
        Some(url) => {
            let prev = obj.get("model").cloned().unwrap_or(serde_json::Value::Null);
            if prev.as_str() != Some(MODEL) {
                links["claude_code"] = serde_json::json!({ "model": prev });
            }
            let env = obj.entry("env").or_insert_with(|| serde_json::json!({}));
            env.as_object_mut().ok_or("agent_settings_unreadable")?.insert("ANTHROPIC_BASE_URL".into(), url.into());
            obj.insert("model".into(), MODEL.into());
        }
        None => {
            if let Some(env) = obj.get_mut("env").and_then(|e| e.as_object_mut()) {
                if env.get("ANTHROPIC_BASE_URL").and_then(|u| u.as_str()).is_some_and(|u| u.starts_with("http://127.0.0.1:")) {
                    env.remove("ANTHROPIC_BASE_URL");
                }
                if env.is_empty() {
                    obj.remove("env");
                }
            }
            if obj.get("model").and_then(|m| m.as_str()) == Some(MODEL) {
                match links["claude_code"]["model"].clone() {
                    serde_json::Value::Null => obj.remove("model"),
                    prev => obj.insert("model".into(), prev),
                };
            }
        }
    }
    serde_json::to_string_pretty(&v).map(|s| s + "\n").map_err(|e| e.to_string())
}

/// Removes what an earlier connect wrote and turns back on the lines it set aside.
fn codex_clean(text: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut skip = false;
    for line in text.lines() {
        if line == format!("{MARK} begin") {
            skip = true;
            continue;
        }
        if line == format!("{MARK} end") {
            skip = false;
            continue;
        }
        if skip || line.ends_with(MARK) {
            continue;
        }
        out.push(line.strip_prefix(OFF).unwrap_or(line).to_string());
    }
    out
}

fn codex(text: &str, url: Option<&str>) -> Result<String, String> {
    let mut lines = codex_clean(text);
    if let Some(url) = url {
        if lines.iter().any(|l| l.trim() == "[model_providers.arkism]") {
            return Err("codex_provider_exists".into());
        }
        // Top-level keys sit before the first table; ours replace the user's (set aside, put back later).
        let first_table = lines.iter().position(|l| l.trim_start().starts_with('[')).unwrap_or(lines.len());
        for line in lines[..first_table].iter_mut() {
            let key = line.split('=').next().unwrap_or("").trim();
            if key == "model" || key == "model_provider" {
                *line = format!("{OFF}{line}");
            }
        }
        lines.insert(0, format!("model = \"{MODEL}\" {MARK}"));
        lines.insert(0, format!("model_provider = \"arkism\" {MARK}"));
        if lines.last().is_some_and(|l| !l.trim().is_empty()) {
            lines.push(String::new());
        }
        lines.extend([format!("{MARK} begin"), "[model_providers.arkism]".into(), "name = \"Arkism\"".into(),
            format!("base_url = \"{url}/v1\""), "wire_api = \"responses\"".into(), format!("{MARK} end")]);
    }
    Ok(lines.join("\n") + "\n")
}

fn gemini(text: &str, url: Option<&str>) -> String {
    let mut lines = codex_clean(text);
    if let Some(url) = url {
        for line in lines.iter_mut() {
            let key = line.split('=').next().unwrap_or("").trim().trim_start_matches("export ").trim();
            if key == "GOOGLE_GEMINI_BASE_URL" || key == "GEMINI_MODEL" {
                *line = format!("{OFF}{line}");
            }
        }
        lines.extend([format!("{MARK} begin"), format!("GOOGLE_GEMINI_BASE_URL={url}"), format!("GEMINI_MODEL={MODEL}"), format!("{MARK} end")]);
    }
    let mut out = lines.join("\n");
    if !out.is_empty() {
        out.push('\n');
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn codex_round_trip_keeps_the_users_lines() {
        let before = "model = \"gpt-5.5\"\napproval_policy = \"never\"\n\n[mcp_servers.x]\ncommand = \"x\"\n";
        let on = codex(before, Some("http://127.0.0.1:8099")).unwrap();
        assert!(on.starts_with("model_provider = \"arkism\" # arkism-gateway\nmodel = \"group/auto\" # arkism-gateway\n# arkism-gateway-off: model = \"gpt-5.5\"\n"));
        assert!(on.contains("[model_providers.arkism]\nname = \"Arkism\"\nbase_url = \"http://127.0.0.1:8099/v1\"\nwire_api = \"responses\"\n"));
        let again = codex(&on, Some("http://127.0.0.1:8100")).unwrap();
        assert_eq!(again.matches("[model_providers.arkism]").count(), 1);
        assert!(again.contains("8100/v1") && !again.contains("8099"));
        assert_eq!(codex(&on, None).unwrap().trim_end(), before.trim_end());
        assert!(codex("[model_providers.arkism]\n", Some("http://127.0.0.1:1")).is_err());
    }

    #[test]
    fn claude_round_trip_restores_the_model() {
        let mut links = serde_json::json!({});
        let before = r#"{"model":"opus","env":{"FOO":"1"},"hooks":{}}"#;
        let on = claude(before, Some("http://127.0.0.1:8099"), &mut links).unwrap();
        let v: serde_json::Value = serde_json::from_str(&on).unwrap();
        assert_eq!(v["model"], "group/auto");
        assert_eq!(v["env"]["ANTHROPIC_BASE_URL"], "http://127.0.0.1:8099");
        assert_eq!(v["env"]["FOO"], "1");
        let on2 = claude(&on, Some("http://127.0.0.1:8100"), &mut links).unwrap();
        assert_eq!(links["claude_code"]["model"], "opus");
        let off: serde_json::Value = serde_json::from_str(&claude(&on2, None, &mut links).unwrap()).unwrap();
        assert_eq!(off, serde_json::from_str::<serde_json::Value>(before).unwrap());
        let mut fresh = serde_json::json!({});
        let off: serde_json::Value = serde_json::from_str(&claude(&claude("", Some("http://127.0.0.1:1"), &mut fresh).unwrap(), None, &mut fresh).unwrap()).unwrap();
        assert_eq!(off, serde_json::json!({}));
    }

    #[test]
    fn current_needs_the_exact_url() {
        let on = codex("", Some("http://127.0.0.1:8099")).unwrap();
        assert!(points_at("codex", &on, "http://127.0.0.1:8099") && !points_at("codex", &on, "http://127.0.0.1:809"));
        let on = gemini("", Some("http://127.0.0.1:8099"));
        assert!(points_at("gemini", &on, "http://127.0.0.1:8099") && !points_at("gemini", &on, "http://127.0.0.1:809"));
        let on = claude("", Some("http://127.0.0.1:8099"), &mut serde_json::json!({})).unwrap();
        assert!(points_at("claude_code", &on, "http://127.0.0.1:8099") && !points_at("claude_code", &on, "http://127.0.0.1:809"));
    }

    #[test]
    fn gemini_round_trip() {
        let before = "GEMINI_API_KEY=abc\nGEMINI_MODEL=gemini-2.5-pro\n";
        let on = gemini(before, Some("http://127.0.0.1:8099"));
        assert!(on.contains("# arkism-gateway-off: GEMINI_MODEL=gemini-2.5-pro\n") && on.contains("GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:8099\nGEMINI_MODEL=group/auto\n"));
        assert_eq!(gemini(&on, None), before);
        assert_eq!(gemini("", None), "");
    }
}
