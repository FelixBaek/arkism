//! Built-in Arkism: a private Postgres and the Arkism server that the app runs on this computer.
//!
//! The installer puts the programs in `runtime/` next to the app (or in the app's resources on
//! macOS and Linux): `pgsql/{bin,lib,share}`, `memoryd`, `memory-principal` and `roles.sql`.
//! Data lives in the app's local data folder, so reinstalling or updating keeps it.
//! Everything listens on 127.0.0.1 only. The passwords and tokens are in `runtime.json`,
//! readable by this user alone; anyone who can read that folder can read the database files too.
use std::fs;
use std::net::TcpListener;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::Mutex;
use std::time::{Duration, Instant};

const PG_PORT: u16 = 55491;
const API_PORT: u16 = 8091;
/// The model gateway agents point at (`arkism gateway`): Claude Code, Codex and Gemini CLI.
const GATEWAY_PORT: u16 = 8099;
const DATABASE: &str = "arkism";
const SCOPE: [&str; 4] = ["--organization", "default", "--project", "default"];

#[derive(serde::Serialize, serde::Deserialize, Clone)]
struct Secrets {
    admin: String,
    init: String,
    app: String,
    owner: String,
    worker: String,
    pg_port: u16,
    api_port: u16,
    /// Set once the owner and worker tokens are registered.
    registered: bool,
    /// Encrypts the archived agent transcripts that memories point back to (32 bytes, hex).
    /// Added after the first release, so older settings files get one on the next start.
    #[serde(default)]
    archive_key: String,
    /// The embedder the stored vectors were last indexed for ("none" or "ollama:<model>").
    #[serde(default)]
    embedder: String,
    /// Where the built-in model gateway listens (0 until it first starts).
    #[serde(default)]
    gateway_port: u16,
}

#[derive(serde::Serialize, Clone)]
pub struct Started {
    pub server: String,
    pub token: String,
}

pub struct Runtime {
    programs: Option<PathBuf>,
    data: PathBuf,
    children: Mutex<Vec<Child>>,
    /// `arkism crew providers watch`: model accounts, quotas, model tests and AI help.
    watcher: Mutex<Option<Child>>,
    /// `arkism gateway`: the agents connected in 라우팅 send their model requests here.
    gateway: Mutex<Option<Child>>,
    /// Start, backup, restore and erase take turns.
    busy: Mutex<()>,
    /// The user's home: the agents' capture queues are under `.config/arkism`.
    home: Option<PathBuf>,
}

impl Runtime {
    /// `programs` is the folder with the bundled programs; None when this build has none.
    pub fn new(programs: Option<PathBuf>, data: PathBuf) -> Self {
        Runtime { programs, data, children: Mutex::new(Vec::new()), watcher: Mutex::new(None), gateway: Mutex::new(None), busy: Mutex::new(()), home: home_dir() }
    }

    /// Leaves the user's home alone (no capture queues read or erased): `--runtime-check`.
    pub fn isolated(mut self) -> Self {
        self.home = None;
        self
    }

    /// The bundled agent client (`arkism`), if this build has one.
    pub fn agent_client(&self) -> Option<PathBuf> {
        find_agent_client(self.programs.as_deref())
    }

    /// The user's home folder (None in `--runtime-check`, which leaves the agents alone).
    pub fn home(&self) -> Option<&Path> {
        self.home.as_deref()
    }

    pub fn data_dir(&self) -> &Path {
        &self.data
    }

    pub fn available(&self) -> bool {
        self.programs.is_some()
    }

    fn programs(&self) -> Result<&Path, String> {
        self.programs.as_deref().ok_or_else(|| "local_unavailable".to_string())
    }

    fn pg_bin(&self, name: &str) -> Result<PathBuf, String> {
        Ok(self.programs()?.join("pgsql").join("bin").join(exe(name)))
    }

    fn pgdata(&self) -> PathBuf {
        self.data.join("pg")
    }

    /// Starts Postgres, prepares the database and starts the server. Safe to call again:
    /// what already runs is reused. Returns the address and the owner token for the UI.
    pub fn start(&self) -> Result<Started, String> {
        let _turn = self.busy.lock().map_err(|_| "local_busy".to_string())?;
        self.start_locked()
    }

    fn start_locked(&self) -> Result<Started, String> {
        self.programs()?;
        fs::create_dir_all(self.data.join("logs")).map_err(|e| format!("local_data_folder: {e}"))?;
        let mut secrets = self.secrets()?;
        self.start_postgres(&mut secrets)?;
        self.prepare_database(&mut secrets)?;
        let server = self.start_server(&mut secrets)?;
        self.write_agent_connection(Some(&server), Some(&secrets), None)?;
        // The gateway is part of the built-in Arkism; routing still works without it, so a failure only logs.
        let _ = self.ensure_gateway();
        Ok(Started { server, token: secrets.owner.clone() })
    }

    /// Starts the model gateway on this computer if it is not running. It serves the models in
    /// 모델 설정 with the user's own logins and keys; nothing leaves for an Arkism server.
    pub fn ensure_gateway(&self) -> Result<String, String> {
        let mut gateway = self.gateway.lock().map_err(|_| "local_busy".to_string())?;
        let mut secrets = self.secrets()?;
        let alive = gateway.as_mut().is_some_and(|c| matches!(c.try_wait(), Ok(None)));
        if alive && secrets.gateway_port != 0 {
            return Ok(gateway_url(secrets.gateway_port));
        }
        let old_port = secrets.gateway_port;
        if secrets.gateway_port == 0 || !port_free(secrets.gateway_port) {
            secrets.gateway_port = free_port(GATEWAY_PORT)?;
        }
        let listen = format!("127.0.0.1:{}", secrets.gateway_port);
        let mut cmd = self.host_command(&["gateway"]).ok_or("local_unavailable")?;
        let log = fs::OpenOptions::new().create(true).append(true).open(self.data.join("logs").join("gateway.log")).map_err(|e| e.to_string())?;
        let err = log.try_clone().map_err(|e| e.to_string())?;
        cmd.env("ARKISM_GATEWAY_LISTEN", &listen).stdin(Stdio::null()).stdout(log).stderr(err);
        no_window(&mut cmd);
        let mut child = cmd.spawn().map_err(|e| format!("gateway_start_failed: {e}"))?;
        // A gateway that exits at once (bad config, port taken) is not reported as running.
        std::thread::sleep(std::time::Duration::from_millis(300));
        if !matches!(child.try_wait(), Ok(None)) {
            return Err("gateway_start_failed".into());
        }
        *gateway = Some(child);
        if secrets.gateway_port != old_port {
            self.save(&secrets)?;
            // Agents linked to the old address follow the gateway to its new port.
            if let Some(home) = self.home() {
                let saved = self.data_dir().join("agent-links.json");
                for agent in crate::agent_link::AGENTS {
                    if old_port != 0 && crate::agent_link::status(home, Some(&gateway_url(old_port)))[agent]["current"] == true {
                        let _ = crate::agent_link::set(home, &saved, agent, Some(&gateway_url(secrets.gateway_port)));
                    }
                }
            }
        }
        Ok(gateway_url(secrets.gateway_port))
    }

    /// The gateway's address while it runs.
    pub fn gateway_address(&self) -> Option<String> {
        let mut gateway = self.gateway.lock().ok()?;
        let alive = gateway.as_mut().is_some_and(|c| matches!(c.try_wait(), Ok(None)));
        let port = self.secrets().ok()?.gateway_port;
        (alive && port != 0).then(|| gateway_url(port))
    }

    /// What the PC agent client (`arkism windows hook|mcp`) reads to talk to this Arkism instead
    /// of a server: `agents.json` and the owner token beside it, both readable by this user only.
    /// `server: None` hands the agents back to the server connection (mode "server").
    fn write_agent_connection(&self, server: Option<&str>, secrets: Option<&Secrets>, ssh: Option<(&str, Option<u16>, Option<&str>)>) -> Result<(), String> {
        let token_file = self.data.join("owner.token");
        let conn = match (server, secrets) {
            (Some(url), Some(secrets)) => {
                write_private(&token_file, secrets.owner.as_bytes()).map_err(|e| format!("local_agent_file: {e}"))?;
                serde_json::json!({ "mode": "local", "url": url, "token_file": token_file })
            }
            _ => {
                let _ = fs::remove_file(&token_file);
                // The agents reach the server over the same SSH address the app connected with.
                match ssh {
                    Some((host, port, key)) => serde_json::json!({ "mode": "server", "ssh_host": host, "ssh_port": port, "ssh_key": key }),
                    None => serde_json::json!({ "mode": "server" }),
                }
            }
        };
        write_private(&self.data.join("agents.json"), conn.to_string().as_bytes()).map_err(|e| format!("local_agent_file: {e}"))
    }

