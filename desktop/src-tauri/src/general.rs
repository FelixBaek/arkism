//! 설정 › 일반 (Figma 198:5962): what the shell itself does — keep running after the window
//! closes, the shortcut that brings the window forward, and desktop notifications.
//! Saved in general.json beside agents.json (the app folder, not the data folder).

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Mutex, OnceLock};

use serde::{Deserialize, Serialize};
use tauri_plugin_global_shortcut::{GlobalShortcutExt, Shortcut};
use tauri_plugin_notification::NotificationExt;

pub const DEFAULT_SHORTCUT: &str = "Ctrl+Shift+Space";

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq)]
pub struct General {
    #[serde(default = "yes")]
    pub keep_running: bool,
    /// Empty: no shortcut.
    #[serde(default = "default_shortcut")]
    pub shortcut: String,
    /// The shortcut could not be taken (another app holds it); the screen says so.
    #[serde(default, skip_deserializing)]
    pub shortcut_error: String,
}

fn yes() -> bool { true }
fn default_shortcut() -> String { DEFAULT_SHORTCUT.into() }

impl Default for General {
    fn default() -> Self { Self { keep_running: true, shortcut: default_shortcut(), shortcut_error: String::new() } }
}

static PATH: OnceLock<PathBuf> = OnceLock::new();
static CURRENT: Mutex<Option<General>> = Mutex::new(None);
static KEEP_RUNNING: AtomicBool = AtomicBool::new(true);

/// Whether closing the main window only hides it (read by the close handler).
pub fn keep_running() -> bool { KEEP_RUNNING.load(Ordering::Relaxed) }

fn read(path: &std::path::Path) -> General {
    std::fs::read(path).ok().and_then(|b| serde_json::from_slice(&b).ok()).unwrap_or_default()
}

/// Loads general.json and takes the shortcut; called once from setup.
pub fn init(app: &tauri::AppHandle, dir: PathBuf) {
    let path = dir.join("general.json");
    let mut g = read(&path);
    let _ = PATH.set(path);
    KEEP_RUNNING.store(g.keep_running, Ordering::Relaxed);
    g.shortcut_error = take_shortcut(app, None, &g.shortcut).err().unwrap_or_default();
    *CURRENT.lock().unwrap_or_else(|e| e.into_inner()) = Some(g);
}

fn take_shortcut(app: &tauri::AppHandle, old: Option<&str>, new: &str) -> Result<(), String> {
    let gs = app.global_shortcut();
    if let Some(old) = old.filter(|o| !o.is_empty()) {
        let _ = gs.unregister(old);
    }
    if new.is_empty() { return Ok(()); }
    let key: Shortcut = new.parse().map_err(|_| "shortcut_invalid".to_string())?;
    gs.register(key).map_err(|_| "shortcut_taken".to_string())
}

#[tauri::command]
pub fn general_get() -> General {
    CURRENT.lock().unwrap_or_else(|e| e.into_inner()).clone().unwrap_or_default()
}

#[tauri::command]
pub fn general_set(app: tauri::AppHandle, keep_running: bool, shortcut: String) -> Result<General, String> {
    let shortcut = shortcut.trim().to_string();
    if !shortcut.is_empty() && shortcut.parse::<Shortcut>().is_err() {
        return Err("shortcut_invalid".into());
    }
    let mut cur = CURRENT.lock().unwrap_or_else(|e| e.into_inner());
    let old = cur.clone().unwrap_or_default();
    let mut next = General { keep_running, shortcut, shortcut_error: String::new() };
    if next.shortcut != old.shortcut || !old.shortcut_error.is_empty() {
        if let Err(e) = take_shortcut(&app, Some(&old.shortcut), &next.shortcut) {
            // Keep the old key working when the new one is taken.
            let _ = take_shortcut(&app, None, &old.shortcut);
            return Err(e);
        }
    }
    let path = PATH.get().ok_or("general_unavailable")?;
    if let Some(dir) = path.parent() { std::fs::create_dir_all(dir).map_err(|e| e.to_string())?; }
    std::fs::write(path, serde_json::to_vec_pretty(&next).map_err(|e| e.to_string())?).map_err(|e| e.to_string())?;
    KEEP_RUNNING.store(next.keep_running, Ordering::Relaxed);
    next.shortcut_error.clear();
    *cur = Some(next.clone());
    Ok(next)
}

/// 설정 › 일반 › 알림: a desktop notification while the window is hidden or unfocused.
/// Without `sound` Windows shows the toast silently.
#[tauri::command]
pub fn notify(app: tauri::AppHandle, title: String, body: String, sound: bool) -> Result<(), String> {
    let mut n = app.notification().builder().title(title.chars().take(120).collect::<String>()).body(body.chars().take(400).collect::<String>());
    if sound { n = n.sound("Default"); }
    n.show().map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_or_broken_file_reads_as_defaults() {
        let dir = std::env::temp_dir().join(format!("arkism-general-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let p = dir.join("general.json");
        assert_eq!(read(&p), General::default());
        std::fs::write(&p, "{not json").unwrap();
        assert_eq!(read(&p), General::default());
        std::fs::write(&p, r#"{"keep_running":false}"#).unwrap();
        let g = read(&p);
        assert!(!g.keep_running);
        assert_eq!(g.shortcut, DEFAULT_SHORTCUT);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn default_shortcut_parses() {
        assert!(DEFAULT_SHORTCUT.parse::<Shortcut>().is_ok());
    }
}
