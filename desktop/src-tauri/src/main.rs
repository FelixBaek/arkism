// Arkism desktop: shows the bundled UI and forwards its API calls.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]
#![deny(clashing_extern_declarations)]

use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::{Arc, OnceLock};
use std::time::{Duration, Instant};

use sha2::{Digest, Sha256};
use tauri::menu::{Menu, MenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{Manager, RunEvent, WebviewWindowBuilder, WindowEvent};

mod agent_link;
mod agent_backends;
mod gateway_token;
mod general;
mod lifecycle;
#[cfg(windows)]
mod windows_process;
#[cfg_attr(not(windows), allow(dead_code))] // Windows-only paths; other builds compile them for the unit tests
mod windows_job;
#[cfg_attr(not(windows), allow(dead_code))] // Windows-only paths; other builds compile them for the unit tests
mod desktop_control;
#[cfg_attr(not(windows), allow(dead_code))] // Windows-only paths; other builds compile them for the unit tests
mod runtime_process;
mod runtime;
#[cfg(any(windows, test))]
mod app_autostart;
// Windows-only paths, and shared with tests/windows_update_installer_identity.rs where each test crate uses part of it.
#[cfg_attr(any(test, not(windows)), allow(dead_code))]
mod update_installer;
mod update_manifest;
#[cfg(windows)]
mod log_retention;
use update_manifest::{Latest, parse_version};
mod ssh_tunnel;
mod ssh_exec;
mod ssh_private;
mod ssh_identity;
mod storage_location;
mod work_handoff;
mod popout;

// Build-time settings, read from desktop/local.env by build-windows.sh. A build without them
// talks to a local server and has no update feed. The WebView can read them (app_defaults)
// but never change them, so it cannot point downloads elsewhere.
const DEFAULT_SERVER: &str = match option_env!("ARKISM_DEFAULT_SERVER") {
    Some(value) => value,
    None => "http://127.0.0.1:8091",
};
const SSH_HOST: &str = match option_env!("ARKISM_SSH_HOST") {
    Some(value) => value,
    None => "localhost",
};
const FEED: Option<&str> = option_env!("ARKISM_UPDATE_FEED");
// The scope a server connection opens first (until the user changes it in Settings > Advanced).
const DEFAULT_WORKSPACE: &str = match option_env!("ARKISM_DEFAULT_WORKSPACE") {
    Some(value) => value,
    None => "default",
};
const DEFAULT_PROJECT: &str = match option_env!("ARKISM_DEFAULT_PROJECT") {
    Some(value) => value,
    None => "default",
};

#[derive(serde::Serialize)]
struct Defaults {
    server: &'static str,
    ssh_host: &'static str,
    updates: bool,
    /// This build carries the built-in Arkism (Postgres and server) and can run without a server.
    local: bool,
    workspace: &'static str,
    project: &'static str,
}

#[tauri::command]
fn app_defaults(rt: tauri::State<Arc<runtime::Runtime>>) -> Defaults {
    Defaults { server: DEFAULT_SERVER, ssh_host: SSH_HOST, updates: FEED.is_some(), local: rt.available(), workspace: DEFAULT_WORKSPACE, project: DEFAULT_PROJECT }
}

/// 저장 위치: the folder the built-in Arkism uses now, the one the next start uses (they differ
/// after a change while it runs) and the default.
#[derive(serde::Serialize)]
struct Location {
    path: std::path::PathBuf,
    next: std::path::PathBuf,
    default: std::path::PathBuf,
    /// The default folder already holds data, which a new folder would leave behind (not move).
    default_has_data: bool,
}

fn location(app: &tauri::AppHandle, rt: &runtime::Runtime) -> Result<Location, String> {
    let app_dir = app.path().app_local_data_dir().map_err(|e| e.to_string())?;
    let default = app_dir.join("runtime");
    Ok(Location { path: rt.data_dir(), next: storage_location::read(&app_dir).unwrap_or_else(|| default.clone()), default_has_data: storage_location::has_data(&default), default })
}

#[tauri::command]
fn local_location(app: tauri::AppHandle, rt: tauri::State<Arc<runtime::Runtime>>) -> Result<Location, String> {
    location(&app, &rt)
}

/// Remembers the chosen folder (None: the default) and uses it right away when nothing runs yet,
/// as on the first start; otherwise from the next app start. Existing data is never moved.
#[tauri::command]
fn local_location_set(app: tauri::AppHandle, rt: tauri::State<Arc<runtime::Runtime>>, path: Option<String>) -> Result<Location, String> {
    let app_dir = app.path().app_local_data_dir().map_err(|e| e.to_string())?;
    let default = app_dir.join("runtime");
    // The default folder holds app files (agents.json, the autostart marker) even without data.
    let chosen = path.as_deref().filter(|p| std::path::Path::new(p.trim()) != default).map(storage_location::check).transpose()?;
    let previous = storage_location::read(&app_dir);
    storage_location::write(&app_dir, &default, chosen.as_deref())?;
    let target = chosen.unwrap_or_else(|| default.clone());
    if target != rt.data_dir() {
        match rt.move_data_dir(target) {
            // Running: keeps the folder it started with; `next` shows the change.
            Err(e) if e != "local_location_running" => {
                let _ = storage_location::write(&app_dir, &default, previous.as_deref());
                return Err(e);
            }
            _ => {}
        }
    }
    location(&app, &rt)
}

/// Starts (or reuses) the built-in Arkism and returns its address and owner token.
#[tauri::command]
async fn local_start(rt: tauri::State<'_, Arc<runtime::Runtime>>) -> Result<runtime::Started, String> {
    let rt = Arc::clone(&rt);
    let started = tauri::async_runtime::spawn_blocking({
        let rt = Arc::clone(&rt);
        move || rt.start()
    })
    .await
    .map_err(|e| e.to_string())??;
    // While the app runs (window or tray): right away, then every minute, what the server's timers
    // would do — agents, sessions, skills and the capture queues.
    static WORK: OnceLock<()> = OnceLock::new();
    WORK.get_or_init(|| {
        std::thread::spawn(move || {
            let mut tick = 0u64;
            loop {
                rt.background_work(tick);
                std::thread::sleep(Duration::from_secs(60));
                tick = tick.wrapping_add(1);
            }
        });
    });
    Ok(started)
}

/// 설정 › 데이터: back up the built-in Arkism now.
#[tauri::command]
async fn local_backup(rt: tauri::State<'_, Arc<runtime::Runtime>>) -> Result<runtime::Backup, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || rt.backup()).await.map_err(|e| e.to_string())?
}

/// The backups in `Documents/Arkism Backups`, newest first, and that folder.
#[tauri::command]
fn local_backups() -> Result<serde_json::Value, String> {
    let folder = runtime::Runtime::backup_dir()?;
    Ok(serde_json::json!({ "folder": folder, "backups": runtime::Runtime::backups()? }))
}

/// Replaces the built-in Arkism's data with a backup (the current data is backed up first).
#[tauri::command]
async fn local_restore(rt: tauri::State<'_, Arc<runtime::Runtime>>, name: String) -> Result<runtime::Restored, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || rt.restore(&name)).await.map_err(|e| e.to_string())?
}

/// Deletes all of the built-in Arkism's data; backups stay.
#[tauri::command]
async fn local_erase(rt: tauri::State<'_, Arc<runtime::Runtime>>) -> Result<(), String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || rt.erase()).await.map_err(|e| e.to_string())?
}

/// 설정 › 고급 › 수집: whether Arkism captures, and the folders it leaves alone.
#[tauri::command]
fn capture_settings(rt: tauri::State<Arc<runtime::Runtime>>) -> Result<runtime::CaptureSettings, String> {
    rt.capture_settings()
}

#[tauri::command]
fn set_capture_settings(rt: tauri::State<Arc<runtime::Runtime>>, settings: runtime::CaptureSettings) -> Result<(), String> {
    rt.set_capture_settings(&settings)
}

/// 설정 › 고급 › 데이터: whether the built-in Arkism searches by meaning (Ollama) or by keywords.
#[tauri::command]
fn search_mode(rt: tauri::State<Arc<runtime::Runtime>>) -> String {
    rt.search_mode()
}

/// 설정 › 일반: whether Arkism starts (in the tray) when the user signs in.
#[tauri::command]
fn autostart_get(app: tauri::AppHandle) -> Result<bool, String> {
    read_autostart(&app)
}

#[tauri::command]
fn autostart_set(app: tauri::AppHandle, rt: tauri::State<Arc<runtime::Runtime>>, on: bool) -> Result<(), String> {
    let _activity = rt.activity()?;
    write_autostart(&app, on)
}

fn read_autostart(_app: &tauri::AppHandle) -> Result<bool, String> {
    #[cfg(windows)] { app_autostart::is_enabled() }
    #[cfg(not(windows))] {
        use tauri_plugin_autostart::ManagerExt;
        _app.autolaunch().is_enabled().map_err(|e| e.to_string())
    }
}

