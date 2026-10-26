//! Built-in Arkism: a private Postgres and the Arkism server that the app runs on this computer.
//!
//! The installer puts the programs in `runtime/` next to the app (or in the app's resources on
//! macOS and Linux): `pgsql/{bin,lib,share}`, `memoryd`, `memory-principal` and `roles.sql`.
//! Data lives in the app's local data folder, so reinstalling or updating keeps it.
//! Everything listens on 127.0.0.1 only. The passwords and tokens are in `runtime.json`,
//! readable by this user alone; anyone who can read that folder can read the database files too.
use std::fs;
use std::collections::BTreeMap;
use std::ffi::OsString;
use std::io::Read;
use std::net::TcpListener;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::{Arc, Mutex};
#[cfg(windows)]
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};
use crate::lifecycle::{self, Activity, Backend, Gate, Role, Stopped};
use crate::runtime_process::{self, Process};

#[path = "runtime_token_check.rs"]
pub(crate) mod token_check;

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
    /// The agents' own token (role `agent`); empty in settings files from before it existed.
    #[serde(default)]
    agent: String,
    // Persist intent before changing the database, so a crash can replay the
    // exact rotation instead of losing the only current credential.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pending_owner: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pending_worker: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pending_agent: Option<String>,
    pg_port: u16,
    api_port: u16,
    /// Set once the owner and worker tokens are registered.
    registered: bool,
    /// Set once the agent token is registered (older installs get it on their next start).
    #[serde(default)]
    agent_registered: bool,
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

// Kept in memory only: arguments and environment can contain credentials. Captured at the
// actual launch, rather than rebuilt from mutable settings during recovery.
#[derive(Clone)]
struct LaunchSpec {
    program: OsString,
    args: Vec<OsString>,
    env: BTreeMap<OsString, OsString>,
    cwd: PathBuf,
    log: Option<PathBuf>,
    endpoint: Option<String>,
}

impl LaunchSpec {
    fn validate(&self) -> Result<(), String> {
        let program = Path::new(&self.program);
        if !program.is_absolute() || !fs::metadata(program).map(|m| m.is_file()).unwrap_or(false)
            || !fs::metadata(&self.cwd).map(|m| m.is_dir()).unwrap_or(false) {
            return Err("update_runtime_launch_spec_unavailable".into());
        }
        if let Some(log) = &self.log {
            if !fs::metadata(log).map(|m| m.is_file()).unwrap_or(false) { return Err("update_runtime_log_unavailable".into()); }
        }
        Ok(())
    }

    fn capture(cmd: &mut Command, log: Option<PathBuf>, endpoint: Option<String>) -> Result<Self, String> {
        Self::capture_with_environment(cmd, log, endpoint, std::env::vars_os())
    }

    fn capture_with_environment(cmd: &mut Command, log: Option<PathBuf>, endpoint: Option<String>, parent: impl IntoIterator<Item = (OsString, OsString)>) -> Result<Self, String> {
        // Hidden Windows shell entries such as =C: describe drive-relative working
        // directories, not application variables. Every launch has an explicit cwd.
        // Filter only inherited pseudo entries; explicit command overrides still go
        // through the strict native environment validator unchanged.
        // Command retains the platform's case-insensitive Path/PATH comparison.
        let mut environment = Command::new(cmd.get_program());
        environment.env_clear().envs(parent.into_iter().filter(|(key, _)| !key.as_encoded_bytes().starts_with(b"=")));
        for (key, value) in cmd.get_envs() {
            match value {
                Some(value) => { environment.env(key, value); }
                None => { environment.env_remove(key); }
            }
        }
        let env = environment.get_envs().filter_map(|(key, value)| value.map(|v| (key.to_owned(), v.to_owned()))).collect();
        let cwd = match cmd.get_current_dir() {
            Some(path) => path.to_owned(),
            None => std::env::current_dir().map_err(|_| "local_launch_directory_unavailable")?,
        };
        let spec = Self { program: cmd.get_program().to_owned(), args: cmd.get_args().map(ToOwned::to_owned).collect(), env, cwd, log, endpoint };
        cmd.env_clear().envs(&spec.env).current_dir(&spec.cwd);
        Ok(spec)
    }

    fn command(&self) -> Result<Command, String> {
        self.validate()?;
        let mut cmd = Command::new(&self.program);
        cmd.args(&self.args).env_clear().envs(&self.env).current_dir(&self.cwd);
        quiet(&mut cmd);
        if let Some(path) = &self.log {
            // Recovery never creates a new log/configuration folder as a startup shortcut.
            let file = fs::OpenOptions::new().append(true).open(path).map_err(|_| "update_runtime_log_unavailable")?;
            let err = file.try_clone().map_err(|_| "update_runtime_log_unavailable")?;
            cmd.stdout(file).stderr(err);
        }
        Ok(cmd)
    }
}

struct ManagedChild { process: Process, role: Role, spec: LaunchSpec }

impl ManagedChild {
    fn try_wait(&mut self) -> Result<Option<u32>, String> { self.process.live() }
    fn kill(&mut self) -> Result<(), String> { self.process.kill() }
    fn wait(&mut self) -> Result<(), String> { self.process.wait() }
}

#[derive(Clone, PartialEq, Eq)]
struct PostgresIdentity { pid: u32, started: u64, directory: PathBuf, port: u16 }

#[derive(Clone)]
struct OwnedPostgres { start: LaunchSpec, identity: PostgresIdentity }

#[derive(Clone)]
enum Replay { Child(LaunchSpec), Postgres(OwnedPostgres) }

// This owner is intentionally separate from managed runtime jobs: ordinary shutdown
// is not evidence that an unverified installer may be killed, restored or deleted.
struct PendingUpdate {
    _installer: crate::update_installer::PendingInstaller,
    _stopped: Vec<(Role, Replay)>,
    _role_leases: BTreeMap<Role, u64>,
    _lease: lifecycle::Lease,
    _error: String,
}

#[derive(Debug)]
pub(crate) enum InstallerHandoffFailure {
    Unstarted(String),
    Unverified(String),
}

fn postgres_identity(directory: &Path) -> Result<PostgresIdentity, String> {
    let file = fs::File::open(directory.join("postmaster.pid")).map_err(|_| "update_postgres_identity_unavailable")?;
    let mut bytes = Vec::new();
    file.take(8193).read_to_end(&mut bytes).map_err(|_| "update_postgres_identity_unavailable")?;
    if bytes.len() > 8192 { return Err("update_postgres_identity_unavailable".into()); }
    let text = std::str::from_utf8(&bytes).map_err(|_| "update_postgres_identity_unavailable")?;
    let lines: Vec<_> = text.lines().collect();
    let parse = || -> Option<PostgresIdentity> {
        let pid = lines.first()?.parse::<u32>().ok().filter(|p| *p != 0)?;
        let path = fs::canonicalize(lines.get(1)?).ok()?;
        let started = lines.get(2)?.parse::<u64>().ok().filter(|s| *s != 0)?;
        let port = lines.get(3)?.parse::<u16>().ok().filter(|p| *p != 0)?;
        (path == fs::canonicalize(directory).ok()?).then_some(PostgresIdentity { pid, started, directory: path, port })
    };
    parse().ok_or_else(|| "update_postgres_identity_unavailable".into())
}

struct UpdateRuntime<'a> {
    rt: &'a Runtime,
    turn: Option<std::sync::MutexGuard<'a, ()>>,
    leases: BTreeMap<Role, u64>,
    safe: bool,
}

#[cfg(test)]
trait PostgresBoundary: Send {
    fn running(&mut self) -> Result<bool,String>;
    fn execute(&mut self, start: bool, command: &Command) -> Result<bool,String>;
}


fn owned_postgres(running: bool, owned: Option<OwnedPostgres>, current: impl FnOnce() -> Result<PostgresIdentity, String>) -> Result<Option<OwnedPostgres>, String> {
    match (running, owned) {
        (false, None) => Ok(None),
        (true, Some(owned)) if current()? == owned.identity => Ok(Some(owned)),
        _ => Err("update_postgres_ownership_unknown".into()),
    }
}

impl UpdateRuntime<'_> {
    fn postgres_running(&self) -> Result<bool, String> {
        let rt = self.rt;
        #[cfg(test)]
        if let Some(boundary) = rt.postgres_boundary.lock().map_err(|_| "postgres_boundary_unavailable")?.as_mut() {
            return boundary.running();
        }
        match fs::metadata(rt.pgdata().join("PG_VERSION")) {
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                // Only an absent cluster is stopped by inspection alone. A dangling version
                // link or an incomplete cluster is unknown, not evidence of a safe stop.
                let absent = |path: PathBuf| matches!(fs::symlink_metadata(path), Err(e) if e.kind() == std::io::ErrorKind::NotFound);
                return if absent(rt.pgdata().join("PG_VERSION")) && absent(rt.pgdata()) { Ok(false) }
                    else { Err("update_postgres_state_unknown".into()) };
            }
            Ok(info) if info.is_file() => (),
            _ => return Err("update_postgres_probe_failed".into()),
        }
        let status = quiet(Command::new(rt.pg_bin("pg_ctl")?).arg("-D").arg(rt.pgdata()).arg("status"))
            .status().map_err(|_| "update_postgres_probe_failed")?;
        match status.code() { Some(0) => Ok(true), Some(3) => Ok(false), _ => Err("update_postgres_probe_failed".into()) }
    }

    fn child_snapshot(child: &mut ManagedChild, out: &mut Vec<(Role, Replay)>) -> Result<(), String> {
        if cfg!(windows) && !child.process.contained() { return Err("update_runtime_ownership_unknown".into()); }
        match child.try_wait() {
            Ok(None) => { out.push((child.role, Replay::Child(child.spec.clone()))); Ok(()) }
            _ => Err(format!("update_runtime_state_unknown: {}", child.role.name())),
        }
    }

    fn stop_postgres(&self) -> Stopped {
        let rt = self.rt;
        let checked = (|| -> Result<Command, String> {
            let owned = rt.postgres.lock().map_err(|_| "update_runtime_busy")?;
            let expected = owned.as_ref().ok_or("update_postgres_ownership_unknown")?;
            if postgres_identity(&rt.pgdata())? != expected.identity { return Err("update_postgres_identity_changed".into()); }
            let mut command = Command::new(rt.pg_bin("pg_ctl")?);
            quiet(command.arg("-D").arg(rt.pgdata()).args(["stop", "-m", "fast", "-w", "-t", "30"]));
            Ok(command)
        })();
        let mut command = match checked { Ok(command) => command, Err(error) => return Stopped { confirmed: false, error: Some(error) } };
        let primary = rt.postgres_update_command(&mut command,false)
            .and_then(|success| if success { Ok(()) } else { Err("update_postgres_stop_failed".into()) });
        match self.postgres_running() {
            Ok(false) => {
                let clear = rt.postgres.lock().map(|mut owned| { *owned = None; }).map_err(|_| "update_runtime_busy".to_string());
                let error = match (primary.err(), clear.err()) {
                    (Some(primary), Some(clear)) => Some(format!("{primary}; {clear}")),
                    (primary, clear) => primary.or(clear),
                };
                Stopped { confirmed: true, error }
            }
            probe => {
                let probe = probe.err().unwrap_or_else(|| "update_postgres_stop_unconfirmed".into());
                Stopped { confirmed: false, error: Some(primary.err().map_or_else(|| probe.clone(), |p| format!("{p}; {probe}"))) }
            }
        }
    }

    fn held_recovery_child(&self, role: Role, spec: &LaunchSpec) -> Result<(ManagedChild,u64), String> {
        // Go installs its recovery hold before Ready/any producer. Capture a normal
        // session-bound idle lease before this role can be eligible for release.
        let mut child = self.rt.spawn_spec(role,spec.clone(),true)?;
        let held = child.process.pause()
            .and_then(|lease| lease.filter(|lease| *lease != 0).ok_or_else(|| "update_runtime_recovery_not_held".into()))
            .and_then(|lease| child.process.verify_idle(lease).map(|_| lease));
        match held {
            Ok(lease) => Ok((child,lease)),
            Err(error) => Err(self.rt.retain_failure(runtime_process::Failure {error,process:Some(child.process)})),
        }
    }

    fn restore_child(&mut self, role: Role, spec: &LaunchSpec) -> Result<(), String> {
        match role {
            Role::Server | Role::Worker => {
                let mut children = self.rt.children.lock().map_err(|_| "update_runtime_busy")?;
                if children.iter().any(|c| c.role == role) { return Err("update_runtime_role_already_present".into()); }
                let (child,lease) = self.held_recovery_child(role,spec)?;
                children.push(child);
                self.leases.insert(role,lease);
            }
            Role::Gateway | Role::Watcher => {
                let slot = if role == Role::Gateway { &self.rt.gateway } else { &self.rt.watcher };
                let mut slot = slot.lock().map_err(|_| "update_runtime_busy")?;
                if slot.is_some() { return Err("update_runtime_role_already_present".into()); }
                let (child,lease) = self.held_recovery_child(role,spec)?;
                *slot = Some(child);
                self.leases.insert(role,lease);
            }
            Role::Postgres => return Err("update_runtime_state_unknown".into()),
        }
        self.wait_restored(role, spec)
    }

    fn with_child<T>(&self, role: Role, action: impl FnOnce(Option<&mut ManagedChild>) -> Result<T,String>) -> Result<T,String> {
        match role {
            Role::Server | Role::Worker => {
                let mut children = self.rt.children.lock().map_err(|_| "update_runtime_busy")?;
                action(children.iter_mut().find(|child| child.role == role))
            }
            Role::Gateway | Role::Watcher => {
                let slot = if role == Role::Gateway { &self.rt.gateway } else { &self.rt.watcher };
                action(slot.lock().map_err(|_| "update_runtime_busy")?.as_mut())
            }
            Role::Postgres => Err("update_runtime_state_unknown".into()),
        }
    }

    fn pause_role(&self, role: Role) -> Result<lifecycle::Pause, String> {
        self.with_child(role, |child| {
            let Some(child) = child else { return Ok(lifecycle::Pause::Absent); };
            if child.try_wait()?.is_some() { return Err("update_runtime_state_unknown".into()); }
            Ok(match child.process.pause()? { Some(lease) => lifecycle::Pause::Idle(lease), None => lifecycle::Pause::Busy })
        })
    }

    fn resume_role(&self, role: Role, lease: u64) -> Result<(), String> {
        self.with_child(role, |child| {
            let child = child.ok_or("update_runtime_state_unknown")?;
            child.process.resume(lease)?;
            if child.try_wait()?.is_some() { return Err("update_runtime_state_unknown".into()); }
            Ok(())
        })
    }

    fn verify_idle_role(&self, role: Role, lease: u64) -> Result<(), String> {
        self.with_child(role, |child| {
            let child = child.ok_or("update_runtime_state_unknown")?;
            child.process.verify_idle(lease)
        })
    }

    fn wait_restored(&self, role: Role, spec: &LaunchSpec) -> Result<(), String> {
        std::thread::sleep(Duration::from_millis(300));
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            let mut observed = Vec::new();
            self.snapshot_children(&mut observed)?;
            self.verify_idle_role(role,*self.leases.get(&role).ok_or("update_runtime_recovery_lease_missing")?)?;
            if self.rt.recovery_endpoint(role,spec)? { return Ok(()); }
            if Instant::now() >= deadline { return Err("update_runtime_endpoint_unavailable".into()); }
            std::thread::sleep(Duration::from_millis(100));
        }
    }

    fn snapshot_children(&self, out: &mut Vec<(Role, Replay)>) -> Result<(), String> {
        for child in self.rt.children.lock().map_err(|_| "update_runtime_busy")?.iter_mut() { Self::child_snapshot(child, out)?; }
        for slot in [&self.rt.gateway, &self.rt.watcher] {
            if let Some(child) = slot.lock().map_err(|_| "update_runtime_busy")?.as_mut() { Self::child_snapshot(child, out)?; }
        }
        Ok(())
    }
}

