use serde::{Deserialize, Serialize};
use std::{fs, net::SocketAddrV4, path::{Path, PathBuf}, sync::OnceLock, time::Duration};

pub const SESSION: &str = "arkism-managed-local";

#[derive(Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Scope {
    workspace_id: String,
    project_id: String,
    user_id: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Profile {
    server: String,
    credentials: PathBuf,
    scope: Scope,
}

#[derive(Serialize)]
pub struct Connected {
    pub server: String,
    pub scope: Scope,
}

pub struct Connection {
    profile: Profile,
    token: String,
    runtime_id: String,
}

pub fn profile_path(dir: &Path) -> PathBuf {
    dir.join("managed-local.json")
}

pub fn agent() -> &'static ureq::Agent {
    static AGENT: OnceLock<ureq::Agent> = OnceLock::new();
    AGENT.get_or_init(|| new_agent(Duration::from_secs(30)))
}

pub fn upload_agent() -> &'static ureq::Agent {
    static AGENT: OnceLock<ureq::Agent> = OnceLock::new();
    AGENT.get_or_init(|| new_agent(Duration::from_secs(30 * 60)))
}

fn new_agent(timeout: Duration) -> ureq::Agent {
    ureq::Agent::config_builder()
        .proxy(None)
        .max_redirects(0)
        .http_status_as_error(false)
        .timeout_global(Some(timeout))
        .build().into()
}

fn read(path: &Path, limit: u64) -> Result<Vec<u8>, String> {
    let bad = || "managed_local_profile_invalid".to_string();
    let meta = fs::symlink_metadata(path).map_err(|_| bad())?;
    if !meta.is_file() || meta.len() > limit {
        return Err(bad());
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::MetadataExt;
        if meta.file_attributes() & 0x400 != 0 {
            return Err(bad());
        }
    }
    crate::update_manifest::read_bounded(fs::File::open(path).map_err(|_| bad())?, limit)
        .map_err(|_| bad())
}

fn valid_server(base: &str) -> bool {
    base.strip_prefix("http://")
        .and_then(|value| value.parse::<SocketAddrV4>().ok())
        .is_some_and(|address| {
            *address.ip() == std::net::Ipv4Addr::LOCALHOST
                && address.port() >= 1024
                && base == format!("http://{address}")
        })
}

impl Connection {
    pub fn read_from(dir: &Path) -> Result<Self, String> {
        let bad = || "managed_local_profile_invalid".to_string();
        let profile: Profile = serde_json::from_slice(&read(&profile_path(dir), 4096)?)
            .map_err(|_| bad())?;
        let valid_id = |id: &str| {
            !id.is_empty() && id.len() <= 128
                && id.bytes().all(|b| b.is_ascii_alphanumeric() || b":._-".contains(&b))
        };
        if !valid_server(&profile.server) || !profile.credentials.is_absolute()
            || ![&profile.scope.workspace_id, &profile.scope.project_id, &profile.scope.user_id]
                .into_iter().all(|id| valid_id(id))
        {
            return Err(bad());
        }
        let secret = |name: &str| -> Result<String, String> {
            let value = String::from_utf8(read(&profile.credentials.join(name), 1024)?)
                .map_err(|_| bad())?;
            let value = value.trim();
            if value.len() != 64 || !value.bytes().all(|b| b.is_ascii_hexdigit()) {
                return Err(bad());
            }
            Ok(value.into())
        };
        let token = secret("owner.token")?;
        let runtime_id = secret("capture.runtime-id")?;
        Ok(Self { profile, token, runtime_id })
    }

    fn check_identity(&self) -> Result<(), String> {
        let unavailable = || "managed_local_server_unavailable".to_string();
        let ready = agent().get(format!("{}/readyz", self.profile.server)).call()
            .map_err(|_| unavailable())?;
        if ready.status().as_u16() != 200
            || ready.headers().get("X-Arkism-Runtime-Id").and_then(|h| h.to_str().ok())
                != Some(self.runtime_id.as_str())
        {
            return Err(unavailable());
        }
        Ok(())
    }

    pub fn check(&self) -> Result<Connected, String> {
        self.check_identity()?;
        let unavailable = || "managed_local_server_unavailable".to_string();
        let scope = &self.profile.scope;
        let caps = agent().get(format!("{}/v1/capabilities", self.profile.server))
            .query("workspace_id", &scope.workspace_id)
            .query("project_id", &scope.project_id)
            .query("user_id", &scope.user_id)
            .header("Authorization", format!("Bearer {}", self.token))
            .call().map_err(|_| unavailable())?;
        if caps.status().as_u16() != 200 {
            return Err(unavailable());
        }
        Ok(Connected { server: self.profile.server.clone(), scope: scope.clone() })
    }

    pub fn into_token(self, base: &str) -> Result<String, String> {
        if self.profile.server != base {
            return Err("managed_local_address_mismatch".into());
        }
        // Verify the server identity before releasing a credential to the request path.
        self.check_identity()?;
        Ok(self.token)
    }
}

#[cfg(test)]
mod tests {
    use super::valid_server;

    #[test]
    fn managed_connection_accepts_only_a_numeric_local_origin() {
        assert!(valid_server("http://127.0.0.1:18091"));
        for invalid in [
            "http://127.0.0.1:18091/", "http://127.0.0.1:18091?x",
            "http://localhost:18091", "http://127.0.0.2:18091",
            "https://127.0.0.1:18091", "http://127.0.0.1:80",
            "http://127.0.0.1:18091@example.com",
        ] {
            assert!(!valid_server(invalid), "{invalid}");
        }
    }
}