    /// What the server's timers do, run here for the built-in Arkism: find the agents and their MCP
    /// servers (설정 › 에이전트, MCP), index the PC's agent sessions (세션·사용량), list the skills
    /// (스킬), and process the capture queues of connected agents. Nothing here needs the agents to
    /// be connected except the queues. `tick` counts minutes since the app started.
    /// The bundled `arkism` program, set up to work against this built-in Arkism.
    fn host_command(&self, args: &[&str]) -> Option<Command> {
        let secrets = self.secrets().ok()?;
        let token_file = self.data.join("owner.token");
        let client = self.agent_client()?;
        if !token_file.is_file() {
            return None;
        }
        let state = self.data.join("host");
        let mut cmd = Command::new(client);
        cmd.args(args)
            .env("ARKISM_URL", format!("http://127.0.0.1:{}", secrets.api_port))
            .env("ARKISM_TOKEN_FILE", &token_file)
            .env("ARKISM_PC_LOCAL", "1")
            .env("ARKISM_AGENTS_STATE", state.join("agents"))
            .env("ARKISM_DATA_DIR", state.join("data"))
            .env("ARKISM_SKILL_CACHE", state.join("skill-cache"))
            .env("ARKISM_USAGE_STATE", state.join("usage").join("state.json"));
        let roles = self.model_roles();
        cmd.env("ARKISM_CAPTURE_MODEL", &roles.extract_model)
            .env("ARKISM_REFLECT_MODEL", &roles.reflect_model)
            .env("ARKISM_REVIEW_MODEL", &roles.reflect_model)
            .env("ARKISM_MAINTAIN_MODEL", &roles.reflect_model);
        for (k, v) in roles.root_env() {
            cmd.env(k, v);
        }
        Some(cmd)
    }

    /// 설정 › 고급 › 데이터 › 에이전트 기억 가져오기: CLAUDE.md, Claude Code's memory files, AGENTS.md and
    /// GEMINI.md become review candidates (`arkism capture import-agents`). `dry_run` only counts.
    pub fn import_agent_memories(&self, dry_run: bool) -> Result<serde_json::Value, String> {
        let mut args = vec!["capture", "import-agents"];
        if dry_run {
            args.push("--dry-run");
        }
        let mut cmd = self.host_command(&args).ok_or("local_unavailable")?;
        cmd.env("ARKISM_REPO_TAGS", "1").stdin(Stdio::null()).stderr(Stdio::null());
        no_window(&mut cmd);
        let out = cmd.output().map_err(|e| e.to_string())?;
        let text = String::from_utf8_lossy(&out.stdout);
        let last = text.lines().rev().find(|l| l.trim_start().starts_with('{')).unwrap_or("");
        serde_json::from_str(last).map_err(|_| "import_failed".to_string())
    }

    /// 설정 › 고급 › 진단: `arkism doctor --json` (with `--fix`: reconnect agents, retry failed captures).
    pub fn doctor(&self, fix: bool) -> Result<serde_json::Value, String> {
        let mut args = vec!["doctor", "--json"];
        if fix {
            args.push("--fix");
        }
        let client = self.agent_client().ok_or("local_unavailable")?;
        let mut cmd = Command::new(client);
        cmd.args(&args).stdin(Stdio::null()).stderr(Stdio::null());
        no_window(&mut cmd);
        let out = cmd.output().map_err(|e| e.to_string())?;
        serde_json::from_slice(&out.stdout).map_err(|_| "doctor_failed".to_string())
    }

    /// The model settings refresh button: read every account's usage now and report it.
    pub fn refresh_providers(&self) -> Result<(), String> {
        let mut cmd = self.host_command(&["crew", "providers", "report", "--fresh"]).ok_or("local_unavailable")?;
        match quiet(&mut cmd).status() {
            Ok(s) if s.success() => Ok(()),
            _ => Err("providers_refresh_failed".into()),
        }
    }

    pub fn background_work(&self, tick: u64) {
        if self.host_command(&[]).is_none() {
            return;
        }
        let command = |args: &[&str]| self.host_command(args).expect("checked above");
        let run = |args: &[&str], extra: &[(&str, PathBuf)]| {
            let mut cmd = command(args);
            for (k, v) in extra {
                cmd.env(k, v);
            }
            let _ = quiet(&mut cmd).status();
        };
        let _ = self.ensure_gateway();
        // Keep the provider watcher running (restart it if it stopped).
        if let Ok(mut watcher) = self.watcher.lock() {
            let alive = watcher.as_mut().is_some_and(|c| matches!(c.try_wait(), Ok(None)));
            if !alive {
                let mut cmd = command(&["crew", "providers", "watch"]);
                *watcher = quiet(&mut cmd).spawn().ok();
            }
        }
        run(&["agents", "sync"], &[]);
        run(&["usage-index", "--execute"], &[]);
        // Skills change rarely and a scan reads every SKILL.md: at start, then every ten minutes.
        if tick % 10 == 0 {
            run(&["sync-skills", "--execute", "--limit", "5000"], &[]);
        }
        if tick % 60 == 5 {
            let _ = self.expire();
            // Skill quality and security scores, when the two free scanners are installed
            // (`uv tool install`); without them this does nothing.
            run(&["skill-checks", "scan"], &[]);
        }
        // What the server's reflect and maintain timers do. Both ask the user's own Claude CLI, the
        // same one capture uses, so they run rarely: lessons from recent sessions every three hours,
        // cleanup suggestions (merge, retire, fix) once a day.
        if tick % 180 == 30 {
            run(&["capture", "reflect"], &[]);
        }
        if tick % 1440 == 90 {
            run(&["maintain", "run"], &[]);
        }
        // Topic and user summaries from approved memories, proposed as candidates for a person to
        // approve; the command itself waits when the Claude login is near its usage limit.
        if tick % 1440 == 120 {
            run(&["digests", "run"], &[]);
        }
        if tick % 60 == 15 {
            let _ = self.auto_backup();
        }
        let Some(home) = &self.home else { return };
        // Gemini CLI, Grok and DeepSeek CLI have no hooks connected: their chat logs are read instead.
        for (origin, folder) in [("gemini", home.join(".gemini").join("tmp")), ("grok", home.join(".grok").join("sessions")), ("deepseek", home.join(".dsh").join("sessions"))] {
            if folder.is_dir() {
                let queue = home.join(".config").join("arkism").join(format!("local-capture-{origin}"));
                run(&["capture", "logs"], &[("ARKISM_ORIGIN", PathBuf::from(origin)), ("ARKISM_CAPTURE_STATE", queue)]);
            }
        }
        for origin in ["claude_code", "codex", "zcode", "gemini", "grok", "deepseek"] {
            let queue = home.join(".config").join("arkism").join(format!("local-capture-{origin}"));
            if !queue.join("spool").is_dir() {
                continue;
            }
            let origin_path = PathBuf::from(origin);
            run(&["capture", "drain"], &[("ARKISM_ORIGIN", origin_path), ("ARKISM_CLIENT", PathBuf::from("pc")), ("ARKISM_REPO_TAGS", PathBuf::from("1")), ("ARKISM_CAPTURE_STATE", queue)]);
        }
    }

    /// What the server's expiry timers do (hourly here): archived transcripts past their keeping
    /// time, and memory candidates nobody reviewed for 14 days.
    pub fn expire(&self) -> Result<(), String> {
        let secrets = self.secrets()?;
        let state = self.data.join("host").join("session-archive").join("state.json");
        let mut archive = self.host_command(&["session-archive", "expire", "--execute"]).ok_or("local_unavailable")?;
        archive.env("ARKISM_SESSION_STATE", state);
        run(quiet(&mut archive), "local_expire_archive_failed")?;
        let url = format!("http://127.0.0.1:{}", secrets.api_port);
        let mut candidates = self
            .host_command(&["expire-candidates", "--url", &url, "--workspace-id", "default", "--project-id", "default",
                "--user-id", "management:owner", "--retention-days", "14", "--batch-size", "50", "--max-batches", "2", "--execute"])
            .ok_or("local_unavailable")?;
        candidates.env("MEMORY_API_TOKEN", &secrets.worker);
        run(quiet(&mut candidates), "local_expire_candidates_failed")
    }

