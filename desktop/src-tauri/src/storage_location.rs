//! Where the built-in Arkism keeps its data (Postgres, tokens, settings, logs). The default is the
//! app's local data folder (`runtime`); a folder the user chose is remembered in a small pointer
//! file beside it, read before the runtime is made. Data is never moved between the two.

use std::fs;
use std::path::{Path, PathBuf};

const POINTER: &str = "runtime-location.json";

/// The folder the user chose, if any. A broken pointer reads as none.
pub fn read(app_dir: &Path) -> Option<PathBuf> {
    let text = fs::read_to_string(app_dir.join(POINTER)).ok()?;
    let value: serde_json::Value = serde_json::from_str(&text).ok()?;
    let path = PathBuf::from(value.get("data")?.as_str()?);
    path.is_absolute().then_some(path)
}

/// Remembers `chosen`; None (or the default folder itself) goes back to the default.
pub fn write(app_dir: &Path, default: &Path, chosen: Option<&Path>) -> Result<(), String> {
    match chosen.filter(|p| *p != default) {
        None => match fs::remove_file(app_dir.join(POINTER)) {
            Err(e) if e.kind() != std::io::ErrorKind::NotFound => Err(format!("local_location_save: {e}")),
            _ => Ok(()),
        },
        Some(path) => {
            fs::create_dir_all(app_dir).map_err(|e| format!("local_location_save: {e}"))?;
            let text = serde_json::json!({ "data": path }).to_string();
            // Replaced whole: a write cut short must not leave a broken pointer that reads as the default.
            crate::runtime::write_private(&app_dir.join(POINTER), text.as_bytes()).map_err(|e| format!("local_location_save: {e}"))
        }
    }
}

/// True when `dir` already holds a built-in Arkism. Its settings (`runtime.json`) are written before
/// its database, so a `pg` folder alone is someone else's (and a start would reset it).
pub fn has_data(dir: &Path) -> bool {
    dir.join("runtime.json").is_file()
}

/// A usable data folder: an absolute path to an empty folder (made if missing) or an existing
/// Arkism data folder, where this user can write. Returns the folder as given.
pub fn check(path: &str) -> Result<PathBuf, String> {
    let path = PathBuf::from(path.trim());
    if path.as_os_str().is_empty() || !path.is_absolute() {
        return Err("local_location_absolute".into());
    }
    if path.exists() {
        if !path.is_dir() {
            return Err("local_location_not_folder".into());
        }
        let empty = fs::read_dir(&path).map_err(|e| format!("local_location_unreadable: {e}"))?.next().is_none();
        if !empty && !has_data(&path) {
            return Err("local_location_not_empty".into());
        }
    } else {
        fs::create_dir_all(&path).map_err(|e| format!("local_location_unwritable: {e}"))?;
    }
    // A new file under a fresh name: never writes through a file or link someone left there.
    let nanos = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_nanos()).unwrap_or(0);
    let probe = path.join(format!(".arkism-write-check-{}-{nanos}", std::process::id()));
    fs::OpenOptions::new().write(true).create_new(true).open(&probe).map_err(|e| format!("local_location_unwritable: {e}"))?;
    let _ = fs::remove_file(&probe);
    Ok(path)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("arkism-location-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn pointer_round_trips_and_default_clears_it() {
        let app = temp("pointer");
        let default = app.join("runtime");
        assert_eq!(read(&app), None);
        let chosen = app.join("elsewhere");
        write(&app, &default, Some(&chosen)).unwrap();
        assert_eq!(read(&app), Some(chosen));
        write(&app, &default, Some(&default)).unwrap();
        assert_eq!(read(&app), None);
        write(&app, &default, None).unwrap();
        fs::write(app.join(POINTER), r#"{"data":"relative/path"}"#).unwrap();
        assert_eq!(read(&app), None, "a relative pointer is ignored");
    }

    #[test]
    fn check_takes_empty_missing_or_arkism_folders_only() {
        let dir = temp("check");
        assert_eq!(check("relative").unwrap_err(), "local_location_absolute");
        assert_eq!(check("").unwrap_err(), "local_location_absolute");
        let missing = dir.join("new");
        assert_eq!(check(missing.to_str().unwrap()).unwrap(), missing);
        assert!(missing.is_dir());
        let other = dir.join("other");
        fs::create_dir_all(&other).unwrap();
        fs::write(other.join("notes.txt"), "x").unwrap();
        assert_eq!(check(other.to_str().unwrap()).unwrap_err(), "local_location_not_empty");
        fs::create_dir_all(other.join("pg")).unwrap();
        assert_eq!(check(other.to_str().unwrap()).unwrap_err(), "local_location_not_empty", "a pg folder alone is not Arkism's");
        fs::write(other.join("runtime.json"), "{}").unwrap();
        assert!(check(other.to_str().unwrap()).is_ok());
        assert_eq!(check(other.join("notes.txt").to_str().unwrap()).unwrap_err(), "local_location_not_folder");
        assert_eq!(fs::read_dir(&other).unwrap().count(), 3, "the write check leaves nothing behind");
    }
}
