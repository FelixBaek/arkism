// 크루 › 전문가 › 실행하는 곳 (Figma 1314:15710): this PC's agent runner connections as `arkism crew backends`
// keeps them (backends.json in the agents state folder). Read only; the token and its keyring reference never
// reach the page — only the name and what the last check found.

use std::path::{Path, PathBuf};

#[derive(serde::Serialize, Debug, PartialEq)]
pub struct Backend {
    pub name: String,
    /// "ok" | "failed: <reason>" | "" (never checked)
    pub status: String,
    pub checked_at: String,
    pub capabilities: std::collections::BTreeMap<String, bool>,
    pub isolated: bool,
    pub own_model: bool,
    pub memory: bool,
}

#[derive(serde::Deserialize)]
struct Row {
    name: String,
    #[serde(default)]
    status: String,
    #[serde(default)]
    checked_at: String,
    #[serde(default)]
    capabilities: std::collections::BTreeMap<String, bool>,
    #[serde(default)]
    isolated: bool,
    #[serde(default)]
    own_model: bool,
    #[serde(default)]
    memory: bool,
}

/// Same folder rule as gateway_token::token_path (internal/host AgentsState): ARKISM_AGENTS_STATE, else ~/.config/arkism.
fn path() -> Option<PathBuf> {
    if let Some(p) = std::env::var_os("ARKISM_AGENTS_STATE").filter(|p| !p.is_empty()) {
        return Some(PathBuf::from(p).join("backends.json"));
    }
    let home = std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" })?;
    Some(PathBuf::from(home).join(".config").join("arkism").join("backends.json"))
}

pub fn list_at(path: &Path) -> Result<Vec<Backend>, String> {
    let raw = match std::fs::read(path) {
        Ok(raw) => raw,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(_) => return Err("backends_unreadable".into()),
    };
    let rows: Vec<Row> = serde_json::from_slice(&raw).map_err(|_| "backends_invalid")?;
    Ok(rows
        .into_iter()
        .map(|r| Backend { name: r.name, status: r.status, checked_at: r.checked_at, capabilities: r.capabilities, isolated: r.isolated, own_model: r.own_model, memory: r.memory })
        .collect())
}

pub fn list() -> Result<Vec<Backend>, String> {
    list_at(&path().ok_or("home_unknown")?)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lists_names_and_checks_without_the_token() {
        let dir = std::env::temp_dir().join(format!("arkism-backends-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("backends.json");
        assert_eq!(list_at(&file).unwrap(), Vec::new());
        std::fs::write(&file, r#"[{"name":"myrunner","url":"https://r.example","token":"secret-value","secret_ref":"keyring","status":"ok","checked_at":"2026-10-26T01:00:00Z","capabilities":{"cancel":true},"own_model":true,"created_at":"x"}]"#).unwrap();
        let got = list_at(&file).unwrap();
        assert_eq!(got.len(), 1);
        assert_eq!((got[0].name.as_str(), got[0].status.as_str(), got[0].own_model, got[0].capabilities.get("cancel")), ("myrunner", "ok", true, Some(&true)));
        assert!(!serde_json::to_string(&got).unwrap().contains("secret"));
        std::fs::write(&file, "{").unwrap();
        assert_eq!(list_at(&file), Err("backends_invalid".into()));
        let _ = std::fs::remove_dir_all(&dir);
    }
}