    /// How the built-in Arkism searches: "ollama:<model>" (meaning and keywords) or "none" (keywords).
    pub fn search_mode(&self) -> String {
        self.secrets().map(|s| if s.embedder.is_empty() { "none".into() } else { s.embedder }).unwrap_or_else(|_| "none".into())
    }

    pub fn model_roles(&self) -> ModelRoles {
        fs::read_to_string(self.data.join("models.json")).ok().and_then(|t| serde_json::from_str(&t).ok()).unwrap_or_default()
    }

    /// Saves the roles and restarts the server so the embedder and judge take effect (a new embedder
    /// re-indexes once). The token is never sent back to the page: the UI only learns whether one is set.
    pub fn set_model_roles(&self, mut roles: ModelRoles) -> Result<(), String> {
        let saved = self.model_roles(); // an empty secret field keeps the saved one
        for (field, old) in [(&mut roles.cloudflare_token, saved.cloudflare_token), (&mut roles.jev_key, saved.jev_key), (&mut roles.picker_key, saved.picker_key)] {
            if field.is_empty() {
                *field = old;
            }
        }
        if !roles.valid() {
            return Err("model_roles_invalid".into());
        }
        let text = serde_json::to_string_pretty(&roles).map_err(|e| e.to_string())?;
        write_private(&self.data.join("models.json"), text.as_bytes()).map_err(|e| e.to_string())?;
        let _turn = self.busy.lock().map_err(|_| "local_busy".to_string())?;
        if self.pgdata().join("PG_VERSION").exists() {
            self.stop_server();
            self.start_locked()?;
        }
        Ok(())
    }

    /// `~/.config/arkism/capture.json`, which the agents' hooks read (capture_exclude.go).
    fn capture_settings_path(&self) -> Result<PathBuf, String> {
        Ok(self.home.as_ref().ok_or("capture_settings_unavailable")?.join(".config").join("arkism").join("capture.json"))
    }