fn endpoint_ready(role: Role, spec: &LaunchSpec) -> Result<bool, String> {
    match role {
        Role::Server => Ok(ready(spec.endpoint.as_deref().ok_or("update_runtime_endpoint_unknown")?)),
        Role::Gateway => tcp_endpoint_ready(spec),
        _ => Ok(true),
    }
}

fn tcp_endpoint_ready(spec: &LaunchSpec) -> Result<bool,String> {
    let address = spec.endpoint.as_deref().and_then(|url| url.strip_prefix("http://")).and_then(|s| s.parse::<std::net::SocketAddr>().ok()).ok_or("update_runtime_endpoint_unknown")?;
    Ok(std::net::TcpStream::connect_timeout(&address, Duration::from_millis(500)).is_ok())
}

fn recovery_endpoint_ready(role: Role, spec: &LaunchSpec) -> Result<bool,String> {
    match role {
        Role::Server | Role::Gateway => tcp_endpoint_ready(spec),
        _ => Ok(true),
    }
}

fn slot_alive(slot: &mut Option<ManagedChild>) -> Result<bool,String> {
    match slot.as_mut().map(ManagedChild::try_wait).transpose()? {
        Some(None) => Ok(true),
        Some(Some(_)) => { *slot = None; Ok(false) },
        None => Ok(false),
    }
}

fn normal_child_stop(child: &mut ManagedChild) -> bool {
    let _ = child.kill();
    child.wait().is_ok() && matches!(child.try_wait(),Ok(Some(_)))
}

#[cfg(any(windows, test))]
fn control_role(role: Role) -> Option<crate::desktop_control::Role> {
    use crate::desktop_control::Role as Control;
    match role {
        Role::Postgres => None,
        Role::Server => Some(Control::Server),
        Role::Worker => Some(Control::Worker),
        Role::Gateway => Some(Control::Gateway),
        Role::Watcher => Some(Control::Watcher),
    }
}

trait ChildControl {
    fn kill_checked(&mut self) -> Result<(), String>;
    fn wait_checked(&mut self) -> Result<(), String>;
    fn stopped(&mut self) -> Result<bool, String>;
}

impl ChildControl for ManagedChild {
    fn kill_checked(&mut self) -> Result<(), String> { self.kill().map_err(|_| "update_runtime_kill_failed".into()) }
    fn stopped(&mut self) -> Result<bool, String> { self.try_wait().map(|s| s.is_some()).map_err(|_| "update_runtime_wait_failed".into()) }
    fn wait_checked(&mut self) -> Result<(), String> {
        let deadline = Instant::now() + Duration::from_secs(5);
        while !self.stopped()? {
            if Instant::now() >= deadline { return Err("update_runtime_stop_timeout".into()); }
            std::thread::sleep(Duration::from_millis(10));
        }
        Ok(())
    }
}

fn stop_child(child: &mut impl ChildControl) -> Stopped {
    let primary = child.kill_checked().and_then(|_| child.wait_checked()).err();
    match child.stopped() {
        Ok(confirmed) => Stopped { confirmed, error: primary },
        Err(probe) => Stopped { confirmed: false, error: Some(primary.map_or_else(|| probe.clone(), |p| format!("{p}; {probe}"))) },
    }
}

impl lifecycle::RoleControl for UpdateRuntime<'_> {
    fn pause_role(&mut self, role: Role) -> Result<lifecycle::Pause,String> { UpdateRuntime::pause_role(self,role) }
    fn verify_idle_role(&mut self, role: Role, lease: u64) -> Result<(),String> { UpdateRuntime::verify_idle_role(self,role,lease) }
    fn resume_role(&mut self, role: Role, lease: u64) -> Result<(),String> { UpdateRuntime::resume_role(self,role,lease) }
    fn control_failed(&mut self) { self.hold_closed(); }
}

impl Backend for UpdateRuntime<'_> {
    type Spec = Replay;

    fn snapshot(&mut self) -> Result<Vec<(Role, Replay)>, String> {
        self.turn = Some(self.rt.busy.try_lock().map_err(|_| "update_runtime_busy")?);
        if !self.rt.unverified.lock().map_err(|_| "update_runtime_busy")?.is_empty() {
            self.hold_closed();
            return Err("update_runtime_ownership_unknown".into());
        }
        let mut out = Vec::new();
        let postgres = self.rt.postgres.lock().map_err(|_| "update_runtime_busy")?.clone();
        if let Some(owned) = owned_postgres(self.postgres_running()?, postgres, || postgres_identity(&self.rt.pgdata()))? {
            owned.start.validate()?;
            out.push((Role::Postgres, Replay::Postgres(owned)));
        }
        if let Err(error) = self.snapshot_children(&mut out) {
            self.hold_closed();
            return Err(error);
        }
        for (role, spec) in &out {
            if let Replay::Child(spec) = spec {
                spec.validate()?;
                if !self.rt.update_endpoint(*role,spec)? { return Err("update_runtime_endpoint_unavailable".into()); }
            }
        }
        Ok(out)
    }

    fn pause(&mut self) -> Result<(), String> {
        let mut leases = std::mem::take(&mut self.leases);
        let result = lifecycle::pause_roles(self,&mut leases);
        self.leases = leases;
        result
    }

    fn resume_remaining(&mut self) -> Result<(), String> {
        let mut leases = std::mem::take(&mut self.leases);
        let result = lifecycle::resume_roles(self,&mut leases);
        self.leases = leases;
        result
    }

    fn hold_closed(&mut self) { self.safe = false; self.rt.gate.block(); }
    fn safe_to_reopen(&self) -> bool { self.safe }

    fn stop(&mut self, role: Role) -> Stopped {
        if role == Role::Postgres { return self.stop_postgres(); }
        let result = (|| -> Result<Stopped, String> {
            if matches!(role, Role::Server | Role::Worker) {
                let mut children = self.rt.children.lock().map_err(|_| "update_runtime_busy")?;
                let index = children.iter().position(|c| c.role == role).ok_or("update_runtime_state_unknown")?;
                let stopped = children[index].process.stop(self.leases.get(&role).copied());
                if stopped.confirmed { children.remove(index); self.leases.remove(&role); }
                return Ok(stopped);
            }
            let slot = if role == Role::Gateway { &self.rt.gateway } else { &self.rt.watcher };
            let mut slot = slot.lock().map_err(|_| "update_runtime_busy")?;
            let child = slot.as_mut().ok_or("update_runtime_state_unknown")?;
            let stopped = child.process.stop(self.leases.get(&role).copied());
            if stopped.confirmed { *slot = None; self.leases.remove(&role); }
            Ok(stopped)
        })();
        result.unwrap_or_else(|error| Stopped { confirmed: false, error: Some(error) })
    }

    fn restore(&mut self, role: Role, spec: &Replay) -> Result<(), String> {
        match (role, spec) {
            (Role::Postgres, Replay::Postgres(owned)) => {
                let mut slot = self.rt.postgres.lock().map_err(|_| "update_runtime_busy")?;
                if slot.is_some() { return Err("update_runtime_role_already_present".into()); }
                if self.postgres_running()? { return Err("update_postgres_ownership_unknown".into()); }
                if !self.rt.postgres_update_command(&mut owned.start.command()?,true)? { return Err("update_postgres_restart_failed".into()); }
                let identity = postgres_identity(&self.rt.pgdata())?;
                if identity.port != owned.identity.port || !self.postgres_running()? { return Err("update_postgres_endpoint_changed".into()); }
                *slot = Some(OwnedPostgres { start: owned.start.clone(), identity });
                Ok(())
            }
            (_, Replay::Child(spec)) => self.restore_child(role, spec),
            _ => Err("update_runtime_state_unknown".into()),
        }
    }
}

pub struct Runtime {
    programs: Option<PathBuf>,
    /// Changes only before anything runs (the first start's 저장 위치), see `move_data_dir`.
    data: std::sync::RwLock<PathBuf>,
    /// The agents look for `agents.json` at one fixed path, wherever the data is.
    agents_file: PathBuf,
    children: Mutex<Vec<ManagedChild>>,
    /// `arkism crew providers watch`: model accounts, quotas, model tests and AI help.
    watcher: Mutex<Option<ManagedChild>>,
    /// `arkism gateway`: the agents connected in 라우팅 send their model requests here.
    gateway: Mutex<Option<ManagedChild>>,
    postgres: Mutex<Option<OwnedPostgres>>,
    gate: Arc<Gate>,
    // Failed native creations/control/finite commands keep their actual owners here.
    unverified: Mutex<Vec<Process>>,
    pending_installer: Mutex<Option<PendingUpdate>>,
    #[cfg(windows)]
    generation: AtomicU64,
    #[cfg(test)]
    process_factory: Mutex<Option<Box<dyn Fn(Role,&LaunchSpec,&Command)->Result<Process,runtime_process::Failure> + Send>>>,
    #[cfg(test)]
    postgres_boundary: Mutex<Option<Box<dyn PostgresBoundary>>>,
    #[cfg(test)]
    endpoint_boundary: Mutex<Option<Box<dyn Fn(Role,&LaunchSpec,bool)->Result<bool,String> + Send>>>,
    #[cfg(test)]
    command_boundary: Mutex<Option<Box<dyn Fn(&mut Command)->Result<runtime_process::Output,runtime_process::Failure> + Send>>>,
    /// Start, backup, restore and erase take turns.
    busy: Mutex<()>,
    /// The user's home: the agents' capture queues are under `.config/arkism`.
    home: Option<PathBuf>,
}

impl Runtime {
    /// `programs` is the folder with the bundled programs; None when this build has none.
    pub fn new(programs: Option<PathBuf>, data: PathBuf) -> Self {
        Runtime { programs, agents_file: data.join("agents.json"), data: std::sync::RwLock::new(data), children: Mutex::new(Vec::new()), watcher: Mutex::new(None), gateway: Mutex::new(None), postgres: Mutex::new(None), gate: Arc::new(Gate::default()), unverified: Mutex::new(Vec::new()), pending_installer: Mutex::new(None),
            #[cfg(windows)] generation: AtomicU64::new(0),
            #[cfg(test)] process_factory: Mutex::new(None),
            #[cfg(test)] postgres_boundary: Mutex::new(None),
            #[cfg(test)] endpoint_boundary: Mutex::new(None),
            #[cfg(test)] command_boundary: Mutex::new(None),
            busy: Mutex::new(()), home: home_dir() }
    }

    pub(crate) fn activity(&self) -> Result<Activity, String> { self.gate.activity() }

    pub(crate) fn gate(&self) -> Arc<Gate> { Arc::clone(&self.gate) }

    fn postgres_update_command(&self, command: &mut Command, start: bool) -> Result<bool,String> {
        #[cfg(test)]
        if let Some(boundary) = self.postgres_boundary.lock().map_err(|_| "postgres_boundary_unavailable")?.as_mut() {
            return boundary.execute(start,command);
        }
        let _ = start;
        command.status().map(|status| status.success()).map_err(|_| "update_postgres_command_failed".into())
    }

    fn update_endpoint(&self, role: Role, spec: &LaunchSpec) -> Result<bool,String> {
        #[cfg(test)]
        if let Some(probe) = self.endpoint_boundary.lock().map_err(|_| "endpoint_boundary_unavailable")?.as_ref() {
            return probe(role,spec,false);
        }
        endpoint_ready(role,spec)
    }

    fn recovery_endpoint(&self, role: Role, spec: &LaunchSpec) -> Result<bool,String> {
        #[cfg(test)]
        if let Some(probe) = self.endpoint_boundary.lock().map_err(|_| "endpoint_boundary_unavailable")?.as_ref() {
            return probe(role,spec,true);
        }
        recovery_endpoint_ready(role,spec)
    }

    fn retain_failure(&self, failure: runtime_process::Failure) -> String {
        if let Some(process) = failure.process {
            self.gate.block();
            match self.unverified.lock() {
                Ok(mut owners) => owners.push(process),
                Err(poisoned) => {
                    poisoned.into_inner().push(process);
                    return format!("{}; runtime_owner_storage_unavailable",failure.error);
                }
            }
        }
        failure.error
    }

    fn spawn_spec(&self, role: Role, spec: LaunchSpec, recovery: bool) -> Result<ManagedChild, String> {
        let mut command = spec.command()?;
        command.env("ARKISM_DESKTOP_RECOVERY",if recovery {"1"}else{"0"});
        #[cfg(test)]
        if let Some(factory) = self.process_factory.lock().map_err(|_| "runtime_factory_unavailable")?.as_ref() {
            let process = factory(role,&spec,&command).map_err(|failure| self.retain_failure(failure))?;
            return Ok(ManagedChild {process,role,spec});
        }
        #[cfg(not(windows))]
        if role == Role::Watcher { quiet(&mut command); }
        #[cfg(windows)]
        let bootstrap = if let Some(control_role) = control_role(role) {
            let generation = self.generation.fetch_update(Ordering::SeqCst,Ordering::SeqCst,|v|v.checked_add(1)).map_err(|_| "runtime_generation_exhausted")? + 1;
            let nonce = secret()?;
            let env = crate::desktop_control::bootstrap_environment(control_role,generation,&nonce).map_err(|e|e.to_string())?;
            command.envs(env);
            Some((control_role,generation,nonce))
        } else { None };
        #[cfg(not(windows))]
        let bootstrap = None;
        let process = runtime_process::spawn(&mut command,spec.log.as_deref(),bootstrap).map_err(|failure| self.retain_failure(failure))?;
        Ok(ManagedChild { process, role, spec })
    }

    fn spawn_child(&self, role: Role, cmd: &mut Command, log: Option<PathBuf>, endpoint: Option<String>) -> Result<ManagedChild, String> {
        let spec = LaunchSpec::capture(cmd,log,endpoint)?;
        #[cfg(windows)]
        { self.spawn_spec(role,spec,false) }
        #[cfg(not(windows))]
        {
            let process = runtime_process::spawn(cmd,spec.log.as_deref(),None).map_err(|failure| self.retain_failure(failure))?;
            Ok(ManagedChild { process,role,spec })
        }
    }

