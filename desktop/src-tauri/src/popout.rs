// 팝업을 창 밖으로: a popup dragged out of the main window opens in its own window that shows only that popup.
// The main window hands over how to reopen it plus its connection (server, token, scope); the new window takes it once.
// So that a popup opens as its own window at once, the main window keeps one hidden spare that has already connected
// and loaded (popout_prepare); popout_open hands the spare the popup and the spare shows itself once drawn (popout_ready).
use std::collections::HashMap;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Mutex;

use tauri::{AppHandle, Emitter, LogicalPosition, LogicalSize, Manager, State, WebviewUrl, WebviewWindowBuilder, Window};

const PREFIX: &str = "popout-";
const MAX_PAYLOAD: usize = 64 * 1024;

static NEXT: AtomicU32 = AtomicU32::new(1);

#[derive(Default)]
pub struct Popouts(Mutex<HashMap<String, String>>);

/// The hidden spare: its label, the session it was built with, and whether it has finished loading.
#[derive(Default)]
pub struct Spare(Mutex<Option<(String, String, bool)>>);

pub fn is_popout(label: &str) -> bool {
    label.starts_with(PREFIX)
}

/// Opens a window for one popup at (x, y) in screen pixels; `payload` is the UI's own JSON, kept here until the window takes it.
/// Async: building a window inside a sync command deadlocks on Windows (WebView2).
#[tauri::command]
#[allow(clippy::too_many_arguments)]
pub async fn popout_open(app: AppHandle, window: Window, store: State<'_, Popouts>, spare: State<'_, Spare>, payload: String, title: String, x: f64, y: f64, width: f64, height: f64, spare_only: Option<bool>) -> Result<Option<String>, String> {
    if window.label() != "main" {
        return Err("popout_from_main_only".into());
    }
    if payload.len() > MAX_PAYLOAD {
        return Err("popout_payload_too_large".into());
    }
    let title: String = title.chars().filter(|c| !c.is_control()).take(80).collect();
    let title = if title.is_empty() { "Arkism".to_string() } else { format!("{title} · Arkism") };
    let (x, y) = (clamp(x, -10000.0, 10000.0), clamp(y, -10000.0, 10000.0));
    let (width, height) = (clamp(width, 360.0, 2400.0), clamp(height, 320.0, 1800.0));
    // A loaded spare takes the popup: placed now, shown by the spare itself once the popup is drawn (popout_ready).
    let ready = {
        let mut slot = spare.0.lock().map_err(|e| e.to_string())?;
        if slot.as_ref().is_some_and(|s| s.2) { slot.take().map(|s| s.0) } else { None } // not take_if: rust-version is 1.77
    };
    if let Some(w) = ready.and_then(|l| app.get_webview_window(&l)) {
        let _ = w.set_title(&title);
        let _ = w.set_size(LogicalSize::new(width, height));
        let _ = w.set_position(LogicalPosition::new(x, y));
        w.emit_to(w.label(), "popout-fill", payload).map_err(|e| e.to_string())?;
        return Ok(Some(w.label().to_string()));
    }
    if spare_only == Some(true) {
        return Ok(None);
    }
    let label = format!("{PREFIX}{}", NEXT.fetch_add(1, Ordering::Relaxed));
    store.0.lock().map_err(|e| e.to_string())?.insert(label.clone(), payload);
    let built = WebviewWindowBuilder::new(&app, &label, WebviewUrl::App("index.html".into()))
        .title(title)
        .inner_size(width, height)
        .min_inner_size(360.0, 320.0)
        .decorations(false) // the app draws its own drag strip and ✕ (window-controls.js)
        .shadow(true)
        .position(x, y)
        .build();
    if let Err(e) = built {
        if let Ok(mut m) = store.0.lock() {
            m.remove(&label);
        }
        return Err(e.to_string());
    }
    Ok(Some(label))
}

/// Keeps one hidden spare connected with `session` (the UI's JSON); None (signed out) drops it. A spare built with
/// another session is replaced, so a spare never holds a token the main window no longer uses.
#[tauri::command]
pub async fn popout_prepare(app: AppHandle, window: Window, store: State<'_, Popouts>, spare: State<'_, Spare>, session: Option<String>) -> Result<(), String> {
    if window.label() != "main" {
        return Err("popout_from_main_only".into());
    }
    if session.as_ref().is_some_and(|s| s.len() > MAX_PAYLOAD) {
        return Err("popout_payload_too_large".into());
    }
    let mut slot = spare.0.lock().map_err(|e| e.to_string())?;
    if slot.as_ref().map(|s| &s.1) == session.as_ref() && slot.as_ref().is_some_and(|s| app.get_webview_window(&s.0).is_some()) {
        return Ok(());
    }
    if let Some((old, _, _)) = slot.take() {
        if let Some(w) = app.get_webview_window(&old) {
            let _ = w.destroy();
        }
    }
    let Some(session) = session else { return Ok(()) };
    let label = format!("{PREFIX}{}", NEXT.fetch_add(1, Ordering::Relaxed));
    store.0.lock().map_err(|e| e.to_string())?.insert(label.clone(), serde_json::json!({ "spare": true, "session": serde_json::from_str::<serde_json::Value>(&session).map_err(|e| e.to_string())? }).to_string());
    WebviewWindowBuilder::new(&app, &label, WebviewUrl::App("index.html".into()))
        .title("Arkism")
        .inner_size(720.0, 640.0)
        .min_inner_size(360.0, 320.0)
        .decorations(false)
        .shadow(true)
        .visible(false)
        .skip_taskbar(true)
        .build()
        .map_err(|e| e.to_string())?;
    *slot = Some((label, session, false));
    Ok(())
}

/// From a popout window: as the spare, "loaded, ready for a popup"; once it holds a popup, "drawn, show me".
/// Async: popout_prepare holds the Spare lock while building a window on the main thread, so waiting for it there deadlocks.
#[tauri::command]
pub async fn popout_ready(window: Window, spare: State<'_, Spare>, drawn: bool) -> Result<(), String> {
    if !is_popout(window.label()) {
        return Err("not_a_popout".into());
    }
    if !drawn {
        if let Some(s) = spare.0.lock().map_err(|e| e.to_string())?.as_mut().filter(|s| s.0 == window.label()) {
            s.2 = true;
        }
        return Ok(());
    }
    let _ = window.set_skip_taskbar(false);
    window.show().map_err(|e| e.to_string())?;
    window.set_focus().map_err(|e| e.to_string())
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