    /// 설정 › 고급 › 수집: paused, and the folders Arkism leaves alone.
    pub fn capture_settings(&self) -> Result<CaptureSettings, String> {
        use std::io::Read;
        let path = self.capture_settings_path()?;
        match fs::metadata(&path) {
            Ok(info) if info.is_file() => (),
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
                // A dangling settings link is unreadable, rather than missing.
                return match fs::symlink_metadata(&path) {
                    Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(CaptureSettings::default()),
                    _ => Err("capture_settings_unavailable".into()),
                };
            }
            _ => return Err("capture_settings_unavailable".into()),
        }
        let file = fs::File::open(&path).map_err(|_| "capture_settings_unavailable")?;
        let mut data = Vec::new();
        file.take((1 << 20) + 1).read_to_end(&mut data).map_err(|_| "capture_settings_unavailable")?;
        if data.len() > 1 << 20 {
            return Err("capture_settings_too_large".into());
        }
        parse_capture_settings(&data)
    }

    pub fn set_capture_settings(&self, settings: &CaptureSettings) -> Result<(), String> {
        let path = self.capture_settings_path()?;
        fs::create_dir_all(path.parent().expect("has a parent")).map_err(|e| e.to_string())?;
        let mut clean = settings.clone();
        clean.exclude = clean.exclude.iter().map(|f| f.trim().to_string()).filter(|f| !f.is_empty()).collect();
        clean.exclude.dedup();
        clean.instructions = clean.instructions.trim().chars().take(2000).collect();
        let text = serde_json::to_string_pretty(&clean).map_err(|e| e.to_string())?;
        write_private(&path, text.as_bytes()).map_err(|e| e.to_string())
    }

    /// The app switched to a server: agents follow it.
    pub fn agents_use_server(&self, ssh_host: &str, ssh_port: Option<u16>, ssh_key: Option<&str>) -> Result<(), String> {
        fs::create_dir_all(&self.data).map_err(|e| e.to_string())?;
        self.write_agent_connection(None, None, Some((ssh_host, ssh_port, ssh_key)))
    }

    /// Stops the server and Postgres. Called when the app quits or before an update.
    pub fn stop(&self) {
        self.stop_server();
        if let (Ok(pg_ctl), true) = (self.pg_bin("pg_ctl"), self.pgdata().join("postmaster.pid").exists()) {
            let _ = quiet(Command::new(pg_ctl).arg("-D").arg(self.pgdata()).args(["stop", "-m", "fast", "-w"])).status();
        }
    }

    /// Stops the server, its worker and the provider watcher; Postgres keeps running.
    fn stop_server(&self) {
        for slot in [&self.watcher, &self.gateway] {
            if let Ok(mut slot) = slot.lock() {
                if let Some(mut child) = slot.take() {
                    let _ = child.kill();
                    let _ = child.wait();
                }
            }
        }
        if let Ok(mut children) = self.children.lock() {
            for child in children.iter_mut() {
                let _ = child.kill();
                let _ = child.wait();
            }
            children.clear();
        }
    }

    fn secrets(&self) -> Result<Secrets, String> {
        let path = self.data.join("runtime.json");
        if let Ok(text) = fs::read_to_string(&path) {
            let mut secrets: Secrets = serde_json::from_str(&text).map_err(|_| "local_settings_damaged".to_string())?;
            if secrets.archive_key.is_empty() {
                secrets.archive_key = secret()?;
                self.save(&secrets)?;
            }
            return Ok(secrets);
        }
        let secrets = Secrets {
            admin: secret()?,
            init: secret()?,
            app: secret()?,
            owner: secret()?,
            worker: secret()?,
            pg_port: free_port(PG_PORT)?,
            api_port: free_port(API_PORT)?,
            registered: false,
            archive_key: secret()?,
            embedder: String::new(),
            gateway_port: 0,
        };
        self.save(&secrets)?;
        Ok(secrets)
    }

    fn save(&self, secrets: &Secrets) -> Result<(), String> {
        let path = self.data.join("runtime.json");
        let text = serde_json::to_string_pretty(secrets).map_err(|e| e.to_string())?;
        write_private(&path, text.as_bytes()).map_err(|e| format!("local_settings_write: {e}"))
    }

    fn start_postgres(&self, secrets: &mut Secrets) -> Result<(), String> {
        let pgdata = self.pgdata();
        if !pgdata.join("PG_VERSION").exists() {
            if pgdata.exists() {
                // A first run that stopped half way left an unusable folder.
                fs::remove_dir_all(&pgdata).map_err(|e| format!("local_database_reset: {e}"))?;
            }
            let pwfile = self.data.join("initdb.pw");
            write_private(&pwfile, secrets.admin.as_bytes()).map_err(|e| e.to_string())?;
            let status = quiet(Command::new(self.pg_bin("initdb")?)
                .arg("-D").arg(&pgdata)
                .args(["-U", "postgres", "-E", "UTF8", "--locale=C", "--auth=scram-sha-256"])
                .arg(format!("--pwfile={}", pwfile.display())))
                .status();
            let _ = fs::remove_file(&pwfile);
            if !status.map(|s| s.success()).unwrap_or(false) {
                return Err("local_database_create_failed".into());
            }
            // A new database has no tokens yet, whatever an earlier run recorded.
            secrets.registered = false;
            self.save(secrets)?;
        }
        let pg_ctl = self.pg_bin("pg_ctl")?;
        let running = quiet(Command::new(&pg_ctl).arg("-D").arg(&pgdata).arg("status")).status().map(|s| s.success()).unwrap_or(false);
        if running {
            return Ok(());
        }
        if !port_free(secrets.pg_port) {
            secrets.pg_port = free_port(PG_PORT)?;
            self.save(secrets)?;
        }
        // Erasing removes the logs folder; a restore starts Postgres before anything else makes it.
        fs::create_dir_all(self.data.join("logs")).map_err(|e| format!("local_data_folder: {e}"))?;
        let log = self.data.join("logs").join("postgres.log");
        let ok = quiet(Command::new(&pg_ctl)
            .arg("-D").arg(&pgdata)
            .arg("-l").arg(&log)
            .arg("-o").arg(format!("-p {} -c listen_addresses=127.0.0.1", secrets.pg_port))
            .args(["-w", "-t", "60", "start"]))
            .status()
            .map(|s| s.success())
            .unwrap_or(false);
        if ok { Ok(()) } else { Err("local_database_start_failed".into()) }
    }

    fn psql(&self, secrets: &Secrets, database: &str) -> Result<Command, String> {
        let mut cmd = Command::new(self.pg_bin("psql")?);
        cmd.args(["--no-psqlrc", "-q", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-U", "postgres", "-d", database])
            .arg("-p").arg(secrets.pg_port.to_string())
            .env("PGPASSWORD", &secrets.admin);
        quiet(&mut cmd);
        Ok(cmd)
    }

    /// Database, roles and schema. `--init-db` runs on every start so an updated app brings its
    /// schema changes; it only adds what is missing.
    fn prepare_database(&self, secrets: &mut Secrets) -> Result<(), String> {
        let exists = self.psql(secrets, "postgres")?
            .args(["-tA", "-c", &format!("SELECT 1 FROM pg_database WHERE datname = '{DATABASE}'")])
            .stdout(Stdio::piped())
            .output()
            .map_err(|e| e.to_string())?;
        if String::from_utf8_lossy(&exists.stdout).trim() != "1" {
            run(self.psql(secrets, "postgres")?.args(["-c", &format!("CREATE DATABASE {DATABASE}")]), "local_database_create_failed")?;
        }
        let roles = self.programs()?.join("roles.sql");
        run(self.psql(secrets, DATABASE)?
            .arg("-v").arg(format!("init_pw={}", secrets.init))
            .arg("-v").arg(format!("app_pw={}", secrets.app))
            .arg("-f").arg(&roles), "local_roles_failed")?;
        let init_url = self.url("arkism_init", &secrets.init, secrets.pg_port);
        run(quiet(Command::new(self.programs()?.join(exe("memoryd")))
            .arg("--init-db")
            .env("MEMORY_STORE", "postgres")
            .env("MEMORY_DATABASE_URL", &init_url)), "local_schema_failed")?;
        if !secrets.registered {
            for (token, principal, role) in [(&secrets.owner, "management:owner", "human"), (&secrets.worker, "worker", "service")] {
                run(quiet(Command::new(self.programs()?.join(exe("memory-principal")))
                    .args(["--principal", principal, "--role", role])
                    .args(SCOPE)
                    .args(["--actions", "*"])
                    .env("MEMORY_DATABASE_URL", &init_url)
                    .env("MEMORY_PRINCIPAL_TOKEN", token)), "local_token_failed")?;
            }
            secrets.registered = true;
            self.save(secrets)?;
        }
        Ok(())
    }

    fn url(&self, user: &str, password: &str, port: u16) -> String {
        format!("postgres://{user}:{password}@127.0.0.1:{port}/{DATABASE}?sslmode=disable")
    }

    fn start_server(&self, secrets: &mut Secrets) -> Result<String, String> {
        let server = |port: u16| format!("http://127.0.0.1:{port}");
        let mut children = self.children.lock().map_err(|_| "local_busy".to_string())?;
        children.retain_mut(|c| matches!(c.try_wait(), Ok(None)));
        if children.len() == 2 && ready(&server(secrets.api_port)) {
            return Ok(server(secrets.api_port));
        }
        for child in children.iter_mut() {
            let _ = child.kill();
            let _ = child.wait();
        }
        children.clear();
        if !port_free(secrets.api_port) {
            secrets.api_port = free_port(API_PORT)?;
            self.save(secrets)?;
        }
        let app_url = self.url("arkism_app", &secrets.app, secrets.pg_port);
        let logs = self.data.join("logs");
        // Meaning search when the user has Ollama with the embedding model (`ollama pull
        // qwen3-embedding:0.6b`); otherwise keyword search. Switching re-queues every record once.
        let roles = self.model_roles();
        let use_ollama = match roles.embedder.as_str() {
            "none" => false,
            "ollama" => true,
            _ => ollama_ready(&roles.embed_model),
        };
        let embedder = if use_ollama { format!("ollama:{}", roles.embed_model) } else { "none".to_string() };
        let mut embed_env: Vec<(&str, String)> = if embedder == "none" {
            vec![("MEMORY_EMBEDDER", "none".into())]
        } else {
            vec![("MEMORY_EMBEDDER", "ollama".into()), ("OLLAMA_ENDPOINT_URL", OLLAMA_URL.into()), ("OLLAMA_MODEL", roles.embed_model.clone())]
        };
        // The judge that picks a model for 라우팅 (and skills): Clef on Cloudflare Workers AI, Jev, or a
        // chat model the user already has (Ollama, OpenAI…). Without one, tasks go to the fixed or first model.
        match roles.judge.as_str() {
            "clef" if !roles.cloudflare_account.is_empty() && !roles.cloudflare_token.is_empty() => {
                embed_env.push(("CLOUDFLARE_ACCOUNT_ID", roles.cloudflare_account.clone()));
                embed_env.push(("CLOUDFLARE_AI_TOKEN", roles.cloudflare_token.clone()));
            }
            "jev" if !roles.jev_key.is_empty() => embed_env.push(("TYPESAFE_API_KEY", roles.jev_key.clone())),
            "chat" if !roles.picker_url.is_empty() && !roles.picker_model.is_empty() => {
                embed_env.push(("ARKISM_ROUTING_PICKER_URL", roles.picker_url.clone()));
                embed_env.push(("ARKISM_ROUTING_PICKER_MODEL", roles.picker_model.clone()));
                if !roles.picker_key.is_empty() {
                    embed_env.push(("ARKISM_ROUTING_PICKER_KEY", roles.picker_key.clone()));
                }
            }
            _ => {}
        }
        if embedder != "none" && secrets.embedder != embedder {
            let init_url = self.url("arkism_init", &secrets.init, secrets.pg_port);
            let mut reindex = Command::new(self.programs()?.join(exe("memoryd")));
            reindex.arg("-reindex").env("MEMORY_STORE", "postgres").env("MEMORY_DATABASE_URL", &init_url);
            for (k, v) in &embed_env {
                reindex.env(k, v);
            }
            if run(quiet(&mut reindex), "local_reindex_failed").is_ok() {
                secrets.embedder = embedder.clone();
                self.save(secrets)?;
            }
        } else if embedder == "none" && !secrets.embedder.is_empty() && secrets.embedder != "none" {
            secrets.embedder = "none".into();
            self.save(secrets)?;
        }
        let spawn = |args: &[&str], log: &str| -> Result<Child, String> {
            let file = fs::OpenOptions::new().create(true).append(true).open(logs.join(log)).map_err(|e| e.to_string())?;
            let err = file.try_clone().map_err(|e| e.to_string())?;
            let mut cmd = Command::new(self.programs()?.join(exe("memoryd")));
            cmd.args(args)
                .env("MEMORY_STORE", "postgres")
                .env("MEMORY_DATABASE_URL", &app_url)
                .envs(embed_env.iter().map(|(k, v)| (*k, v.as_str())))
                .env("MEMORY_WORKER_TOKEN", &secrets.worker)
                .env("MEMORY_CANDIDATE_DAILY_LIMIT", "50")
                .env("MEMORY_LOCAL_STANDALONE", "true")
                // No agent runtime keeps copies outside this database: deletions finish here.
                .env("MEMORY_PRIVACY_NO_AGENT_RUNTIME", "true")
                .env("MEMORY_SESSION_ARCHIVE_KEY", &secrets.archive_key)
                .stdin(Stdio::null())
                .stdout(file)
                .stderr(err);
            no_window(&mut cmd);
            cmd.spawn().map_err(|e| format!("local_server_start_failed: {e}"))
        };
        let listen = format!("127.0.0.1:{}", secrets.api_port);
        children.push(spawn(&["--listen", &listen], "server.log")?);
        children.push(spawn(&["--worker"], "worker.log")?);
        let deadline = Instant::now() + Duration::from_secs(30);
        while Instant::now() < deadline {
            if ready(&server(secrets.api_port)) {
                return Ok(server(secrets.api_port));
            }
            if children.iter_mut().any(|c| !matches!(c.try_wait(), Ok(None))) {
                break;
            }
            std::thread::sleep(Duration::from_millis(300));
        }
        Err("local_server_start_failed".into())
    }

    /// Where backups go: `Documents/Arkism Backups`, outside the app's data so erasing or
    /// uninstalling keeps them.
    pub fn backup_dir() -> Result<PathBuf, String> {
        Ok(home_dir().ok_or("local_backup_folder")?.join("Documents").join("Arkism Backups"))
    }

    /// 설정 › 데이터 › 지금 백업: a folder `arkism-<UTC time>` with the whole database
    /// (`arkism.dump`, pg_dump custom format) and `backup.json`, which holds the tokens the
    /// database knows and the key of the archived transcripts. Both are readable by this user only;
    /// anyone holding the folder can read every memory, the same as the app's data folder.
    pub fn backup(&self) -> Result<Backup, String> {
        self.backup_into(&Self::backup_dir()?)
    }

    /// `backup` into another folder (`--runtime-check`).
    pub fn backup_into(&self, folder: &Path) -> Result<Backup, String> {
        let _turn = self.busy.lock().map_err(|_| "local_busy".to_string())?;
        self.backup_locked(folder, false)
    }

    fn backup_locked(&self, into: &Path, auto: bool) -> Result<Backup, String> {
        self.programs()?;
        let mut secrets = self.secrets()?;
        if !self.pgdata().join("PG_VERSION").exists() {
            return Err("local_backup_nothing".into());
        }
        self.start_postgres(&mut secrets)?;
        let name = format!("arkism-{}", utc_stamp(std::time::SystemTime::now()));
        let folder = into.join(&name);
        fs::create_dir_all(&folder).map_err(|e| format!("local_backup_folder: {e}"))?;
        let dump = folder.join("arkism.dump");
        let ok = quiet(Command::new(self.pg_bin("pg_dump")?)
            .args(["-h", "127.0.0.1", "-U", "postgres", "-Fc", "-d", DATABASE])
            .arg("-p").arg(secrets.pg_port.to_string())
            .arg("-f").arg(&dump)
            .env("PGPASSWORD", &secrets.admin))
            .status()
            .map(|s| s.success())
            .unwrap_or(false);
        if !ok {
            let _ = fs::remove_dir_all(&folder);
            return Err("local_backup_failed".into());
        }
        let info = BackupInfo {
            format: 1,
            app_version: env!("CARGO_PKG_VERSION").to_string(),
            created: name.trim_start_matches("arkism-").to_string(),
            owner: secrets.owner.clone(),
            worker: secrets.worker.clone(),
            archive_key: secrets.archive_key.clone(),
            auto,
        };
        let text = serde_json::to_string_pretty(&info).map_err(|e| e.to_string())?;
        write_private(&folder.join("backup.json"), text.as_bytes()).map_err(|e| format!("local_backup_failed: {e}"))?;
        Backup::read(&folder).ok_or_else(|| "local_backup_failed".into())
    }

    /// Once a day: a backup when the newest one is a day old, keeping the last seven automatic
    /// ones (backups the user made are never removed).
    pub fn auto_backup(&self) -> Result<(), String> {
        const KEEP: usize = 7;
        let dir = Self::backup_dir()?;
        let newest = Self::backups()?.into_iter().next();
        let stale = newest.map_or(true, |b| b.created < utc_stamp(std::time::SystemTime::now() - Duration::from_secs(86_400)));
        if !stale {
            return Ok(());
        }
        {
            let _turn = self.busy.lock().map_err(|_| "local_busy".to_string())?;
            self.backup_locked(&dir, true)?;
        }
        for old in Self::backups()?.into_iter().filter(|b| b.auto).skip(KEEP) {
            let _ = fs::remove_dir_all(&old.path);
        }
        Ok(())
    }

    /// The backups in the backup folder, newest first.
    pub fn backups() -> Result<Vec<Backup>, String> {
        let dir = Self::backup_dir()?;
        let mut found: Vec<Backup> = fs::read_dir(&dir)
            .map(|entries| entries.flatten().filter_map(|e| Backup::read(&e.path())).collect())
            .unwrap_or_default();
        found.sort_by(|a, b| b.name.cmp(&a.name));
        Ok(found)
    }

    /// Replaces everything with a backup. The current data is backed up first (the result names
    /// that safety copy). The database is recreated, the backup loaded as it was, and the app then
    /// uses the backup's tokens and transcript key, so the UI must call `start` again.
    pub fn restore(&self, name: &str) -> Result<Restored, String> {
        self.restore_from(&Self::backup_dir()?, name)
    }

    /// `restore` from another folder (`--runtime-check`).
    pub fn restore_from(&self, dir: &Path, name: &str) -> Result<Restored, String> {
        let _turn = self.busy.lock().map_err(|_| "local_busy".to_string())?;
        let backup = valid_backup_name(name).then(|| Backup::read(&dir.join(name))).flatten().ok_or("local_backup_missing")?;
        let info: BackupInfo = serde_json::from_str(&fs::read_to_string(dir.join(name).join("backup.json")).map_err(|e| e.to_string())?)
            .map_err(|_| "local_backup_damaged".to_string())?;
        if info.format != 1 {
            return Err("local_backup_newer".into());
        }
        let safety = if self.pgdata().join("PG_VERSION").exists() { Some(self.backup_locked(dir, false)?.name) } else { None };
        self.stop_server();
        let mut secrets = self.secrets()?;
        self.start_postgres(&mut secrets)?;
        let admin = |sql: &str| -> Result<(), String> { run(self.psql(&secrets, "postgres")?.args(["-c", sql]), "local_restore_failed") };
        // roles.sql also makes the schemas, which the backup brings itself: run it before (the roles
        // are shared by the whole Postgres and survive the drop) and after (grants and settings of the
        // new database).
        let roles = || -> Result<(), String> {
            run(self.psql(&secrets, DATABASE)?
                .arg("-v").arg(format!("init_pw={}", secrets.init))
                .arg("-v").arg(format!("app_pw={}", secrets.app))
                .arg("-f").arg(self.programs()?.join("roles.sql")), "local_roles_failed")
        };
        let _ = admin(&format!("CREATE DATABASE {DATABASE}"));
        roles()?;
        admin(&format!("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '{DATABASE}' AND pid <> pg_backend_pid()"))?;
        admin(&format!("DROP DATABASE IF EXISTS {DATABASE}"))?;
        admin(&format!("CREATE DATABASE {DATABASE}"))?;
        let ok = quiet(Command::new(self.pg_bin("pg_restore")?)
            .args(["-h", "127.0.0.1", "-U", "postgres", "--exit-on-error", "-d", DATABASE])
            .arg("-p").arg(secrets.pg_port.to_string())
            .arg(dir.join(name).join("arkism.dump"))
            .env("PGPASSWORD", &secrets.admin))
            .status()
            .map(|s| s.success())
            .unwrap_or(false);
        let ok = ok && roles().is_ok();
        if !ok {
            return Err(match safety {
                Some(copy) => format!("local_restore_failed: {copy}"),
                None => "local_restore_failed".into(),
            });
        }
        secrets.owner = info.owner;
        secrets.worker = info.worker;
        secrets.archive_key = info.archive_key;
        secrets.registered = true;
        secrets.embedder.clear(); // the restored records are indexed again for this computer's embedder
        self.save(&secrets)?;
        let started = self.start_locked()?;
        Ok(Restored { backup: backup.name, safety_copy: safety, started })
    }

    /// 설정 › 데이터 › 모든 데이터 지우기: stops everything and deletes the database, the settings
    /// with its tokens and keys, the logs, the indexes of sessions, usage and skills, and the agents'
    /// capture queues that wait to be sent. Backups and the agents' own files stay. The next start
    /// makes a new, empty Arkism.
    pub fn erase(&self) -> Result<(), String> {
        let _turn = self.busy.lock().map_err(|_| "local_busy".to_string())?;
        self.stop();
        for name in ["pg", "host", "logs"] {
            let path = self.data.join(name);
            if path.exists() {
                fs::remove_dir_all(&path).map_err(|e| format!("local_erase_failed: {e}"))?;
            }
        }
        for name in ["runtime.json", "owner.token", "initdb.pw"] {
            let _ = fs::remove_file(self.data.join(name));
        }
        if let Some(home) = &self.home {
            if let Ok(entries) = fs::read_dir(home.join(".config").join("arkism")) {
                for entry in entries.flatten() {
                    if entry.file_name().to_string_lossy().starts_with("local-capture-") {
                        let _ = fs::remove_dir_all(entry.path());
                    }
                }
            }
        }
        Ok(())
    }
}

/// 설정 › 고급 › 역할별 모델: which model does which job in the built-in Arkism (`models.json`).
#[derive(serde::Serialize, serde::Deserialize, Clone, Debug, PartialEq)]
pub struct ModelRoles {
    /// Meaning search embeddings: "auto" (Ollama when it has the model), "ollama" or "none" (keywords only).
    #[serde(default = "default_auto")]
    pub embedder: String,
    #[serde(default = "default_embed_model")]
    pub embed_model: String,
    /// Routing and skill judge: "fixed" (the fixed model), "clef" (Cloudflare Workers AI), "jev"
    /// (TypeSafe Jev) or "chat" (any OpenAI-compatible chat model: Ollama, OpenAI, OpenRouter…).
    #[serde(default = "default_fixed")]
    pub judge: String,
    #[serde(default)]
    pub cloudflare_account: String,
    /// Kept only in this user's data folder (0600), like the other built-in secrets.
    #[serde(default)]
    pub cloudflare_token: String,
    #[serde(default)]
    pub jev_key: String,
    /// "chat": the base URL (https, or http on this computer), the model and its key (empty for Ollama).
    #[serde(default)]
    pub picker_url: String,
    #[serde(default)]
    pub picker_model: String,
    #[serde(default)]
    pub picker_key: String,
    /// The Claude CLI model that extracts memories, and the one that reflects, reviews and suggests cleanups.
    #[serde(default = "default_haiku")]
    pub extract_model: String,
    #[serde(default = "default_sonnet")]
    pub reflect_model: String,
    /// 라우팅 설정 › 스킬·MCP·플러그인 › 폴더 위치: empty means the host's default under its data folder.
    #[serde(default)]
    pub skills_root: String,
    #[serde(default)]
    pub mcp_root: String,
    #[serde(default)]
    pub plugins_root: String,
}

fn default_auto() -> String { "auto".into() }

/// https anywhere, plain http only on this computer (the server refuses anything else too).
fn picker_url_ok(url: &str) -> bool {
    url.len() <= 512
        && !url.chars().any(char::is_whitespace)
        && (url.starts_with("https://") || ["http://127.0.0.1:", "http://localhost:", "http://127.0.0.1/", "http://localhost/"].iter().any(|p| url.starts_with(p)))
}

fn gateway_url(port: u16) -> String {
    format!("http://127.0.0.1:{port}")
}
fn default_embed_model() -> String { OLLAMA_MODEL.into() }
fn default_fixed() -> String { "fixed".into() }
fn default_haiku() -> String { "haiku".into() }
fn default_sonnet() -> String { "sonnet".into() }

impl Default for ModelRoles {
    fn default() -> Self {
        serde_json::from_str("{}").expect("defaults")
    }
}

impl ModelRoles {
    fn valid(&self) -> bool {
        let model = |m: &str| !m.is_empty() && m.len() <= 128 && m.chars().all(|c| c.is_ascii_alphanumeric() || "._:-/".contains(c));
        ["auto", "ollama", "none"].contains(&self.embedder.as_str())
            && model(&self.embed_model)
            && ["fixed", "clef", "jev", "chat"].contains(&self.judge.as_str())
            && self.cloudflare_account.len() <= 128
            && [&self.cloudflare_token, &self.jev_key, &self.picker_key].iter().all(|k| k.len() <= 512 && !k.contains(['\n', '\r']))
            && (self.picker_url.is_empty() || picker_url_ok(&self.picker_url))
            && (self.picker_model.is_empty() || model(&self.picker_model))
            && model(&self.extract_model)
            && model(&self.reflect_model)
            && [&self.skills_root, &self.mcp_root, &self.plugins_root].iter().all(|p| p.is_empty() || root_ok(p))
    }

    /// The folder overrides the host reads (internal/host/client.go); unset ones keep its defaults.
    fn root_env(&self) -> Vec<(&'static str, &str)> {
        [("ARKISM_SKILLS_ROOT", &self.skills_root), ("ARKISM_MCP_ROOT", &self.mcp_root), ("ARKISM_PLUGINS_ROOT", &self.plugins_root)]
            .into_iter()
            .filter(|(_, v)| !v.is_empty())
            .map(|(k, v)| (k, v.as_str()))
            .collect()
    }
}

/// An absolute folder path: `/…`, `C:\…` or `C:/…`, one line, not absurdly long.
fn root_ok(p: &str) -> bool {
    let b = p.as_bytes();
    p.len() <= 1024
        && !p.contains(['\n', '\r', '\0'])
        && (p.starts_with('/') || (b.len() >= 3 && b[0].is_ascii_alphabetic() && b[1] == b':' && (b[2] == b'\\' || b[2] == b'/')))
}

// Read the same known field types as the host, including case-insensitive field
// names, without replacing malformed or null values with an enabled default.
fn parse_capture_settings(data: &[u8]) -> Result<CaptureSettings, String> {
    struct SettingsReader;
    impl<'de> serde::de::Visitor<'de> for SettingsReader {
        type Value = CaptureSettings;
        fn expecting(&self, formatter: &mut std::fmt::Formatter) -> std::fmt::Result {
            formatter.write_str("a capture settings object")
        }
        fn visit_map<A>(self, mut fields: A) -> Result<Self::Value, A::Error>
        where A: serde::de::MapAccess<'de> {
            let mut settings = CaptureSettings::default();
            while let Some(key) = fields.next_key::<String>()? {
                match key.to_ascii_lowercase().as_str() {
                    "paused" => settings.paused = fields.next_value::<bool>()?,
                    "exclude" => settings.exclude = fields.next_value::<Vec<String>>()?,
                    "instructions" => settings.instructions = fields.next_value::<String>()?,
                    _ => { fields.next_value::<serde::de::IgnoredAny>()?; }
                }
            }
            Ok(settings)
        }
    }
    let mut decoder = serde_json::Deserializer::from_slice(data);
    let settings = serde::de::Deserializer::deserialize_map(&mut decoder, SettingsReader)
        .map_err(|_| "capture_settings_invalid")?;
    decoder.end().map_err(|_| "capture_settings_invalid")?;
    Ok(settings)
}