fn write_autostart(_app: &tauri::AppHandle, on: bool) -> Result<(), String> {
    #[cfg(windows)] { app_autostart::set_enabled(on) }
    #[cfg(not(windows))] {
        use tauri_plugin_autostart::ManagerExt;
        let launcher = _app.autolaunch();
        if on { launcher.enable() } else { launcher.disable() }.map_err(|e| e.to_string())
    }
}

/// Imports the agents' own memory files as review candidates (dry run: count only).
#[tauri::command]
async fn import_agent_memories(rt: tauri::State<'_, Arc<runtime::Runtime>>, dry_run: bool) -> Result<serde_json::Value, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || rt.import_agent_memories(dry_run)).await.map_err(|e| e.to_string())?
}

/// 설정 › 고급 › 진단: checks (and with `fix`, safe repairs) of this PC's Arkism setup.
#[tauri::command]
async fn doctor(rt: tauri::State<'_, Arc<runtime::Runtime>>, fix: bool) -> Result<serde_json::Value, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || rt.doctor(fix)).await.map_err(|e| e.to_string())?
}

/// 설정 › 고급 › 역할별 모델. The Cloudflare token never goes back to the page: only whether one is saved.
#[tauri::command]
fn model_roles(rt: tauri::State<Arc<runtime::Runtime>>) -> serde_json::Value {
    let roles = rt.model_roles();
    let mut v = serde_json::to_value(&roles).unwrap_or_default();
    for (key, secret) in [("cloudflare_token", &roles.cloudflare_token), ("jev_key", &roles.jev_key), ("picker_key", &roles.picker_key), ("summary_key", &roles.summary_key)] {
        v[key] = serde_json::Value::String(String::new());
        v[format!("{key}_saved")] = serde_json::Value::Bool(!secret.is_empty());
    }
    v
}

#[tauri::command]
async fn set_model_roles(rt: tauri::State<'_, Arc<runtime::Runtime>>, roles: runtime::ModelRoles) -> Result<(), String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || rt.set_model_roles(roles)).await.map_err(|e| e.to_string())?
}

/// 라우팅 › 에이전트 연결: the built-in gateway's address and which agents point at it.
#[tauri::command]
fn gateway_info(rt: tauri::State<Arc<runtime::Runtime>>) -> Result<serde_json::Value, String> {
    let home = rt.home().ok_or("local_unavailable")?;
    let url = rt.gateway_address();
    Ok(serde_json::json!({ "url": url, "agents": agent_link::status(home, url.as_deref()) }))
}

/// Connects an agent (Claude Code, Codex, Gemini CLI) to the gateway, starting it if needed, or disconnects it.
#[tauri::command]
fn agent_link_set(rt: tauri::State<Arc<runtime::Runtime>>, agent: String, on: bool) -> Result<serde_json::Value, String> {
    let _activity = rt.activity()?;
    let home = rt.home().ok_or("local_unavailable")?;
    let url = if on { Some(rt.ensure_gateway()?) } else { None };
    agent_link::set(home, &rt.data_dir().join("agent-links.json"), &agent, url.as_deref())?;
    gateway_info(rt)
}

/// Opens the backup folder in the file manager.
#[tauri::command]
fn open_backups(rt: tauri::State<Arc<runtime::Runtime>>) -> Result<(), String> {
    let _activity = rt.activity()?;
    let folder = runtime::Runtime::backup_dir()?;
    std::fs::create_dir_all(&folder).map_err(|e| e.to_string())?;
    let opener = if cfg!(windows) { "explorer" } else if cfg!(target_os = "macos") { "open" } else { "xdg-open" };
    std::process::Command::new(opener).arg(&folder).spawn().map(|_| ()).map_err(|e| e.to_string())
}

/// 서버에 연결: the SSH address is all the user types. The app reads the server's connection
/// details over that login, forwards its API here, and points the PC agents at the same address.
#[tauri::command]
async fn ssh_connect(rt: tauri::State<'_, Arc<runtime::Runtime>>, form: SshForm) -> Result<ssh_tunnel::Connected, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || {
        let _activity = rt.activity()?;
        let target = ssh_tunnel::target_from_form(&form.into())?;
        let c = ssh_tunnel::connect(target, rt.gate(), ssh_identity::Store::new(&rt.data_dir()))?;
        // Stop the built-in Arkism's agents file pointing at this PC; capture follows the server.
        rt.agents_use_server(&c.ssh_host, c.ssh_port, c.ssh_key.as_deref(), &c.pin)?;
        Ok(c)
    })
    .await
    .map_err(|e| e.to_string())?
}

#[derive(serde::Deserialize)]
struct SshForm {
    host: String,
    user: Option<String>,
    port: Option<u16>,
    key: Option<String>,
    bin: Option<String>,
    auth: Option<String>,
    password: Option<String>,
}

impl From<SshForm> for ssh_tunnel::Form {
    fn from(f: SshForm) -> Self {
        ssh_tunnel::Form { host: f.host, user: f.user, port: f.port, key: f.key, bin: f.bin, auth: f.auth, password: f.password }
    }
}

/// SSH 테스트: logs in and asks the server's Arkism for its details, without connecting.
#[tauri::command]
async fn ssh_test(rt: tauri::State<'_, Arc<runtime::Runtime>>, form: SshForm) -> Result<serde_json::Value, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || {
        let _activity = rt.activity()?;
        let target = ssh_tunnel::target_from_form(&form.into())?;
        let (api, workspace) = ssh_tunnel::connect_info_for(&target, &ssh_identity::Store::new(&rt.data_dir()))?;
        Ok(serde_json::json!({ "host": target.host, "api": api, "workspace_id": workspace }))
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Collects the target's host key without offering target login credentials.
#[tauri::command]
async fn ssh_host_key_check(rt: tauri::State<'_, Arc<runtime::Runtime>>, form: Option<SshForm>, host: Option<String>, force: Option<bool>) -> Result<ssh_identity::Review, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || {
        let _activity = rt.activity()?;
        let target = match (form, host) {
            (Some(form), None) => ssh_tunnel::target_from_form(&form.into())?,
            (None, Some(host)) => ssh_tunnel::target_for_host(&host)?,
            _ => return Err("ssh_address_invalid".into()),
        };
        ssh_identity::Store::new(&rt.data_dir()).inspect(&target, force.unwrap_or(false))
    }).await.map_err(|_| "ssh_host_key_probe_failed".to_string())?
}

#[tauri::command]
async fn ssh_host_key_approve(rt: tauri::State<'_, Arc<runtime::Runtime>>, challenge: String, replace: bool) -> Result<ssh_identity::Review, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || {
        let _activity = rt.activity()?;
        ssh_identity::Store::new(&rt.data_dir()).approve(&challenge, replace)
    }).await.map_err(|_| "ssh_host_key_probe_failed".to_string())?
}