    pub(crate) fn command_output(&self, command: &mut Command) -> Result<runtime_process::Output, String> {
        let _activity = self.activity()?;
        let _spec = LaunchSpec::capture(command,None,None)?;
        #[cfg(test)]
        if let Some(boundary) = self.command_boundary.lock().map_err(|_| "command_boundary_unavailable")?.as_ref() {
            return boundary(command).map_err(|failure| self.retain_failure(failure));
        }
        runtime_process::output(command).map_err(|failure| self.retain_failure(failure))
    }

    fn host_status(&self, command: &mut Command, error: &str) -> Result<(), String> {
        let output = self.command_output(command)?;
        if output.code == 0 { Ok(()) } else { Err(error.into()) }
    }

    fn installer_slot(&self) -> Result<std::sync::MutexGuard<'_, Option<PendingUpdate>>, String> {
        match self.pending_installer.try_lock() {
            Ok(slot) => Ok(slot),
            Err(std::sync::TryLockError::WouldBlock) => Err("update_runtime_busy".into()),
            Err(std::sync::TryLockError::Poisoned(_)) => {
                self.gate.block();
                Err("update_installer_owner_storage_unavailable".into())
            }
        }
    }

    /// Reserve installer storage before the mutation lease, exclusive turn and role slots.
    pub(crate) fn update_handoff(&self, launch: impl FnOnce() -> Result<(), String>) -> Result<(), String> {
        let slot = self.installer_slot()?;
        if slot.is_some() { return Err("update_installer_unverified".into()); }
        let mut backend = UpdateRuntime { rt:self, turn:None, leases:BTreeMap::new(), safe:true };
        lifecycle::coordinated(&self.gate,&mut backend,launch)
    }

    pub(crate) fn update_installer_handoff(&self, launch: impl FnOnce() -> lifecycle::Launch<crate::update_installer::PendingInstaller>) -> Result<(), InstallerHandoffFailure> {
        // The guard remains held through launch. No fallible lock acquisition or
        // replacement is needed after a child owner has become unverified.
        let mut slot = self.installer_slot().map_err(InstallerHandoffFailure::Unstarted)?;
        if slot.is_some() { return Err(InstallerHandoffFailure::Unstarted("update_installer_unverified".into())); }
        let mut backend = UpdateRuntime { rt:self, turn:None, leases:BTreeMap::new(), safe:true };
        match lifecycle::coordinated_owned(&self.gate,&mut backend,launch) {
            lifecycle::OwnedHandoff::Started => Ok(()),
            lifecycle::OwnedHandoff::Unstarted(error) => Err(InstallerHandoffFailure::Unstarted(error)),
            lifecycle::OwnedHandoff::Unverified { owner, error, stopped, lease } => {
                *slot = Some(PendingUpdate {
                    _installer:owner, _stopped:stopped,
                    _role_leases:std::mem::take(&mut backend.leases), _lease:lease, _error:error,
                });
                Err(InstallerHandoffFailure::Unverified("update_installer_unverified".into()))
            }
        }
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

    pub fn data_dir(&self) -> PathBuf {
        self.data.read().unwrap_or_else(std::sync::PoisonError::into_inner).clone()
    }

    /// Keeps `agents.json` at `path` instead of beside the data (the app's default data folder).
    pub fn agents_file(mut self, path: PathBuf) -> Self {
        self.agents_file = path;
        self
    }

    /// Points the built-in Arkism at another data folder. Only while nothing of it runs: a running
    /// Postgres or server keeps the old folder, so the change waits for the next app start.
    pub fn move_data_dir(&self, path: PathBuf) -> Result<(), String> {
        let _activity = self.activity()?;
        let _turn = self.busy.lock().map_err(|_| "local_busy".to_string())?;
        let idle = self.postgres.lock().map(|p| p.is_none()).unwrap_or(false)
            && self.children.lock().map(|c| c.is_empty()).unwrap_or(false)
            && self.gateway.lock().map(|g| g.is_none()).unwrap_or(false)
            && self.watcher.lock().map(|w| w.is_none()).unwrap_or(false)
            && !self.pgdata().join("postmaster.pid").exists()
            // A Postgres already running in the target (another app build or user) would be reused, then stopped.
            && !path.join("pg").join("postmaster.pid").exists();
        if !idle {
            return Err("local_location_running".into());
        }
        *self.data.write().unwrap_or_else(std::sync::PoisonError::into_inner) = path;
        Ok(())
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
        self.data_dir().join("pg")
    }

    /// Starts Postgres, prepares the database and starts the server. Safe to call again:
    /// what already runs is reused. Returns the address and the owner token for the UI.
    pub fn start(&self) -> Result<Started, String> {
        let _activity = self.activity()?;
        let _turn = self.busy.lock().map_err(|_| "local_busy".to_string())?;
        self.start_locked()
    }

    fn start_locked(&self) -> Result<Started, String> {
        self.programs()?;
        fs::create_dir_all(self.data_dir().join("logs")).map_err(|e| format!("local_data_folder: {e}"))?;
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
        let _activity = self.activity()?;
        let mut gateway = self.gateway.lock().map_err(|_| "local_busy".to_string())?;
        let alive = slot_alive(&mut gateway).map_err(|e| { self.gate.block(); e })?;
        let mut secrets = self.secrets()?;
        if alive && secrets.gateway_port != 0 {
            return Ok(gateway_url(secrets.gateway_port));
        }
        let old_port = secrets.gateway_port;
        if secrets.gateway_port == 0 || !port_free(secrets.gateway_port) {
            secrets.gateway_port = free_port(GATEWAY_PORT)?;
        }
        let listen = format!("127.0.0.1:{}", secrets.gateway_port);
        let mut cmd = self.host_command(&["gateway"]).ok_or("local_unavailable")?;
        let log = fs::OpenOptions::new().create(true).append(true).open(self.data_dir().join("logs").join("gateway.log")).map_err(|e| e.to_string())?;
        let err = log.try_clone().map_err(|e| e.to_string())?;
        cmd.env("GOMEMLIMIT", "512MiB").env("ARKISM_GATEWAY_LISTEN", &listen).stdin(Stdio::null()).stdout(log).stderr(err);
        no_window(&mut cmd);
        let child = self.spawn_child(Role::Gateway, &mut cmd, Some(self.data_dir().join("logs").join("gateway.log")), Some(gateway_url(secrets.gateway_port)))?;
        // A gateway that exits at once (bad config, port taken) is not reported as running.
        *gateway = Some(child);
        std::thread::sleep(std::time::Duration::from_millis(300));
        if !slot_alive(&mut gateway).map_err(|e| { self.gate.block(); e })? {
            return Err("gateway_start_failed".into());
        }
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
        let _activity = self.activity().ok()?;
        let mut gateway = self.gateway.lock().ok()?;
        let alive = gateway.as_mut().is_some_and(|c| matches!(c.try_wait(), Ok(None)));
        let port = self.secrets().ok()?.gateway_port;
        (alive && port != 0).then(|| gateway_url(port))
    }

    /// What the PC agent client (`arkism windows hook|mcp`) reads to talk to this Arkism instead
    /// of a server: `agents.json` and the agent token beside it, both readable by this user only.
    /// The owner token beside them is for the app's own jobs (`host_command`), never the agents.
    /// `server: None` hands the agents back to the server connection (mode "server").
    fn write_agent_connection(&self, server: Option<&str>, secrets: Option<&Secrets>, ssh: Option<(&str, Option<u16>, Option<&str>, &crate::ssh_identity::Pin)>) -> Result<(), String> {
        let token_file = self.data_dir().join("owner.token");
        let agent_file = self.data_dir().join("agent.token");
        let conn = match (server, secrets) {
            (Some(url), Some(secrets)) => {
                write_private(&token_file, secrets.owner.as_bytes()).map_err(|e| format!("local_agent_file: {e}"))?;
                write_private(&agent_file, secrets.agent.as_bytes()).map_err(|e| format!("local_agent_file: {e}"))?;
                serde_json::json!({ "mode": "local", "url": url, "token_file": agent_file })
            }
            _ => {
                let _ = fs::remove_file(&token_file);
                let _ = fs::remove_file(&agent_file);
                // The agents reach the server over the same SSH address the app connected with.
                match ssh {
                    Some((host, port, key, pin)) => serde_json::json!({ "mode": "server", "ssh_host": host, "ssh_port": port, "ssh_key": key, "ssh_known_hosts": pin.file, "ssh_host_alias": pin.alias, "ssh_host_algorithms": pin.algorithms }),
                    None => serde_json::json!({ "mode": "server" }),
                }
            }
        };
        fs::create_dir_all(self.agents_file.parent().unwrap_or(Path::new("."))).map_err(|e| format!("local_agent_file: {e}"))?;
        write_private(&self.agents_file, conn.to_string().as_bytes()).map_err(|e| format!("local_agent_file: {e}"))
    }

    /// What the server's timers do, run here for the built-in Arkism: find the agents and their MCP
    /// servers (설정 › 에이전트, MCP), index the PC's agent sessions (세션·사용량), list the skills
    /// (스킬), and process the capture queues of connected agents. Nothing here needs the agents to
    /// be connected except the queues. `tick` counts minutes since the app started.
    /// The bundled `arkism` program, set up to work against this built-in Arkism.
    fn host_command(&self, args: &[&str]) -> Option<Command> {
        let secrets = self.secrets().ok()?;
        let token_file = self.data_dir().join("owner.token");
        let client = self.agent_client()?;
        if !token_file.is_file() {
            return None;
        }
        let state = self.data_dir().join("host");
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
        // 스킬 요약 (sync-skills): without a model each skill is listed from its own frontmatter.
        if roles.summary == "chat" {
            cmd.env("ARKISM_SKILL_LLM_URL", summary_endpoint(&roles.summary_url))
                .env("ARKISM_SKILL_LLM_MODEL", &roles.summary_model)
                .env("ARKISM_SKILL_LLM_KEY_FILE", self.data_dir().join("skill-llm.key"));
        }
        // 스킬 보안 검사: an empty program name turns an unchosen scanner off (exec finds nothing).
        for (k, env) in SCANNERS {
            if !roles.scanners.iter().any(|s| s == k) {
                cmd.env(env, "");
            }
        }
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
        let _activity = self.activity()?;
        let mut args = vec!["capture", "import-agents"];
        if dry_run {
            args.push("--dry-run");
        }
        let mut cmd = self.host_command(&args).ok_or("local_unavailable")?;
        cmd.env("ARKISM_REPO_TAGS", "1").stdin(Stdio::null()).stderr(Stdio::null());
        no_window(&mut cmd);
        let out = self.command_output(&mut cmd)?;
        let text = String::from_utf8_lossy(&out.stdout);
        let last = text.lines().rev().find(|l| l.trim_start().starts_with('{')).unwrap_or("");
        serde_json::from_str(last).map_err(|_| "import_failed".to_string())
    }

    /// 설정 › 고급 › 진단: `arkism doctor --json` (with `--fix`: reconnect agents, retry failed captures).
    pub fn doctor(&self, fix: bool) -> Result<serde_json::Value, String> {
        let _activity = self.activity()?;
        let mut args = vec!["doctor", "--json"];
        if fix {
            args.push("--fix");
        }
        let client = self.agent_client().ok_or("local_unavailable")?;
        let mut cmd = Command::new(client);
        cmd.args(&args).stdin(Stdio::null()).stderr(Stdio::null());
        no_window(&mut cmd);
        let out = self.command_output(&mut cmd)?;
        serde_json::from_slice(&out.stdout).map_err(|_| "doctor_failed".to_string())
    }

    /// The model settings refresh button: read every account's usage now and report it.
    pub fn refresh_providers(&self) -> Result<(), String> {
        let _activity = self.activity()?;
        let mut cmd = self.host_command(&["crew", "providers", "report", "--fresh"]).ok_or("local_unavailable")?;
        self.host_status(quiet(&mut cmd),"providers_refresh_failed")
    }

    pub fn background_work(&self, tick: u64) {
        let Ok(_activity) = self.activity() else { return };
        if self.host_command(&[]).is_none() {
            return;
        }
        let command = |args: &[&str]| self.host_command(args).expect("checked above");
        let run = |args: &[&str], extra: &[(&str, PathBuf)]| {
            let mut cmd = command(args);
            for (k, v) in extra {
                cmd.env(k, v);
            }
            let _ = self.host_status(quiet(&mut cmd),"background_host_failed");
        };
        let _ = self.ensure_gateway();
        // Keep the provider watcher running (restart it if it stopped).
        if let Ok(mut watcher) = self.watcher.lock() {
            let alive = match slot_alive(&mut watcher) {
                Ok(alive) => alive,
                Err(_) => { self.gate.block(); return; }
            };
            if !alive {
                let mut cmd = command(&["crew", "providers", "watch"]);
                quiet(&mut cmd);
                match self.spawn_child(Role::Watcher, &mut cmd, None, None) {
                    Ok(child) => *watcher = Some(child),
                    Err(_) => return,
                }
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
        // Audit rows older than MEMORY_AUDIT_RETENTION_DAYS (30 unless set), once a day.
        if tick % 1440 == 150 {
            let _ = self.expire_audit_events();
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
    /// Deletes audit events past retention as the schema owner, the only role that sees them all.
    fn expire_audit_events(&self) -> Result<(), String> {
        let secrets = self.secrets()?;
        let init_url = self.url("arkism_init", &secrets.init, secrets.pg_port);
        let mut memoryd = Command::new(self.programs()?.join(exe("memoryd")));
        memoryd.args(["-audit-expiry", "execute"]).env("MEMORY_STORE", "postgres").env("MEMORY_DATABASE_URL", init_url);
        run(quiet(&mut memoryd), "local_audit_expiry_failed")
    }

    pub fn expire(&self) -> Result<(), String> {
        let _activity = self.activity()?;
        let secrets = self.secrets()?;
        let state = self.data_dir().join("host").join("session-archive").join("state.json");
        let mut archive = self.host_command(&["session-archive", "expire", "--execute"]).ok_or("local_unavailable")?;
        archive.env("ARKISM_SESSION_STATE", state);
        self.host_status(quiet(&mut archive), "local_expire_archive_failed")?;
        let url = format!("http://127.0.0.1:{}", secrets.api_port);
        let mut candidates = self
            .host_command(&["expire-candidates", "--url", &url, "--workspace-id", "default", "--project-id", "default",
                "--user-id", "management:owner", "--retention-days", "14", "--batch-size", "50", "--max-batches", "2", "--execute"])
            .ok_or("local_unavailable")?;
        candidates.env("MEMORY_API_TOKEN", &secrets.worker);
        self.host_status(quiet(&mut candidates), "local_expire_candidates_failed")
    }

    /// How the built-in Arkism searches: "ollama:<model>" (meaning and keywords) or "none" (keywords).
    pub fn search_mode(&self) -> String {
        let Ok(_activity) = self.activity() else { return "none".into() };
        self.secrets().map(|s| if s.embedder.is_empty() { "none".into() } else { s.embedder }).unwrap_or_else(|_| "none".into())
    }

    pub fn model_roles(&self) -> ModelRoles {
        fs::read_to_string(self.data_dir().join("models.json")).ok().and_then(|t| serde_json::from_str(&t).ok()).unwrap_or_default()
    }

    /// Saves the roles and restarts the server so the embedder and judge take effect (a new embedder
    /// re-indexes once). The token is never sent back to the page: the UI only learns whether one is set.
    pub fn set_model_roles(&self, mut roles: ModelRoles) -> Result<(), String> {
        let _activity = self.activity()?;
        let saved = self.model_roles(); // an empty secret field keeps the saved one
        for (field, old) in [(&mut roles.cloudflare_token, saved.cloudflare_token), (&mut roles.jev_key, saved.jev_key), (&mut roles.picker_key, saved.picker_key), (&mut roles.summary_key, saved.summary_key)] {
            if field.is_empty() {
                *field = old;
            }
        }
        if !roles.valid() {
            return Err("model_roles_invalid".into());
        }
        let text = serde_json::to_string_pretty(&roles).map_err(|e| e.to_string())?;
        write_private(&self.data_dir().join("models.json"), text.as_bytes()).map_err(|e| e.to_string())?;
        // sync-skills reads the summary key from a file (empty for Ollama).
        write_private(&self.data_dir().join("skill-llm.key"), roles.summary_key.as_bytes()).map_err(|e| e.to_string())?;
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
        let _activity = self.activity()?;
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
    pub fn agents_use_server(&self, ssh_host: &str, ssh_port: Option<u16>, ssh_key: Option<&str>, pin: &crate::ssh_identity::Pin) -> Result<(), String> {
        let _activity = self.activity()?;
        self.write_agent_connection(None, None, Some((ssh_host, ssh_port, ssh_key, pin)))
    }

    /// Stops the server and Postgres. Called when the app quits or before an update.
    pub fn stop(&self) {
        let Ok(_activity) = self.activity() else { return };
        self.shutdown();
    }

    /// App-exit teardown also runs after a successful update's terminal handoff. It is not
    /// exposed as a command and does not reopen the ordinary mutation gate.
    pub(crate) fn shutdown(&self) {
        self.stop_server();
        if let (Ok(pg_ctl), true) = (self.pg_bin("pg_ctl"), self.pgdata().join("postmaster.pid").exists()) {
            let _ = quiet(Command::new(pg_ctl).arg("-D").arg(self.pgdata()).args(["stop", "-m", "fast", "-w"])).status();
        }
        if let Ok(mut postgres) = self.postgres.lock() { *postgres = None; }
    }

    /// Stops the server, its worker and the provider watcher; Postgres keeps running.
    fn stop_server(&self) {
        for slot in [&self.watcher,&self.gateway] {
            if let Ok(mut slot) = slot.lock() {
                let stopped = slot.as_mut().map_or(true,normal_child_stop);
                if stopped { *slot = None; } else { self.gate.block(); }
            }
        }
        if let Ok(mut children) = self.children.lock() {
            children.retain_mut(|child| {
                let stopped = normal_child_stop(child);
                if !stopped { self.gate.block(); }
                !stopped
            });
        }
        if let Ok(mut owners) = self.unverified.lock() {
            owners.retain_mut(|owner| {
                let _ = owner.kill();
                owner.wait().is_err() || !matches!(owner.live(),Ok(Some(_)))
            });
        }
    }

    fn secrets(&self) -> Result<Secrets, String> {
        let path = self.data_dir().join("runtime.json");
        if let Ok(text) = fs::read_to_string(&path) {
            let mut secrets: Secrets = serde_json::from_str(&text).map_err(|_| "local_settings_damaged".to_string())?;
            if secrets.archive_key.is_empty() {
                secrets.archive_key = secret()?;
                self.save(&secrets)?;
            }
            if secrets.agent.is_empty() {
                secrets.agent = secret()?;
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
            agent: secret()?,
            pending_owner: None,
            pending_worker: None,
            pending_agent: None,
            pg_port: free_port(PG_PORT)?,
            api_port: free_port(API_PORT)?,
            registered: false,
            agent_registered: false,
            archive_key: secret()?,
            embedder: String::new(),
            gateway_port: 0,
        };
        self.save(&secrets)?;
        Ok(secrets)
    }

    fn save(&self, secrets: &Secrets) -> Result<(), String> {
        let path = self.data_dir().join("runtime.json");
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
            let pwfile = self.data_dir().join("initdb.pw");
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
        fs::create_dir_all(self.data_dir().join("logs")).map_err(|e| format!("local_data_folder: {e}"))?;
        let log = self.data_dir().join("logs").join("postgres.log");
        let mut command = Command::new(&pg_ctl);
        quiet(command.arg("-D").arg(&pgdata)
            .arg("-l").arg(&log)
            .arg("-o").arg(format!("-p {} -c listen_addresses=127.0.0.1", secrets.pg_port))
            .args(["-w", "-t", "60", "start"]));
        let spec = LaunchSpec::capture(&mut command, None, None)?;
        let ok = command
            .status()
            .map(|s| s.success())
            .unwrap_or(false);
        if ok {
            // A reused/external PG never acquires ownership by merely being observed running.
            // If its identity cannot be read, normal startup is unchanged and updates fail closed.
            *self.postgres.lock().map_err(|_| "local_busy")? = postgres_identity(&pgdata).ok()
                .filter(|identity| identity.port == secrets.pg_port)
                .map(|identity| OwnedPostgres { start: spec, identity });
        }
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
        // The agent principal came after the first release: installs that already have the owner
        // and worker register it alone.
        let missing: &[ManagedPrincipal] = match (secrets.registered, secrets.agent_registered) {
            (false, _) => &[ManagedPrincipal::Owner, ManagedPrincipal::Worker, ManagedPrincipal::Agent],
            (true, false) => &[ManagedPrincipal::Agent],
            (true, true) => &[],
        };
        for principal in missing {
            run(quiet(Command::new(self.programs()?.join(exe("memory-principal")))
                .args(["--principal", principal.id(), "--role", principal.role()])
                .args(SCOPE)
                .args(["--actions", "*"])
                .env("MEMORY_DATABASE_URL", &init_url)
                .env("MEMORY_PRINCIPAL_TOKEN", principal.current(secrets))), "local_token_failed")?;
        }
        if !missing.is_empty() {
            secrets.registered = true;
            secrets.agent_registered = true;
            self.save(secrets)?;
        }
        self.rotate_managed_tokens(secrets, &init_url)?;
        Ok(())
    }

    fn rotate_managed_tokens(&self, secrets: &mut Secrets, init_url: &str) -> Result<(), String> {
        let program = self.programs()?.join(exe("memory-principal"));
        for principal in [ManagedPrincipal::Owner, ManagedPrincipal::Worker, ManagedPrincipal::Agent] {
            let mut status = |id: &str, token: &str| {
                let mut cmd = Command::new(&program);
                quiet(&mut cmd).args(["--token-status", "--principal", id])
                    .stdout(Stdio::piped())
                    .env("MEMORY_DATABASE_URL", init_url).env("MEMORY_PRINCIPAL_TOKEN", token);
                let output = self.command_output(&mut cmd)?;
                if output.code != 0 { return Err("local_token_status_failed".into()); }
                managed_token_status(&output.stdout, id)
            };
            let mut rotate = |id: &str, old: &str, next: &str| {
                self.stop_server();
                let mut cmd = Command::new(&program);
                quiet(&mut cmd).args(["--rotate-token", "--principal", id])
                    .env("MEMORY_DATABASE_URL", init_url).env("MEMORY_PRINCIPAL_TOKEN", next)
                    .env("MEMORY_PREVIOUS_PRINCIPAL_TOKEN", old);
                self.host_status(&mut cmd, "local_token_rotation_failed")
            };
            // A pending rotation may itself have expired while the PC was off.
            // Finish its replay, then check once more before starting workers.
            for _ in 0..2 {
                if !rotate_managed_token(secrets, principal, &mut status, &|s| self.save(s), &mut rotate)? { break; }
            }
            if status(principal.id(), principal.current(secrets))? { return Err("local_token_refresh_failed".into()); }
        }
        Ok(())
    }

    fn url(&self, user: &str, password: &str, port: u16) -> String {
        format!("postgres://{user}:{password}@127.0.0.1:{port}/{DATABASE}?sslmode=disable")
    }

    fn start_server(&self, secrets: &mut Secrets) -> Result<String, String> {
        let server = |port: u16| format!("http://127.0.0.1:{port}");
        let mut children = self.children.lock().map_err(|_| "local_busy".to_string())?;
        let mut stopped = Vec::new();
        for (index,child) in children.iter_mut().enumerate() {
            match child.try_wait() {
                Ok(Some(_)) => stopped.push(index),
                Ok(None) => (),
                Err(error) => { self.gate.block(); return Err(error); }
            }
        }
        for index in stopped.into_iter().rev() { children.remove(index); }
        if children.len() == 2 && ready(&server(secrets.api_port)) {
            return Ok(server(secrets.api_port));
        }
        if !children.iter_mut().all(normal_child_stop) {
            self.gate.block();
            return Err("local_server_stop_unconfirmed".into());
        }
        children.clear();
        if !port_free(secrets.api_port) {
            secrets.api_port = free_port(API_PORT)?;
            self.save(secrets)?;
        }
        let app_url = self.url("arkism_app", &secrets.app, secrets.pg_port);
        let logs = self.data_dir().join("logs");
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
        // 스킬 판정 can use a different judge (memoryd reads ARKISM_SKILL_JUDGE / ARKISM_MODEL_JUDGE).
        let skill_judge = roles.skill_judge().to_string();
        let uses = |j: &str| roles.judge == j || skill_judge == j;
        if uses("clef") && !roles.cloudflare_account.is_empty() && !roles.cloudflare_token.is_empty() {
            embed_env.push(("CLOUDFLARE_ACCOUNT_ID", roles.cloudflare_account.clone()));
            embed_env.push(("CLOUDFLARE_AI_TOKEN", roles.cloudflare_token.clone()));
        }
        if uses("jev") && !roles.jev_key.is_empty() {
            embed_env.push(("TYPESAFE_API_KEY", roles.jev_key.clone()));
        }
        if roles.judge == "chat" && !roles.picker_url.is_empty() && !roles.picker_model.is_empty() {
            embed_env.push(("ARKISM_ROUTING_PICKER_URL", roles.picker_url.clone()));
            embed_env.push(("ARKISM_ROUTING_PICKER_MODEL", roles.picker_model.clone()));
            if !roles.picker_key.is_empty() {
                embed_env.push(("ARKISM_ROUTING_PICKER_KEY", roles.picker_key.clone()));
            }
        }
        let model_judge = if ["clef", "jev"].contains(&roles.judge.as_str()) { roles.judge.clone() } else { "fixed".into() };
        embed_env.push(("ARKISM_MODEL_JUDGE", model_judge));
        embed_env.push(("ARKISM_SKILL_JUDGE", skill_judge));
        embed_env.push(("ARKISM_SKILL_CANDIDATES", roles.skill_candidates.to_string()));
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
        let spawn = |role: Role, args: &[&str], log: &str| -> Result<ManagedChild, String> {
            let file = fs::OpenOptions::new().create(true).append(true).open(logs.join(log)).map_err(|e| e.to_string())?;
            let err = file.try_clone().map_err(|e| e.to_string())?;
            let mut cmd = Command::new(self.programs()?.join(exe("memoryd")));
            cmd.args(args)
                .env("GOMEMLIMIT", "512MiB")
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
            self.spawn_child(role, &mut cmd, Some(logs.join(log)), (role == Role::Server).then(|| server(secrets.api_port)))
        };
        let listen = format!("127.0.0.1:{}", secrets.api_port);
        children.push(spawn(Role::Server, &["--listen", &listen], "server.log")?);
        children.push(spawn(Role::Worker, &["--worker"], "worker.log")?);
        let deadline = Instant::now() + Duration::from_secs(30);
        while Instant::now() < deadline {
            for child in children.iter_mut() {
                match child.try_wait() {
                    Ok(Some(_)) => return Err("local_server_start_failed".into()),
                    Ok(None) => (),
                    Err(error) => { self.gate.block(); return Err(error); }
                }
            }
            if ready(&server(secrets.api_port)) { return Ok(server(secrets.api_port)); }
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
        let _activity = self.activity()?;
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
            format: 2,
            app_version: env!("CARGO_PKG_VERSION").to_string(),
            created: name.trim_start_matches("arkism-").to_string(),
            owner: secrets.owner.clone(),
            worker: secrets.worker.clone(),
            agent: secrets.agent.clone(),
            pending_owner: secrets.pending_owner.clone(),
            pending_worker: secrets.pending_worker.clone(),
            pending_agent: secrets.pending_agent.clone(),
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
        self.automatic_backup(true)
    }

    /// Right before an update replaces the built-in programs: a backup now, whatever the newest
    /// one's age, kept and pruned with the automatic ones. The update stops when it fails.
    pub fn backup_before_update(&self) -> Result<(), String> {
        self.automatic_backup(false)
    }

    fn automatic_backup(&self, only_when_stale: bool) -> Result<(), String> {
        let _activity = self.activity()?;
        const KEEP: usize = 7;
        let dir = Self::backup_dir()?;
        let newest = Self::backups()?.into_iter().next();
        let stale = newest.map_or(true, |b| b.created < utc_stamp(std::time::SystemTime::now() - Duration::from_secs(86_400)));
        if only_when_stale && !stale {
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
        let _activity = self.activity()?;
        let _turn = self.busy.lock().map_err(|_| "local_busy".to_string())?;
        let backup = valid_backup_name(name).then(|| Backup::read(&dir.join(name))).flatten().ok_or("local_backup_missing")?;
        let info: BackupInfo = serde_json::from_str(&fs::read_to_string(dir.join(name).join("backup.json")).map_err(|e| e.to_string())?)
            .map_err(|_| "local_backup_damaged".to_string())?;
        if !matches!(info.format, 1 | 2) {
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
        secrets.agent = info.agent;
        secrets.pending_owner = info.pending_owner;
        secrets.pending_worker = info.pending_worker;
        secrets.pending_agent = info.pending_agent;
        secrets.archive_key = info.archive_key;
        secrets.registered = true;
        // A backup from before the agent principal restores a database without it: made on this start.
        secrets.agent_registered = !secrets.agent.is_empty();
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
        let _activity = self.activity()?;
        let _turn = self.busy.lock().map_err(|_| "local_busy".to_string())?;
        self.stop();
        for name in ["pg", "host", "logs"] {
            let path = self.data_dir().join(name);
            if path.exists() {
                fs::remove_dir_all(&path).map_err(|e| format!("local_erase_failed: {e}"))?;
            }
        }
        for name in ["runtime.json", "owner.token", "agent.token", "initdb.pw"] {
            let _ = fs::remove_file(self.data_dir().join(name));
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
    /// 스킬 판정: "same" follows `judge` (Clef or Jev; anything else ranks by rules), or "fixed", "clef", "jev".
    #[serde(default = "default_same")]
    pub skill_judge: String,
    /// 스킬 후보 찾기: how many skills the search hands the judge (1–8).
    #[serde(default = "default_candidates")]
    pub skill_candidates: u8,
    /// 스킬 요약: "basic" (from each SKILL.md's own description) or "chat" (an OpenAI-compatible model).
    #[serde(default = "default_basic")]
    pub summary: String,
    #[serde(default)]
    pub summary_url: String,
    #[serde(default)]
    pub summary_model: String,
    #[serde(default)]
    pub summary_key: String,
    /// 스킬 보안 검사: the optional scanners to run next to the built-in check, when installed.
    #[serde(default = "default_scanners")]
    pub scanners: Vec<String>,
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

/// sync-skills posts to the chat completions URL itself; the screen asks for the base (…/v1) like 라우팅 판정.
fn summary_endpoint(base: &str) -> String {
    let base = base.trim_end_matches('/');
    if base.ends_with("/chat/completions") { base.to_string() } else { format!("{base}/chat/completions") }
}

fn gateway_url(port: u16) -> String {
    format!("http://127.0.0.1:{port}")
}
fn default_embed_model() -> String { OLLAMA_MODEL.into() }
fn default_fixed() -> String { "fixed".into() }
fn default_same() -> String { "same".into() }
fn default_basic() -> String { "basic".into() }
fn default_candidates() -> u8 { 8 }
fn default_scanners() -> Vec<String> { SCANNERS.iter().map(|(k, _)| k.to_string()).collect() }
/// 스킬 보안 검사 engines and the env that names each one's program (internal/host/skills/skillchecks.go).
const SCANNERS: [(&str, &str); 3] = [("spector", "ARKISM_SKILLSPECTOR_BIN"), ("evaluator", "ARKISM_SKILLEVALUATOR_BIN"), ("cisco", "ARKISM_CISCO_SCANNER_BIN")];
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
            && [&self.cloudflare_token, &self.jev_key, &self.picker_key, &self.summary_key].iter().all(|k| k.len() <= 512 && !k.contains(['\n', '\r']))
            && (self.picker_url.is_empty() || picker_url_ok(&self.picker_url))
            && (self.picker_model.is_empty() || model(&self.picker_model))
            && ["same", "fixed", "clef", "jev"].contains(&self.skill_judge.as_str())
            && (1..=8).contains(&self.skill_candidates)
            && ["basic", "chat"].contains(&self.summary.as_str())
            && (self.summary_url.is_empty() || picker_url_ok(&self.summary_url))
            && (self.summary_model.is_empty() || model(&self.summary_model))
            && (self.summary != "chat" || (!self.summary_url.is_empty() && !self.summary_model.is_empty()))
            && self.scanners.iter().all(|k| SCANNERS.iter().any(|(s, _)| s == k))
            && model(&self.extract_model)
            && model(&self.reflect_model)
            && [&self.skills_root, &self.mcp_root, &self.plugins_root].iter().all(|p| p.is_empty() || root_ok(p))
    }

    /// The skill judge the server runs: `same` means Clef or Jev when 라우팅 판정 is one of them, else rules.
    fn skill_judge(&self) -> &str {
        match self.skill_judge.as_str() {
            "same" if ["clef", "jev"].contains(&self.judge.as_str()) => &self.judge,
            "same" => "fixed",
            other => other,
        }
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
    #[serde(default)]
    agent: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pending_owner: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pending_worker: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pending_agent: Option<String>,
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

#[derive(Clone, Copy)]
enum ManagedPrincipal { Owner, Worker, Agent }

impl ManagedPrincipal {
    fn id(self) -> &'static str { match self { Self::Owner => "management:owner", Self::Worker => "worker", Self::Agent => "agent" } }
    /// The server role each token is issued with. The agents' token is never the owner's (human).
    fn role(self) -> &'static str { match self { Self::Owner => "human", Self::Worker => "service", Self::Agent => "agent" } }
    fn current(self, s: &Secrets) -> &str { match self { Self::Owner => &s.owner, Self::Worker => &s.worker, Self::Agent => &s.agent } }
    fn pending(self, s: &Secrets) -> Option<&str> { match self { Self::Owner => s.pending_owner.as_deref(), Self::Worker => s.pending_worker.as_deref(), Self::Agent => s.pending_agent.as_deref() } }
    fn stage(self, s: &mut Secrets, token: String) { match self { Self::Owner => s.pending_owner = Some(token), Self::Worker => s.pending_worker = Some(token), Self::Agent => s.pending_agent = Some(token) } }
    fn complete(self, s: &mut Secrets, token: String) { match self {
        Self::Owner => { s.owner = token; s.pending_owner = None; }
        Self::Worker => { s.worker = token; s.pending_worker = None; }
        Self::Agent => { s.agent = token; s.pending_agent = None; }
    } }
}

fn managed_token_status(data: &[u8], id: &str) -> Result<bool, String> {
    #[derive(serde::Deserialize)]
    struct Status { principal_id: String, active: bool, needs_rotation: bool }
    let status: Status = serde_json::from_slice(data).map_err(|_| "local_token_status_failed")?;
    if status.principal_id != id || !status.active { return Err("local_token_revoked".into()); }
    Ok(status.needs_rotation)
}

fn rotate_managed_token(
    secrets: &mut Secrets, principal: ManagedPrincipal,
    status: &mut impl FnMut(&str, &str) -> Result<bool, String>,
    save: &impl Fn(&Secrets) -> Result<(), String>,
    rotate: &mut impl FnMut(&str, &str, &str) -> Result<(), String>,
) -> Result<bool, String> {
    let old = principal.current(secrets).to_owned();
    if principal.pending(secrets).is_none() {
        if !status(principal.id(), &old)? { return Ok(false); }
        principal.stage(secrets, secret()?);
        save(secrets)?;
    }
    let next = principal.pending(secrets).ok_or("local_token_pending_missing")?.to_owned();
    rotate(principal.id(), &old, &next)?;
    principal.complete(secrets, next);
    save(secrets)?;
    Ok(true)
}

fn secret() -> Result<String, String> {
    let mut buf = [0u8; 32];
    getrandom::fill(&mut buf).map_err(|e| e.to_string())?;
    Ok(buf.iter().map(|b| format!("{b:02x}")).collect())
}

pub(crate) fn write_private(path: &Path, data: &[u8]) -> std::io::Result<()> {
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

    fn update_test_dir() -> PathBuf {
        let dir = std::env::temp_dir().join(format!("arkism-update-lifecycle-{}", secret().unwrap()));
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn update_spec(dir: &Path) -> LaunchSpec {
        LaunchSpec { program: dir.join("inert-program").into_os_string(), args: vec!["--original".into()],
            env: BTreeMap::from([("ARKISM_URL".into(), "http://127.0.0.1:12345".into())]), cwd: dir.to_owned(), log: None, endpoint: None }
    }

    #[derive(Default)]
    struct ProcessObservations {
        alive: std::collections::BTreeSet<Role>,
        stopping: std::collections::BTreeSet<Role>,
        events: Vec<String>,
        busy: Option<Role>,
        pause_panic: Option<Role>,
        control_error: Option<Role>,
        resume_error: Option<Role>,
        stop_error: Option<Role>,
        job_error: Option<Role>,
        descendant_after_root: Option<Role>,
        query_error: Option<Role>,
        replay_error: Option<Role>,
        pg_running: bool,
        pg_stop_error: bool,
        generations: BTreeMap<Role,u64>,
        idle: BTreeMap<Role,(u64,u64)>,
        bootstrap_held: std::collections::BTreeSet<Role>,
        verified: BTreeMap<Role,u64>,
        idle_error: Option<Role>,
        recovery_pause_busy: Option<Role>,
        recovery_pause_error: Option<Role>,
        resumed_generations: Vec<(Role,u64)>,
        endpoint_probes: Vec<(Role,bool)>,
    }
    type Observations = Arc<Mutex<ProcessObservations>>;
    const FIXTURE_ROLES: [Role;5] = [Role::Postgres,Role::Server,Role::Worker,Role::Gateway,Role::Watcher];
    struct InertProcess { role: Role, observed: Observations, generation: u64 }
    impl InertProcess {
        fn lease(&self)->u64 {50+self.role as u64+100*self.generation}
    }
    impl runtime_process::StopEvidence for InertProcess {
        fn role_stopped(&mut self,lease:u64)->Result<(),String> {
            assert_eq!(lease,self.lease());
            let mut state = self.observed.lock().unwrap();
            state.events.push(format!("ack:{}",self.role.name()));
            state.stopping.insert(self.role);
            if state.stop_error == Some(self.role) { Err("inert_stop_receipt_unavailable".into()) } else { Ok(()) }
        }
        fn job_empty(&mut self)->Result<(),String> {
            let mut state=self.observed.lock().unwrap();
            state.events.push(format!("job0:{}",self.role.name()));
            if state.job_error == Some(self.role) { return Err("inert_job_query_failed".into()); }
            state.alive.remove(&self.role);
            Ok(())
        }
    }
    impl runtime_process::ProcessBoundary for InertProcess {
        fn live(&mut self)->Result<Option<u32>,String> {
            let state=self.observed.lock().unwrap();
            if state.query_error == Some(self.role) { return Err("inert_job_query_failed".into()); }
            if state.descendant_after_root == Some(self.role) { return runtime_process::checked_live(Some(0),1); }
            if state.alive.contains(&self.role) { runtime_process::checked_live(None,1) } else { runtime_process::checked_live(Some(0),0) }
        }
        fn pause(&mut self)->Result<Option<u64>,String> {
            let mut state=self.observed.lock().unwrap();
            state.events.push(format!("pause:{}",self.role.name()));
            if state.pause_panic == Some(self.role) {
                drop(state);
                panic!("inert pause panic");
            }
            if state.control_error == Some(self.role) { return Err("inert_receipt_unknown".into()); }
            if self.generation>0 {
                assert!(state.bootstrap_held.contains(&self.role),"replay admitted work before first pause");
                if state.recovery_pause_error==Some(self.role) {return Err("inert_recovery_pause_unknown".into());}
                if state.recovery_pause_busy==Some(self.role) {return Ok(None);}
            }
            if state.busy == Some(self.role) {return Ok(None);}
            state.idle.insert(self.role,(self.generation,self.lease()));
            Ok(Some(self.lease()))
        }
        fn verify_idle(&mut self,lease:u64)->Result<(),String> {
            {
                let state=self.observed.lock().unwrap();
                if state.idle_error==Some(self.role) || state.stopping.contains(&self.role) || state.idle.get(&self.role)!=Some(&(self.generation,lease)) {
                    return Err("inert_idle_unverified".into());
                }
            }
            if self.live()?.is_some(){return Err("inert_idle_owner_exited".into());}
            self.observed.lock().unwrap().verified.insert(self.role,self.generation);
            Ok(())
        }
        fn resume(&mut self,lease:u64)->Result<(),String> {
            assert_eq!(lease,self.lease());
            let mut state=self.observed.lock().unwrap();
            state.events.push(format!("resume:{}",self.role.name()));
            if state.resume_error == Some(self.role) || state.stopping.contains(&self.role) {return Err("inert_resume_unknown".into());}
            assert_eq!(state.idle.remove(&self.role),Some((self.generation,lease)));
            state.bootstrap_held.remove(&self.role);
            state.resumed_generations.push((self.role,self.generation));
            Ok(())
        }
        fn stop(&mut self,lease:Option<u64>)->Stopped {
            runtime_process::checked_role_stop(self,lease)
        }
    }
    struct InertPostgres(Observations);
    impl PostgresBoundary for InertPostgres {
        fn running(&mut self)->Result<bool,String> { Ok(self.0.lock().unwrap().pg_running) }
        fn execute(&mut self,start:bool,command:&Command)->Result<bool,String> {
            assert!(command.get_args().any(|a|a==if start {"start"} else {"stop"}));
            let mut state=self.0.lock().unwrap();
            state.events.push(if start {"replay:postgres"}else{"job0:postgres"}.into());
            if start && state.replay_error == Some(Role::Postgres) { return Err("inert_replay_failed".into()); }
            state.pg_running=start;
            if start {state.alive.insert(Role::Postgres);} else {state.alive.remove(&Role::Postgres);}
            if !start && state.pg_stop_error { return Err("inert_pg_ctl_stop_failed".into()); }
            Ok(true)
        }
    }
    fn inert_process(role:Role,observed:&Observations)->Process {
        let generation=observed.lock().unwrap().generations.get(&role).copied().unwrap_or(0);
        Process::Injected(Box::new(InertProcess{role,observed:Arc::clone(observed),generation}))
    }
    fn integrated_runtime_fixture()->(Runtime,Observations,PathBuf) {
        let dir=update_test_dir();
        let bundle=dir.join("programs");
        fs::create_dir_all(bundle.join("pgsql/bin")).unwrap();
        fs::create_dir_all(dir.join("pg")).unwrap();
        fs::write(bundle.join("pgsql/bin").join(exe("pg_ctl")),b"inert, never executed").unwrap();
        fs::write(dir.join("pg/PG_VERSION"),b"16").unwrap();
        fs::write(dir.join("pg/postmaster.pid"),format!("101\n{}\n123\n55491\n",fs::canonicalize(dir.join("pg")).unwrap().display())).unwrap();
        for name in ["runtime.json","agents.json","owner.token","models.json","agent-links.json"] {
            fs::write(dir.join(name),format!("original-{name}")).unwrap();
        }
        let rt=Runtime::new(Some(bundle),dir.clone()).isolated();
        let observed=Arc::new(Mutex::new(ProcessObservations {alive:FIXTURE_ROLES.into_iter().collect(),pg_running:true,..Default::default()}));
        let mut specs=BTreeMap::new();
        for role in FIXTURE_ROLES {
            let program=dir.join(format!("inert-{}",role.name()));
            let log=dir.join(format!("{}.log",role.name()));
            fs::write(&program,b"inert, never executed").unwrap();
            fs::write(&log,b"original-log").unwrap();
            let spec=LaunchSpec {program:program.into_os_string(),args:vec!["--original".into(),role.name().into()],
                env:BTreeMap::from([("USER_SETTING".into(),"original-value".into()),("ARKISM_URL".into(),"http://127.0.0.1:12345".into())]),
                cwd:dir.clone(),log:Some(log),endpoint:Some(format!("http://127.0.0.1:{}",12000+role as u16))};
            specs.insert(role,spec.clone());
            let child=ManagedChild {process:inert_process(role,&observed),role,spec};
            match role {
                Role::Postgres=>(),
                Role::Server|Role::Worker=>rt.children.lock().unwrap().push(child),
                Role::Gateway=>*rt.gateway.lock().unwrap()=Some(child),
                Role::Watcher=>*rt.watcher.lock().unwrap()=Some(child),
            }
        }
        let identity=postgres_identity(&dir.join("pg")).unwrap();
        let mut pg_spec=specs[&Role::Postgres].clone();
        pg_spec.args.push("start".into());
        *rt.postgres.lock().unwrap()=Some(OwnedPostgres {start:pg_spec,identity});
        *rt.postgres_boundary.lock().unwrap()=Some(Box::new(InertPostgres(Arc::clone(&observed))));
        let expected=specs.clone();
        let probes=Arc::clone(&observed);
        *rt.endpoint_boundary.lock().unwrap()=Some(Box::new(move |role,spec,held| {
            assert_eq!(spec.endpoint,expected[&role].endpoint);
            probes.lock().unwrap().endpoint_probes.push((role,held));
            Ok(true)
        }));
        let state=Arc::clone(&observed);
        *rt.process_factory.lock().unwrap()=Some(Box::new(move |role,spec,command| {
            assert!(command.get_envs().any(|(key,value)|key=="ARKISM_DESKTOP_RECOVERY" && value==Some(std::ffi::OsStr::new("1"))));
            let expected=&specs[&role];
            assert_eq!(spec.program,expected.program); assert_eq!(spec.args,expected.args);
            assert_eq!(spec.env,expected.env); assert_eq!(spec.cwd,expected.cwd); assert_eq!(spec.log,expected.log);
            assert_eq!(spec.endpoint,expected.endpoint);
            let mut observation=state.lock().unwrap();
            observation.events.push(format!("replay:{}",role.name()));
            if observation.replay_error==Some(role) {return Err(runtime_process::Failure {error:"inert_replay_failed".into(),process:None});}
            observation.alive.insert(role); observation.stopping.remove(&role);
            *observation.generations.entry(role).or_default()+=1;
            observation.idle.remove(&role);
            observation.verified.remove(&role);
            observation.bootstrap_held.insert(role);
            drop(observation);
            Ok(inert_process(role,&state))
        }));
        (rt,observed,dir)
    }
    fn assert_integrated_files_preserved(dir:&Path) {
        for name in ["runtime.json","agents.json","owner.token","models.json","agent-links.json"] {
            assert_eq!(fs::read(dir.join(name)).unwrap(),format!("original-{name}").as_bytes());
        }
    }
    #[test]
    fn update_integrated_finite_command_activity_survives_root_exit_until_job_zero() {
        let dir=update_test_dir();
        let rt=Arc::new(Runtime::new(None,dir.clone()).isolated());
        let (entered,started)=std::sync::mpsc::channel();
        let (empty,wait_empty)=std::sync::mpsc::channel();
        *rt.command_boundary.lock().unwrap()=Some(Box::new(move |_| {
            // The native capture core separately tests root-exited/descendant-live polling.
            entered.send(()).unwrap(); wait_empty.recv().unwrap();
            Ok(runtime_process::Output {code:0,stdout:vec![],stderr:vec![]})
        }));
        let worker=Arc::clone(&rt);
        let command=dir.join("never-executed");
        let thread=std::thread::spawn(move || worker.command_output(&mut Command::new(command)));
        started.recv().unwrap();
        assert_eq!(rt.update_handoff(||panic!("finite descendants are still active")),Err("update_runtime_busy".into()));
        empty.send(()).unwrap(); thread.join().unwrap().unwrap();
        assert_eq!(rt.update_handoff(||Err("inert_install_failure".into())),Err("inert_install_failure".into()));
        assert!(rt.activity().is_ok()); fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn update_integrated_recovery_marker_is_parent_owned_and_not_replayed_from_user_environment() {
        let (rt,state,dir)=integrated_runtime_fixture();
        let mut spec=rt.gateway.lock().unwrap().as_ref().unwrap().spec.clone();
        spec.env.insert("ARKISM_DESKTOP_RECOVERY".into(),"old-untrusted-marker".into());
        let markers=Arc::new(Mutex::new(Vec::new()));
        let recorded=Arc::clone(&markers);
        *rt.process_factory.lock().unwrap()=Some(Box::new(move |role,spec,command| {
            assert_eq!(spec.env.get(std::ffi::OsStr::new("ARKISM_DESKTOP_RECOVERY")).unwrap(),"old-untrusted-marker");
            let marker=command.get_envs().find_map(|(key,value)|(key=="ARKISM_DESKTOP_RECOVERY").then_some(value).flatten()).unwrap();
            recorded.lock().unwrap().push(marker.to_owned());
            Ok(inert_process(role,&state))
        }));
        let _normal=rt.spawn_spec(Role::Gateway,spec.clone(),false).unwrap();
        let _replay=rt.spawn_spec(Role::Gateway,spec,true).unwrap();
        assert_eq!(*markers.lock().unwrap(),vec![std::ffi::OsString::from("0"),std::ffi::OsString::from("1")]);
        assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn update_integrated_default_roles_recover_exactly_after_installer_launch_failure() {
        let (rt,state,dir)=integrated_runtime_fixture();
        let error=rt.update_handoff(|| {
            assert!(rt.activity().is_err()); assert!(rt.busy.try_lock().is_err());
            assert!(state.lock().unwrap().alive.is_empty());
            Err("update_start_failed: inert installer".into())
        }).unwrap_err();
        assert_eq!(error,"update_start_failed: inert installer");
        assert_eq!(state.lock().unwrap().alive,FIXTURE_ROLES.into_iter().collect());
        assert_eq!(state.lock().unwrap().events,vec!["pause:server","pause:worker","pause:gateway","pause:watcher","ack:watcher","job0:watcher","ack:gateway","job0:gateway",
            "ack:worker","job0:worker","ack:server","job0:server","job0:postgres","replay:postgres","replay:server","pause:server","replay:worker","pause:worker","replay:gateway","pause:gateway","replay:watcher","pause:watcher",
            "resume:server","resume:worker","resume:gateway","resume:watcher"]);
        assert!(rt.activity().is_ok()); assert_integrated_files_preserved(&dir);
        fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn update_installer_unverified_owner_keeps_checkpoint_and_blocks_reentry() {
        fn require_send<T: Send>() {}
        require_send::<PendingUpdate>();
        let (rt,state,dir)=integrated_runtime_fixture();
        let result=rt.update_installer_handoff(|| {
            assert!(rt.pending_installer.try_lock().is_err(), "storage is reserved before launch");
            assert!(rt.busy.try_lock().is_err());
            assert_eq!(rt.update_handoff(||panic!("reentrant launch")),Err("update_runtime_busy".into()));
            lifecycle::Launch::Unverified {
                owner:crate::update_installer::pending_test_owner(), error:"update_installer_unverified".into(),
            }
        });
        assert!(matches!(result,Err(InstallerHandoffFailure::Unverified(ref e)) if e=="update_installer_unverified"));
        assert!(state.lock().unwrap().alive.is_empty());
        assert!(state.lock().unwrap().events.iter().all(|e| !e.starts_with("replay:") && !e.starts_with("resume:")));
        assert!(rt.unverified.lock().unwrap().is_empty(), "installer is not a managed runtime job");
        assert!(rt.busy.try_lock().is_ok(), "the borrowed turn guard is not retained");
        {
            let slot=rt.pending_installer.lock().unwrap();
            let pending=slot.as_ref().unwrap();
            assert_eq!(pending._stopped.iter().map(|(role,_)|*role).collect::<Vec<_>>(),FIXTURE_ROLES.into_iter().rev().collect::<Vec<_>>());
            assert!(pending._role_leases.is_empty());
            assert_eq!(pending._error,"update_installer_unverified");
            assert_eq!(pending._installer.primary_error().causes[0].operation,"inert_pending_installer");
        }
        assert!(rt.activity().is_err());
        assert!(matches!(rt.update_installer_handoff(||panic!("pending owner cannot be overwritten")),Err(InstallerHandoffFailure::Unstarted(ref e)) if e=="update_installer_unverified"));
        assert_eq!(rt.update_handoff(||panic!("ordinary update cannot bypass pending owner")),Err("update_installer_unverified".into()));
        rt.stop_server();
        assert_eq!(rt.pending_installer.lock().unwrap().as_ref().unwrap()._installer.primary_error().causes[0].operation,"inert_pending_installer");
        assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn update_installer_unstarted_recovers_through_the_same_runtime_boundary() {
        let (rt,state,dir)=integrated_runtime_fixture();
        let result=rt.update_installer_handoff(|| lifecycle::Launch::Unstarted("inert_installer_unstarted".into()));
        assert!(matches!(result,Err(InstallerHandoffFailure::Unstarted(ref e)) if e=="inert_installer_unstarted"));
        assert_eq!(state.lock().unwrap().alive,FIXTURE_ROLES.into_iter().collect());
        assert_eq!(state.lock().unwrap().resumed_generations.len(),4);
        assert!(rt.pending_installer.lock().unwrap().is_none());
        assert!(rt.activity().is_ok());
        assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn update_installer_storage_errors_precede_launch_and_preserve_blocked_state() {
        let dir=update_test_dir();
        let rt=Runtime::new(None,dir.clone()).isolated();
        let slot=rt.pending_installer.lock().unwrap();
        assert!(matches!(rt.update_installer_handoff(||panic!("contended storage cannot launch")),Err(InstallerHandoffFailure::Unstarted(ref e)) if e=="update_runtime_busy"));
        drop(slot);
        assert!(rt.activity().is_ok());
        let panic=std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            let _slot=rt.pending_installer.lock().unwrap();
            panic!("inert storage poisoning");
        }));
        assert!(panic.is_err());
        assert!(matches!(rt.update_installer_handoff(||panic!("poisoned storage cannot launch")),Err(InstallerHandoffFailure::Unstarted(ref e)) if e=="update_installer_owner_storage_unavailable"));
        assert!(rt.activity().is_err());
        assert!(!rt.data_dir().join("runtime.json").exists());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn update_installer_pause_panic_drops_turn_without_resuming_or_replaying() {
        let (rt,state,dir)=integrated_runtime_fixture();
        state.lock().unwrap().pause_panic=Some(Role::Worker);
        let panic=std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            rt.update_installer_handoff(||panic!("pause panic must prevent launch"))
        }));
        assert!(panic.is_err());
        let observed=state.lock().unwrap();
        assert_eq!(observed.events,vec!["pause:server","pause:worker"]);
        assert_eq!(observed.alive,FIXTURE_ROLES.into_iter().collect());
        assert!(observed.idle.contains_key(&Role::Server));
        drop(observed);
        // Unwinding releases the borrowed turn guard; poison still records the panic.
        assert!(matches!(rt.busy.try_lock(),Err(std::sync::TryLockError::Poisoned(_))));
        assert!(rt.activity().is_err());
        assert!(rt.update_installer_handoff(||panic!("panic must keep later launch closed")).is_err());
        assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn update_integrated_busy_reopens_only_after_previous_idle_role_resumes() {
        let (rt,state,dir)=integrated_runtime_fixture();
        state.lock().unwrap().busy=Some(Role::Watcher);
        assert_eq!(rt.update_handoff(||panic!("busy work cannot launch installer")),Err("update_runtime_busy".into()));
        assert_eq!(state.lock().unwrap().events,vec!["pause:server","pause:worker","pause:gateway","pause:watcher","resume:server","resume:worker","resume:gateway"]);
        assert_eq!(state.lock().unwrap().alive,FIXTURE_ROLES.into_iter().collect());
        assert!(rt.activity().is_ok()); assert_integrated_files_preserved(&dir);
        fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn update_integrated_memoryd_busy_never_stops_any_role_or_launches() {
        for role in [Role::Server,Role::Worker] {
            let (rt,state,dir)=integrated_runtime_fixture();
            state.lock().unwrap().busy=Some(role);
            assert_eq!(rt.update_handoff(||panic!("active memory work cannot launch installer")),Err("update_runtime_busy".into()));
            let expected=if role==Role::Server {vec!["pause:server"]} else {vec!["pause:server","pause:worker","resume:server"]};
            assert_eq!(state.lock().unwrap().events,expected);
            assert_eq!(state.lock().unwrap().alive,FIXTURE_ROLES.into_iter().collect());
            assert!(rt.children.lock().unwrap().len()==2 && rt.activity().is_ok());
            assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
        }
    }
    #[test]
    fn update_integrated_memoryd_stop_requires_lease_and_retains_unknown_child() {
        for role in [Role::Server,Role::Worker] {
            let (rt,state,dir)=integrated_runtime_fixture();
            let mut backend=UpdateRuntime {rt:&rt,turn:None,leases:BTreeMap::new(),safe:true};
            let stopped=backend.stop(role);
            assert!(!stopped.confirmed);
            assert_eq!(stopped.error.as_deref(),Some("runtime_control_lease_missing"));
            assert!(state.lock().unwrap().events.is_empty());
            assert_eq!(rt.children.lock().unwrap().len(),2);
            state.lock().unwrap().control_error=Some(role);
            let error=rt.update_handoff(||panic!("unknown memoryd receipt cannot launch")).unwrap_err();
            assert!(error.contains("inert_receipt_unknown"));
            assert!(rt.activity().is_err());
            assert_eq!(rt.children.lock().unwrap().len(),2);
            assert_eq!(state.lock().unwrap().alive,FIXTURE_ROLES.into_iter().collect());
            assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
        }
    }
    #[test]
    fn update_all_four_child_roles_use_control_and_postgres_is_separate() {
        use crate::desktop_control::Role as Control;
        assert_eq!(control_role(Role::Postgres),None);
        for (role,expected) in [(Role::Server,Control::Server),(Role::Worker,Control::Worker),(Role::Gateway,Control::Gateway),(Role::Watcher,Control::Watcher)] {
            assert_eq!(control_role(role),Some(expected));
        }
    }
    #[test]
    fn update_integrated_unknown_receipt_or_failed_resume_keeps_gate_and_owners() {
        for failed_resume in [false,true] {
            let (rt,state,dir)=integrated_runtime_fixture();
            if failed_resume {
                state.lock().unwrap().busy=Some(Role::Watcher);
                state.lock().unwrap().resume_error=Some(Role::Gateway);
            } else { state.lock().unwrap().control_error=Some(Role::Watcher); }
            let error=rt.update_handoff(||panic!("unknown control cannot launch installer")).unwrap_err();
            assert!(error.contains(if failed_resume {"inert_resume_unknown"} else {"inert_receipt_unknown"}));
            assert_eq!(state.lock().unwrap().events,vec!["pause:server","pause:worker","pause:gateway","pause:watcher","resume:server","resume:worker","resume:gateway"]);
            assert!(rt.activity().is_err()); assert!(rt.gateway.lock().unwrap().is_some()); assert!(rt.watcher.lock().unwrap().is_some());
            assert_integrated_files_preserved(&dir); fs::remove_dir_all(dir).unwrap();
        }
    }
    #[test]
    fn update_integrated_partial_stop_restores_confirmed_subset_without_replacing_unknown_owner() {
        let (rt,state,dir)=integrated_runtime_fixture();
        state.lock().unwrap().stop_error=Some(Role::Gateway);
        let error=rt.update_handoff(||panic!("failed stop cannot launch installer")).unwrap_err();
        assert!(error.contains("inert_stop_receipt_unavailable"));
        let events=state.lock().unwrap().events.clone();
        assert!(events.contains(&"replay:watcher".into()));
        assert!(!events.contains(&"replay:gateway".into())); assert!(!events.contains(&"job0:gateway".into()));
        assert!(events.iter().all(|event| !event.starts_with("resume:")));
        assert!(rt.gateway.lock().unwrap().is_some()); assert!(rt.watcher.lock().unwrap().is_some());
        assert!(rt.activity().is_err()); assert_integrated_files_preserved(&dir); fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn update_integrated_postgres_stop_error_with_confirmed_stop_restores_all_roles() {
        let (rt,state,dir)=integrated_runtime_fixture();
        state.lock().unwrap().pg_stop_error=true;
        let error=rt.update_handoff(||panic!("stop error prevents installer launch")).unwrap_err();
        assert!(error.contains("inert_pg_ctl_stop_failed"));
        let events=state.lock().unwrap().events.clone();
        assert!(events.contains(&"replay:server".into())); assert!(events.contains(&"replay:postgres".into()));
        assert_eq!(state.lock().unwrap().alive,FIXTURE_ROLES.into_iter().collect());
        assert!(rt.activity().is_ok()); assert_integrated_files_preserved(&dir); fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn update_integrated_job_unknown_or_root_exit_with_descendants_never_loses_slot() {
        for mode in 0..3 {
            let (rt,state,dir)=integrated_runtime_fixture();
            match mode {0=>state.lock().unwrap().descendant_after_root=Some(Role::Gateway),1=>state.lock().unwrap().query_error=Some(Role::Gateway),_=>state.lock().unwrap().job_error=Some(Role::Watcher)}
            assert!(rt.update_handoff(||panic!("unconfirmed Job cannot launch installer")).is_err());
            assert!(rt.activity().is_err());
            if mode<2 {
                assert!(state.lock().unwrap().events.is_empty());
                let mut slot=rt.gateway.lock().unwrap();
                assert!(slot_alive(&mut slot).is_err()); assert!(slot.is_some());
            } else { assert!(rt.watcher.lock().unwrap().is_some()); }
            assert_integrated_files_preserved(&dir); fs::remove_dir_all(dir).unwrap();
        }
    }
    #[test]
    fn update_integrated_recovery_failure_preserves_both_errors_and_blocks_new_work() {
        for role in FIXTURE_ROLES {
            let (rt,state,dir)=integrated_runtime_fixture();
            state.lock().unwrap().replay_error=Some(role);
            let error=rt.update_handoff(||Err("update_start_failed: inert installer".into())).unwrap_err();
            assert!(error.contains("update_start_failed: inert installer")); assert!(error.contains("inert_replay_failed"));
            assert!(error.contains("update_runtime_recovery_failed")); assert!(rt.activity().is_err());
            assert_integrated_files_preserved(&dir); fs::remove_dir_all(dir).unwrap();
        }
    }
    #[test]
    fn update_integrated_replayed_roles_stay_held_until_every_role_is_verified() {
        let (rt,state,dir)=integrated_runtime_fixture();
        assert!(rt.update_handoff(||Err("inert_installer_failed".into())).is_err());
        let observed=state.lock().unwrap();
        let last_replay=observed.events.iter().rposition(|event|event.starts_with("replay:")).unwrap();
        let first_resume=observed.events.iter().position(|event|event.starts_with("resume:")).unwrap();
        assert!(first_resume>last_replay);
        assert_eq!(observed.resumed_generations,vec![(Role::Server,1),(Role::Worker,1),(Role::Gateway,1),(Role::Watcher,1)]);
        assert_eq!(observed.verified,BTreeMap::from([(Role::Server,1),(Role::Worker,1),(Role::Gateway,1),(Role::Watcher,1)]));
        assert!(observed.bootstrap_held.is_empty() && observed.idle.is_empty());
        let server_probes:Vec<_>=observed.endpoint_probes.iter().filter(|(role,_)| *role==Role::Server).collect();
        assert!(!server_probes[0].1 && server_probes.iter().skip(1).all(|(_,held)| *held));
        drop(observed);
        assert!(rt.activity().is_ok());
        assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn update_integrated_partial_replay_failure_never_releases_held_work() {
        for unknown_stop in [false,true] {
            let (rt,state,dir)=integrated_runtime_fixture();
            if unknown_stop {state.lock().unwrap().stop_error=Some(Role::Gateway);}
            else {state.lock().unwrap().replay_error=Some(Role::Worker);}
            let error=rt.update_handoff(||Err("inert_installer_failed".into())).unwrap_err();
            assert!(error.contains(if unknown_stop {"inert_stop_receipt_unavailable"} else {"inert_replay_failed"}));
            let observed=state.lock().unwrap();
            assert!(observed.events.iter().all(|event|!event.starts_with("resume:")));
            assert!(observed.resumed_generations.is_empty());
            assert!(!observed.bootstrap_held.is_empty());
            for role in &observed.bootstrap_held {assert!(observed.idle.contains_key(role));}
            drop(observed);
            assert!(rt.activity().is_err());
            assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
        }
    }
    #[test]
    fn update_integrated_recovery_pause_error_or_busy_retains_owner_without_release() {
        for busy in [false,true] {
            let (rt,state,dir)=integrated_runtime_fixture();
            if busy {state.lock().unwrap().recovery_pause_busy=Some(Role::Server);}
            else {state.lock().unwrap().recovery_pause_error=Some(Role::Server);}
            let error=rt.update_handoff(||Err("inert_installer_failed".into())).unwrap_err();
            assert!(error.contains(if busy {"update_runtime_recovery_not_held"} else {"inert_recovery_pause_unknown"}));
            assert!(state.lock().unwrap().events.iter().all(|event|!event.starts_with("resume:")));
            assert_eq!(rt.unverified.lock().unwrap().len(),1);
            assert!(rt.activity().is_err());
            assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
        }
    }
    #[test]
    fn update_integrated_resume_preflight_and_first_failure_preserve_later_leases() {
        for failure in [Role::Server,Role::Worker] {
            let (rt,state,dir)=integrated_runtime_fixture();
            state.lock().unwrap().resume_error=Some(failure);
            let error=rt.update_handoff(||Err("inert_installer_failed".into())).unwrap_err();
            assert!(error.contains("inert_resume_unknown"));
            let observed=state.lock().unwrap();
            assert_eq!(observed.verified.len(),4);
            assert!(observed.events.iter().all(|event|event!="resume:gateway" && event!="resume:watcher"));
            if failure==Role::Worker {assert_eq!(observed.resumed_generations,vec![(Role::Server,1)]);}
            else {assert!(observed.resumed_generations.is_empty());}
            assert!(observed.idle.contains_key(&Role::Gateway) && observed.idle.contains_key(&Role::Watcher));
            drop(observed);assert!(rt.activity().is_err());
            assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
        }
    }
    #[test]
    fn update_integrated_failed_final_idle_proof_releases_no_child() {
        let (rt,state,dir)=integrated_runtime_fixture();
        let observed=Arc::clone(&state);
        *rt.endpoint_boundary.lock().unwrap()=Some(Box::new(move |role,_spec,held| {
            if held && role==Role::Watcher {observed.lock().unwrap().idle_error=Some(Role::Server);}
            Ok(true)
        }));
        let error=rt.update_handoff(||Err("inert_installer_failed".into())).unwrap_err();
        assert!(error.contains("inert_idle_unverified"));
        let observed=state.lock().unwrap();
        assert!(observed.resumed_generations.is_empty());
        assert!(observed.events.iter().all(|event|!event.starts_with("resume:")));
        assert_eq!(observed.bootstrap_held.len(),4);
        assert_eq!(observed.idle.len(),4);
        drop(observed);
        assert!(rt.activity().is_err());
        assert_integrated_files_preserved(&dir);fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn update_recovery_endpoint_connects_saved_listener_without_http_business_request() {
        let dir=update_test_dir();
        let listener=TcpListener::bind("127.0.0.1:0").unwrap();
        let mut spec=update_spec(&dir);
        spec.endpoint=Some(format!("http://{}",listener.local_addr().unwrap()));
        let reader=std::thread::spawn(move || {
            let (mut stream,_)=listener.accept().unwrap();
            stream.set_read_timeout(Some(Duration::from_secs(1))).unwrap();
            let mut byte=[0u8;1];
            stream.read(&mut byte).unwrap()
        });
        assert!(recovery_endpoint_ready(Role::Server,&spec).unwrap());
        assert_eq!(reader.join().unwrap(),0);
        spec.endpoint=Some("http://foreign.invalid:1".into());
        assert!(recovery_endpoint_ready(Role::Server,&spec).is_err());
        fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn update_integrated_success_keeps_terminal_gate_closed() {
        let (rt,state,dir)=integrated_runtime_fixture();
        rt.update_handoff(|| {assert!(state.lock().unwrap().alive.is_empty());Ok(())}).unwrap();
        assert!(rt.activity().is_err()); assert!(state.lock().unwrap().events.iter().all(|e|!e.starts_with("replay:")));
        assert_integrated_files_preserved(&dir); fs::remove_dir_all(dir).unwrap();
    }
    #[test]
    fn update_integrated_failed_spawn_retains_its_native_owner() {
        let (rt,state,dir)=integrated_runtime_fixture();
        let error=rt.retain_failure(runtime_process::Failure {error:"native_start_and_cleanup_failed".into(),process:Some(inert_process(Role::Gateway,&state))});
        assert_eq!(error,"native_start_and_cleanup_failed"); assert_eq!(rt.unverified.lock().unwrap().len(),1);
        assert!(rt.activity().is_err()); assert!(rt.update_handoff(||panic!("retained unknown owner blocks launch")).is_err());
        assert_integrated_files_preserved(&dir); fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn update_lifecycle_stopped_runtime_does_not_initialize_or_rewrite_settings() {
        let dir = update_test_dir();
        // A stopped runtime must not parse/migrate even malformed old settings to update.
        let files = [("runtime.json", b"legacy-unread-settings".as_slice()), ("agents.json", b"server-routing-original"),
            ("models.json", b"original-model-settings")];
        for (name, bytes) in files { fs::write(dir.join(name), bytes).unwrap(); }
        for bundled in [false, true] {
            let rt = Runtime::new(bundled.then(|| dir.join("absent-bundle")), dir.clone()).isolated();
            let error = rt.update_handoff(|| {
                assert_eq!(rt.start().err().as_deref(), Some("update_runtime_busy"));
                assert!(rt.busy.try_lock().is_err(), "the exclusive turn is retained through launch");
                Err("update_start_failed: fixture".into())
            }).unwrap_err();
            assert_eq!(error, "update_start_failed: fixture");
            assert!(rt.activity().is_ok());
            for (name, bytes) in files { assert_eq!(fs::read(dir.join(name)).unwrap(), bytes); }
            assert!(!dir.join("owner.token").exists());
            assert!(!dir.join("pg").exists());
            assert!(!dir.join("logs").exists());
        }
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn update_lifecycle_busy_probe_and_terminal_paths_never_launch_unchecked() {
        let dir = update_test_dir();
        let rt = Runtime::new(None, dir.clone()).isolated();
        let turn = rt.busy.lock().unwrap();
        assert_eq!(rt.update_handoff(|| panic!("busy runtime cannot launch")), Err("update_runtime_busy".into()));
        drop(turn);
        assert!(rt.activity().is_ok());
        fs::create_dir(dir.join("pg")).unwrap();
        assert_eq!(rt.update_handoff(|| panic!("partial cluster cannot launch")), Err("update_postgres_state_unknown".into()));
        fs::write(dir.join("pg/PG_VERSION"), "16").unwrap();
        assert!(rt.update_handoff(|| panic!("unavailable PG probe cannot launch")).is_err());
        assert!(rt.activity().is_ok());
        fs::remove_dir_all(dir.join("pg")).unwrap();
        rt.update_handoff(|| Ok(())).unwrap();
        rt.shutdown(); // app-exit teardown is permitted without reopening the gate
        assert!(rt.activity().is_err());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn launch_capture_excludes_inherited_shell_pseudo_entries_but_preserves_overrides() {
        let dir = update_test_dir();
        let program = dir.join("inert-program");
        fs::write(&program, b"never executed").unwrap();
        let parent = vec![
            (OsString::from("=C:"), OsString::from("C:/shell-directory")),
            (OsString::from("=ExitCode"), OsString::from("1")),
            (OsString::from("ARKISM_TEST_KEEP"), OsString::from("parent")),
            (OsString::from("ARKISM_TEST_REMOVE"), OsString::from("remove")),
        ];
        let mut command = Command::new(&program);
        command.current_dir(&dir).env("ARKISM_TEST_KEEP", "override").env_remove("ARKISM_TEST_REMOVE");
        let spec = LaunchSpec::capture_with_environment(&mut command, None, None, parent).unwrap();
        assert_eq!(spec.env, BTreeMap::from([(OsString::from("ARKISM_TEST_KEEP"), OsString::from("override"))]));
        let replay = spec.command().unwrap();
        assert_eq!(replay.get_current_dir(), Some(dir.as_path()));
        assert!(replay.get_envs().all(|(key, _)| !key.as_encoded_bytes().starts_with(b"=")));
        // An explicit invalid name is not silently removed or made acceptable.
        let mut invalid = Command::new(&program);
        invalid.current_dir(&dir).env("bad=name", "explicit");
        let spec = LaunchSpec::capture_with_environment(&mut invalid, None, None, Vec::new()).unwrap();
        assert!(spec.env.iter().any(|(key, _)| key == "bad=name"));
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn update_lifecycle_launch_spec_replays_original_environment_arguments_and_endpoint() {
        let dir = update_test_dir();
        let program = dir.join("inert-program");
        let log = dir.join("existing.log");
        fs::write(&program, b"not executable and never executed").unwrap();
        fs::write(&log, b"old log").unwrap();
        let mut command = Command::new(&program);
        command.args(["--listen", "127.0.0.1:12345"]).env("ARKISM_UPDATE_TEST_VALUE", "original").current_dir(&dir);
        let spec = LaunchSpec::capture(&mut command, Some(log.clone()), Some("http://127.0.0.1:12345".into())).unwrap();
        command.arg("--later-change").env("ARKISM_UPDATE_TEST_VALUE", "changed");
        let replay = spec.command().unwrap();
        assert_eq!(replay.get_program(), program.as_os_str());
        assert_eq!(replay.get_args().collect::<Vec<_>>(), vec!["--listen", "127.0.0.1:12345"]);
        assert_eq!(replay.get_current_dir(), Some(dir.as_path()));
        assert!(replay.get_envs().any(|(k, v)| k == "ARKISM_UPDATE_TEST_VALUE" && v == Some(std::ffi::OsStr::new("original"))));
        assert_eq!(spec.endpoint.as_deref(), Some("http://127.0.0.1:12345"));
        assert_eq!(fs::read(&log).unwrap(), b"old log");
        fs::remove_file(log).unwrap();
        assert!(spec.command().is_err(), "no recovery log creation");
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn update_lifecycle_postgres_requires_direct_identity_and_never_adopts_external_running_pg() {
        let dir = update_test_dir();
        let identity = PostgresIdentity { pid: 123, started: 456, directory: dir.clone(), port: 12345 };
        let owned = OwnedPostgres { start: update_spec(&dir), identity: identity.clone() };
        assert!(owned_postgres(false, None, || panic!("stopped state needs no identity read")).unwrap().is_none());
        assert!(owned_postgres(true, None, || panic!("external PG cannot become owned")).is_err());
        assert!(owned_postgres(false, Some(owned.clone()), || panic!("stale record cannot become running")).is_err());
        assert!(owned_postgres(true, Some(owned.clone()), || Err("probe_fixture".into())).is_err());
        let mut replaced = identity.clone();
        replaced.started += 1;
        assert!(owned_postgres(true, Some(owned.clone()), || Ok(replaced)).is_err());
        assert!(owned_postgres(true, Some(owned), || Ok(identity)).unwrap().is_some());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn update_lifecycle_postgres_identity_reader_is_bounded_and_checks_cluster_and_port() {
        let dir = update_test_dir();
        let pid = dir.join("postmaster.pid");
        fs::write(&pid, format!("123\n{}\n456\n12345\n", dir.display())).unwrap();
        let read = postgres_identity(&dir).unwrap();
        assert_eq!((read.pid, read.started, read.port), (123, 456, 12345));
        for text in [" ".repeat(8193), "-1\n/other\n456\n12345\n".into(), format!("123\n{}\n456\n0\n", dir.display()), "null".into()] {
            fs::write(&pid, text).unwrap();
            assert!(postgres_identity(&dir).is_err());
        }
        fs::remove_dir_all(dir).unwrap();
    }

    struct FakeChild {
        kill: Result<(), String>, wait: Result<(), String>, probe: Result<bool, String>, waited: bool,
    }

    impl ChildControl for FakeChild {
        fn kill_checked(&mut self) -> Result<(), String> { self.kill.clone() }
        fn wait_checked(&mut self) -> Result<(), String> { self.waited = true; self.wait.clone() }
        fn stopped(&mut self) -> Result<bool, String> { self.probe.clone() }
    }

    #[test]
    fn update_lifecycle_checked_stop_keeps_unconfirmed_ownership_and_both_errors() {
        for confirmed in [false, true] {
            let mut child = FakeChild { kill: Err("kill_fixture".into()), wait: Ok(()), probe: Ok(confirmed), waited: false };
            let result = stop_child(&mut child);
            assert_eq!(result.confirmed, confirmed);
            assert_eq!(result.error.as_deref(), Some("kill_fixture"));
            assert!(!child.waited);
        }
        let mut child = FakeChild { kill: Ok(()), wait: Err("wait_fixture".into()), probe: Err("probe_fixture".into()), waited: false };
        let result = stop_child(&mut child);
        assert!(!result.confirmed);
        assert!(result.error.unwrap().contains("wait_fixture; probe_fixture"));
        assert!(child.waited);
        child.wait = Ok(());
        child.probe = Ok(true);
        let result = stop_child(&mut child);
        assert!(result.confirmed && result.error.is_none());
    }

    #[test]
    fn secrets_are_long_and_different() {
        let (a, b) = (secret().unwrap(), secret().unwrap());
        assert_eq!(a.len(), 64);
        assert_ne!(a, b);
    }

    #[test]
    fn managed_token_rotation_resumes_after_database_failure() {
        let dir = std::env::temp_dir().join(format!("arkism-token-test-{}", secret().unwrap()));
        fs::create_dir_all(&dir).unwrap();
        let rt = Runtime::new(None, dir.clone());
        let mut secrets = rt.secrets().unwrap();
        let old = secrets.owner.clone();
        let worker = secrets.worker.clone();
        let mut status = |_: &str, _: &str| Ok(true);
        let mut failed = |_: &str, _: &str, _: &str| Err("injected_database_failure".into());
        assert!(rotate_managed_token(&mut secrets, ManagedPrincipal::Owner, &mut status, &|s| rt.save(s), &mut failed).is_err());
        let mut persisted = rt.secrets().unwrap();
        assert_eq!(persisted.owner, old);
        assert!(persisted.pending_owner.is_some());
        let next = persisted.pending_owner.clone().unwrap();
        let mut no_status = |_: &str, _: &str| -> Result<bool,String> { panic!("pending intent must replay before status") };
        let mut succeed = |id: &str, previous: &str, token: &str| {
            assert_eq!(id, "management:owner"); assert_eq!(previous, old); assert_eq!(token, next); Ok(())
        };
        assert!(rotate_managed_token(&mut persisted, ManagedPrincipal::Owner, &mut no_status, &|s| rt.save(s), &mut succeed).unwrap());
        let saved = rt.secrets().unwrap();
        assert_eq!(saved.owner, next);
        assert_eq!(saved.worker, worker);
        assert!(saved.pending_owner.is_none());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn managed_token_rotation_recovers_after_final_save_failure() {
        use std::cell::RefCell;
        let dir = std::env::temp_dir().join(format!("arkism-token-test-{}", secret().unwrap()));
        fs::create_dir_all(&dir).unwrap();
        let rt = Runtime::new(None, dir.clone());
        let mut secrets = rt.secrets().unwrap();
        let old = secrets.worker.clone();
        let database = RefCell::new(old.clone());
        let saves = RefCell::new(0);
        let save = |s: &Secrets| {
            *saves.borrow_mut() += 1;
            if *saves.borrow() == 2 { Err("injected_settings_failure".into()) } else { rt.save(s) }
        };
        let mut rotate = |_: &str, previous: &str, next: &str| {
            assert_eq!(*database.borrow(), previous); *database.borrow_mut() = next.to_owned(); Ok(())
        };
        assert!(rotate_managed_token(&mut secrets, ManagedPrincipal::Worker, &mut |_,_| Ok(true), &save, &mut rotate).is_err());
        let mut persisted = rt.secrets().unwrap();
        assert_eq!(persisted.worker, old);
        assert_eq!(persisted.pending_worker.as_deref(), Some(database.borrow().as_str()));
        let mut replay = |_: &str, previous: &str, next: &str| {
            assert_eq!(previous, old); assert_eq!(*database.borrow(), next); Ok(())
        };
        assert!(rotate_managed_token(&mut persisted, ManagedPrincipal::Worker, &mut |_,_| panic!("unexpected status"), &|s| rt.save(s), &mut replay).unwrap());
        assert_eq!(rt.secrets().unwrap().worker, *database.borrow());
        assert!(rt.secrets().unwrap().pending_worker.is_none());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn managed_principals_are_issued_with_their_own_roles() {
        for (principal, id, role) in [(ManagedPrincipal::Owner, "management:owner", "human"), (ManagedPrincipal::Worker, "worker", "service"), (ManagedPrincipal::Agent, "agent", "agent")] {
            assert_eq!((principal.id(), principal.role()), (id, role));
        }
    }

    #[test]
    fn agents_get_the_agent_token_not_the_owner_token() {
        let dir = std::env::temp_dir().join(format!("arkism-agent-token-test-{}", secret().unwrap()));
        fs::create_dir_all(&dir).unwrap();
        let rt = Runtime::new(None, dir.clone());
        let secrets = rt.secrets().unwrap();
        assert_ne!(secrets.agent, secrets.owner);
        rt.write_agent_connection(Some("http://127.0.0.1:1"), Some(&secrets), None).unwrap();
        let conn: serde_json::Value = serde_json::from_str(&fs::read_to_string(dir.join("agents.json")).unwrap()).unwrap();
        let token_file = PathBuf::from(conn["token_file"].as_str().unwrap());
        assert_eq!(token_file, dir.join("agent.token"));
        assert_eq!(fs::read_to_string(&token_file).unwrap(), secrets.agent);
        assert_eq!(fs::read_to_string(dir.join("owner.token")).unwrap(), secrets.owner, "the app's own jobs keep the owner token");
        rt.write_agent_connection(None, None, None).unwrap();
        assert!(!token_file.exists() && !dir.join("owner.token").exists());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn settings_from_before_the_agent_principal_get_one_to_register() {
        let dir = std::env::temp_dir().join(format!("arkism-agent-upgrade-test-{}", secret().unwrap()));
        fs::create_dir_all(&dir).unwrap();
        fs::write(dir.join("runtime.json"), br#"{"admin":"a","init":"i","app":"p","owner":"o","worker":"w","pg_port":1,"api_port":2,"registered":true,"archive_key":"k"}"#).unwrap();
        let rt = Runtime::new(None, dir.clone());
        let secrets = rt.secrets().unwrap();
        assert_eq!(secrets.agent.len(), 64);
        assert!(secrets.registered && !secrets.agent_registered, "the owner and worker stay registered; the agent is still to register");
        assert_eq!(rt.secrets().unwrap().agent, secrets.agent, "the new token is saved before anything registers it");
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn managed_token_status_rejects_revoked_or_mismatched_identity() {
        assert!(managed_token_status(br#"{"principal_id":"worker","active":false,"needs_rotation":true}"#, "worker").is_err());
        assert!(managed_token_status(br#"{"principal_id":"other","active":true,"needs_rotation":false}"#, "worker").is_err());
        assert!(managed_token_status(br#"{}"#, "worker").is_err());
        assert!(!managed_token_status(br#"{"principal_id":"worker","active":true,"needs_rotation":false}"#, "worker").unwrap());
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
    fn data_moves_only_while_idle_and_agents_file_stays_put() {
        let dir = std::env::temp_dir().join(format!("arkism-move-test-{}", secret().unwrap()));
        let rt = Runtime::new(None, dir.join("default")).isolated().agents_file(dir.join("default").join("agents.json"));
        fs::create_dir_all(dir.join("busy").join("pg")).unwrap();
        fs::write(dir.join("busy").join("pg").join("postmaster.pid"), b"1").unwrap();
        assert_eq!(rt.move_data_dir(dir.join("busy")).unwrap_err(), "local_location_running", "a target with a running Postgres waits");
        assert_eq!(rt.data_dir(), dir.join("default"));
        rt.move_data_dir(dir.join("chosen")).unwrap();
        assert_eq!(rt.data_dir(), dir.join("chosen"));
        rt.write_agent_connection(None, None, None).unwrap();
        assert!(dir.join("default").join("agents.json").is_file() && !dir.join("chosen").join("agents.json").exists());
        fs::create_dir_all(dir.join("chosen").join("pg")).unwrap();
        fs::write(dir.join("chosen").join("pg").join("postmaster.pid"), b"1").unwrap();
        assert_eq!(rt.move_data_dir(dir.join("other")).unwrap_err(), "local_location_running");
        assert_eq!(rt.data_dir(), dir.join("chosen"));
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
    fn skill_routing_roles_validate_and_pick_the_judge() {
        let mut r = ModelRoles::default();
        assert_eq!((r.skill_judge.as_str(), r.skill_candidates, r.summary.as_str(), r.scanners.len()), ("same", 8, "basic", 3));
        assert_eq!(r.skill_judge(), "fixed"); // same + fixed 라우팅 판정 → rules
        r.judge = "clef".into();
        assert_eq!(r.skill_judge(), "clef");
        r.skill_judge = "jev".into();
        assert_eq!(r.skill_judge(), "jev");
        assert!(r.valid());
        for bad in [|r: &mut ModelRoles| r.skill_candidates = 0, |r: &mut ModelRoles| r.skill_candidates = 9,
            |r: &mut ModelRoles| r.skill_judge = "chat".into(), |r: &mut ModelRoles| r.summary = "chat".into(),
            |r: &mut ModelRoles| r.scanners.push("other".into()), |r: &mut ModelRoles| r.summary_url = "http://example.com/v1".into()] {
            let mut b = r.clone();
            bad(&mut b);
            assert!(!b.valid());
        }
        r.summary = "chat".into();
        r.summary_url = "http://127.0.0.1:11434/v1".into();
        r.summary_model = "qwen3:4b".into();
        assert!(r.valid());
        assert_eq!(summary_endpoint("http://127.0.0.1:11434/v1/"), "http://127.0.0.1:11434/v1/chat/completions");
        assert_eq!(summary_endpoint("https://x/v1/chat/completions"), "https://x/v1/chat/completions");
    }

    /// 스킬 요약 and 스킬 보안 검사 reach every host run (sync-skills, skill-checks).
    #[cfg(unix)]
    #[test]
    fn summary_model_and_scanners_reach_the_host_run() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!("arkism-skillenv-test-{}", secret().unwrap()));
        let client = dir.join("programs").join("integration").join("arkism");
        fs::create_dir_all(client.parent().unwrap()).unwrap();
        fs::write(&client, "#!/bin/sh\necho \"url=$ARKISM_SKILL_LLM_URL model=$ARKISM_SKILL_LLM_MODEL key=$(cat \"$ARKISM_SKILL_LLM_KEY_FILE\" 2>/dev/null) spector=${ARKISM_SKILLSPECTOR_BIN-unset} cisco=${ARKISM_CISCO_SCANNER_BIN-unset}\"\n").unwrap();
        fs::set_permissions(&client, fs::Permissions::from_mode(0o755)).unwrap();
        let rt = Runtime::new(Some(dir.join("programs")), dir.join("data"));
        fs::create_dir_all(dir.join("data")).unwrap();
        rt.secrets().unwrap();
        fs::write(dir.join("data").join("owner.token"), b"t").unwrap();
        let run = || String::from_utf8(rt.host_command(&[]).unwrap().output().unwrap().stdout).unwrap();
        assert_eq!(run().trim(), "url= model= key= spector=unset cisco=unset");
        let mut roles = rt.model_roles();
        roles.summary = "chat".into();
        roles.summary_url = "https://llm.example/v1".into();
        roles.summary_model = "qwen3.8-flash".into();
        roles.summary_key = "sk".into();
        roles.scanners = vec!["cisco".into()];
        rt.set_model_roles(roles).unwrap();
        assert_eq!(run().trim(), "url=https://llm.example/v1/chat/completions model=qwen3.8-flash key=sk spector= cisco=unset");
        let mut keep = rt.model_roles();
        keep.summary_key.clear(); // an empty field keeps the saved key
        rt.set_model_roles(keep).unwrap();
        assert!(run().contains("key=sk "));
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