#[derive(serde::Serialize, serde::Deserialize, Clone, Default, Debug, PartialEq)]
pub struct CaptureSettings {
    #[serde(default)]
    pub paused: bool,
    #[serde(default)]
    pub exclude: Vec<String>,
    /// The user's note on what to remember or skip (extraction reads at most 2000 characters).
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub instructions: String,
}

#[derive(serde::Serialize, serde::Deserialize)]
struct BackupInfo {
    format: u32,
    app_version: String,
    created: String,
    owner: String,
    worker: String,
    archive_key: String,
    /// Made by the daily automatic backup (only those are pruned).
    #[serde(default)]
    auto: bool,
}

/// One backup as the UI lists it.
#[derive(serde::Serialize, Clone, Debug)]
pub struct Backup {
    pub name: String,
    pub path: PathBuf,
    /// UTC, `YYYYMMDD-HHMMSS`.
    pub created: String,
    pub app_version: String,
    pub bytes: u64,
    pub auto: bool,
}

impl Backup {
    fn read(folder: &Path) -> Option<Backup> {
        let name = folder.file_name()?.to_string_lossy().into_owned();
        if !valid_backup_name(&name) {
            return None;
        }
        let info: serde_json::Value = serde_json::from_str(&fs::read_to_string(folder.join("backup.json")).ok()?).ok()?;
        let bytes = fs::metadata(folder.join("arkism.dump")).ok()?.len();
        Some(Backup {
            name,
            path: folder.to_path_buf(),
            created: info["created"].as_str().unwrap_or_default().to_string(),
            app_version: info["app_version"].as_str().unwrap_or_default().to_string(),
            bytes,
            auto: info["auto"].as_bool().unwrap_or(false),
        })
    }
}

