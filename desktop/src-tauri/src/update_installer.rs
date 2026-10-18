//! Holds verified staging bytes and their path components through process creation.
//!
//! The Windows file sharing and loader contract needs the native inert fixture.
//! This guard is not an installer signature check or a privileged-process sandbox.

use sha2::{Digest, Sha256};
use std::fmt;
use std::io::{self, Read};
#[cfg(windows)]
use std::path::Path;
use std::time::Duration;

pub const MAX_BYTES: u64 = 512 * 1024 * 1024;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Cause {
    pub operation: &'static str,
    pub code: Option<i32>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Error {
    pub causes: Vec<Cause>,
}

impl Error {
    fn fixed(operation: &'static str) -> Self {
        Self {
            causes: vec![Cause {
                operation,
                code: None,
            }],
        }
    }
    fn io(operation: &'static str, error: io::Error) -> Self {
        Self {
            causes: vec![Cause {
                operation,
                code: error.raw_os_error(),
            }],
        }
    }
    fn with_cleanup(mut self, cleanup: Result<(), Self>) -> Self {
        if let Err(error) = cleanup {
            self.causes.extend(error.causes);
        }
        self
    }
}

impl fmt::Display for Error {
    fn fmt(&self, out: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(out, "update_installer")?;
        for cause in &self.causes {
            write!(out, ":{}", cause.operation)?;
            if let Some(code) = cause.code {
                write!(out, "({code})")?;
            }
        }
        Ok(())
    }
}
impl std::error::Error for Error {}

fn expected_hash(value: &str) -> Result<[u8; 32], Error> {
    if value.len() != 64 || !value.bytes().all(|b| b.is_ascii_hexdigit()) {
        return Err(Error::fixed("invalid_hash"));
    }
    let mut hash = [0; 32];
    for (index, byte) in hash.iter_mut().enumerate() {
        *byte = u8::from_str_radix(&value[index * 2..index * 2 + 2], 16)
            .map_err(|_| Error::fixed("invalid_hash"))?;
    }
    Ok(hash)
}

fn validate_name(name: &str) -> Result<(), Error> {
    if !name.starts_with("Arkism-")
        || !name.ends_with("-setup.exe")
        || name.len() > 128
        || !name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
    {
        return Err(Error::fixed("invalid_filename"));
    }
    Ok(())
}

fn validate_length(length: u64) -> Result<(), Error> {
    if length == 0 || length > MAX_BYTES {
        return Err(Error::fixed("invalid_length"));
    }
    Ok(())
}

fn verify_reader(reader: &mut dyn Read, length: u64, expected: &[u8; 32]) -> Result<(), Error> {
    let limit = length
        .checked_add(1)
        .ok_or_else(|| Error::fixed("invalid_length"))?;
    let mut reader = reader.take(limit);
    let mut hash = Sha256::new();
    let mut buffer = [0; 64 * 1024];
    let mut total = 0u64;
    loop {
        let count = reader
            .read(&mut buffer)
            .map_err(|e| Error::io("read_locked", e))?;
        if count == 0 {
            break;
        }
        total += count as u64;
        if total > length {
            return Err(Error::fixed("length_changed"));
        }
        hash.update(&buffer[..count]);
    }
    if total != length {
        return Err(Error::fixed("length_changed"));
    }
    if hash.finalize().as_slice() != expected {
        return Err(Error::fixed("hash_changed"));
    }
    Ok(())
}

trait Storage {
    fn write(&mut self, bytes: &[u8]) -> Result<(), Error>;
    fn seal(&mut self) -> Result<(), Error>;
    fn reader(&mut self) -> Result<&mut dyn Read, Error>;
    fn cleanup(&mut self) -> Result<(), Error>;
}

fn prepare_core<S: Storage>(
    bytes: &[u8],
    hash: &str,
    filename: &str,
    create: impl FnOnce() -> Result<S, Error>,
) -> Result<S, Error> {
    validate_name(filename)?;
    let expected = expected_hash(hash)?;
    validate_length(bytes.len() as u64)?;
    if Sha256::digest(bytes).as_slice() != expected {
        return Err(Error::fixed("input_hash_mismatch"));
    }
    let mut storage = create()?;
    let prepared = (|| {
        storage.write(bytes)?;
        storage.seal()?;
        verify_reader(storage.reader()?, bytes.len() as u64, &expected)
    })();
    match prepared {
        Ok(()) => Ok(storage),
        Err(error) => Err(error.with_cleanup(storage.cleanup())),
    }
}

/// Opaque, non-cloneable owner. Dropping releases handles without deleting files.
/// A successful launch deliberately leaves its staging file for the installer.
pub struct Installer {
    #[cfg(windows)]
    storage: windows::Staging,
    launched: bool,
}

/// Process creation has succeeded. Waiting/closing this observer never reclassifies
/// that handoff as a failed launch. The child owns inherited protection handles.
pub struct Launched {
    #[cfg(windows)]
    process: windows::Process,
}

impl Launched {
    pub fn wait(&mut self, timeout: Duration) -> Result<Option<u32>, Error> {
        #[cfg(windows)]
        {
            self.process.wait(timeout)
        }
        #[cfg(not(windows))]
        {
            let _ = timeout;
            Err(Error::fixed("windows_only"))
        }
    }
}

impl Installer {
    pub fn prepare(bytes: &[u8], hash: &str, filename: &str) -> Result<Self, Error> {
        #[cfg(windows)]
        {
            Self::prepare_in(&std::env::temp_dir(), bytes, hash, filename)
        }
        #[cfg(not(windows))]
        {
            let _ = (bytes, hash, filename);
            Err(Error::fixed("windows_only"))
        }
    }

    #[cfg(windows)]
    pub fn prepare_in(
        root: &Path,
        bytes: &[u8],
        hash: &str,
        filename: &str,
    ) -> Result<Self, Error> {
        let storage = prepare_core(bytes, hash, filename, || windows::Staging::create(root))?;
        Ok(Self {
            storage,
            launched: false,
        })
    }

    pub fn spawn(&mut self) -> Result<Launched, Error> {
        #[cfg(windows)]
        {
            self.spawn_arguments(&[std::ffi::OsString::from("/S")], None)
        }
        #[cfg(not(windows))]
        {
            Err(Error::fixed("windows_only"))
        }
    }

    #[cfg(windows)]
    fn spawn_arguments(
        &mut self,
        arguments: &[std::ffi::OsString],
        environment: Option<&[(std::ffi::OsString, std::ffi::OsString)]>,
    ) -> Result<Launched, Error> {
        if self.launched {
            return Err(Error::fixed("already_launched"));
        }
        self.storage.verify()?;
        let process = self.storage.spawn(arguments, environment)?;
        self.launched = true;
        Ok(Launched { process })
    }

    #[cfg(all(test, windows))]
    pub fn path(&self) -> &Path {
        &self.storage.path
    }

    /// Only a failed, unlaunched handoff may request cleanup. No recursive deletion.
    pub fn cleanup(self) -> Result<(), Error> {
        if self.launched {
            return Err(Error::fixed("launched_file_preserved"));
        }
        #[cfg(windows)]
        {
            let mut owned = self;
            owned.storage.cleanup()
        }
        #[cfg(not(windows))]
        {
            Err(Error::fixed("windows_only"))
        }
    }

    #[cfg(all(test, windows))]
    pub fn spawn_inert(
        &mut self,
        arguments: &[std::ffi::OsString],
        environment: &[(std::ffi::OsString, std::ffi::OsString)],
    ) -> Result<Launched, Error> {
        self.spawn_arguments(arguments, Some(environment))
    }

    #[cfg(all(test, windows))]
    pub fn prepare_interleaved(
        root: &Path,
        bytes: &[u8],
        hash: &str,
        hook: Box<dyn FnOnce(&Path)>,
    ) -> Result<Self, Error> {
        let storage = prepare_core(bytes, hash, "Arkism-1-setup.exe", || {
            let mut storage = windows::Staging::create(root)?;
            storage.before_lock = Some(hook);
            Ok(storage)
        })?;
        Ok(Self {
            storage,
            launched: false,
        })
    }

    #[cfg(all(test, windows))]
    pub fn prepare_named_for_test(
        root: &Path,
        bytes: &[u8],
        hash: &str,
        name: &str,
    ) -> Result<Self, Error> {
        let storage = prepare_core(bytes, hash, "Arkism-1-setup.exe", || {
            windows::Staging::create_named(root, name)
        })?;
        Ok(Self {
            storage,
            launched: false,
        })
    }

    #[cfg(all(test, windows))]
    pub fn inherited_guards_for_test() -> Result<usize, Error> {
        windows::inherited_guards_for_test()
    }

    #[cfg(all(test, windows))]
    pub fn locked_hash_for_test(&mut self, length: u64, hash: &str) -> Result<(), Error> {
        self.storage.rehash_for_test(length, hash)
    }

    #[cfg(all(test, windows))]
    pub fn prepare_before_create(
        root: &Path,
        bytes: &[u8],
        hash: &str,
        hook: impl FnOnce(&Path),
    ) -> Result<Self, Error> {
        let storage = prepare_core(bytes, hash, "Arkism-1-setup.exe", || {
            windows::Staging::create_interleaved(root, hook)
        })?;
        Ok(Self {
            storage,
            launched: false,
        })
    }
}

#[cfg(windows)]
mod windows {
    #[cfg(test)]
    use super::{expected_hash, verify_reader};
    use super::{Error, Storage};
    use std::ffi::{c_void, OsStr, OsString};
    use std::fs::{File, OpenOptions};
    #[cfg(test)]
    use std::io::Seek;
    use std::io::{self, Read, Write};
    use std::os::windows::ffi::{OsStrExt, OsStringExt};
    use std::os::windows::fs::OpenOptionsExt;
    use std::os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle};
    use std::path::{Component, Path, PathBuf, Prefix};
    use std::time::Duration;

    const READ_ATTRIBUTES: u32 = 0x80;
    const DELETE: u32 = 0x10000;
    const GENERIC_WRITE: u32 = 0x40000000;
    const SHARE_READ: u32 = 1;
    const SHARE_ALL: u32 = 7;
    const OPEN_REPARSE: u32 = 0x00200000;
    const BACKUP_SEMANTICS: u32 = 0x02000000;
    const DIRECTORY: u32 = 0x10;
    const REPARSE: u32 = 0x400;
    const MAX_PATH_UNITS: usize = 32768;
    const SYNCHRONIZE: u32 = 0x100000;

    #[repr(C)]
    struct UnicodeString {
        length: u16,
        maximum: u16,
        buffer: *mut u16,
    }
    #[repr(C)]
    struct ObjectAttributes {
        length: u32,
        root: *mut c_void,
        name: *mut UnicodeString,
        attributes: u32,
        security: *mut c_void,
        quality: *mut c_void,
    }
    #[repr(C)]
    struct IoStatus {
        status: usize,
        information: usize,
    }
    #[repr(C)]
    struct StartupInfo {
        size: u32,
        reserved: *mut u16,
        desktop: *mut u16,
        title: *mut u16,
        x: u32,
        y: u32,
        width: u32,
        height: u32,
        columns: u32,
        rows: u32,
        fill: u32,
        flags: u32,
        show: u16,
        reserved_size: u16,
        reserved_bytes: *mut u8,
        stdin: *mut c_void,
        stdout: *mut c_void,
        stderr: *mut c_void,
    }
    #[repr(C)]
    struct StartupEx {
        info: StartupInfo,
        attributes: *mut c_void,
    }
    #[repr(C)]
    #[derive(Default)]
    struct ProcessInformation {
        process: *mut c_void,
        thread: *mut c_void,
        pid: u32,
        tid: u32,
    }

    #[link(name = "ntdll")]
    extern "system" {
        fn NtCreateFile(
            file: *mut *mut c_void,
            access: u32,
            object: *mut ObjectAttributes,
            status: *mut IoStatus,
            allocation: *const i64,
            attributes: u32,
            sharing: u32,
            disposition: u32,
            options: u32,
            ea: *mut c_void,
            ea_length: u32,
        ) -> i32;
        fn RtlNtStatusToDosError(status: i32) -> u32;
    }

    #[repr(C)]
    #[derive(Default)]
    struct FileTime {
        low: u32,
        high: u32,
    }
    #[repr(C)]
    #[derive(Default)]
    struct Information {
        attributes: u32,
        creation: FileTime,
        access: FileTime,
        write: FileTime,
        volume: u32,
        size_high: u32,
        size_low: u32,
        links: u32,
        index_high: u32,
        index_low: u32,
    }
    #[derive(Clone, Copy, PartialEq, Eq)]
    struct Identity {
        volume: u64,
        id: [u8; 16],
    }
    #[repr(C)]
    #[derive(Default)]
    struct FileIdInformation {
        volume: u64,
        id: [u8; 16],
    }

    #[link(name = "kernel32")]
    extern "system" {
        fn GetFileInformationByHandle(handle: *mut c_void, information: *mut Information) -> i32;
        fn GetFileInformationByHandleEx(
            handle: *mut c_void,
            class: u32,
            information: *mut c_void,
            size: u32,
        ) -> i32;
        fn GetFinalPathNameByHandleW(
            handle: *mut c_void,
            path: *mut u16,
            size: u32,
            flags: u32,
        ) -> u32;
        fn SetFileInformationByHandle(
            handle: *mut c_void,
            class: u32,
            data: *const c_void,
            size: u32,
        ) -> i32;
        fn GetCurrentProcess() -> *mut c_void;
        fn DuplicateHandle(
            source_process: *mut c_void,
            source: *mut c_void,
            target_process: *mut c_void,
            target: *mut *mut c_void,
            access: u32,
            inherit: i32,
            options: u32,
        ) -> i32;
        fn InitializeProcThreadAttributeList(
            list: *mut c_void,
            count: u32,
            flags: u32,
            size: *mut usize,
        ) -> i32;
        fn UpdateProcThreadAttribute(
            list: *mut c_void,
            flags: u32,
            attribute: usize,
            value: *mut c_void,
            size: usize,
            previous: *mut c_void,
            returned: *mut usize,
        ) -> i32;
        fn DeleteProcThreadAttributeList(list: *mut c_void);
        fn CreateProcessW(
            application: *const u16,
            command: *mut u16,
            process_security: *const c_void,
            thread_security: *const c_void,
            inherit: i32,
            flags: u32,
            environment: *const c_void,
            directory: *const u16,
            startup: *const StartupInfo,
            information: *mut ProcessInformation,
        ) -> i32;
        fn WaitForSingleObject(handle: *mut c_void, milliseconds: u32) -> u32;
        fn GetExitCodeProcess(handle: *mut c_void, code: *mut u32) -> i32;
    }

    fn create_relative(parent: &File, name: &OsStr) -> Result<File, Error> {
        let mut name: Vec<u16> = name.encode_wide().collect();
        let length = u16::try_from(name.len() * 2).map_err(|_| Error::fixed("name_too_long"))?;
        if name.is_empty() || name.iter().any(|c| [0, 47, 92, 58].contains(c)) {
            return Err(Error::fixed("invalid_stage_name"));
        }
        let mut unicode = UnicodeString {
            length,
            maximum: length,
            buffer: name.as_mut_ptr(),
        };
        let mut object = ObjectAttributes {
            length: std::mem::size_of::<ObjectAttributes>() as u32,
            root: parent.as_raw_handle(),
            name: &mut unicode,
            attributes: 0x40,
            security: std::ptr::null_mut(),
            quality: std::ptr::null_mut(),
        };
        let mut status = IoStatus {
            status: 0,
            information: 0,
        };
        let mut handle = std::ptr::null_mut();
        // FILE_CREATE and FILE_NON_DIRECTORY_FILE: atomic ownership, relative to
        // the held directory object rather than another traversal of its name.
        let result = unsafe {
            NtCreateFile(
                &mut handle,
                GENERIC_WRITE | READ_ATTRIBUTES | DELETE | SYNCHRONIZE,
                &mut object,
                &mut status,
                std::ptr::null(),
                0x80,
                SHARE_READ,
                2,
                0x40 | 0x20 | 0x00200000,
                std::ptr::null_mut(),
                0,
            )
        };
        if result < 0 {
            return Err(Error::io(
                "create_new_relative",
                io::Error::from_raw_os_error(unsafe { RtlNtStatusToDosError(result) } as i32),
            ));
        }
        if handle.is_null() || handle as isize == -1 {
            return Err(Error::fixed("create_handle_missing"));
        }
        let file = unsafe { File::from_raw_handle(handle) };
        if result != 0 || status.information != 2 {
            return Err(Error::fixed("creation_unconfirmed"));
        }
        Ok(file)
    }

    fn information(file: &File, directory: bool) -> Result<Identity, Error> {
        information_raw(file.as_raw_handle(), directory)
    }

    fn information_raw(handle: *mut c_void, directory: bool) -> Result<Identity, Error> {
        let mut info = Information::default();
        if unsafe { GetFileInformationByHandle(handle, &mut info) } == 0 {
            return Err(Error::io("query_identity", io::Error::last_os_error()));
        }
        if info.attributes & REPARSE != 0 || (info.attributes & DIRECTORY != 0) != directory {
            return Err(Error::fixed("nonordinary_object"));
        }
        if !directory && info.links != 1 {
            return Err(Error::fixed("unexpected_links"));
        }
        let mut id = FileIdInformation::default();
        if unsafe {
            GetFileInformationByHandleEx(
                handle,
                18,
                &mut id as *mut _ as *mut c_void,
                std::mem::size_of::<FileIdInformation>() as u32,
            )
        } == 0
        {
            return Err(Error::io("file_identity", io::Error::last_os_error()));
        }
        Ok(Identity {
            volume: id.volume,
            id: id.id,
        })
    }

    #[cfg(test)]
    pub(super) fn inherited_guards_for_test() -> Result<usize, Error> {
        let values = std::env::var("ARKISM_UPDATE_IDENTITY_HANDLES")
            .map_err(|_| Error::fixed("test_handles_missing"))?;
        let mut count = 0;
        let mut files = 0;
        for value in values.split(',') {
            let fields: Vec<_> = value.split(':').collect();
            if fields.len() != 4 || !matches!(fields[3], "0" | "1") {
                return Err(Error::fixed("test_handles_invalid"));
            }
            let directory = fields[3] == "1";
            let handle = fields[0]
                .parse::<usize>()
                .map_err(|_| Error::fixed("test_handles_invalid"))?;
            let volume = fields[1]
                .parse::<u64>()
                .map_err(|_| Error::fixed("test_handles_invalid"))?;
            let id = test_file_id(fields[2])?;
            if information_raw(handle as *mut c_void, directory)? != (Identity { volume, id }) {
                return Err(Error::fixed("test_inherited_identity_changed"));
            }
            count += 1;
            if !directory {
                files += 1;
            }
        }
        if count < 2 || files != 1 {
            return Err(Error::fixed("test_handles_incomplete"));
        }
        Ok(count)
    }

    #[cfg(test)]
    fn test_file_id(value: &str) -> Result<[u8; 16], Error> {
        if value.len() != 32 || !value.bytes().all(|byte| byte.is_ascii_hexdigit()) {
            return Err(Error::fixed("test_handles_invalid"));
        }
        let mut id = [0; 16];
        for (index, byte) in id.iter_mut().enumerate() {
            *byte = u8::from_str_radix(&value[index * 2..index * 2 + 2], 16)
                .map_err(|_| Error::fixed("test_handles_invalid"))?;
        }
        Ok(id)
    }

    fn directory(path: &Path) -> Result<File, Error> {
        let file = OpenOptions::new()
            .access_mode(READ_ATTRIBUTES)
            .share_mode(SHARE_READ)
            .custom_flags(OPEN_REPARSE | BACKUP_SEMANTICS)
            .open(path)
            .map_err(|e| Error::io("hold_directory", e))?;
        information(&file, true)?;
        Ok(file)
    }

    fn valid_root(path: &Path) -> bool {
        if !path.is_absolute() {
            return false;
        }
        match path.components().next() {
            Some(Component::Prefix(prefix)) => {
                matches!(prefix.kind(), Prefix::Disk(_) | Prefix::VerbatimDisk(_))
                    || matches!(prefix.kind(), Prefix::Verbatim(value) if value.to_str().map_or(false, |v| v.starts_with("Volume{") && v.ends_with('}')))
            }
            _ => false,
        }
    }

    fn hold_directories(path: &Path) -> Result<Vec<(File, Identity)>, Error> {
        if !valid_root(path) {
            return Err(Error::fixed("unsupported_temp_path"));
        }
        let mut current = PathBuf::new();
        let mut holds = Vec::new();
        for component in path.components() {
            match component {
                Component::Prefix(_) => {
                    current.push(component.as_os_str());
                }
                Component::RootDir | Component::Normal(_) => {
                    current.push(component.as_os_str());
                    let file = directory(&current)?;
                    let id = information(&file, true)?;
                    holds.push((file, id));
                }
                _ => return Err(Error::fixed("nonordinary_path")),
            }
        }
        if holds.is_empty() {
            return Err(Error::fixed("unsupported_temp_path"));
        }
        Ok(holds)
    }

    fn final_path(file: &File) -> Result<PathBuf, Error> {
        let mut buffer = vec![0u16; MAX_PATH_UNITS];
        let count = unsafe {
            GetFinalPathNameByHandleW(
                file.as_raw_handle(),
                buffer.as_mut_ptr(),
                buffer.len() as u32,
                1,
            )
        } as usize;
        if count == 0 {
            return Err(Error::io("volume_path", io::Error::last_os_error()));
        }
        if count >= buffer.len() {
            return Err(Error::fixed("volume_path_too_long"));
        }
        let path = PathBuf::from(OsString::from_wide(&buffer[..count]));
        if !valid_root(&path) {
            return Err(Error::fixed("unsupported_volume_path"));
        }
        Ok(path)
    }

    struct Attributes {
        buffer: Vec<usize>,
        initialized: bool,
    }
    impl Attributes {
        fn new(handles: &mut [*mut c_void]) -> Result<Self, Error> {
            let mut size = 0usize;
            let first =
                unsafe { InitializeProcThreadAttributeList(std::ptr::null_mut(), 1, 0, &mut size) };
            if first != 0
                || io::Error::last_os_error().raw_os_error() != Some(122)
                || size == 0
                || size > 65536
            {
                return Err(Error::fixed("attribute_size"));
            }
            let mut list = Self {
                buffer: vec![
                    0;
                    (size + std::mem::size_of::<usize>() - 1)
                        / std::mem::size_of::<usize>()
                ],
                initialized: false,
            };
            if unsafe { InitializeProcThreadAttributeList(list.pointer(), 1, 0, &mut size) } == 0 {
                return Err(Error::io(
                    "attribute_initialize",
                    io::Error::last_os_error(),
                ));
            }
            list.initialized = true;
            if unsafe {
                UpdateProcThreadAttribute(
                    list.pointer(),
                    0,
                    0x00020002,
                    handles.as_mut_ptr() as *mut c_void,
                    std::mem::size_of_val(handles),
                    std::ptr::null_mut(),
                    std::ptr::null_mut(),
                )
            } == 0
            {
                return Err(Error::io("attribute_handles", io::Error::last_os_error()));
            }
            Ok(list)
        }
        fn pointer(&mut self) -> *mut c_void {
            self.buffer.as_mut_ptr() as *mut c_void
        }
    }
    impl Drop for Attributes {
        fn drop(&mut self) {
            if self.initialized {
                unsafe {
                    DeleteProcThreadAttributeList(self.pointer());
                }
            }
        }
    }

    fn inherited(file: &File) -> Result<OwnedHandle, Error> {
        let mut duplicate = std::ptr::null_mut();
        let process = unsafe { GetCurrentProcess() };
        if unsafe {
            DuplicateHandle(
                process,
                file.as_raw_handle(),
                process,
                &mut duplicate,
                0,
                1,
                2,
            )
        } == 0
        {
            return Err(Error::io("duplicate_guard", io::Error::last_os_error()));
        }
        Ok(unsafe { OwnedHandle::from_raw_handle(duplicate) })
    }

    fn quoted(value: &OsStr, out: &mut Vec<u16>) -> Result<(), Error> {
        out.push(34);
        let mut slashes = 0;
        for value in value.encode_wide() {
            if value == 0 {
                return Err(Error::fixed("nul_argument"));
            }
            if value == 92 {
                slashes += 1;
                continue;
            }
            out.extend(std::iter::repeat(92).take(if value == 34 {
                slashes * 2 + 1
            } else {
                slashes
            }));
            out.push(value);
            slashes = 0;
        }
        out.extend(std::iter::repeat(92).take(slashes * 2));
        out.push(34);
        Ok(())
    }

    fn environment_block(entries: &[(OsString, OsString)]) -> Result<Vec<u16>, Error> {
        let mut entries = entries.to_vec();
        entries.sort_by_cached_key(|(key, _)| key.to_string_lossy().to_uppercase());
        let mut block = Vec::new();
        let mut previous = None;
        for (key, value) in entries {
            let folded = key.to_string_lossy().to_uppercase();
            if previous.as_ref() == Some(&folded) || key.is_empty() {
                return Err(Error::fixed("invalid_environment"));
            }
            previous = Some(folded);
            let key: Vec<_> = key.encode_wide().collect();
            let value: Vec<_> = value.encode_wide().collect();
            if key.iter().any(|c| *c == 0 || *c == 61) || value.contains(&0) {
                return Err(Error::fixed("invalid_environment"));
            }
            block.extend(key);
            block.push(61);
            block.extend(value);
            block.push(0);
        }
        if block.is_empty() {
            block.push(0);
        }
        block.push(0);
        Ok(block)
    }

    pub(super) struct Process {
        handle: OwnedHandle,
    }
    impl Process {
        pub fn wait(&mut self, timeout: Duration) -> Result<Option<u32>, Error> {
            let milliseconds =
                u32::try_from(timeout.as_millis()).map_err(|_| Error::fixed("invalid_wait"))?;
            if milliseconds == u32::MAX {
                return Err(Error::fixed("invalid_wait"));
            }
            match unsafe { WaitForSingleObject(self.handle.as_raw_handle(), milliseconds) } {
                0 => {
                    let mut code = 0;
                    if unsafe { GetExitCodeProcess(self.handle.as_raw_handle(), &mut code) } == 0 {
                        return Err(Error::io("exit_code", io::Error::last_os_error()));
                    }
                    Ok(Some(code))
                }
                258 => Ok(None),
                _ => Err(Error::io("wait", io::Error::last_os_error())),
            }
        }
    }

    pub(super) struct Staging {
        pub path: PathBuf,
        holds: Vec<(File, Identity)>,
        writer: Option<File>,
        locked: Option<File>,
        // A separate attribute-only open keeps the creator object alive across
        // writer/read/delete reopens without retaining a writer FileObject.
        anchor: File,
        identity: Identity,
        #[cfg(test)]
        pub before_lock: Option<Box<dyn FnOnce(&Path)>>,
    }

    impl Staging {
        pub fn create(root: &Path) -> Result<Self, Error> {
            let mut random = [0u8; 16];
            getrandom::fill(&mut random).map_err(|_| Error::fixed("random_failed"))?;
            let unique: String = random.iter().map(|b| format!("{b:02x}")).collect();
            Self::create_named(root, &format!("Arkism-update-{unique}-setup.exe"))
        }

        pub fn create_named(root: &Path, name: &str) -> Result<Self, Error> {
            let holds = hold_directories(root)?;
            Self::create_held(root, name, holds)
        }

        #[cfg(test)]
        pub fn create_interleaved(root: &Path, hook: impl FnOnce(&Path)) -> Result<Self, Error> {
            let holds = hold_directories(root)?;
            hook(root);
            Self::create_held(root, "Arkism-update-relative-probe-setup.exe", holds)
        }

        fn create_held(
            root: &Path,
            name: &str,
            holds: Vec<(File, Identity)>,
        ) -> Result<Self, Error> {
            let path = root.join(name);
            let writer = create_relative(
                &holds
                    .last()
                    .ok_or_else(|| Error::fixed("temp_handle_missing"))?
                    .0,
                path.file_name()
                    .ok_or_else(|| Error::fixed("stage_name_missing"))?,
            )?;
            let identity = information(&writer, false)?;
            let anchor = OpenOptions::new()
                .access_mode(READ_ATTRIBUTES)
                .share_mode(SHARE_ALL)
                .custom_flags(OPEN_REPARSE)
                .open(&path)
                .map_err(|e| Error::io("anchor_file", e))?;
            if information(&anchor, false)? != identity {
                return Err(Error::fixed("anchor_identity_changed"));
            }
            Ok(Self {
                path,
                holds,
                writer: Some(writer),
                locked: None,
                anchor,
                identity,
                #[cfg(test)]
                before_lock: None,
            })
        }

        pub fn verify(&self) -> Result<(), Error> {
            if information(&self.anchor, false)? != self.identity {
                return Err(Error::fixed("anchor_identity_changed"));
            }
            for (file, expected) in &self.holds {
                if information(file, true)? != *expected {
                    return Err(Error::fixed("directory_identity_changed"));
                }
            }
            let file = self
                .locked
                .as_ref()
                .ok_or_else(|| Error::fixed("not_locked"))?;
            if information(file, false)? != self.identity {
                return Err(Error::fixed("file_identity_changed"));
            }
            Ok(())
        }

        #[cfg(test)]
        pub fn rehash_for_test(&mut self, length: u64, hash: &str) -> Result<(), Error> {
            self.verify()?;
            let file = self
                .locked
                .as_mut()
                .ok_or_else(|| Error::fixed("not_locked"))?;
            file.rewind().map_err(|e| Error::io("test_rewind", e))?;
            verify_reader(file, length, &expected_hash(hash)?)
        }

        pub fn spawn(
            &self,
            arguments: &[OsString],
            environment: Option<&[(OsString, OsString)]>,
        ) -> Result<Process, Error> {
            let mut inherited_handles = Vec::new();
            for (file, _) in &self.holds {
                inherited_handles.push(inherited(file)?);
            }
            inherited_handles.push(inherited(
                self.locked
                    .as_ref()
                    .ok_or_else(|| Error::fixed("not_locked"))?,
            )?);
            // Explicit NUL stdio avoids inheriting unrelated console/pipe handles.
            // In particular an unread parent pipe cannot stall the silent child.
            let null = OpenOptions::new()
                .read(true)
                .write(true)
                .open("NUL")
                .map_err(|e| Error::io("null_stdio", e))?;
            let null = inherited(&null)?;
            let mut handles: Vec<_> = inherited_handles
                .iter()
                .map(AsRawHandle::as_raw_handle)
                .collect();
            handles.push(null.as_raw_handle());
            let mut attributes = Attributes::new(&mut handles)?;
            let mut startup: StartupEx = unsafe { std::mem::zeroed() };
            startup.info.size = std::mem::size_of::<StartupEx>() as u32;
            startup.info.flags = 0x100;
            startup.info.stdin = null.as_raw_handle();
            startup.info.stdout = null.as_raw_handle();
            startup.info.stderr = null.as_raw_handle();
            startup.attributes = attributes.pointer();
            let mut application: Vec<_> = self.path.as_os_str().encode_wide().collect();
            if application.contains(&0) {
                return Err(Error::fixed("nul_path"));
            }
            application.push(0);
            let mut command = Vec::new();
            quoted(self.path.as_os_str(), &mut command)?;
            for argument in arguments {
                command.push(32);
                quoted(argument, &mut command)?;
            }
            if command.len() >= 32767 {
                return Err(Error::fixed("command_too_long"));
            }
            command.push(0);
            let entries = environment.map(|entries| {
                #[allow(unused_mut)]
                let mut entries = entries.to_vec();
                #[cfg(test)]
                {
                    let identities = self
                        .holds
                        .iter()
                        .map(|(_, id)| (*id, true))
                        .chain(std::iter::once((self.identity, false)));
                    let observed: Vec<_> = handles
                        .iter()
                        .zip(identities)
                        .map(|(handle, (id, dir))| {
                            format!(
                                "{}:{}:{}:{}",
                                *handle as usize,
                                id.volume,
                                id.id
                                    .iter()
                                    .map(|byte| format!("{byte:02x}"))
                                    .collect::<String>(),
                                u8::from(dir)
                            )
                        })
                        .collect();
                    entries.push((
                        "ARKISM_UPDATE_IDENTITY_HANDLES".into(),
                        observed.join(",").into(),
                    ));
                }
                entries
            });
            let block = entries
                .as_ref()
                .map(|entries| environment_block(entries))
                .transpose()?;
            self.verify()?;
            let mut information = ProcessInformation::default();
            let created = unsafe {
                CreateProcessW(
                    application.as_ptr(),
                    command.as_mut_ptr(),
                    std::ptr::null(),
                    std::ptr::null(),
                    1,
                    0x00080000 | 0x400,
                    block
                        .as_ref()
                        .map_or(std::ptr::null(), |b| b.as_ptr() as *const c_void),
                    std::ptr::null(),
                    &startup.info,
                    &mut information,
                )
            };
            if created == 0 {
                return Err(Error::io("spawn", io::Error::last_os_error()));
            }
            // Nothing after successful CreateProcess may return a launch error.
            // Only explicit guard duplicates were inherited. Closing these parent
            // copies cannot release the child's file/directory sharing protection.
            let process = unsafe { OwnedHandle::from_raw_handle(information.process) };
            let _thread = unsafe { OwnedHandle::from_raw_handle(information.thread) };
            Ok(Process { handle: process })
        }
    }

    impl Storage for Staging {
        fn write(&mut self, bytes: &[u8]) -> Result<(), Error> {
            let writer = self
                .writer
                .as_mut()
                .ok_or_else(|| Error::fixed("writer_missing"))?;
            writer.write_all(bytes).map_err(|e| Error::io("write", e))?;
            writer.sync_all().map_err(|e| Error::io("flush", e))
        }
        fn seal(&mut self) -> Result<(), Error> {
            self.writer.take();
            #[cfg(test)]
            if let Some(hook) = self.before_lock.take() {
                hook(&self.path);
            }
            let file = OpenOptions::new()
                .read(true)
                .share_mode(SHARE_READ)
                .custom_flags(OPEN_REPARSE)
                .open(&self.path)
                .map_err(|e| Error::io("lock_file", e))?;
            if information(&file, false)? != self.identity {
                return Err(Error::fixed("file_identity_changed"));
            }
            self.locked = Some(file);
            let canonical = final_path(self.locked.as_ref().unwrap())?;
            self.holds.extend(hold_directories(
                canonical
                    .parent()
                    .ok_or_else(|| Error::fixed("volume_parent_missing"))?,
            )?);
            let same = OpenOptions::new()
                .read(true)
                .share_mode(SHARE_READ)
                .custom_flags(OPEN_REPARSE)
                .open(&canonical)
                .map_err(|e| Error::io("lock_volume_file", e))?;
            if information(&same, false)? != self.identity {
                return Err(Error::fixed("volume_identity_changed"));
            }
            self.path = canonical;
            self.verify()
        }
        fn reader(&mut self) -> Result<&mut dyn Read, Error> {
            Ok(self
                .locked
                .as_mut()
                .ok_or_else(|| Error::fixed("not_locked"))?)
        }
        fn cleanup(&mut self) -> Result<(), Error> {
            // Keep every ancestor fixed while upgrading only the owned file handle.
            if information(&self.anchor, false)? != self.identity {
                return Err(Error::fixed("cleanup_anchor_changed"));
            }
            for (file, id) in &self.holds {
                if information(file, true)? != *id {
                    return Err(Error::fixed("cleanup_directory_changed"));
                }
            }
            self.locked.take();
            let file = match self.writer.take() {
                Some(file) => file,
                None => OpenOptions::new()
                    .access_mode(DELETE | READ_ATTRIBUTES)
                    .share_mode(SHARE_READ)
                    .custom_flags(OPEN_REPARSE)
                    .open(&self.path)
                    .map_err(|e| Error::io("cleanup_open", e))?,
            };
            if information(&file, false)? != self.identity {
                return Err(Error::fixed("cleanup_identity_changed"));
            }
            let delete: u8 = 1;
            if unsafe {
                SetFileInformationByHandle(
                    file.as_raw_handle(),
                    4,
                    &delete as *const _ as *const c_void,
                    1,
                )
            } == 0
            {
                return Err(Error::io("cleanup_disposition", io::Error::last_os_error()));
            }
            Ok(())
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Cursor;
    use std::sync::{Arc, Mutex};

    const NAME: &str = "Arkism-1.2.3-setup.exe";
    fn hash(bytes: &[u8]) -> String {
        Sha256::digest(bytes)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect()
    }
    struct Fake {
        events: Arc<Mutex<Vec<&'static str>>>,
        bytes: Cursor<Vec<u8>>,
        fault: &'static str,
    }
    impl Fake {
        fn event(&self, name: &'static str) {
            self.events.lock().unwrap().push(name);
        }
    }
    impl Storage for Fake {
        fn write(&mut self, bytes: &[u8]) -> Result<(), Error> {
            self.event("write");
            if self.fault == "write" || self.fault == "cleanup" {
                return Err(Error::fixed("write_failed"));
            }
            self.bytes = Cursor::new(bytes.to_vec());
            Ok(())
        }
        fn seal(&mut self) -> Result<(), Error> {
            self.event("seal");
            if self.fault == "seal" {
                return Err(Error::fixed("writer_conflict"));
            }
            if self.fault == "mutate" {
                self.bytes = Cursor::new(b"bad".to_vec());
            }
            Ok(())
        }
        fn reader(&mut self) -> Result<&mut dyn Read, Error> {
            self.event("locked_read");
            Ok(&mut self.bytes)
        }
        fn cleanup(&mut self) -> Result<(), Error> {
            self.event("cleanup");
            if self.fault == "cleanup" {
                Err(Error::fixed("cleanup_unknown"))
            } else {
                Ok(())
            }
        }
    }
    fn prepare(fault: &'static str) -> (Result<Fake, Error>, Vec<&'static str>) {
        let events = Arc::new(Mutex::new(Vec::new()));
        let factory = || {
            events.lock().unwrap().push("create");
            Ok(Fake {
                events: events.clone(),
                bytes: Cursor::new(Vec::new()),
                fault,
            })
        };
        let result = prepare_core(b"abc", &hash(b"abc"), NAME, factory);
        let observed = events.lock().unwrap().clone();
        (result, observed)
    }

    #[test]
    fn installer_prepare_orders_locked_rehash_before_returning_guard() {
        let (result, events) = prepare("");
        assert!(result.is_ok());
        assert_eq!(events, ["create", "write", "seal", "locked_read"]);
    }
    #[test]
    fn installer_prepare_rejects_conflict_or_changed_bytes_and_cleans_only_failure() {
        for fault in ["write", "seal", "mutate"] {
            let (result, events) = prepare(fault);
            assert!(result.is_err());
            assert_eq!(events.last(), Some(&"cleanup"));
            if fault == "seal" {
                assert!(!events.contains(&"locked_read"));
            }
        }
    }
    #[test]
    fn installer_invalid_inputs_have_zero_storage_effects() {
        for (bytes, expected, name) in [
            (b"abc".as_slice(), hash(b"bad"), NAME),
            (b"".as_slice(), hash(b""), NAME),
            (b"abc".as_slice(), "a".repeat(63), NAME),
            (b"abc".as_slice(), hash(b"abc"), "../Arkism-1-setup.exe"),
        ] {
            let result: Result<Fake, Error> = prepare_core(bytes, &expected, name, || {
                panic!("invalid input created storage")
            });
            assert!(result.is_err());
        }
    }
    #[test]
    fn installer_uppercase_expected_hash_keeps_same_bytes() {
        let expected = hash(b"abc").to_ascii_uppercase();
        assert!(verify_reader(
            &mut Cursor::new(b"abc"),
            3,
            &expected_hash(&expected).unwrap()
        )
        .is_ok());
    }
    #[test]
    fn installer_cleanup_preserves_primary_and_cleanup_causes_without_paths() {
        let (result, events) = prepare("cleanup");
        assert_eq!(
            result.err().unwrap().to_string(),
            "update_installer:write_failed:cleanup_unknown"
        );
        assert_eq!(events, ["create", "write", "cleanup"]);
        assert_eq!(
            Error::io(
                "read_locked",
                io::Error::new(io::ErrorKind::Other, "private path")
            )
            .to_string(),
            "update_installer:read_locked"
        );
    }
    #[test]
    fn installer_payload_length_boundaries_are_checked_before_storage() {
        assert!(validate_length(0).is_err());
        assert!(validate_length(1).is_ok());
        assert!(validate_length(MAX_BYTES).is_ok());
        assert!(validate_length(MAX_BYTES + 1).is_err());
    }
    #[test]
    fn installer_locked_reader_rejects_short_extra_and_io_failure() {
        struct Broken;
        impl Read for Broken {
            fn read(&mut self, _: &mut [u8]) -> io::Result<usize> {
                Err(io::ErrorKind::PermissionDenied.into())
            }
        }
        let expected = expected_hash(&hash(b"abc")).unwrap();
        for mut reader in [
            Box::new(Cursor::new(b"ab".to_vec())) as Box<dyn Read>,
            Box::new(Cursor::new(b"abcd".to_vec())),
            Box::new(Broken),
        ] {
            assert!(verify_reader(&mut reader, 3, &expected).is_err());
        }
    }
    #[test]
    fn installer_locked_endless_reader_stops_at_exact_limit_plus_one() {
        struct Endless(usize);
        impl Read for Endless {
            fn read(&mut self, out: &mut [u8]) -> io::Result<usize> {
                out.fill(b'a');
                self.0 += out.len();
                Ok(out.len())
            }
        }
        let mut reader = Endless(0);
        assert!(verify_reader(&mut reader, 7, &[0; 32]).is_err());
        assert_eq!(reader.0, 8);
    }
}
