// 설정 › 연결 › 모델 중계기: the state of this PC's gateway token (~/.config/arkism/gateway.token, the
// file `arkism gateway` and install-arkism use) and making a new one. The token value never goes to the
// page: only whether there is a usable one and when it was written.

use std::io::Write;
use std::path::PathBuf;
use std::time::UNIX_EPOCH;

#[derive(serde::Serialize, Debug, PartialEq)]
pub struct Status {
    /// "set" | "missing" | "invalid" (not a plain private file, or not 64 lowercase hex characters)
    pub state: &'static str,
    /// When the file was last written (ms since 1970), when there is one.
    pub updated_ms: Option<u64>,
    /// ARKISM_GATEWAY_TOKEN is set for this app: the file is not what the gateway would use here.
    pub from_env: bool,
}

/// Same rule as internal/host.GatewayTokenFile: ARKISM_GATEWAY_TOKEN_FILE, else the agents state folder.
pub fn token_path() -> Option<PathBuf> {
    if let Some(p) = std::env::var_os("ARKISM_GATEWAY_TOKEN_FILE").filter(|p| !p.is_empty()) {
        return Some(PathBuf::from(p));
    }
    if let Some(p) = std::env::var_os("ARKISM_AGENTS_STATE").filter(|p| !p.is_empty()) {
        return Some(PathBuf::from(p).join("gateway.token"));
    }
    let home = std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" })?;
    Some(PathBuf::from(home).join(".config").join("arkism").join("gateway.token"))
}

fn valid(token: &str) -> bool {
    token.len() == 64 && token.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

pub fn status_at(path: &std::path::Path) -> Status {
    let from_env = std::env::var_os("ARKISM_GATEWAY_TOKEN").is_some_and(|v| !v.is_empty());
    let meta = match std::fs::symlink_metadata(path) {
        Ok(m) => m,
        Err(_) => return Status { state: "missing", updated_ms: None, from_env },
    };
    let updated_ms = meta.modified().ok().and_then(|t| t.duration_since(UNIX_EPOCH).ok()).map(|d| d.as_millis() as u64);
    #[cfg(unix)]
    let private = {
        use std::os::unix::fs::PermissionsExt;
        meta.permissions().mode() & 0o077 == 0
    };
    #[cfg(not(unix))]
    let private = true;
    let ok = meta.file_type().is_file() && private && std::fs::read_to_string(path).is_ok_and(|s| valid(s.trim()));
    Status { state: if ok { "set" } else { "invalid" }, updated_ms, from_env }
}

pub fn status() -> Result<Status, String> {
    Ok(status_at(&token_path().ok_or("home_unknown")?))
}

/// Writes a fresh token in place of the old one (owner-only, replaced in one rename).
pub fn regenerate_at(path: &std::path::Path) -> Result<(), String> {
    let dir = path.parent().ok_or("token_path_invalid")?;
    std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700));
    }
    let mut raw = [0u8; 32];
    getrandom::fill(&mut raw).map_err(|e| e.to_string())?;
    let token: String = raw.iter().map(|b| format!("{b:02x}")).collect();
    let tmp = dir.join(format!(".gateway-token-{}", std::process::id()));
    let mut opts = std::fs::OpenOptions::new();
    opts.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let _ = std::fs::remove_file(&tmp);
    let result = (|| {
        let mut f = opts.open(&tmp)?;
        f.write_all(format!("{token}\n").as_bytes())?;
        f.sync_all()?;
        drop(f);
        // A link or folder in the way is not overwritten through: take it out first.
        if std::fs::symlink_metadata(path).is_ok_and(|m| !m.file_type().is_file()) {
            std::fs::remove_file(path)?;
        }
        std::fs::rename(&tmp, path)
    })();
    if result.is_err() {
        let _ = std::fs::remove_file(&tmp);
    }
    result.map_err(|e| e.to_string())
}

pub fn regenerate() -> Result<(), String> {
    regenerate_at(&token_path().ok_or("home_unknown")?)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_then_regenerated_then_broken() {
        let dir = std::env::temp_dir().join(format!("arkism-gwtok-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let path = dir.join("arkism").join("gateway.token");
        assert_eq!(status_at(&path).state, "missing");
        regenerate_at(&path).unwrap();
        let first = std::fs::read_to_string(&path).unwrap();
        assert!(valid(first.trim()));
        let st = status_at(&path);
        assert_eq!(st.state, "set");
        assert!(st.updated_ms.is_some());
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(std::fs::metadata(&path).unwrap().permissions().mode() & 0o777, 0o600);
        }
        regenerate_at(&path).unwrap();
        assert_ne!(std::fs::read_to_string(&path).unwrap(), first);
        std::fs::write(&path, "not a token\n").unwrap();
        assert_eq!(status_at(&path).state, "invalid");
        let _ = std::fs::remove_dir_all(&dir);
    }
}