#[tauri::command]
fn ssh_host_key_cancel(rt: tauri::State<Arc<runtime::Runtime>>, challenge: String) {
    ssh_identity::Store::new(&rt.data_dir()).cancel(&challenge);
}

/// 인증 키 파일 choices: the private keys in ~/.ssh.
#[tauri::command]
fn ssh_keys() -> Vec<String> {
    ssh_tunnel::list_keys()
}

/// 호스트 choices: the Host aliases in ~/.ssh/config.
#[tauri::command]
fn ssh_hosts() -> Vec<String> {
    ssh_tunnel::list_hosts()
}

#[tauri::command]
fn ssh_disconnect(rt: tauri::State<Arc<runtime::Runtime>>) -> Result<(), String> {
    ssh_tunnel::forget_current();
    ssh_tunnel::stop(&rt.gate())
}

/// Runs the bundled agent client with one setup command (`install-arkism`).
fn run_agent_setup(rt: &runtime::Runtime, command: &'static str) -> Result<String, String> {
    let _activity = rt.activity()?;
    let client = rt.agent_client().ok_or("agents_connect_unavailable")?;
    let mut cmd = std::process::Command::new(client);
    cmd.arg(command).stdin(std::process::Stdio::null());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        cmd.creation_flags(0x0800_0000);
    }
    let out = rt.command_output(&mut cmd)?;
    if out.code == 0 {
        Ok(String::from_utf8_lossy(&out.stdout).into_owned())
    } else {
        Err(format!("agents_setup_failed: {}", String::from_utf8_lossy(&out.stderr).trim()))
    }
}

/// 설정 › 연결 › 모델 중계기: whether this PC has a usable gateway token (never the value itself).
#[tauri::command]
fn gateway_token_status() -> Result<gateway_token::Status, String> {
    gateway_token::status()
}

#[tauri::command]
fn agent_backends() -> Result<Vec<agent_backends::Backend>, String> {
    agent_backends::list()
}

/// 다시 만들기: a new token, then install-arkism puts it in the agents' settings (Claude Code headers).
#[tauri::command]
async fn gateway_token_regenerate(rt: tauri::State<'_, Arc<runtime::Runtime>>) -> Result<serde_json::Value, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || {
        let _activity = rt.activity()?;
        gateway_token::regenerate()?;
        let agents_updated = run_agent_setup(&rt, "install-arkism").is_ok();
        Ok(serde_json::json!({ "status": gateway_token::status()?, "agents_updated": agents_updated }))
    })
    .await
    .map_err(|e| e.to_string())?
}

/// 설정 › 모델 설정 refresh: a fresh usage reading of every account, then the new report.
#[tauri::command]
async fn providers_refresh(rt: tauri::State<'_, Arc<runtime::Runtime>>) -> Result<(), String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || rt.refresh_providers()).await.map_err(|e| e.to_string())?
}

fn show_main(app: &tauri::AppHandle) {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.unminimize();
        let _ = w.show();
        let _ = w.set_focus();
    }
}

/// Really quit: stop the built-in Arkism first so Postgres shuts down cleanly.
fn quit(app: &tauri::AppHandle) {
    let rt = app.state::<Arc<runtime::Runtime>>();
    let Ok(_activity) = rt.activity() else { return };
    rt.stop();
    app.exit(0);
}

#[tauri::command]
async fn git_op(rt: tauri::State<'_, Arc<runtime::Runtime>>, host: String, request: serde_json::Value) -> Result<serde_json::Value, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || { let _activity = rt.activity()?; work_handoff::git(&host, request, &ssh_identity::Store::new(&rt.data_dir())) })
        .await.map_err(|_| "git_unavailable".to_string())?
}

#[tauri::command]
async fn skill_execution(rt: tauri::State<'_, Arc<runtime::Runtime>>, host: String, request: serde_json::Value) -> Result<serde_json::Value, String> {
    let rt = Arc::clone(&rt);
    tauri::async_runtime::spawn_blocking(move || { let _activity = rt.activity()?; work_handoff::skill_execution(&host, request, &ssh_identity::Store::new(&rt.data_dir())) })
        .await.map_err(|_| "skill_execution_unavailable".to_string())?
}

#[derive(serde::Serialize)]
struct Reply {
    status: u16,
    body: String,
}

/// Only the Arkism API surface the UI uses; anything else is refused here,
/// so a script running in the WebView cannot turn this into a general proxy.
fn allowed(method: &str, path: &str) -> bool {
    let api = path.starts_with("/v1/") || path == "/readyz" || path.starts_with("/readyz?");
    // DELETE only for 기억 잊기 (DELETE /v1/memories/{id}), 워크플로우 지우기 (DELETE /v1/crew/{id}) and the
    // gallery's 삭제 (DELETE /v1/prompts/{id}, /v1/design_systems/{id}); every other change goes through POST.
    // desktop/ui/delete-allowlist.test.js checks every DELETE the UI sends is listed here.
    let one_id = |prefix: &str| path.strip_prefix(prefix).is_some_and(|id| !id.is_empty() && !id.contains('/') && !id.contains('?'));
    let delete = method == "DELETE"
        && (path.starts_with("/v1/memories/") || one_id("/v1/crew/") || one_id("/v1/prompts/") || one_id("/v1/design_systems/"));
    api && !path.contains("..") && (method == "GET" || method == "POST" || delete)
}

fn valid_base(base: &str) -> bool {
    (base.starts_with("http://") || base.starts_with("https://")) && !base.ends_with('/') && !base.contains('?') && !base.contains('#')
}

fn api_agent() -> &'static ureq::Agent {
    static AGENT: OnceLock<ureq::Agent> = OnceLock::new();
    // Error statuses are replies the UI reads, not transport failures.
    AGENT.get_or_init(|| ureq::Agent::config_builder().timeout_global(Some(Duration::from_secs(30))).http_status_as_error(false).build().into())
}

#[tauri::command]
async fn api(rt: tauri::State<'_, Arc<runtime::Runtime>>, base: String, token: String, method: String, path: String, body: Option<String>) -> Result<Reply, String> {
    api_with_gate(rt.gate(), base, token, method, path, body).await
}

async fn api_with_gate(gate: Arc<lifecycle::Gate>, base: String, token: String, method: String, path: String, body: Option<String>) -> Result<Reply, String> {
    if !valid_base(&base) {
        return Err("server_address_invalid".into());
    }
    if !allowed(&method, &path) {
        return Err("path_not_allowed".into());
    }
    tauri::async_runtime::spawn_blocking(move || {
        let _activity = gate.activity()?;
        let req = ureq::http::Request::builder()
            .method(method.as_str())
            .uri(format!("{base}{path}"))
            .header("Authorization", format!("Bearer {token}"))
            .header("Content-Type", "application/json");
        let res = match body {
            Some(b) => req.body(b).map_err(|e| e.to_string()).and_then(|r| api_agent().run(r).map_err(|e| format!("server_unreachable: {e}"))),
            None => req.body(()).map_err(|e| e.to_string()).and_then(|r| api_agent().run(r).map_err(|e| format!("server_unreachable: {e}"))),
        };
        let mut r = res?;
        let status = r.status().as_u16();
        let body = r.body_mut().read_to_string().map_err(|e| e.to_string())?;
        Ok(Reply { status, body })
    })
    .await
    .map_err(|e| e.to_string())?
}

#[cfg(test)]
fn pct_encode(s: &str) -> String {
    s.bytes()
        .map(|b| match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => (b as char).to_string(),
            _ => format!("%{b:02X}"),
        })
        .collect()
}