#[derive(serde::Serialize)]
pub struct Restored {
    pub backup: String,
    /// The backup made of the data that was replaced.
    pub safety_copy: Option<String>,
    pub started: Started,
}

/// `arkism-YYYYMMDD-HHMMSS`: also keeps a name from the UI inside the backup folder.
fn valid_backup_name(name: &str) -> bool {
    let Some(stamp) = name.strip_prefix("arkism-") else { return false };
    stamp.len() == 15 && stamp.bytes().enumerate().all(|(i, b)| if i == 8 { b == b'-' } else { b.is_ascii_digit() })
}

/// UTC `YYYYMMDD-HHMMSS` without a date library (days to civil date, Howard Hinnant's method).
fn utc_stamp(t: std::time::SystemTime) -> String {
    let secs = t.duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0) as i64;
    let (days, rem) = (secs.div_euclid(86_400), secs.rem_euclid(86_400));
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = yoe + era * 400 + if m <= 2 { 1 } else { 0 };
    format!("{y:04}{m:02}{d:02}-{:02}{:02}{:02}", rem / 3600, rem % 3600 / 60, rem % 60)
}

/// The `arkism` agent client that ships with the app: `integration/` beside the app on Windows,
/// `runtime/integration/` among the bundled programs on macOS and Linux.
fn find_agent_client(programs: Option<&Path>) -> Option<PathBuf> {
    let dir = std::env::current_exe().ok()?.parent()?.to_path_buf();
    let mut places = vec![dir.join("integration").join(exe("arkism"))];
    if let Some(programs) = programs {
        places.push(programs.join("integration").join(exe("arkism")));
    }
    places.into_iter().find(|p| p.is_file())
}

