// 팝업을 창 밖으로: the main window opens an empty window with window.open and moves the popup's own DOM into it
// (the way Obsidian and Electron preference panes do), so the popup keeps its state and needs no loading.
// This side only builds that window: on the opener's own browser environment, so the main window can reach into it.
use tauri::webview::{NewWindowFeatures, NewWindowResponse};
use tauri::{AppHandle, Emitter, Manager, Runtime, Url, WebviewUrl, WebviewWindowBuilder, Window};

const PREFIX: &str = "popout-";

pub fn is_popout(label: &str) -> bool {
    label.starts_with(PREFIX)
}

/// The window a request from the main window's window.open("about:blank#popout-N|title") gets; anything else
/// (a link opened with window.open) keeps the default.
pub fn on_new_window<R: Runtime>(app: &AppHandle<R>, url: Url, features: NewWindowFeatures) -> NewWindowResponse<R> {
    let Some((label, title)) = popout_request(&url) else { return NewWindowResponse::Allow };
    if app.get_webview_window(&label).is_some() {
        return NewWindowResponse::Deny;
    }
    let built = WebviewWindowBuilder::new(app, &label, WebviewUrl::External("about:blank".parse().expect("about:blank")))
        .window_features(features)
        .title(if title.is_empty() { "Arkism".to_string() } else { format!("{title} · Arkism") })
        .min_inner_size(360.0, 320.0)
        .focused(true)
        .build();
    match built {
        Ok(window) => NewWindowResponse::Create { window },
        Err(_) => NewWindowResponse::Deny,
    }
}

/// "about:blank#popout-7|기억 처리" → ("popout-7", "기억 처리"); the label must be popout- and digits.
fn popout_request(url: &Url) -> Option<(String, String)> {
    if url.scheme() != "about" || url.path() != "blank" {
        return None;
    }
    let frag = url.fragment()?;
    let (label, title) = frag.split_once('|').unwrap_or((frag, ""));
    let n = label.strip_prefix(PREFIX)?;
    if n.is_empty() || n.len() > 9 || !n.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    let title = percent_decode(title);
    Some((label.to_string(), title.chars().filter(|c| !c.is_control()).take(80).collect()))
}

fn percent_decode(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' && i + 2 < b.len() {
            if let Ok(v) = u8::from_str_radix(&s[i + 1..i + 3], 16) {
                out.push(v);
                i += 3;
                continue;
            }
        }
        out.push(b[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// The OS ✕ on a popout window: ask the main window, which closes the popup the way Esc does (so unsaved edits ask first).
pub fn close_requested<R: Runtime>(window: &Window<R>) {
    let _ = window.app_handle().emit_to("main", "popout-close", window.label());
}

/// From the main window: its popup closed, so the window goes.
#[tauri::command]
pub fn popout_destroy(app: AppHandle, window: Window, label: String) -> Result<(), String> {
    if window.label() != "main" || !is_popout(&label) {
        return Err("popout_from_main_only".into());
    }
    match app.get_webview_window(&label) {
        Some(w) => w.destroy().map_err(|e| e.to_string()),
        None => Ok(()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn popout_requests() {
        let u = |s: &str| Url::parse(s).unwrap();
        assert_eq!(popout_request(&u("about:blank#popout-3|%EA%B8%B0%EC%96%B5")), Some(("popout-3".into(), "기억".into())));
        assert_eq!(popout_request(&u("about:blank#popout-12")), Some(("popout-12".into(), String::new())));
        assert_eq!(popout_request(&u("about:blank#main")), None);
        assert_eq!(popout_request(&u("about:blank#popout-x")), None);
        assert_eq!(popout_request(&u("https://github.com/#popout-1")), None);
        assert_eq!(popout_request(&u("about:blank")), None);
        assert!(is_popout("popout-3") && !is_popout("main"));
    }
}