fn pct_decode(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        let hex = b.get(i + 1..i + 3).and_then(|h| std::str::from_utf8(h).ok()).and_then(|h| u8::from_str_radix(h, 16).ok());
        match (b[i], hex) {
            (b'%', Some(v)) => {
                out.push(v);
                i += 3;
                continue;
            }
            (b'+', _) => out.push(b' '),
            (c, _) => out.push(c),
        }
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// The query of the browser's redirect: GET /callback?code=..&state=.. HTTP/1.1
fn callback_params(request_line: &str) -> Vec<(String, String)> {
    let target = request_line.split_whitespace().nth(1).unwrap_or("");
    let query = target.split_once('?').map(|(_, q)| q).unwrap_or("");
    query
        .split('&')
        .filter_map(|kv| kv.split_once('='))
        .map(|(k, v)| (k.to_string(), pct_decode(v)))
        .collect()
}

fn open_browser(url: &str) -> std::io::Result<()> {
    #[cfg(windows)]
    let mut cmd = std::process::Command::new("rundll32");
    #[cfg(windows)]
    cmd.args(["url.dll,FileProtocolHandler", url]);
    #[cfg(target_os = "macos")]
    let mut cmd = std::process::Command::new("open");
    #[cfg(all(unix, not(target_os = "macos")))]
    let mut cmd = std::process::Command::new("xdg-open");
    #[cfg(unix)]
    cmd.arg(url);
    cmd.spawn().map(|_| ())
}

/// Waits on a loopback listener for the browser's redirect whose `state` matches, answers it
/// with DONE_PAGE, and returns the request target (path and query). Stray requests get a 404.
fn wait_redirect(listener: &TcpListener, state: &str, timeout: Duration) -> Result<String, String> {
    listener.set_nonblocking(true).map_err(|e| e.to_string())?;
    let deadline = Instant::now() + timeout;
    loop {
        if Instant::now() > deadline {
            return Err("login_timeout".into());
        }
        let (mut stream, _) = match listener.accept() {
            Ok(c) => c,
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                std::thread::sleep(Duration::from_millis(200));
                continue;
            }
            Err(e) => return Err(e.to_string()),
        };
        stream.set_nonblocking(false).ok();
        stream.set_read_timeout(Some(Duration::from_secs(5))).ok();
        let mut line = String::new();
        BufReader::new(&stream).read_line(&mut line).ok();
        let params = callback_params(&line);
        if !params.iter().any(|(k, v)| k == "state" && v == state) {
            // A stray request (favicon, another tab): answer and keep waiting.
            let _ = stream.write_all(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n");
            continue;
        }
        let page = format!("HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}", DONE_PAGE.len(), DONE_PAGE);
        let _ = stream.write_all(page.as_bytes());
        return Ok(line.split_whitespace().nth(1).unwrap_or("").to_string());
    }
}

const DONE_PAGE: &str = "<!doctype html><meta charset=utf-8><title>Arkism</title><body style=\"font:16px system-ui;background:#232221;color:#f2f0ec;display:grid;place-items:center;height:90vh\"><p>로그인했어요. 이 창을 닫고 Arkism으로 돌아가세요.</p>";

/// MCP sign-in for an agent CLI running on the server: open its sign-in address in the
/// browser and catch the redirect on this PC's loopback, on the port the CLI chose. The
/// returned path and query go back to the server, which replays them where the CLI waits.
fn mcp_login_blocking(auth_url: &str) -> Result<String, String> {
    if !auth_url.starts_with("https://") || auth_url.len() > 8000 || auth_url.contains(char::is_whitespace) {
        return Err("login_address_invalid".into());
    }
    let params = callback_params(&format!("GET {auth_url} HTTP/1.1"));
    let get = |k: &str| params.iter().find(|(n, _)| n == k).map(|(_, v)| v.clone()).unwrap_or_default();
    let (redirect, state) = (get("redirect_uri"), get("state"));
    let port = redirect
        .strip_prefix("http://localhost:")
        .or_else(|| redirect.strip_prefix("http://127.0.0.1:"))
        .and_then(|rest| rest.split('/').next())
        .and_then(|p| p.parse::<u16>().ok())
        .ok_or("login_address_invalid")?;
    if state.is_empty() {
        return Err("login_address_invalid".into());
    }
    let listener = TcpListener::bind(("127.0.0.1", port)).map_err(|_| "login_port_busy".to_string())?;
    open_browser(auth_url).map_err(|e| e.to_string())?;
    wait_redirect(&listener, &state, Duration::from_secs(600))
}

#[tauri::command]
async fn mcp_login(app: tauri::AppHandle, auth_url: String) -> Result<String, String> {
    let rt = Arc::clone(&app.state::<Arc<runtime::Runtime>>());
    let result = tauri::async_runtime::spawn_blocking(move || { let _activity = rt.activity()?; mcp_login_blocking(&auth_url) })
        .await
        .map_err(|e| e.to_string())?;
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.set_focus();
    }
    result
}

// Updates come from a static feed (latest.json + the installer) fixed at build time
// (ARKISM_UPDATE_FEED above), so the WebView cannot point the app at another download.
fn feed() -> Result<&'static str, String> {
    let feed = FEED.ok_or_else(|| "update_feed_disabled".to_string())?;
    update_manifest::validate_configuration(feed, update_manifest::PUBLIC_KEY, update_manifest::CHANNEL)?;
    Ok(feed)
}

#[derive(serde::Serialize)]
struct Update {
    current: String,
    latest: String,
    newer: bool,
}

fn strictly_newer(current: &str, latest: &str) -> Result<bool, String> {
    let current = parse_version(current).ok_or_else(|| "update_current_invalid".to_string())?;
    let latest = parse_version(latest).ok_or_else(|| "update_feed_invalid".to_string())?;
    Ok(latest > current)
}

fn install_update_core(
    current: &str,
    fetch: impl FnOnce() -> Result<Latest, String>,
    install: impl FnOnce(Latest) -> Result<(), String>,
) -> Result<(), String> {
    let latest = fetch()?;
    if !strictly_newer(current, &latest.version)? {
        return Err("update_not_newer".into());
    }
    install(latest)
}

const UPDATE_DOWNLOAD_MAX_BYTES: u64 = 512 << 20;

fn download_update_core(
    reader: impl Read,
    expected_hash: &str,
    limit: u64,
    install: impl FnOnce(Vec<u8>) -> Result<(), String>,
) -> Result<(), String> {
    // One extra byte distinguishes a complete bounded response from a truncated prefix.
    let budget = limit.checked_add(1).ok_or_else(|| "update_download_limit_invalid".to_string())?;
    let mut bytes = Vec::new();
    reader.take(budget).read_to_end(&mut bytes).map_err(|e| format!("update_download_failed: {e}"))?;
    if bytes.len() as u64 > limit {
        return Err("update_download_too_large".into());
    }
    let hash: String = Sha256::digest(&bytes).iter().map(|b| format!("{b:02x}")).collect();
    if !hash.eq_ignore_ascii_case(expected_hash) {
        return Err("update_hash_mismatch".into());
    }
    install(bytes)
}

fn finish_update_handoff<T>(
    prepared: Option<T>,
    outcome: Result<(), runtime::InstallerHandoffFailure>,
    cleanup: impl FnOnce(T) -> Result<(), String>,
) -> Result<(), String> {
    match outcome {
        Ok(()) => Ok(()),
        Err(runtime::InstallerHandoffFailure::Unstarted(primary)) => {
            let Some(installer) = prepared else { return Err(primary); };
            match cleanup(installer) {
                Ok(()) => Err(primary),
                Err(error) => Err(format!("{primary}; {error}")),
            }
        },
        // The runtime retains the unresolved installer and paused-role checkpoint.
        Err(runtime::InstallerHandoffFailure::Unverified(primary)) => Err(primary),
    }
}

