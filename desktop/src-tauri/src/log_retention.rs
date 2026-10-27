//! Bounded Windows snapshots. Pin directories and files against replacement;
//! reject reparse points and hard links before touching log contents.
use std::fs::{self, File, OpenOptions};
use std::io::{Read, Seek, SeekFrom, Write};
use std::os::windows::{fs::OpenOptionsExt, io::AsRawHandle};
use std::path::Path;
use windows_sys::Win32::Storage::FileSystem::{
    GetFileInformationByHandle, BY_HANDLE_FILE_INFORMATION, FILE_ATTRIBUTE_DIRECTORY,
    FILE_ATTRIBUTE_REPARSE_POINT, FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT,
    FILE_SHARE_READ, FILE_SHARE_WRITE,
};

const LIMIT: u64 = 8 << 20;
const TAIL: u64 = 1 << 20;

fn validate(file: &File, directory: bool) -> Result<(), String> {
    let mut info: BY_HANDLE_FILE_INFORMATION = unsafe { std::mem::zeroed() };
    if unsafe { GetFileInformationByHandle(file.as_raw_handle() as _, &mut info) } == 0 {
        return Err("log_metadata".into());
    }
    if info.dwFileAttributes & FILE_ATTRIBUTE_REPARSE_POINT != 0
        || (info.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY != 0) != directory
        || (!directory && info.nNumberOfLinks != 1)
    {
        return Err("log_unsafe_file".into());
    }
    Ok(())
}

fn open(path: &Path, write: bool, create: bool) -> Result<Option<File>, String> {
    let file = match OpenOptions::new()
        .read(true)
        .write(write)
        .create(create)
        // No FILE_SHARE_DELETE: the validated handle and path cannot be swapped.
        .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
        .custom_flags(FILE_FLAG_OPEN_REPARSE_POINT)
        .open(path)
    {
        Ok(file) => file,
        Err(e) if !create && e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(_) => return Err("log_open".into()),
    };
    validate(&file, false)?;
    Ok(Some(file))
}

fn pin_directories(path: &Path) -> Result<Vec<File>, String> {
    if !path.is_absolute() {
        return Err("log_absolute_directory".into());
    }
    let mut holds = Vec::new();
    // Pin from the root inward, so replacing any ancestor also fails.
    for ancestor in path.ancestors().collect::<Vec<_>>().into_iter().rev() {
        let file = OpenOptions::new()
            .read(true)
            .share_mode(FILE_SHARE_READ | FILE_SHARE_WRITE)
            .custom_flags(FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT)
            .open(ancestor)
            .map_err(|_| "log_directory_open")?;
        validate(&file, true)?;
        holds.push(file);
    }
    Ok(holds)
}

fn write_snapshot(path: &Path, bytes: &[u8]) -> Result<(), String> {
    // Never truncate until the opened handle has passed link/type validation.
    let mut output = open(path, true, true)?.ok_or("log_snapshot_open")?;
    output.set_len(0).map_err(|_| "log_snapshot_truncate")?;
    output
        .write_all(bytes)
        .and_then(|_| output.sync_all())
        .map_err(|_| "log_snapshot_write".into())
}

fn rotate(directory: &Path, name: &str) -> Result<(), String> {
    let Some(mut active) = open(&directory.join(name), true, false)? else {
        return Ok(());
    };
    if active.metadata().map_err(|_| "log_metadata")?.len() <= LIMIT {
        return Ok(());
    }
    active
        .seek(SeekFrom::End(-(TAIL as i64)))
        .map_err(|_| "log_seek")?;
    let mut tail = Vec::with_capacity(TAIL as usize);
    (&mut active)
        .take(TAIL)
        .read_to_end(&mut tail)
        .map_err(|_| "log_read")?;
    for n in (1..3).rev() {
        if let Some(mut source) = open(&directory.join(format!("{name}.{n}")), false, false)? {
            let mut bytes = Vec::new();
            (&mut source)
                .take(TAIL)
                .read_to_end(&mut bytes)
                .map_err(|_| "log_snapshot_read")?;
            write_snapshot(&directory.join(format!("{name}.{}", n + 1)), &bytes)?;
        }
    }
    write_snapshot(&directory.join(format!("{name}.1")), &tail)?;
    active.set_len(0).map_err(|_| "log_truncate".into())
}

pub fn maintain(directory: &Path) -> Result<(), String> {
    match fs::symlink_metadata(directory) {
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(_) => return Err("log_metadata".into()),
        Ok(_) => {}
    }
    let _holds = pin_directories(directory)?;
    for name in ["server.log", "worker.log", "gateway.log", "postgres.log"] {
        rotate(directory, name)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn root() -> std::path::PathBuf {
        // a counter, not only the clock: Windows tests running in parallel can read the same time
        static NEXT: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let root = std::env::temp_dir().join(format!(
            "arkism-log-test-{}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos(),
            NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
        fs::create_dir(&root).unwrap();
        root
    }
    #[test]
    fn live_append_handle_survives_bounded_snapshots() {
        let root = root();
        let path = root.join("worker.log");
        let mut child = OpenOptions::new()
            .create_new(true)
            .append(true)
            .open(&path)
            .unwrap();
        for _ in 0..5 {
            OpenOptions::new()
                .write(true)
                .open(&path)
                .unwrap()
                .set_len(LIMIT + 1)
                .unwrap();
            maintain(&root).unwrap();
            child.write_all(b"after rotation\n").unwrap();
            assert_eq!(fs::read(&path).unwrap(), b"after rotation\n");
        }
        for n in 1..=3 {
            assert!(
                fs::metadata(root.join(format!("worker.log.{n}")))
                    .unwrap()
                    .len()
                    <= TAIL
            );
        }
        assert!(!root.join("worker.log.4").exists());
        drop(child);
        fs::remove_dir_all(root).unwrap();
    }
    #[test]
    fn rejects_linked_active_file_and_snapshot_without_truncating_target() {
        for name in ["worker.log", "worker.log.1"] {
            let root = root();
            let outside = root.join("unrelated.txt");
            fs::write(&outside, b"must survive").unwrap();
            if name.ends_with(".1") {
                File::create(root.join("worker.log"))
                    .unwrap()
                    .set_len(LIMIT + 1)
                    .unwrap();
            }
            fs::hard_link(&outside, root.join(name)).unwrap();
            assert!(maintain(&root).is_err());
            assert_eq!(fs::read(&outside).unwrap(), b"must survive");
            if name.ends_with(".1") {
                assert_eq!(
                    fs::metadata(root.join("worker.log")).unwrap().len(),
                    LIMIT + 1
                );
            }
            fs::remove_dir_all(root).unwrap();
        }
    }
    #[test]
    fn pinned_directory_and_file_cannot_be_replaced() {
        let root = root();
        let logs = root.join("logs");
        fs::create_dir(&logs).unwrap();
        fs::write(logs.join("worker.log"), b"before").unwrap();
        let holds = pin_directories(&logs).unwrap();
        let file = open(&logs.join("worker.log"), true, false)
            .unwrap()
            .unwrap();
        assert!(fs::rename(&logs, root.join("moved")).is_err());
        assert!(fs::rename(logs.join("worker.log"), logs.join("moved.log")).is_err());
        drop(file);
        drop(holds);
        fs::remove_dir_all(root).unwrap();
    }
}
