// 팝업을 창 밖으로: a popup dragged out of the main window opens in its own window that shows only that popup.
// The main window hands over how to reopen it plus its connection (server, token, scope); the new window takes it once.
use std::collections::HashMap;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Mutex;

use tauri::{AppHandle, State, WebviewUrl, WebviewWindowBuilder, Window};

const PREFIX: &str = "popout-";
const MAX_PAYLOAD: usize = 64 * 1024;

static NEXT: AtomicU32 = AtomicU32::new(1);

#[derive(Default)]
pub struct Popouts(Mutex<HashMap<String, String>>);

pub fn is_popout(label: &str) -> bool {
    label.starts_with(PREFIX)
}

/// Opens a window for one popup at (x, y) in screen pixels; `payload` is the UI's own JSON, kept here until the window takes it.
#[tauri::command]
#[allow(clippy::too_many_arguments)]
pub fn popout_open(app: AppHandle, window: Window, store: State<'_, Popouts>, payload: String, title: String, x: f64, y: f64, width: f64, height: f64) -> Result<String, String> {
    if window.label() != "main" {
        return Err("popout_from_main_only".into());
    }
    if payload.len() > MAX_PAYLOAD {
        return Err("popout_payload_too_large".into());
    }
    let label = format!("{PREFIX}{}", NEXT.fetch_add(1, Ordering::Relaxed));
    store.0.lock().map_err(|e| e.to_string())?.insert(label.clone(), payload);
    let title: String = title.chars().filter(|c| !c.is_control()).take(80).collect();
    let built = WebviewWindowBuilder::new(&app, &label, WebviewUrl::App("index.html".into()))
        .title(if title.is_empty() { "Arkism".to_string() } else { format!("{title} · Arkism") })
        .inner_size(clamp(width, 360.0, 2400.0), clamp(height, 320.0, 1800.0))
        .min_inner_size(360.0, 320.0)
        .position(clamp(x, -10000.0, 10000.0), clamp(y, -10000.0, 10000.0))
        .build();
    if let Err(e) = built {
        if let Ok(mut m) = store.0.lock() {
            m.remove(&label);
        }
        return Err(e.to_string());
    }
    Ok(label)
}

/// What this window should show: {"popout": false} for the main window, else the payload (null once taken, e.g. after a reload).
#[tauri::command]
pub fn popout_take(window: Window, store: State<'_, Popouts>) -> serde_json::Value {
    if !is_popout(window.label()) {
        return serde_json::json!({ "popout": false });
    }
    let payload = store.0.lock().ok().and_then(|mut m| m.remove(window.label()));
    serde_json::json!({ "popout": true, "payload": payload })
}

/// Closes the calling popout window (its popup was closed).
#[tauri::command]
pub fn popout_close(window: Window) -> Result<(), String> {
    if !is_popout(window.label()) {
        return Err("not_a_popout".into());
    }
    window.destroy().map_err(|e| e.to_string())
}

fn clamp(v: f64, lo: f64, hi: f64) -> f64 {
    if v.is_finite() { v.clamp(lo, hi) } else { lo }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn popout_labels_and_clamp() {
        assert!(is_popout("popout-3"));
        assert!(!is_popout("main"));
        assert_eq!(clamp(f64::NAN, 360.0, 2400.0), 360.0);
        assert_eq!(clamp(5000.0, 360.0, 2400.0), 2400.0);
    }
}