fn fetch_latest() -> Result<Latest, String> {
    let feed = feed()?;
    let name = if update_manifest::CHANNEL == "beta" { "latest-beta.json" } else { "latest.json" };
    let client = update_manifest::agent(Duration::from_secs(30));
    let get = |name: &str, limit| -> Result<Vec<u8>, String> {
        let response = client.get(&format!("{feed}/{name}")).call()
            .map_err(|_| "update_feed_unreachable")?;
        update_manifest::read_bounded(response.into_body().into_reader(), limit)
    };
    let bytes = get(name, 64 << 10)?;
    let signature = get(&format!("{name}.minisig"), 8 << 10)?;
    let signature = std::str::from_utf8(&signature).map_err(|_| "update_signature_invalid")?;
    update_manifest::decode_manifest(&bytes, signature, update_manifest::PUBLIC_KEY, update_manifest::CHANNEL)
}

#[tauri::command]
async fn update_check(app: tauri::AppHandle) -> Result<Update, String> {
    let current = app.package_info().version.to_string();
    let latest = tauri::async_runtime::spawn_blocking(fetch_latest).await.map_err(|e| e.to_string())??;
    let newer = strictly_newer(&current, &latest.version)?;
    Ok(Update { current, latest: latest.version, newer })
}

/// Downloads the installer, authenticates its signature and hash, starts it silently (it closes this
/// window, replaces the exe and reopens Arkism) and exits.
#[tauri::command]
async fn update_install(app: tauri::AppHandle) -> Result<(), String> {
    // macOS and Linux bundles are replaced by downloading the new one (the release page).
    if !cfg!(windows) {
        return Err("update_manual".into());
    }
    let current = app.package_info().version.to_string();
    let rt = Arc::clone(&app.state::<Arc<runtime::Runtime>>());
    tauri::async_runtime::spawn_blocking(move || {
        install_update_core(&current, fetch_latest, |latest| {
            let reader = update_manifest::agent(Duration::from_secs(300))
                .get(&format!("{}/{}", feed()?, latest.file))
                .call()
                .map_err(|e| format!("update_download_failed: {e}"))?
                .into_body()
                .into_reader();
            download_update_core(reader, &latest.sha256, UPDATE_DOWNLOAD_MAX_BYTES, |bytes| {
                latest.verify_installer(&bytes, update_manifest::PUBLIC_KEY)?;
                // The installer replaces the database programs: a backup first, or no update.
                rt.backup_before_update().map_err(|e| format!("update_backup_failed: {e}"))?;
                let mut prepared = Some(update_installer::Installer::prepare(&bytes, &latest.sha256, &latest.file)
                    .map_err(|error| error.to_string())?);
                // The installer replaces the built-in programs, so they must not be running.
                let outcome = rt.update_installer_handoff(|| {
                    let Some(installer) = prepared.take() else {
                        return lifecycle::Launch::Unstarted("update_installer_state_invalid".into());
                    };
                    match installer.launch() {
                        update_installer::LaunchDisposition::Unstarted { installer, error } => {
                            prepared = Some(installer);
                            lifecycle::Launch::Unstarted(error.to_string())
                        },
                        update_installer::LaunchDisposition::Started(_observer) => lifecycle::Launch::Started,
                        update_installer::LaunchDisposition::Unverified(owner) => {
                            lifecycle::Launch::Unverified {
                                owner,
                                error: "update_installer_unverified".into(),
                            }
                        },
                    }
                });
                finish_update_handoff(prepared, outcome, |installer| installer.cleanup().map_err(|e| e.to_string()))
            })
        })
    })
    .await
    .map_err(|e| e.to_string())??;
    app.exit(0);
    Ok(())
}

/// `Arkism --runtime-check DIR`: starts the built-in Arkism with its data in DIR, checks that it
/// answers with the owner token, stops it and writes the outcome to DIR/check.json. No window;
/// used to test an installer (and CI) without touching the user's data.
fn runtime_check(dir: &std::path::Path) -> i32 {
    if runtime::token_check::fresh_directory(dir).is_err() {
        eprintln!("runtime_check_requires_fresh_ordinary_absolute_directory");
        return 2;
    }
    if std::fs::write(dir.join("models.json"), br#"{"embedder":"none","judge":"fixed"}"#).is_err() {
        return 2;
    }
    let rt = runtime::Runtime::new(runtime::find_programs(None), dir.to_path_buf()).isolated();
    let started = Instant::now();
    let mut phase = "start";
    let outcome = rt.start().and_then(|s| {
        phase = "capabilities";
        let caps = ureq::get(&format!("{}/v1/capabilities?workspace_id=default&project_id=default&user_id=management%3Aowner", s.server))
            .header("Authorization", &format!("Bearer {}", s.token))
            .call()
            .map_err(|e| format!("capabilities: {e}"))?
            .status()
            .as_u16();
        phase = "managed_token_lifecycle";
        let (s, token_checks) = rt.check_managed_tokens(&s)?;
        phase = "reuse_start";
        let again = rt.start()?; // a second start reuses what runs
        // Backup, erase everything, restore: the old owner token opens the restored Arkism again.
        let backups = dir.join("backups");
        phase = "backup";
        let backup = rt.backup_into(&backups)?;
        phase = "erase";
        rt.erase()?;
        phase = "restore";
        let restored = rt.restore_from(&backups, &backup.name)?;
        // The hourly expiry runs against the built-in Arkism (needs the bundled agent client).
        let expiry = if rt.agent_client().is_some() { rt.expire().map(|_| "ok".to_string()).unwrap_or_else(|e| e) } else { "no_client".into() };
        phase = "restored_capabilities";
        let after = ureq::get(&format!("{}/v1/capabilities?workspace_id=default&project_id=default&user_id=management%3Aowner", restored.started.server))
            .header("Authorization", &format!("Bearer {}", s.token))
            .call()
            .map_err(|e| format!("capabilities after restore: {e}"))?
            .status()
            .as_u16();
        Ok(serde_json::json!({ "server": s.server, "capabilities": caps, "token_checks":token_checks, "reused": again.server == s.server,
            "backup_bytes": backup.bytes, "restored_capabilities": after, "safety_copy": restored.safety_copy.is_some(), "expiry": expiry }))
    });
    rt.stop();
    let (code, report) = match outcome {
        Ok(mut v) => {
            v["ok"] = true.into();
            phase = "complete";
            (0, v)
        }
        Err(e) => (1, serde_json::json!({ "ok": false, "error": e })),
    };
    let mut report = report;
    report["phase"] = phase.into();
    report["seconds"] = started.elapsed().as_secs_f64().into();
    let _ = std::fs::create_dir_all(dir);
    let _ = std::fs::write(dir.join("check.json"), report.to_string());
    code
}