fn home_dir() -> Option<PathBuf> {
    std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" }).map(PathBuf::from)
}

fn exe(name: &str) -> String {
    if cfg!(windows) { format!("{name}.exe") } else { name.to_string() }
}

/// No console window, and nothing inherited: Postgres outlives pg_ctl and must not hold our handles.
fn quiet(cmd: &mut Command) -> &mut Command {
    cmd.stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null());
    no_window(cmd);
    cmd
}

#[cfg(windows)]
fn no_window(cmd: &mut Command) {
    use std::os::windows::process::CommandExt;
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    cmd.creation_flags(CREATE_NO_WINDOW);
}

#[cfg(not(windows))]
fn no_window(_: &mut Command) {}

fn run(cmd: &mut Command, error: &str) -> Result<(), String> {
    match cmd.status() {
        Ok(s) if s.success() => Ok(()),
        _ => Err(error.to_string()),
    }
}

/// An HTTP client whose whole request, body included, must finish within `timeout`.
pub fn http_agent(timeout: Duration) -> ureq::Agent {
    ureq::Agent::config_builder().timeout_global(Some(timeout)).build().into()
}

fn ready(server: &str) -> bool {
    http_agent(Duration::from_secs(2))
        .get(&format!("{server}/readyz"))
        .call()
        .is_ok()
}

const OLLAMA_URL: &str = "http://127.0.0.1:11434";
const OLLAMA_MODEL: &str = "qwen3-embedding:0.6b";

/// Ollama answers on this computer and has this embedding model.
fn ollama_ready(model: &str) -> bool {
    let reply = http_agent(Duration::from_secs(2)).get(&format!("{OLLAMA_URL}/api/tags")).call();
    let Ok(mut reply) = reply else { return false };
    let Ok(tags) = reply.body_mut().read_json::<serde_json::Value>() else { return false };
    tags["models"].as_array().is_some_and(|models| {
        models.iter().any(|m| m["name"].as_str().or(m["model"].as_str()).is_some_and(|n| n == model || n == format!("{model}:latest")))
    })
}

fn port_free(port: u16) -> bool {
    TcpListener::bind(("127.0.0.1", port)).is_ok()
}

/// The preferred port when it is free, otherwise any free one.
fn free_port(preferred: u16) -> Result<u16, String> {
    if port_free(preferred) {
        return Ok(preferred);
    }
    TcpListener::bind(("127.0.0.1", 0))
        .and_then(|l| l.local_addr())
        .map(|a| a.port())
        .map_err(|e| format!("local_no_port: {e}"))
}

fn secret() -> Result<String, String> {
    let mut buf = [0u8; 32];
    getrandom::fill(&mut buf).map_err(|e| e.to_string())?;
    Ok(buf.iter().map(|b| format!("{b:02x}")).collect())
}

fn write_private(path: &Path, data: &[u8]) -> std::io::Result<()> {
    let tmp = path.with_extension("tmp");
    {
        let mut options = fs::OpenOptions::new();
        options.write(true).create(true).truncate(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        use std::io::Write;
        let mut file = options.open(&tmp)?;
        file.write_all(data)?;
        file.sync_all()?;
    }
    fs::rename(&tmp, path)
}

/// Where the bundled programs are: `runtime/` beside the app (Windows installer), or in the
/// app's resources (macOS and Linux bundles). None for a build without them.
pub fn find_programs(resources: Option<PathBuf>) -> Option<PathBuf> {
    let beside = std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.join("runtime")));
    [beside, resources.map(|r| r.join("runtime"))]
        .into_iter()
        .flatten()
        .find(|dir| dir.join(exe("memoryd")).is_file() && dir.join("pgsql").join("bin").join(exe("postgres")).is_file())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn secrets_are_long_and_different() {
        let (a, b) = (secret().unwrap(), secret().unwrap());
        assert_eq!(a.len(), 64);
        assert_ne!(a, b);
    }

    #[test]
    fn a_taken_port_is_replaced() {
        let held = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let port = held.local_addr().unwrap().port();
        assert!(!port_free(port));
        assert_ne!(free_port(port).unwrap(), port);
    }

    #[test]
    fn settings_file_round_trips_privately() {
        let dir = std::env::temp_dir().join(format!("arkism-runtime-test-{}", secret().unwrap()));
        fs::create_dir_all(&dir).unwrap();
        let rt = Runtime::new(None, dir.clone());
        assert!(!rt.available());
        assert_eq!(rt.start().err().as_deref(), Some("local_unavailable"));
        let first = rt.secrets().unwrap();
        let again = rt.secrets().unwrap();
        assert_eq!(first.owner, again.owner);
        assert_eq!(first.archive_key.len(), 64);
        assert_eq!(first.archive_key, again.archive_key);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(fs::metadata(dir.join("runtime.json")).unwrap().permissions().mode() & 0o777, 0o600);
        }
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn backup_names_are_stamps_only() {
        let t = std::time::UNIX_EPOCH + Duration::from_secs(1_791_835_481);
        let name = format!("arkism-{}", utc_stamp(t));
        assert_eq!(name, "arkism-20261012-200441");
        assert!(valid_backup_name(&name));
        assert_eq!(utc_stamp(std::time::UNIX_EPOCH + Duration::from_secs(951_782_400)), "20000229-000000");
        for bad in ["arkism-../x", "arkism-2026101-1624411", "x-20261012-162441", "arkism-20261012_162441", ""] {
            assert!(!valid_backup_name(bad), "{bad}");
        }
    }

    #[test]
    fn a_backup_folder_is_listed_with_its_details() {
        let dir = std::env::temp_dir().join(format!("arkism-backup-test-{}", secret().unwrap()));
        let folder = dir.join("arkism-20261013-010203");
        fs::create_dir_all(&folder).unwrap();
        fs::write(folder.join("arkism.dump"), b"12345").unwrap();
        fs::write(folder.join("backup.json"), br#"{"format":1,"app_version":"0.2.6","created":"20261013-010203","owner":"o","worker":"w","archive_key":"k"}"#).unwrap();
        fs::create_dir_all(dir.join("not-a-backup")).unwrap();
        let b = Backup::read(&folder).unwrap();
        assert_eq!((b.bytes, b.app_version.as_str(), b.created.as_str()), (5, "0.2.6", "20261013-010203"));
        assert!(Backup::read(&dir.join("not-a-backup")).is_none());
        fs::remove_file(folder.join("arkism.dump")).unwrap();
        assert!(Backup::read(&folder).is_none(), "a backup without its database is not offered");
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn erase_without_programs_removes_settings() {
        let dir = std::env::temp_dir().join(format!("arkism-erase-test-{}", secret().unwrap()));
        fs::create_dir_all(dir.join("host").join("agents")).unwrap();
        let mut rt = Runtime::new(None, dir.clone());
        rt.home = Some(dir.join("home"));
        let queue = dir.join("home").join(".config").join("arkism").join("local-capture-codex").join("spool");
        fs::create_dir_all(&queue).unwrap();
        fs::create_dir_all(dir.join("home").join(".config").join("arkism").join("backups")).unwrap();
        rt.secrets().unwrap();
        fs::write(dir.join("owner.token"), b"t").unwrap();
        fs::write(dir.join("keep.txt"), b"x").unwrap();
        rt.erase().unwrap();
        assert!(!dir.join("runtime.json").exists() && !dir.join("owner.token").exists() && !dir.join("host").exists());
        assert!(dir.join("keep.txt").exists());
        assert!(!queue.exists(), "waiting captures are deleted");
        assert!(dir.join("home").join(".config").join("arkism").join("backups").exists());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn capture_settings_round_trip_in_the_agents_folder() {
        let dir = std::env::temp_dir().join(format!("arkism-capture-test-{}", secret().unwrap()));
        let mut rt = Runtime::new(None, dir.join("data"));
        rt.home = Some(dir.join("home"));
        assert_eq!(rt.capture_settings().unwrap(), CaptureSettings::default());
        rt.set_capture_settings(&CaptureSettings { paused: true, exclude: vec![" /a/b ".into(), "".into(), "/c".into()], instructions: " 회의록은 빼 줘 ".into() }).unwrap();
        let read = rt.capture_settings().unwrap();
        assert!(read.paused);
        assert_eq!(read.exclude, vec!["/a/b".to_string(), "/c".to_string()]);
        assert_eq!(read.instructions, "회의록은 빼 줘");
        assert!(dir.join("home/.config/arkism/capture.json").is_file());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn capture_settings_empty_writer_stays_enabled_and_uses_an_array() {
        let dir = std::env::temp_dir().join(format!("arkism-capture-empty-test-{}", secret().unwrap()));
        let mut rt = Runtime::new(None, dir.join("data"));
        rt.home = Some(dir.join("home"));
        rt.set_capture_settings(&CaptureSettings::default()).unwrap();
        let raw = fs::read(rt.capture_settings_path().unwrap()).unwrap();
        let value: serde_json::Value = serde_json::from_slice(&raw).unwrap();
        assert_eq!(value["exclude"], serde_json::json!([]));
        assert_eq!(rt.capture_settings().unwrap(), CaptureSettings::default());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn capture_settings_refuse_uncheckable_files_instead_of_enabled_defaults() {
        let dir = std::env::temp_dir().join(format!("arkism-capture-invalid-test-{}", secret().unwrap()));
        let mut rt = Runtime::new(None, dir.join("data"));
        rt.home = Some(dir.join("home"));
        assert_eq!(rt.capture_settings().unwrap(), CaptureSettings::default());
        let path = rt.capture_settings_path().unwrap();
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        for text in ["", "null", "[]", "{", r#"{"paused":null}"#, r#"{"Paused":null}"#,
            r#"{"paused":"false"}"#, r#"{"exclude":null}"#, r#"{"exclude":[null]}"#,
            r#"{"instructions":null}"#, r#"{"instructions":3}"#, r#"{} {}"#] {
            fs::write(&path, text).unwrap();
            assert_eq!(rt.capture_settings().unwrap_err(), "capture_settings_invalid");
        }
        fs::write(&path, " ".repeat((1 << 20) + 1)).unwrap();
        assert_eq!(rt.capture_settings().unwrap_err(), "capture_settings_too_large");
        fs::write(&path, format!("{{}}{}", " ".repeat((1 << 20) - 2))).unwrap();
        assert_eq!(rt.capture_settings().unwrap(), CaptureSettings::default());
        fs::write(&path, r#"{"future":{"paused":null}}"#).unwrap();
        assert_eq!(rt.capture_settings().unwrap(), CaptureSettings::default());
        fs::write(&path, r#"{"Paused":true,"Exclude":["/synthetic/project"],"Instructions":"public note"}"#).unwrap();
        assert!(rt.capture_settings().unwrap().paused);
        fs::remove_file(&path).unwrap();
        fs::create_dir(&path).unwrap();
        assert_eq!(rt.capture_settings().unwrap_err(), "capture_settings_unavailable");
        fs::remove_dir(&path).unwrap();
        #[cfg(unix)] {
            std::os::unix::fs::symlink(dir.join("missing"), &path).unwrap();
            assert_eq!(rt.capture_settings().unwrap_err(), "capture_settings_unavailable");
            fs::remove_file(&path).unwrap();
        }
        assert_eq!(rt.capture_settings().unwrap(), CaptureSettings::default());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn model_roles_default_validate_and_keep_the_token() {
        let dir = std::env::temp_dir().join(format!("arkism-roles-test-{}", secret().unwrap()));
        fs::create_dir_all(&dir).unwrap();
        let rt = Runtime::new(None, dir.clone());
        let d = rt.model_roles();
        assert_eq!((d.embedder.as_str(), d.judge.as_str(), d.extract_model.as_str(), d.reflect_model.as_str()), ("auto", "fixed", "haiku", "sonnet"));
        let mut r = d.clone();
        r.judge = "clef".into();
        r.cloudflare_account = "acc".into();
        r.cloudflare_token = "tok".into();
        rt.set_model_roles(r).unwrap();
        let mut keep = rt.model_roles();
        keep.cloudflare_token.clear();
        rt.set_model_roles(keep).unwrap();
        assert_eq!(rt.model_roles().cloudflare_token, "tok");
        let mut bad = rt.model_roles();
        bad.extract_model = "haiku; rm".into();
        assert!(rt.set_model_roles(bad).is_err());
        bad = rt.model_roles();
        bad.embedder = "gpu".into();
        assert!(rt.set_model_roles(bad).is_err());
        bad = rt.model_roles();
        bad.mcp_root = "relative/mcp".into();
        assert!(rt.set_model_roles(bad).is_err());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn folder_roots_become_host_env_only_when_set() {
        let mut r = ModelRoles::default();
        assert!(r.root_env().is_empty());
        r.skills_root = "/srv/skills".into();
        r.plugins_root = "D:\\ark\\plugins".into();
        assert!(r.valid());
        assert_eq!(r.root_env(), vec![("ARKISM_SKILLS_ROOT", "/srv/skills"), ("ARKISM_PLUGINS_ROOT", "D:\\ark\\plugins")]);
    }

    /// 저장 → models.json → the next `arkism` run gets the folders (and loses them once cleared).
    #[cfg(unix)]
    #[test]
    fn saved_folders_reach_the_next_host_run() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!("arkism-roots-test-{}", secret().unwrap()));
        let client = dir.join("programs").join("integration").join("arkism");
        fs::create_dir_all(client.parent().unwrap()).unwrap();
        fs::write(&client, "#!/bin/sh\necho \"skills=$ARKISM_SKILLS_ROOT mcp=$ARKISM_MCP_ROOT plugins=$ARKISM_PLUGINS_ROOT\"\n").unwrap();
        fs::set_permissions(&client, fs::Permissions::from_mode(0o755)).unwrap();
        let rt = Runtime::new(Some(dir.join("programs")), dir.join("data"));
        fs::create_dir_all(dir.join("data")).unwrap();
        rt.secrets().unwrap();
        fs::write(dir.join("data").join("owner.token"), b"t").unwrap();
        let run = || {
            let out = rt.host_command(&[]).unwrap().output().unwrap();
            String::from_utf8(out.stdout).unwrap()
        };
        let mut roles = rt.model_roles();
        roles.skills_root = "/kit/skills".into();
        roles.mcp_root = "/kit/mcp".into();
        rt.set_model_roles(roles).unwrap();
        assert_eq!(run().trim(), "skills=/kit/skills mcp=/kit/mcp plugins=");
        let mut roles = rt.model_roles();
        roles.skills_root.clear();
        rt.set_model_roles(roles).unwrap();
        assert_eq!(run().trim(), "skills= mcp=/kit/mcp plugins=");
        fs::remove_dir_all(dir).unwrap();
    }
}