fn main() {
    // ssh runs this program as its SSH_ASKPASS for a password login; answer and quit before any app setup.
    if let Some(code) = ssh_tunnel::askpass() {
        std::process::exit(code);
    }
    let args: Vec<String> = std::env::args().collect();
    #[cfg(windows)]
    if args.iter().any(|arg| arg == "--remove-autostart") {
        if args.len() != 2 { std::process::exit(2); }
        std::process::exit(if app_autostart::set_enabled(false).is_ok() { 0 } else { 1 });
    }
    if let Some(i) = args.iter().position(|a| a == "--runtime-check") {
        let dir = args.get(i + 1).map(std::path::PathBuf::from).unwrap_or_else(|| std::env::temp_dir().join("arkism-runtime-check"));
        std::process::exit(runtime_check(&dir));
    }
    let app = tauri::Builder::default()
        // A second launch only brings the running window forward; two copies would share one database.
        .plugin(tauri_plugin_single_instance::init(|app, _, _| show_main(app)));
        // Capture and the timers run only while the app does: it starts with the user's session,
        // in the tray (`--hidden`), unless the user turned that off in 설정 › 일반.
    // 설정 › 일반: desktop notifications and the shortcut that brings the window forward (general.rs).
    let app = app.plugin(tauri_plugin_notification::init()).plugin(
        tauri_plugin_global_shortcut::Builder::new()
            .with_handler(|app, _, event| {
                if event.state == tauri_plugin_global_shortcut::ShortcutState::Pressed { show_main(app) }
            })
            .build(),
    );
    #[cfg(not(windows))]
    let app = app.plugin(tauri_plugin_autostart::init(tauri_plugin_autostart::MacosLauncher::LaunchAgent, Some(vec!["--hidden"])));
    let app = app.setup(|app| {
            // The main window is built here (not from tauri.conf.json) so that window.open can open popouts (popout.rs).
            let conf = app.config().app.windows.iter().find(|w| w.label == "main").cloned().ok_or("main window config missing")?;
            let handle = app.handle().clone();
            WebviewWindowBuilder::from_config(app, &conf)?
                .on_new_window(move |url, features| popout::on_new_window(&handle, url, features))
                .build()?;
            if std::env::args().any(|a| a == "--hidden") {
                if let Some(w) = app.get_webview_window("main") {
                    let _ = w.hide();
                }
            }
            let app_dir = app.path().app_local_data_dir()?;
            // The default folder keeps what is about the app, not the data: agents.json (the agents
            // look for it there) and the autostart marker. The rest follows the chosen 저장 위치.
            let default = app_dir.join("runtime");
            let data = storage_location::read(&app_dir).unwrap_or_else(|| default.clone());
            let programs = runtime::find_programs(app.path().resource_dir().ok());
            let rt = Arc::new(runtime::Runtime::new(programs, data).agents_file(default.join("agents.json")));
            #[cfg(windows)]
            {
                let logging_runtime = Arc::downgrade(&rt);
                std::thread::spawn(move || loop {
                    let Some(rt) = logging_runtime.upgrade() else { break };
                    if let Ok(_activity) = rt.activity() {
                        let _ = log_retention::maintain(&rt.data_dir().join("logs"));
                    }
                    drop(rt);
                    std::thread::sleep(Duration::from_secs(60));
                });
            }

            // The PC's agents are connected without asking, whether the app uses the built-in Arkism
            // or a server: on the first start and after each update (the client they run is refreshed
            // then). Uninstalling the app disconnects them again.
            // Start at login by default, once: after that the user's choice in 설정 › 일반 stays.
            general::init(app.handle(), default.clone());
            let autostart_marker = default.join("autostart-default");
            #[cfg(windows)]
            let _ = app_autostart::migrate_owned();
            if !autostart_marker.exists() && write_autostart(app.handle(), true).is_ok() {
                let _ = std::fs::create_dir_all(&default);
                let _ = std::fs::write(&autostart_marker, "on");
            }
            let version = app.package_info().version.to_string();
            if std::fs::read_to_string(rt.data_dir().join("agents-connected")).ok().as_deref() != Some(version.as_str()) {
                let rt = Arc::clone(&rt);
                std::thread::spawn(move || {
                    let Ok(_activity) = rt.activity() else { return };
                    if run_agent_setup(&rt, "install-arkism").is_ok() {
                        // Read after the setup: the first start may have picked another 저장 위치 meanwhile.
                        let marker = rt.data_dir().join("agents-connected");
                        let _ = std::fs::create_dir_all(rt.data_dir());
                        let _ = std::fs::write(&marker, version);
                    }
                });
            }
            app.manage(rt);
            let open = MenuItem::with_id(app, "open", "Arkism 열기", true, None::<&str>)?;
            let quit_item = MenuItem::with_id(app, "quit", "끝내기", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&open, &quit_item])?;
            let mut tray = TrayIconBuilder::with_id("main")
                .tooltip("Arkism")
                .menu(&menu)
                .show_menu_on_left_click(false)
                .on_menu_event(|app, event| match event.id.as_ref() {
                    "open" => show_main(app),
                    "quit" => quit(app),
                    _ => {}
                })
                .on_tray_icon_event(|tray, event| {
                    if let TrayIconEvent::Click { button: MouseButton::Left, button_state: MouseButtonState::Up, .. } = event {
                        show_main(tray.app_handle());
                    }
                });
            if let Some(icon) = app.default_window_icon() {
                tray = tray.icon(icon.clone());
            }
            tray.build(app)?;
            Ok(())
        })
        // Closing the window keeps Arkism in the tray, so agents can keep saving memories
        // (unless 설정 › 일반 › 창을 닫아도 계속 실행 is off: then it quits).
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                if popout::is_popout(window.label()) {
                    api.prevent_close();
                    popout::close_requested(window); // the main window closes its popup, then the window
                    return;
                }
                api.prevent_close();
                if window.label() == "main" && !general::keep_running() {
                    quit(window.app_handle()); // busy (an update or a move is running): it stays in the tray
                }
                let _ = window.hide();
            }
        })
        .invoke_handler(tauri::generate_handler![api, app_defaults, local_location, local_location_set, local_start, local_backup, local_backups, local_restore, local_erase, open_backups, model_roles, set_model_roles, gateway_info, agent_link_set, doctor, import_agent_memories, autostart_get, autostart_set, search_mode, capture_settings, set_capture_settings, ssh_connect, ssh_test, ssh_host_key_check, ssh_host_key_approve, ssh_host_key_cancel, ssh_disconnect, ssh_keys, ssh_hosts, providers_refresh, git_op, skill_execution, mcp_login, update_check, update_install, gateway_token_status, gateway_token_regenerate, general::general_get, general::general_set, general::notify, agent_backends, popout::popout_destroy])
        .build(tauri::generate_context!())
        .expect("Arkism failed to start");
    app.run(|app, event| {
        if let RunEvent::Exit = event {
            ssh_tunnel::shutdown();
            app.state::<Arc<runtime::Runtime>>().shutdown();
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn update_unstarted_cleans_only_returned_stage_and_preserves_primary_failure() {
        for cleanup_error in [None, Some("cleanup_failed")] {
            let calls = std::cell::Cell::new(0);
            // This also covers preflight refusal before the launch closure runs.
            let outcome = Err(runtime::InstallerHandoffFailure::Unstarted("primary_failure".into()));
            let result = finish_update_handoff(Some(17), outcome, |stage| {
                assert_eq!(stage, 17);
                calls.set(calls.get() + 1);
                cleanup_error.map_or(Ok(()), |error| Err(error.into()))
            });
            let expected = cleanup_error.map_or("primary_failure".into(), |error| format!("primary_failure; {error}"));
            assert_eq!(result, Err(expected));
            assert_eq!(calls.get(), 1);
        }
    }

    #[test]
    fn update_started_and_unverified_never_cleanup_retained_child_stage() {
        for (outcome, expected) in [
            (Ok(()), Ok(())),
            (Err(runtime::InstallerHandoffFailure::Unverified("update_installer_unverified".into())), Err("update_installer_unverified".into())),
        ] {
            let result = finish_update_handoff(None::<()>, outcome, |_| panic!("child-owned stage must not be deleted"));
            assert_eq!(result, expected);
        }
        let result = finish_update_handoff(None::<()>, Err(runtime::InstallerHandoffFailure::Unstarted("primary_failure".into())), |_| panic!("no returned stage to clean"));
        assert_eq!(result, Err("primary_failure".into()));
    }

    #[test]
    fn update_lifecycle_ui_requests_reject_handoff_before_http() {
        let gate = Arc::new(lifecycle::Gate::default());
        let _lease = gate.lease().unwrap();
        let result = tauri::async_runtime::block_on(api_with_gate(gate.clone(), "http://127.0.0.1:1".into(),
            "fixture".into(), "POST".into(), "/v1/memories".into(), Some("{}".into())));
        assert_eq!(result.err().as_deref(), Some("update_runtime_busy"));
    }

    /// One HTTP exchange on a local port: returns the raw request and replies with `reply`.
    fn serve_once(reply: &'static str) -> (String, std::thread::JoinHandle<String>) {
        use std::io::{BufRead, BufReader, Write};
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let handle = std::thread::spawn(move || {
            let (mut conn, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(conn.try_clone().unwrap());
            let (mut head, mut len) = (String::new(), 0);
            loop {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                if let Some(v) = line.to_ascii_lowercase().strip_prefix("content-length:") {
                    len = v.trim().parse().unwrap();
                }
                head.push_str(&line);
                if line == "\r\n" {
                    break;
                }
            }
            let mut body = vec![0; len];
            reader.read_exact(&mut body).unwrap();
            conn.write_all(reply.as_bytes()).unwrap();
            head + &String::from_utf8(body).unwrap()
        });
        (base, handle)
    }

    #[test]
    fn api_returns_error_status_body_and_sends_json() {
        let (base, server) = serve_once("HTTP/1.1 404 Not Found\r\nContent-Length: 13\r\nConnection: close\r\n\r\n{\"error\":\"x\"}");
        let reply = tauri::async_runtime::block_on(api_with_gate(Arc::new(lifecycle::Gate::default()), base, "t0k".into(), "POST".into(), "/v1/memories".into(), Some("{\"a\":1}".into()))).unwrap();
        assert_eq!((reply.status, reply.body.as_str()), (404, "{\"error\":\"x\"}"));
        let request = server.join().unwrap();
        assert!(request.starts_with("POST /v1/memories HTTP/1.1\r\n"), "{request}");
        assert!(request.contains("authorization: Bearer t0k") || request.contains("Authorization: Bearer t0k"), "{request}");
        assert!(request.ends_with("{\"a\":1}"), "{request}");

        let (base, server) = serve_once("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok");
        let reply = tauri::async_runtime::block_on(api_with_gate(Arc::new(lifecycle::Gate::default()), base, "t".into(), "GET".into(), "/readyz".into(), None)).unwrap();
        assert_eq!((reply.status, reply.body.as_str()), (200, "ok"));
        assert!(server.join().unwrap().starts_with("GET /readyz HTTP/1.1"));
    }

    #[test]
    fn only_api_paths_pass() {
        assert!(allowed("GET", "/v1/memories?status=active"));
        assert_eq!(mcp_login_blocking("http://x/authorize?state=s"), Err("login_address_invalid".to_string()));
        assert_eq!(mcp_login_blocking("https://x/a?redirect_uri=https%3A%2F%2Fevil.example%2Fcb&state=s"), Err("login_address_invalid".to_string()));
        assert_eq!(mcp_login_blocking("https://x/a?redirect_uri=http%3A%2F%2Flocalhost%3A5%2Fcb"), Err("login_address_invalid".to_string()));
        assert!(allowed("POST", "/v1/memory_candidates/x/review"));
        // 들어가는 기억 미리보기: the hooks' own calls plus the token count.
        assert!(allowed("POST", "/v1/search"));
        assert!(allowed("POST", "/v1/context/tokens"));
        assert!(!allowed("DELETE", "/v1/context/tokens"));
        assert!(allowed("DELETE", "/v1/memories/x"));
        assert!(allowed("DELETE", "/v1/crew/x"));
        assert!(!allowed("DELETE", "/v1/crew/x/runs"));
        assert!(!allowed("DELETE", "/v1/crew/"));
        assert!(allowed("DELETE", "/v1/prompts/p1"));
        assert!(allowed("DELETE", "/v1/design_systems/ds1"));
        assert!(!allowed("DELETE", "/v1/design_systems/ds1/restore"));
        assert!(!allowed("DELETE", "/v1/prompts/"));
        assert!(!allowed("DELETE", "/v1/crew_runs/x"));
        assert!(!allowed("GET", "/metrics"));
        assert!(!allowed("GET", "/v1/../metrics"));
        assert!(valid_base("http://arkism.example.internal:8092"));
        assert!(valid_base(DEFAULT_SERVER));
        assert!(!valid_base("file:///etc"));
        assert!(!valid_base("http://x/"));
    }

    #[test]
    fn versions_compare_numerically() {
        assert!(parse_version("0.10.0") > parse_version("0.9.9"));
        assert!(parse_version("0.5.0") > parse_version("0.4.0"));
        assert_eq!(parse_version("1.2"), None);
        assert_eq!(parse_version("1.2.3.4"), None);
        assert_eq!(parse_version("a.b.c"), None);
    }

    fn update_fixture(version: &str) -> Latest {
        Latest {
            version: version.into(),
            file: format!("Arkism-{version}-setup.exe"),
            sha256: "a".repeat(64),
            channel: "stable".into(),
            signature: String::new(),
        }
    }

    #[test]
    fn update_install_rejects_older_and_equal_before_installer_effects() {
        for version in ["1.2.2", "1.2.3"] {
            let fetches = std::cell::Cell::new(0);
            let installs = std::cell::Cell::new(0);
            let result = install_update_core("1.2.3", || {
                fetches.set(fetches.get() + 1);
                Ok(update_fixture(version))
            }, |_| {
                installs.set(installs.get() + 1);
                Ok(())
            });
            assert_eq!(result, Err("update_not_newer".into()), "{version}");
            assert_eq!((fetches.get(), installs.get()), (1, 0));
            assert_eq!(strictly_newer("1.2.3", version), Ok(false));
        }
    }

    #[test]
    fn update_install_rejects_invalid_versions_before_installer_effects() {
        for (current, latest, error) in [
            ("a.b.c", "1.2.4", "update_current_invalid"),
            ("1.2", "1.2.4", "update_current_invalid"),
            ("4294967296.0.0", "1.2.4", "update_current_invalid"),
            ("1.2.3", "a.b.c", "update_feed_invalid"),
            ("1.2.3", "1.2", "update_feed_invalid"),
            ("1.2.3", "4294967296.0.0", "update_feed_invalid"),
        ] {
            let fetches = std::cell::Cell::new(0);
            let installs = std::cell::Cell::new(0);
            let result = install_update_core(current, || {
                fetches.set(fetches.get() + 1);
                Ok(update_fixture(latest))
            }, |_| {
                installs.set(installs.get() + 1);
                Ok(())
            });
            assert_eq!(result, Err(error.into()), "{current} -> {latest}");
            assert_eq!((fetches.get(), installs.get()), (1, 0));
            assert_eq!(strictly_newer(current, latest), Err(error.into()));
        }
    }

    #[test]
    fn update_install_passes_the_same_newer_candidate_exactly_once() {
        for (current, version) in [("0.9.9", "0.10.0"), ("1.2.3", "1.2.4")] {
            let latest = update_fixture(version);
            let expected = (latest.version.clone(), latest.file.clone(), latest.sha256.clone());
            let fetches = std::cell::Cell::new(0);
            let installs = std::cell::Cell::new(0);
            let result = install_update_core(current, || {
                fetches.set(fetches.get() + 1);
                Ok(latest)
            }, |candidate| {
                installs.set(installs.get() + 1);
                assert_eq!((candidate.version, candidate.file, candidate.sha256), expected);
                Ok(())
            });
            assert_eq!(result, Ok(()));
            assert_eq!((fetches.get(), installs.get()), (1, 1));
            assert_eq!(strictly_newer(current, version), Ok(true));
        }
    }

    #[test]
    fn update_install_propagates_fetch_and_installer_errors() {
        let installs = std::cell::Cell::new(0);
        let result = install_update_core("1.2.3", || Err("synthetic_fetch_failed".into()), |_| {
            installs.set(installs.get() + 1);
            Ok(())
        });
        assert_eq!(result, Err("synthetic_fetch_failed".into()));
        assert_eq!(installs.get(), 0);

        let result = install_update_core("1.2.3", || Ok(update_fixture("1.2.4")), |_| {
            installs.set(installs.get() + 1);
            Err("synthetic_installer_failed".into())
        });
        assert_eq!(result, Err("synthetic_installer_failed".into()));
        assert_eq!(installs.get(), 1);
    }

    const UPDATE_TEST_HASH: &str = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; // SHA256("abc")

    struct ChunkedUpdateReader<R> {
        inner: R,
        chunk: usize,
        read_bytes: usize,
    }

    impl<R: Read> Read for ChunkedUpdateReader<R> {
        fn read(&mut self, buffer: &mut [u8]) -> std::io::Result<usize> {
            let size = buffer.len().min(self.chunk);
            let read = self.inner.read(&mut buffer[..size])?;
            self.read_bytes += read;
            Ok(read)
        }
    }

    struct FailedUpdateReader {
        kind: std::io::ErrorKind,
        message: &'static str,
        wrapped: bool,
    }

    impl Read for FailedUpdateReader {
        fn read(&mut self, _: &mut [u8]) -> std::io::Result<usize> {
            let error = std::io::Error::new(self.kind, self.message);
            Err(if self.wrapped { std::io::Error::other(error) } else { error })
        }
    }

    #[test]
    fn update_download_accepts_exact_bound_and_case_insensitive_hash() {
        for limit in [3, 8] {
            let installs = std::cell::Cell::new(0);
            let result = download_update_core(std::io::Cursor::new(b"abc"), &UPDATE_TEST_HASH.to_ascii_uppercase(), limit, |bytes| {
                assert_eq!(bytes, b"abc");
                installs.set(installs.get() + 1);
                Ok(())
            });
            assert_eq!(result, Ok(()));
            assert_eq!(installs.get(), 1);
        }
    }

    #[test]
    fn update_download_rejects_one_extra_byte_even_with_prefix_hash() {
        let installs = std::cell::Cell::new(0);
        let result = download_update_core(std::io::Cursor::new(b"abcd"), UPDATE_TEST_HASH, 3, |_| {
            installs.set(installs.get() + 1);
            Ok(())
        });
        assert_eq!(result, Err("update_download_too_large".into()));
        assert_eq!(installs.get(), 0);
    }

    #[test]
    fn update_download_bounds_multichunk_and_endless_readers() {
        let mut finite = ChunkedUpdateReader { inner: std::io::Cursor::new(b"abcdefghi"), chunk: 1, read_bytes: 0 };
        let result = download_update_core(&mut finite, UPDATE_TEST_HASH, 3, |_| panic!("oversized input reached installer effects"));
        assert_eq!(result, Err("update_download_too_large".into()));
        assert_eq!(finite.read_bytes, 4);
        assert_eq!(finite.inner.position(), 4); // The remaining response is never consumed.

        let mut endless = ChunkedUpdateReader { inner: std::io::repeat(b'a'), chunk: 2, read_bytes: 0 };
        let result = download_update_core(&mut endless, UPDATE_TEST_HASH, 7, |_| panic!("endless input reached installer effects"));
        assert_eq!(result, Err("update_download_too_large".into()));
        assert_eq!(endless.read_bytes, 8);
    }

    #[test]
    fn update_download_preserves_truncated_and_wrapped_read_errors() {
        for (kind, message, wrapped) in [
            (std::io::ErrorKind::UnexpectedEof, "synthetic truncated response", false),
            (std::io::ErrorKind::ConnectionReset, "synthetic wrapped response failed", true),
        ] {
            // Even an error after exactly the matching prefix must not pass as EOF.
            let reader = std::io::Cursor::new(b"abc").chain(FailedUpdateReader { kind, message, wrapped });
            let installs = std::cell::Cell::new(0);
            let result = download_update_core(reader, UPDATE_TEST_HASH, 3, |_| {
                installs.set(installs.get() + 1);
                Ok(())
            });
            assert_eq!(result, Err(format!("update_download_failed: {message}")));
            assert_eq!(installs.get(), 0);
        }
    }

    #[test]
    fn update_download_hash_mismatch_stops_before_installer_effects() {
        let result = download_update_core(std::io::Cursor::new(b"ab"), UPDATE_TEST_HASH, 3, |_| panic!("hash mismatch reached installer effects"));
        assert_eq!(result, Err("update_hash_mismatch".into()));
    }

    #[test]
    fn update_download_overflow_limit_fails_before_reading() {
        let mut reader = ChunkedUpdateReader { inner: std::io::repeat(b'a'), chunk: 1, read_bytes: 0 };
        let result = download_update_core(&mut reader, UPDATE_TEST_HASH, u64::MAX, |_| panic!("invalid limit reached installer effects"));
        assert_eq!(result, Err("update_download_limit_invalid".into()));
        assert_eq!(reader.read_bytes, 0);
    }

    #[test]
    fn update_download_propagates_verified_installer_errors() {
        let installs = std::cell::Cell::new(0);
        let result = download_update_core(std::io::Cursor::new(b"abc"), UPDATE_TEST_HASH, 3, |bytes| {
            assert_eq!(bytes, b"abc");
            installs.set(installs.get() + 1);
            Err("synthetic_installer_failed".into())
        });
        assert_eq!(result, Err("synthetic_installer_failed".into()));
        assert_eq!(installs.get(), 1);
    }

    #[test]
    fn update_install_version_gate_precedes_bounded_download() {
        for (current, version, body, expected, read_bytes, installs) in [
            ("1.2.3", "1.2.2", b"abcd".as_slice(), Err("update_not_newer".into()), 0, 0),
            ("invalid", "1.2.4", b"abcd".as_slice(), Err("update_current_invalid".into()), 0, 0),
            ("1.2.3", "1.2.4", b"abcd".as_slice(), Err("update_download_too_large".into()), 4, 0),
            ("1.2.3", "1.2.4", b"abc".as_slice(), Ok(()), 3, 1),
        ] {
            let mut reader = ChunkedUpdateReader { inner: std::io::Cursor::new(body), chunk: 1, read_bytes: 0 };
            let effects = std::cell::Cell::new(0);
            let result = install_update_core(current, || {
                let mut latest = update_fixture(version);
                latest.sha256 = UPDATE_TEST_HASH.into();
                Ok(latest)
            }, |latest| download_update_core(&mut reader, &latest.sha256, 3, |bytes| {
                assert_eq!(bytes, b"abc");
                effects.set(effects.get() + 1);
                Ok(())
            }));
            assert_eq!(result, expected);
            assert_eq!((reader.read_bytes, effects.get()), (read_bytes, installs));
        }
    }

    #[test]
    fn api_reuses_connection_with_request_scoped_auth() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}/v1/capabilities", listener.local_addr().unwrap());
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            stream.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            for expected in ["synthetic-a", "synthetic-b"] {
                let mut headers = String::new();
                loop {
                    let mut line = String::new();
                    assert!(reader.read_line(&mut line).unwrap() > 0, "connection closed between requests");
                    if line == "\r\n" { break; }
                    headers.push_str(&line);
                }
                assert!(headers.to_lowercase().contains(&format!("authorization: bearer {expected}\r\n")));
                stream.write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}").unwrap();
            }
        });
        for token in ["synthetic-a", "synthetic-b"] {
            let body = api_agent().get(&url).config().timeout_global(Some(Duration::from_secs(3))).build()
                .header("Authorization", &format!("Bearer {token}"))
                .call().unwrap().body_mut().read_to_string().unwrap();
            assert_eq!(body, "{}");
        }
        server.join().unwrap();
    }

    #[test]
    fn callback_query_is_decoded() {
        let p = callback_params("GET /callback?state=abc&code=4%2F0Ab_c-d&scope=email+openid HTTP/1.1\r\n");
        assert_eq!(p[0], ("state".into(), "abc".into()));
        assert_eq!(p[1], ("code".into(), "4/0Ab_c-d".into()));
        assert_eq!(p[2].1, "email openid");
        assert_eq!(pct_encode("http://127.0.0.1:5/callback"), "http%3A%2F%2F127.0.0.1%3A5%2Fcallback");
        assert_eq!(pct_decode("50%"), "50%");
    }
}
