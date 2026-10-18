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

/// Outcomes keep uncertain native ownership out of ordinary launch failures.
pub enum LaunchDisposition {
    Unstarted { installer: Installer, error: Error },
    Started(Launched),
    Unverified(PendingInstaller),
}

/// Closing a process observer is not confirmation that its child stopped.
/// An accidentally dropped unresolved owner conservatively retains its handles
/// and original guards until process teardown; it never kills or deletes.
struct Retained<T>(std::mem::ManuallyDrop<Option<T>>);
impl<T> Retained<T> {
    fn new(owner: T) -> Self {
        Self(std::mem::ManuallyDrop::new(Some(owner)))
    }
    fn get_mut(&mut self) -> &mut T {
        self.0.as_mut().expect("retained_owner_present")
    }
    fn take(mut self) -> T {
        self.0.take().expect("retained_owner_present")
    }
}

pub struct PendingInstaller {
    #[cfg(windows)]
    _state: Option<Retained<windows::PendingState>>,
    error: Error,
    phase: &'static str,
    resume: Option<u32>,
    may_have_run: bool,
}
impl PendingInstaller {
    pub fn primary_error(&self) -> &Error {
        &self.error
    }
    pub fn phase(&self) -> &'static str {
        self.phase
    }
    pub fn resume_observation(&self) -> Option<u32> {
        self.resume
    }
    pub fn may_have_run(&self) -> bool {
        self.may_have_run
    }
}

/// Inert metadata owner for Runtime/lifecycle storage tests only; no native state.
#[cfg(test)]
pub(crate) fn pending_test_owner() -> PendingInstaller {
    PendingInstaller {
        #[cfg(windows)]
        _state: None,
        error: Error::fixed("inert_pending_installer"),
        phase: "inert_factory",
        resume: None,
        may_have_run: false,
    }
}

trait SuspendedControl {
    fn verify_binding(&mut self) -> Result<(), Error>;
    fn abort_before_resume(&mut self) -> Result<(), Error>;
    fn resume_once(&mut self) -> Result<u32, Error>;
}
impl<T: SuspendedControl> SuspendedControl for Retained<T> {
    fn verify_binding(&mut self) -> Result<(), Error> {
        self.get_mut().verify_binding()
    }
    fn abort_before_resume(&mut self) -> Result<(), Error> {
        self.get_mut().abort_before_resume()
    }
    fn resume_once(&mut self) -> Result<u32, Error> {
        self.get_mut().resume_once()
    }
}
enum CoreDisposition<T> {
    Unstarted {
        owner: T,
        error: Error,
    },
    Started(T),
    Unverified {
        owner: T,
        error: Error,
        phase: &'static str,
        resume: Option<u32>,
        may_have_run: bool,
    },
}

fn exact_native_name(expected: &[u16], actual: &[u16]) -> bool {
    expected == actual
}

fn suspended_core<T: SuspendedControl>(mut owner: T) -> CoreDisposition<T> {
    if let Err(primary) = owner.verify_binding() {
        return match owner.abort_before_resume() {
            Ok(()) => CoreDisposition::Unstarted {
                owner,
                error: primary,
            },
            Err(abort) => CoreDisposition::Unverified {
                owner,
                error: primary.with_cleanup(Err(abort)),
                phase: "abort",
                resume: None,
                may_have_run: false,
            },
        };
    }
    match owner.resume_once() {
        Ok(1) => CoreDisposition::Started(owner),
        Ok(value) => CoreDisposition::Unverified {
            owner,
            error: Error::fixed("resume_observation"),
            phase: "resume",
            resume: Some(value),
            may_have_run: true,
        },
        Err(error) => CoreDisposition::Unverified {
            owner,
            error,
            phase: "resume",
            resume: Some(u32::MAX),
            may_have_run: true,
        },
    }
}

/// Opaque, non-cloneable owner. Dropping releases handles without deleting files.
/// A successful launch deliberately leaves its staging file for the installer.
pub struct Installer {
    #[cfg(windows)]
    storage: windows::Staging,
    launched: bool,
    #[cfg(windows)]
    length: u64,
    #[cfg(windows)]
    expected: [u8; 32],
    #[cfg(all(test, windows))]
    test_fault: BindingFault,
    #[cfg(all(test, windows))]
    test_other: Option<Box<Installer>>,
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
        #[cfg(test)]
        test_diagnostics::image(bytes);
        let storage = prepare_core(bytes, hash, filename, || windows::Staging::create(root))?;
        Ok(Self {
            storage,
            launched: false,
            length: bytes.len() as u64,
            expected: expected_hash(hash)?,
            #[cfg(test)]
            test_fault: BindingFault::None,
            #[cfg(test)]
            test_other: None,
        })
    }

    /// Consumes the original locked staging owner. Only an exact resumed binding
    /// commits; uncertainty always carries its native owner to the caller.
    pub fn launch(self) -> LaunchDisposition {
        #[cfg(windows)]
        {
            self.launch_arguments(None)
        }
        #[cfg(not(windows))]
        {
            LaunchDisposition::Unstarted {
                installer: self,
                error: Error::fixed("windows_only"),
            }
        }
    }

    #[cfg(windows)]
    fn launch_arguments(
        mut self,
        environment: Option<&[(std::ffi::OsString, std::ffi::OsString)]>,
    ) -> LaunchDisposition {
        #[cfg(test)]
        if self.launched {
            return LaunchDisposition::Unverified(PendingInstaller {
                _state: Some(Retained::new(windows::PendingState::already_started(self))),
                error: Error::fixed("already_launched"),
                phase: "already_started",
                resume: None,
                may_have_run: true,
            });
        }
        let binding = match self.storage.launch_binding(self.length, &self.expected) {
            Ok(binding) => binding,
            Err(error) => {
                return LaunchDisposition::Unstarted {
                    installer: self,
                    error,
                }
            }
        };
        let created = match self.storage.create_process(
            &[std::ffi::OsString::from("/S")],
            environment,
            Some(&binding),
            true,
        ) {
            Ok(created) => created,
            Err(error) => {
                return LaunchDisposition::Unstarted {
                    installer: self,
                    error,
                }
            }
        };
        let owner = Retained::new(windows::PendingState::new(self, created, binding));
        #[cfg(test)]
        let mut owner = owner;
        #[cfg(test)]
        owner
            .get_mut()
            .created
            .as_ref()
            .expect("created_owner_present")
            .record_created();
        #[cfg(test)]
        if owner.get_mut().installer.test_fault == BindingFault::CaseOnlyName {
            windows::change_native_case_for_test(owner.get_mut().binding_for_test());
        }
        match suspended_core(owner) {
            CoreDisposition::Unstarted { owner, error } => {
                let state = owner.take();
                LaunchDisposition::Unstarted {
                    installer: state.installer,
                    error,
                }
            }
            CoreDisposition::Started(owner) => {
                let mut state = owner.take();
                state.installer.launched = true;
                LaunchDisposition::Started(Launched {
                    process: state.created.expect("created_owner_present").process,
                })
            }
            CoreDisposition::Unverified {
                owner,
                error,
                phase,
                resume,
                may_have_run,
            } => LaunchDisposition::Unverified(PendingInstaller {
                _state: Some(owner),
                error,
                phase,
                resume,
                may_have_run,
            }),
        }
    }

    #[cfg(all(test, windows))]
    pub fn launch_inert(
        self,
        environment: &[(std::ffi::OsString, std::ffi::OsString)],
        fault: BindingFault,
    ) -> LaunchDisposition {
        let mut installer = self;
        installer.test_fault = fault;
        self::test_diagnostics::bound_start();
        installer.launch_arguments(Some(environment))
    }

    #[cfg(all(test, windows))]
    pub fn launch_wrong_image_for_test(
        mut self,
        mut wrong: Installer,
        environment: &[(std::ffi::OsString, std::ffi::OsString)],
    ) -> LaunchDisposition {
        self::test_diagnostics::bound_start();
        let binding = match wrong.storage.launch_binding(wrong.length, &wrong.expected) {
            Ok(binding) => binding,
            Err(error) => {
                return LaunchDisposition::Unstarted {
                    installer: self,
                    error,
                }
            }
        };
        self.storage.test_redirect = Some(binding.path);
        self.test_other = Some(Box::new(wrong));
        self::test_diagnostics::bound_start();
        self.launch_arguments(Some(environment))
    }

    #[cfg(test)]
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

    #[cfg(all(test, windows))]
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
            windows::Interleaved::new(root, hook)
        })?
        .storage;
        Ok(Self {
            storage,
            launched: false,
            length: bytes.len() as u64,
            expected: expected_hash(hash)?,
            #[cfg(test)]
            test_fault: BindingFault::None,
            #[cfg(test)]
            test_other: None,
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
            length: bytes.len() as u64,
            expected: expected_hash(hash)?,
            #[cfg(test)]
            test_fault: BindingFault::None,
            #[cfg(test)]
            test_other: None,
        })
    }

    #[cfg(all(test, windows))]
    /// Diagnostic matrix only. This does not change the production namespace.
    pub fn select_physical_dos_for_test(&mut self, length: u64, hash: &str) -> Result<(), Error> {
        self.storage.select_physical_dos(length, hash)
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
            length: bytes.len() as u64,
            expected: expected_hash(hash)?,
            #[cfg(test)]
            test_fault: BindingFault::None,
            #[cfg(test)]
            test_other: None,
        })
    }
}

#[cfg(all(test, windows))]
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum BindingFault {
    None,
    CaseOnlyName,
}

#[cfg(windows)]
mod windows {
    #[cfg(test)]
    use super::expected_hash;
    #[cfg(test)]
    use super::test_diagnostics;
    use super::{verify_reader, Installer, SuspendedControl};
    use super::{Error, Storage};
    use std::ffi::{c_void, OsStr, OsString};
    use std::fs::{File, OpenOptions};
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
        #[cfg(test)]
        fn GetHandleInformation(handle: *mut c_void, flags: *mut u32) -> i32;
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
        fn QueryFullProcessImageNameW(
            handle: *mut c_void,
            flags: u32,
            name: *mut u16,
            length: *mut u32,
        ) -> i32;
        fn ResumeThread(thread: *mut c_void) -> u32;
        fn TerminateProcess(process: *mut c_void, exit_code: u32) -> i32;

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

    pub(super) struct LaunchBinding {
        pub path: PathBuf,
        native: Vec<u16>,
        length: u64,
        expected: [u8; 32],
    }

    pub(super) struct Created {
        pub process: Process,
        thread: OwnedHandle,
        #[cfg(test)]
        diagnostic: usize,
        #[cfg(test)]
        suspended: bool,
    }
    #[cfg(test)]
    impl Created {
        pub fn record_created(&self) {
            test_diagnostics::returned(self.diagnostic, true, None);
            if self.suspended {
                test_diagnostics::bound_created();
            }
        }
    }

    pub(super) struct PendingState {
        pub installer: Installer,
        pub created: Option<Created>,
        binding: Option<LaunchBinding>,
    }
    impl PendingState {
        pub fn new(installer: Installer, created: Created, binding: LaunchBinding) -> Self {
            Self {
                installer,
                created: Some(created),
                binding: Some(binding),
            }
        }
        #[cfg(test)]
        pub fn binding_for_test(&mut self) -> &mut LaunchBinding {
            self.binding.as_mut().expect("binding_present")
        }
        #[cfg(test)]
        pub fn already_started(installer: Installer) -> Self {
            Self {
                installer,
                created: None,
                binding: None,
            }
        }
    }
    impl SuspendedControl for PendingState {
        fn verify_binding(&mut self) -> Result<(), Error> {
            let created = self
                .created
                .as_ref()
                .ok_or_else(|| Error::fixed("created_owner_missing"))?;
            let binding = self
                .binding
                .as_ref()
                .ok_or_else(|| Error::fixed("launch_binding_missing"))?;
            let actual = process_native_name(&created.process)?;
            let matched = super::exact_native_name(&binding.native, &actual);
            #[cfg(test)]
            test_diagnostics::bound_name(actual.len(), binding.native.len(), matched);
            if !matched {
                return Err(Error::fixed("native_image_name_mismatch"));
            }
            let checked = self.installer.storage.verify_launch(binding);
            #[cfg(test)]
            test_diagnostics::bound_reverified(checked.is_ok());
            checked
        }
        fn abort_before_resume(&mut self) -> Result<(), Error> {
            let process = &mut self
                .created
                .as_mut()
                .ok_or_else(|| Error::fixed("created_owner_missing"))?
                .process;
            #[cfg(test)]
            test_diagnostics::bound_abort(false);
            if unsafe { TerminateProcess(process.handle.as_raw_handle(), 1) } == 0 {
                return Err(Error::io("abort_terminate", io::Error::last_os_error()));
            }
            match process.wait(Duration::from_secs(5))? {
                Some(_) => {
                    #[cfg(test)]
                    test_diagnostics::bound_abort(true);
                    Ok(())
                }
                None => Err(Error::fixed("abort_signal_timeout")),
            }
        }
        fn resume_once(&mut self) -> Result<u32, Error> {
            let created = self
                .created
                .as_ref()
                .ok_or_else(|| Error::fixed("created_owner_missing"))?;
            let result = unsafe { ResumeThread(created.thread.as_raw_handle()) };
            // Preserve the original OS code before any diagnostic observation.
            let error = if result == u32::MAX {
                Some(io::Error::last_os_error())
            } else {
                None
            };
            #[cfg(test)]
            test_diagnostics::bound_resume(result);
            match error {
                Some(error) => Err(Error::io("resume_thread", error)),
                None => Ok(result),
            }
        }
    }

    fn bounded_native_name(buffer: &[u16], count: usize) -> Result<Vec<u16>, Error> {
        if count == 0 || count >= buffer.len() || buffer[..count].contains(&0) {
            return Err(Error::fixed("native_name_length"));
        }
        let name = &buffer[..count];
        let prefix: Vec<_> = "\\Device\\HarddiskVolume".encode_utf16().collect();
        let suffix = name
            .strip_prefix(prefix.as_slice())
            .ok_or_else(|| Error::fixed("unsupported_native_volume"))?;
        let digits = suffix
            .iter()
            .take_while(|value| (48..=57).contains(*value))
            .count();
        if digits == 0 || suffix.get(digits) != Some(&92) || suffix.len() <= digits + 1 {
            return Err(Error::fixed("unsupported_native_volume"));
        }
        Ok(name.to_vec())
    }
    fn file_native_name(file: &File) -> Result<Vec<u16>, Error> {
        let mut buffer = vec![0; MAX_PATH_UNITS];
        let count = unsafe {
            GetFinalPathNameByHandleW(
                file.as_raw_handle(),
                buffer.as_mut_ptr(),
                buffer.len() as u32,
                2,
            )
        } as usize;
        if count == 0 {
            return Err(Error::io("file_native_name", io::Error::last_os_error()));
        }
        bounded_native_name(&buffer, count)
    }
    fn process_native_name(process: &Process) -> Result<Vec<u16>, Error> {
        let mut buffer = vec![0; MAX_PATH_UNITS];
        let mut count = buffer.len() as u32;
        if unsafe {
            QueryFullProcessImageNameW(
                process.handle.as_raw_handle(),
                1,
                buffer.as_mut_ptr(),
                &mut count,
            )
        } == 0
        {
            return Err(Error::io("process_native_name", io::Error::last_os_error()));
        }
        bounded_native_name(&buffer, count as usize)
    }

    #[cfg(test)]
    pub fn change_native_case_for_test(binding: &mut LaunchBinding) {
        let start = binding
            .native
            .iter()
            .rposition(|value| *value == 92)
            .map_or(0, |position| position + 1);
        let letter = binding.native[start..]
            .iter_mut()
            .find(|value| (65..=90).contains(&**value) || (97..=122).contains(&**value))
            .expect("native_leaf_has_letter");
        *letter ^= 32;
    }

    #[cfg(test)]
    pub(super) struct Interleaved {
        pub storage: Staging,
        hook: Option<Box<dyn FnOnce(&Path)>>,
    }
    #[cfg(test)]
    impl Interleaved {
        pub fn new(root: &Path, hook: Box<dyn FnOnce(&Path)>) -> Result<Self, Error> {
            Ok(Self {
                storage: Staging::create(root)?,
                hook: Some(hook),
            })
        }
    }
    #[cfg(test)]
    impl Storage for Interleaved {
        fn write(&mut self, bytes: &[u8]) -> Result<(), Error> {
            self.storage.write(bytes)
        }
        fn seal(&mut self) -> Result<(), Error> {
            self.storage.writer.take();
            if let Some(hook) = self.hook.take() {
                hook(&self.storage.path);
            }
            self.storage.seal()
        }
        fn reader(&mut self) -> Result<&mut dyn Read, Error> {
            self.storage.reader()
        }
        fn cleanup(&mut self) -> Result<(), Error> {
            self.storage.cleanup()
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
        test_launch: Option<(PathBuf, u64, [u8; 32])>,
        #[cfg(test)]
        pub(super) test_redirect: Option<PathBuf>,
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
                test_launch: None,
                #[cfg(test)]
                test_redirect: None,
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

        #[cfg(test)]
        // Independently declared namespace comparison, never an error fallback.
        pub fn select_physical_dos(&mut self, length: u64, hash: &str) -> Result<(), Error> {
            self.verify()?;
            let mut buffer = vec![0u16; MAX_PATH_UNITS];
            let count = unsafe {
                GetFinalPathNameByHandleW(
                    self.locked
                        .as_ref()
                        .ok_or_else(|| Error::fixed("not_locked"))?
                        .as_raw_handle(),
                    buffer.as_mut_ptr(),
                    buffer.len() as u32,
                    0,
                )
            } as usize;
            if count == 0 {
                return Err(Error::io("test_dos_path", io::Error::last_os_error()));
            }
            if count >= buffer.len() {
                return Err(Error::fixed("test_dos_path_length"));
            }
            let candidate = PathBuf::from(OsString::from_wide(&buffer[..count]));
            if !matches!(candidate.components().next(), Some(Component::Prefix(prefix))
                if matches!(prefix.kind(), Prefix::VerbatimDisk(_)))
                || candidate.file_name() != self.path.file_name()
            {
                return Err(Error::fixed("test_dos_path_shape"));
            }
            let holds = hold_directories(
                candidate
                    .parent()
                    .ok_or_else(|| Error::fixed("test_dos_parent"))?,
            )?;
            let expected = expected_hash(hash)?;
            self.verify_test_candidate(&candidate, length, &expected)?;
            for (file, identity) in holds {
                // Original GUID and input handles remain alive; avoid adding the
                // same physical ancestor twice solely because its spelling differs.
                if !self.holds.iter().any(|(_, held)| *held == identity) {
                    self.holds.push((file, identity));
                }
            }
            self.test_launch = Some((candidate, length, expected));
            Ok(())
        }

        #[cfg(test)]
        fn verify_test_candidate(
            &self,
            path: &Path,
            length: u64,
            hash: &[u8; 32],
        ) -> Result<(), Error> {
            self.verify()?;
            let mut file = OpenOptions::new()
                .read(true)
                .share_mode(SHARE_READ)
                .custom_flags(OPEN_REPARSE)
                .open(path)
                .map_err(|error| Error::io("test_dos_file", error))?;
            if information(&file, false)? != self.identity {
                return Err(Error::fixed("test_dos_identity"));
            }
            verify_reader(&mut file, length, hash)
        }

        pub fn launch_binding(
            &mut self,
            length: u64,
            expected: &[u8; 32],
        ) -> Result<LaunchBinding, Error> {
            self.rehash_locked(length, expected)?;
            let file = self
                .locked
                .as_ref()
                .ok_or_else(|| Error::fixed("not_locked"))?;
            let mut buffer = vec![0; MAX_PATH_UNITS];
            let count = unsafe {
                GetFinalPathNameByHandleW(
                    file.as_raw_handle(),
                    buffer.as_mut_ptr(),
                    buffer.len() as u32,
                    0,
                )
            } as usize;
            if count == 0 {
                return Err(Error::io("launch_dos_path", io::Error::last_os_error()));
            }
            if count >= buffer.len() {
                return Err(Error::fixed("launch_dos_path_length"));
            }
            let path = PathBuf::from(OsString::from_wide(&buffer[..count]));
            if !matches!(path.components().next(), Some(Component::Prefix(prefix)) if matches!(prefix.kind(), Prefix::VerbatimDisk(_)))
                || path.file_name() != self.path.file_name()
            {
                return Err(Error::fixed("launch_dos_path_shape"));
            }
            let holds = hold_directories(
                path.parent()
                    .ok_or_else(|| Error::fixed("launch_dos_parent"))?,
            )?;
            let binding = LaunchBinding {
                path,
                native: file_native_name(file)?,
                length,
                expected: *expected,
            };
            self.verify_launch(&binding)?;
            for (file, identity) in holds {
                if !self.holds.iter().any(|(_, held)| *held == identity) {
                    self.holds.push((file, identity));
                }
            }
            Ok(binding)
        }

        fn rehash_locked(&mut self, length: u64, expected: &[u8; 32]) -> Result<(), Error> {
            self.verify()?;
            let file = self
                .locked
                .as_mut()
                .ok_or_else(|| Error::fixed("not_locked"))?;
            file.rewind()
                .map_err(|error| Error::io("rewind_locked", error))?;
            verify_reader(file, length, expected)
        }

        fn verify_launch(&mut self, binding: &LaunchBinding) -> Result<(), Error> {
            self.rehash_locked(binding.length, &binding.expected)?;
            let file = self
                .locked
                .as_ref()
                .ok_or_else(|| Error::fixed("not_locked"))?;
            if file_native_name(file)? != binding.native {
                return Err(Error::fixed("native_original_name_changed"));
            }
            let mut candidate = OpenOptions::new()
                .read(true)
                .share_mode(SHARE_READ)
                .custom_flags(OPEN_REPARSE)
                .open(&binding.path)
                .map_err(|error| Error::io("launch_dos_file", error))?;
            if information(&candidate, false)? != self.identity {
                return Err(Error::fixed("launch_dos_identity"));
            }
            verify_reader(&mut candidate, binding.length, &binding.expected)
        }

        #[cfg(test)]
        pub fn spawn(
            &mut self,
            arguments: &[OsString],
            environment: Option<&[(OsString, OsString)]>,
        ) -> Result<Process, Error> {
            self.create_process(arguments, environment, None, false)
                .map(|created| {
                    created.record_created();
                    created.process
                })
        }

        pub fn create_process(
            &mut self,
            arguments: &[OsString],
            environment: Option<&[(OsString, OsString)]>,
            binding: Option<&LaunchBinding>,
            suspended: bool,
        ) -> Result<Created, Error> {
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
            if let Some(binding) = binding {
                application = binding.path.as_os_str().encode_wide().collect();
            }

            #[cfg(test)]
            if let Some((path, length, hash)) = &self.test_launch {
                // Point-in-time DOS mapping verification only; a DOS device-map
                // change after this check is outside this diagnostic's guarantee.
                self.verify_test_candidate(path, *length, hash)?;
                application = path.as_os_str().encode_wide().collect();
            }
            if application.contains(&0) {
                return Err(Error::fixed("nul_path"));
            }
            #[cfg(test)]
            if let Some(path) = &self.test_redirect {
                application = path.as_os_str().encode_wide().collect();
            }
            application.push(0);
            let mut command = Vec::new();
            quoted(self.path.as_os_str(), &mut command)?;
            if let Some(binding) = binding {
                command.clear();
                quoted(binding.path.as_os_str(), &mut command)?;
            }

            #[cfg(test)]
            if let Some((path, _, _)) = &self.test_launch {
                command.clear();
                quoted(path.as_os_str(), &mut command)?;
            }
            #[cfg(test)]
            if let Some(path) = &self.test_redirect {
                command.clear();
                quoted(path.as_os_str(), &mut command)?;
            }
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
            if let Some(binding) = binding {
                self.verify_launch(binding)?;
            }
            #[cfg(test)]
            let diagnostic = {
                let mut valid = true;
                let mut inheritable = true;
                for handle in &handles {
                    let mut flags = 0;
                    let observed = unsafe { GetHandleInformation(*handle, &mut flags) } != 0;
                    valid &= observed;
                    inheritable &= observed && flags & 1 != 0;
                }
                test_diagnostics::begin(test_diagnostics::Shape {
                    namespace: test_diagnostics::namespace(&application),
                    creation_suspended: suspended,
                    application_units: application.len(),
                    command_units: command.len(),
                    environment_units: block.as_ref().map_or(0, Vec::len),
                    environment_supplied: block.is_some(),
                    environment_double_nul: block
                        .as_ref()
                        .map_or(true, |value| value.ends_with(&[0, 0])),
                    argument_count: arguments.len(),
                    handle_count: handles.len(),
                    guard_count: inherited_handles.len(),
                    directory_count: self.holds.len(),
                    handles_valid: valid,
                    handles_inheritable: inheritable,
                    startup_bytes: std::mem::size_of::<StartupInfo>(),
                    startup_ex_bytes: std::mem::size_of::<StartupEx>(),
                    parent_pointer_bits: usize::BITS,
                })
            };
            let mut information = ProcessInformation::default();
            let created = unsafe {
                CreateProcessW(
                    application.as_ptr(),
                    command.as_mut_ptr(),
                    std::ptr::null(),
                    std::ptr::null(),
                    1,
                    0x00080000 | 0x400 | if suspended { 0x4 } else { 0 },
                    block
                        .as_ref()
                        .map_or(std::ptr::null(), |b| b.as_ptr() as *const c_void),
                    std::ptr::null(),
                    &startup.info,
                    &mut information,
                )
            };
            if created == 0 {
                #[cfg(test)]
                {
                    let error = io::Error::last_os_error();
                    test_diagnostics::returned(diagnostic, false, error.raw_os_error());
                    return Err(Error::io("spawn", error));
                }
                #[cfg(not(test))]
                return Err(Error::io("spawn", io::Error::last_os_error()));
            }
            // Adopt both exact native handles before any fallible observation.
            let process = unsafe { OwnedHandle::from_raw_handle(information.process) };
            let thread = unsafe { OwnedHandle::from_raw_handle(information.thread) };
            Ok(Created {
                process: Process { handle: process },
                thread,
                #[cfg(test)]
                diagnostic,
                #[cfg(test)]
                suspended,
            })
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
pub mod test_diagnostics {
    use sha2::{Digest, Sha256};
    use std::cell::RefCell;

    #[derive(Clone, Default)]
    struct Image {
        valid: bool,
        machine: u16,
        magic: u16,
        subsystem: u16,
        sha256: String,
    }

    fn read16(bytes: &[u8], offset: usize) -> Option<u16> {
        Some(u16::from_le_bytes(
            bytes.get(offset..offset.checked_add(2)?)?.try_into().ok()?,
        ))
    }

    fn read32(bytes: &[u8], offset: usize) -> Option<u32> {
        Some(u32::from_le_bytes(
            bytes.get(offset..offset.checked_add(4)?)?.try_into().ok()?,
        ))
    }

    fn pe(bytes: &[u8]) -> Option<Image> {
        if bytes.get(..2)? != b"MZ" {
            return None;
        }
        let header = read32(bytes, 0x3c)? as usize;
        if bytes.get(header..header.checked_add(4)?)? != b"PE\0\0" {
            return None;
        }
        let optional = header.checked_add(24)?;
        let optional_size = read16(bytes, header.checked_add(20)?)? as usize;
        if optional_size < 70
            || bytes
                .get(optional..optional.checked_add(optional_size)?)
                .is_none()
        {
            return None;
        }
        Some(Image {
            valid: true,
            machine: read16(bytes, header.checked_add(4)?)?,
            magic: read16(bytes, optional)?,
            subsystem: read16(bytes, optional.checked_add(68)?)?,
            sha256: String::new(),
        })
    }

    pub fn pe_fields(bytes: &[u8]) -> Option<(u16, u16, u16)> {
        pe(bytes).map(|image| (image.machine, image.magic, image.subsystem))
    }

    /// Parse resources as bytes, without loading or running the inspected image.
    /// All RT_MANIFEST/1 language variants must contain the same bounded bytes.
    pub fn embedded_manifest(bytes: &[u8]) -> Option<Vec<u8>> {
        resources::manifest(bytes)
    }

    mod resources {
        use super::{pe, read16, read32};

        struct Image<'a> {
            bytes: &'a [u8],
            sections: &'a [u8],
            root: &'a [u8],
        }
        impl<'a> Image<'a> {
            fn range(bytes: &[u8], start: usize, length: usize) -> Option<&[u8]> {
                bytes.get(start..start.checked_add(length)?)
            }
            fn open(bytes: &'a [u8]) -> Option<Self> {
                let fields = pe(bytes)?;
                let header = read32(bytes, 0x3c)? as usize;
                let count = read16(bytes, header.checked_add(6)?)? as usize;
                if count == 0 || count > 96 {
                    return None;
                }
                let optional = header.checked_add(24)?;
                let size = read16(bytes, header.checked_add(20)?)? as usize;
                let (directory, count_offset) = match fields.magic {
                    0x10b => (112, 92),
                    0x20b => (128, 108),
                    _ => return None,
                };
                if size < directory + 8 || read32(bytes, optional.checked_add(count_offset)?)? < 3 {
                    return None;
                }
                let sections =
                    Self::range(bytes, optional.checked_add(size)?, count.checked_mul(40)?)?;
                let mut image = Self {
                    bytes,
                    sections,
                    root: &[],
                };
                let rva = read32(bytes, optional.checked_add(directory)?)?;
                let length = read32(bytes, optional.checked_add(directory + 4)?)? as usize;
                if length == 0 || length > 16 * 1024 * 1024 {
                    return None;
                }
                image.root = image.rva(rva, length)?;
                Some(image)
            }
            fn rva(&self, rva: u32, length: usize) -> Option<&'a [u8]> {
                let mut found = None;
                for section in self.sections.chunks_exact(40) {
                    let Some(delta) = rva.checked_sub(read32(section, 12)?) else {
                        continue;
                    };
                    let raw_size = read32(section, 16)? as usize;
                    let end = (delta as usize).checked_add(length)?;
                    if end > raw_size {
                        continue;
                    }
                    let offset = (read32(section, 20)? as usize).checked_add(delta as usize)?;
                    if found.is_some() {
                        return None;
                    }
                    found = Some(Self::range(self.bytes, offset, length)?);
                }
                found
            }
            fn entries(&self, offset: usize) -> Option<&'a [u8]> {
                let named = read16(self.root, offset.checked_add(12)?)? as usize;
                let ids = read16(self.root, offset.checked_add(14)?)? as usize;
                let count = named.checked_add(ids)?;
                if count == 0 || count > 128 {
                    return None;
                }
                Self::range(self.root, offset.checked_add(16)?, count.checked_mul(8)?)
            }
            fn child(&self, offset: usize, id: u32) -> Option<usize> {
                let mut result = None;
                for entry in self.entries(offset)?.chunks_exact(8) {
                    if read32(entry, 0)? != id {
                        continue;
                    }
                    let target = read32(entry, 4)?;
                    if result.is_some() || target & 0x8000_0000 == 0 {
                        return None;
                    }
                    result = Some((target & 0x7fff_ffff) as usize);
                }
                result
            }
            fn data(&self, entry: &[u8]) -> Option<Vec<u8>> {
                if read32(entry, 0)? & 0x8000_0000 != 0 {
                    return None;
                }
                let target = read32(entry, 4)?;
                if target & 0x8000_0000 != 0 {
                    return None;
                }
                let offset = target as usize;
                let length = read32(self.root, offset.checked_add(4)?)? as usize;
                if length == 0 || length > 65536 {
                    return None;
                }
                Some(self.rva(read32(self.root, offset)?, length)?.to_vec())
            }
        }
        pub(super) fn manifest(bytes: &[u8]) -> Option<Vec<u8>> {
            let image = Image::open(bytes)?;
            let kind = image.child(0, 24)?;
            let name = image.child(kind, 1)?;
            let entries = image.entries(name)?;
            if entries.len() > 16 * 8 {
                return None;
            }
            let mut manifest = None;
            for entry in entries.chunks_exact(8) {
                let data = image.data(entry)?;
                if manifest.as_ref().is_some_and(|previous| *previous != data) {
                    return None;
                }
                manifest = Some(data);
            }
            manifest
        }
    }

    #[derive(Clone, Default)]
    pub struct BoundSnapshot {
        pub created: bool,
        pub native_name_match: Option<bool>,
        pub expected_name_units: usize,
        pub actual_name_units: usize,
        pub original_reverified: Option<bool>,
        pub resume_calls: u32,
        pub resume_observation: Option<u32>,
        pub abort_attempted: bool,
        pub abort_signaled: bool,
    }
    thread_local! { static BOUND: RefCell<BoundSnapshot> = RefCell::new(BoundSnapshot::default()); }
    pub fn bound_start() {
        BOUND.with(|state| *state.borrow_mut() = BoundSnapshot::default());
    }
    pub fn bound_created() {
        BOUND.with(|state| state.borrow_mut().created = true);
    }
    pub fn bound_name(actual: usize, expected: usize, matched: bool) {
        BOUND.with(|state| {
            let mut state = state.borrow_mut();
            state.native_name_match = Some(matched);
            state.actual_name_units = actual;
            state.expected_name_units = expected;
        });
    }
    pub fn bound_reverified(verified: bool) {
        BOUND.with(|state| state.borrow_mut().original_reverified = Some(verified));
    }
    pub fn bound_resume(observation: u32) {
        BOUND.with(|state| {
            let mut state = state.borrow_mut();
            state.resume_calls += 1;
            state.resume_observation = Some(observation);
        });
    }
    pub fn bound_abort(signaled: bool) {
        BOUND.with(|state| {
            let mut state = state.borrow_mut();
            state.abort_attempted = true;
            state.abort_signaled = signaled;
        });
    }
    pub fn bound_snapshot() -> BoundSnapshot {
        BOUND.with(|state| state.borrow().clone())
    }

    pub fn image(bytes: &[u8]) {
        let mut image = pe(bytes).unwrap_or_default();
        image.sha256 = Sha256::digest(bytes)
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect();
        TRACE.with(|trace| trace.borrow_mut().image = image);
    }

    #[derive(Clone, Default)]
    pub struct Shape {
        pub namespace: &'static str,
        pub creation_suspended: bool,
        pub application_units: usize,
        pub command_units: usize,
        pub environment_units: usize,
        pub environment_supplied: bool,
        pub environment_double_nul: bool,
        pub argument_count: usize,
        pub handle_count: usize,
        pub guard_count: usize,
        pub directory_count: usize,
        pub handles_valid: bool,
        pub handles_inheritable: bool,
        pub startup_bytes: usize,
        pub startup_ex_bytes: usize,
        pub parent_pointer_bits: u32,
    }

    struct Attempt {
        target: &'static str,
        image: Image,
        shape: Shape,
        returned: bool,
        created: bool,
        code: Option<i32>,
    }

    struct Trace {
        phase: &'static str,
        target: &'static str,
        image: Image,
        attempts: Vec<Attempt>,
        overflow: bool,
        operation: &'static str,
        code: Option<i32>,
    }

    impl Default for Trace {
        fn default() -> Self {
            Self {
                phase: "not_started",
                target: "other",
                image: Image::default(),
                attempts: Vec::new(),
                overflow: false,
                operation: "none",
                code: None,
            }
        }
    }

    thread_local! { static TRACE: RefCell<Trace> = RefCell::new(Trace::default()); }

    pub fn phase(value: &'static str) {
        let allowed = [
            "inputs",
            "mapping",
            "file_guards",
            "replacement",
            "same_object",
            "reparse",
            "prepare",
            "create_process",
            "wait",
            "payload",
            "cleanup",
            "empty_parent",
            "comparison_required",
            "complete",
        ];
        let value = if allowed.contains(&value) {
            value
        } else {
            "unknown"
        };
        TRACE.with(|trace| {
            let mut trace = trace.borrow_mut();
            trace.phase = value;
            trace.operation = "none";
            trace.code = None;
        });
    }

    pub fn target(value: &'static str) {
        let value = match value {
            "rust_peer" | "rust_peer_long" | "inert_nsis" | "invalid_image" | "managed_x86"
            | "managed_x64" | "nsis_dos" | "bound_positive" | "bound_wrong_image"
            | "bound_case_only" => value,
            _ => "other",
        };
        TRACE.with(|trace| trace.borrow_mut().target = value);
    }

    pub fn namespace(application: &[u16]) -> &'static str {
        let starts =
            |prefix: &str| application.starts_with(&prefix.encode_utf16().collect::<Vec<_>>());
        if starts("\\\\?\\Volume{") {
            "volume_guid"
        } else if starts("\\\\?\\")
            && application.get(5) == Some(&58)
            && application.get(6) == Some(&92)
            && application.get(4).is_some_and(|letter| {
                (*letter >= 65 && *letter <= 90) || (*letter >= 97 && *letter <= 122)
            })
        {
            "physical_dos"
        } else if starts("\\\\?\\") {
            "verbatim_other"
        } else if starts("\\\\") {
            "unc"
        } else if application.get(1) == Some(&58) {
            "drive"
        } else {
            "other"
        }
    }

    pub fn begin(mut shape: Shape) -> usize {
        if ![
            "volume_guid",
            "physical_dos",
            "verbatim_other",
            "unc",
            "drive",
            "other",
        ]
        .contains(&shape.namespace)
        {
            shape.namespace = "other";
        }
        TRACE.with(|trace| {
            let mut trace = trace.borrow_mut();
            trace.phase = "create_process";
            if trace.attempts.len() >= 16 {
                trace.overflow = true;
                return usize::MAX;
            }
            let attempt = Attempt {
                target: trace.target,
                image: trace.image.clone(),
                shape,
                returned: false,
                created: false,
                code: None,
            };
            trace.attempts.push(attempt);
            trace.attempts.len() - 1
        })
    }

    pub fn returned(index: usize, created: bool, code: Option<i32>) {
        TRACE.with(|trace| {
            if let Some(attempt) = trace.borrow_mut().attempts.get_mut(index) {
                attempt.returned = true;
                attempt.created = created;
                attempt.code = code;
            }
        });
    }

    pub fn error(error: &super::Error) {
        if let Some(cause) = error.causes.first() {
            let allowed = [
                "spawn",
                "wait",
                "exit_code",
                "anchor_file",
                "file_identity",
                "lock_file",
                "lock_volume_file",
                "hold_directory",
                "read_locked",
                "write",
                "flush",
                "volume_path",
            ];
            let operation = if allowed.contains(&cause.operation) {
                cause.operation
            } else {
                "guard_operation"
            };
            TRACE.with(|trace| {
                let mut trace = trace.borrow_mut();
                trace.operation = operation;
                trace.code = cause.code;
            });
        }
    }

    pub fn healthy() -> bool {
        TRACE.with(|trace| !trace.borrow().overflow)
    }

    fn attempt_json(attempt: &Attempt) -> String {
        let image = &attempt.image;
        let shape = &attempt.shape;
        let code = attempt
            .code
            .map_or_else(|| "null".to_string(), |code| code.to_string());
        let operation = if attempt.returned && !attempt.created {
            "spawn"
        } else {
            "none"
        };
        format!("{{\"target\":\"{}\",\"pe_valid\":{},\"pe_machine\":{},\"pe_magic\":{},\"pe_subsystem\":{},\"image_sha256\":\"{}\",\"namespace\":\"{}\",\"creation_suspended\":{},\"application_utf16_units\":{},\"command_utf16_units\":{},\"environment_utf16_units\":{},\"environment_supplied\":{},\"environment_double_nul\":{},\"argument_count\":{},\"handle_count\":{},\"guard_count\":{},\"directory_count\":{},\"stdio_unique_count\":1,\"handles_valid\":{},\"handles_inheritable\":{},\"startup_bytes\":{},\"startup_ex_bytes\":{},\"parent_pointer_bits\":{},\"create_returned\":{},\"created\":{},\"operation\":\"{operation}\",\"os_code\":{code}}}",
            attempt.target, image.valid, image.machine, image.magic, image.subsystem, image.sha256,
            shape.namespace, shape.creation_suspended, shape.application_units, shape.command_units, shape.environment_units,
            shape.environment_supplied, shape.environment_double_nul, shape.argument_count,
            shape.handle_count, shape.guard_count, shape.directory_count, shape.handles_valid,
            shape.handles_inheritable, shape.startup_bytes, shape.startup_ex_bytes,
            shape.parent_pointer_bits, attempt.returned, attempt.created)
    }

    pub fn json() -> String {
        TRACE.with(|trace| {
            let trace = trace.borrow();
            let attempts = trace.attempts.iter().map(attempt_json).collect::<Vec<_>>().join(",");
            let code = trace.code.map_or_else(|| "null".to_string(), |value| value.to_string());
            format!("{{\"phase\":\"{}\",\"target\":\"{}\",\"operation\":\"{}\",\"os_code\":{code},\"overflow\":{},\"attempts\":[{attempts}]}}", trace.phase, trace.target, trace.operation, trace.overflow)
        })
    }

    #[cfg(test)]
    mod resource_tests {
        use super::{embedded_manifest, pe_fields};
        fn put16(bytes: &mut [u8], offset: usize, value: u16) {
            bytes[offset..offset + 2].copy_from_slice(&value.to_le_bytes());
        }
        fn put32(bytes: &mut [u8], offset: usize, value: u32) {
            bytes[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
        }
        fn fixture(wide: bool) -> Vec<u8> {
            let mut bytes = vec![0; 0x800];
            bytes[..2].copy_from_slice(b"MZ");
            put32(&mut bytes, 0x3c, 0x80);
            bytes[0x80..0x84].copy_from_slice(b"PE\0\0");
            put16(&mut bytes, 0x84, if wide { 0x8664 } else { 0x14c });
            put16(&mut bytes, 0x86, 1);
            let size = if wide { 240 } else { 224 };
            put16(&mut bytes, 0x94, size);
            put16(&mut bytes, 0x98, if wide { 0x20b } else { 0x10b });
            put16(&mut bytes, 0x98 + 68, 2);
            put32(&mut bytes, 0x98 + if wide { 108 } else { 92 }, 3);
            let data = 0x98 + if wide { 128 } else { 112 };
            put32(&mut bytes, data, 0x1000);
            put32(&mut bytes, data + 4, 0x200);
            let section = 0x98 + size as usize;
            put32(&mut bytes, section + 12, 0x1000);
            put32(&mut bytes, section + 16, 0x200);
            put32(&mut bytes, section + 20, 0x400);
            for offset in [0x400, 0x420, 0x440] {
                put16(&mut bytes, offset + 14, 1);
            }
            put32(&mut bytes, 0x410, 24);
            put32(&mut bytes, 0x414, 0x80000020);
            put32(&mut bytes, 0x430, 1);
            put32(&mut bytes, 0x434, 0x80000040);
            put32(&mut bytes, 0x450, 1033);
            put32(&mut bytes, 0x454, 0x60);
            put32(&mut bytes, 0x460, 0x1100);
            put32(&mut bytes, 0x464, 3);
            bytes[0x500..0x503].copy_from_slice(b"one");
            bytes
        }
        #[test]
        fn embedded_manifest_measures_actual_pe32_and_pe64_gui_resources() {
            for (wide, fields) in [(false, (0x14c, 0x10b, 2)), (true, (0x8664, 0x20b, 2))] {
                let bytes = fixture(wide);
                assert_eq!(pe_fields(&bytes), Some(fields));
                assert_eq!(embedded_manifest(&bytes), Some(b"one".to_vec()));
            }
        }
        #[test]
        fn embedded_manifest_rejects_missing_ambiguous_and_unbounded_resources() {
            for wide in [false, true] {
                for count in [0, 2] {
                    let mut bytes = fixture(wide);
                    put32(&mut bytes, 0x98 + if wide { 108 } else { 92 }, count);
                    assert!(
                        embedded_manifest(&bytes).is_none(),
                        "undeclared resource accepted"
                    );
                }
            }
            for (offset, value) in [
                (0x410, 23),
                (0x430, 2),
                (0x454, 0x80000040),
                (0x464, 65537),
                (0x460, u32::MAX),
                (0x98 + 112, 0xfffffff0),
            ] {
                let mut bytes = fixture(false);
                put32(&mut bytes, offset, value);
                assert!(
                    embedded_manifest(&bytes).is_none(),
                    "malformed resource accepted"
                );
            }
            for length in [0, 0x40, 0x98, 0x450, 0x520] {
                assert!(embedded_manifest(&fixture(false)[..length]).is_none());
            }
        }
        #[test]
        fn embedded_manifest_requires_all_language_variants_to_match() {
            let mut bytes = fixture(false);
            put16(&mut bytes, 0x44e, 2);
            put32(&mut bytes, 0x458, 1041);
            put32(&mut bytes, 0x45c, 0x80);
            put32(&mut bytes, 0x480, 0x1120);
            put32(&mut bytes, 0x484, 3);
            bytes[0x520..0x523].copy_from_slice(b"one");
            assert_eq!(embedded_manifest(&bytes), Some(b"one".to_vec()));
            bytes[0x520..0x523].copy_from_slice(b"two");
            assert!(embedded_manifest(&bytes).is_none());
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

#[cfg(test)]
mod bound_core_tests {
    use super::{
        exact_native_name, suspended_core, CoreDisposition, Error, Retained, SuspendedControl,
    };
    use std::sync::{Arc, Mutex};

    struct Fake {
        events: Arc<Mutex<Vec<&'static str>>>,
        verify: &'static str,
        abort: &'static str,
        resume: Option<u32>,
    }
    impl Drop for Fake {
        fn drop(&mut self) {
            self.events.lock().unwrap().push("release");
        }
    }
    impl SuspendedControl for Fake {
        fn verify_binding(&mut self) -> Result<(), Error> {
            self.events.lock().unwrap().push("verify");
            if self.verify == "panic" {
                panic!("inert_binding_panic");
            }
            if self.verify == "ok" {
                Ok(())
            } else {
                Err(Error::fixed(self.verify))
            }
        }
        fn abort_before_resume(&mut self) -> Result<(), Error> {
            self.events.lock().unwrap().push("abort");
            if self.abort == "ok" {
                Ok(())
            } else {
                Err(Error::fixed(self.abort))
            }
        }
        fn resume_once(&mut self) -> Result<u32, Error> {
            self.events.lock().unwrap().push("resume");
            self.resume
                .ok_or_else(|| Error::io("resume_thread", std::io::Error::from_raw_os_error(5)))
        }
    }
    fn fake(
        verify: &'static str,
        abort: &'static str,
        resume: Option<u32>,
    ) -> (Retained<Fake>, Arc<Mutex<Vec<&'static str>>>) {
        let events = Arc::new(Mutex::new(Vec::new()));
        (
            Retained::new(Fake {
                events: events.clone(),
                verify,
                abort,
                resume,
            }),
            events,
        )
    }
    #[test]
    fn bound_core_exact_one_resume_is_the_only_commit() {
        for result in [Some(0), Some(1), Some(2), Some(u32::MAX), None] {
            let (owner, events) = fake("ok", "ok", result);
            match suspended_core(owner) {
                CoreDisposition::Started(owner) => {
                    assert_eq!(result, Some(1));
                    drop(owner.take());
                }
                CoreDisposition::Unverified {
                    owner,
                    phase,
                    resume,
                    may_have_run,
                    ..
                } => {
                    assert_ne!(result, Some(1));
                    assert_eq!(phase, "resume");
                    assert!(may_have_run);
                    assert_eq!(resume, Some(result.unwrap_or(u32::MAX)));
                    drop(owner);
                }
                CoreDisposition::Unstarted { .. } => {
                    panic!("resume uncertainty became recoverable")
                }
            }
            let events = events.lock().unwrap();
            assert_eq!(events.iter().filter(|event| **event == "resume").count(), 1);
            assert!(!events.contains(&"abort"));
            assert_eq!(events.contains(&"release"), result == Some(1));
        }
    }
    #[test]
    fn bound_core_pre_resume_rejection_needs_confirmed_abort() {
        for primary in [
            "native_image_name_mismatch",
            "process_native_name",
            "hash_changed",
        ] {
            let (owner, events) = fake(primary, "ok", Some(1));
            match suspended_core(owner) {
                CoreDisposition::Unstarted { owner, error } => {
                    assert_eq!(error.causes[0].operation, primary);
                    drop(owner.take());
                }
                _ => panic!("confirmed abort was not returned with its staging owner"),
            }
            assert_eq!(*events.lock().unwrap(), ["verify", "abort", "release"]);
        }
    }
    #[test]
    fn bound_core_uncertain_abort_retains_owner_and_both_causes() {
        for failure in [
            "abort_terminate",
            "abort_signal_timeout",
            "wait",
            "exit_code",
        ] {
            let (owner, events) = fake("native_image_name_mismatch", failure, Some(1));
            match suspended_core(owner) {
                CoreDisposition::Unverified {
                    owner,
                    error,
                    phase,
                    resume,
                    may_have_run,
                } => {
                    assert_eq!(phase, "abort");
                    assert_eq!(resume, None);
                    assert!(!may_have_run);
                    assert_eq!(
                        error
                            .causes
                            .iter()
                            .map(|cause| cause.operation)
                            .collect::<Vec<_>>(),
                        ["native_image_name_mismatch", failure]
                    );
                    drop(owner);
                }
                _ => panic!("unknown abort lost its native owner"),
            }
            assert_eq!(*events.lock().unwrap(), ["verify", "abort"]);
        }
    }
    #[test]
    fn bound_core_unwind_cannot_release_an_unverified_owner() {
        let (owner, events) = fake("panic", "ok", Some(1));
        assert!(
            std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| suspended_core(owner)))
                .is_err()
        );
        assert_eq!(*events.lock().unwrap(), ["verify"]);
    }
    #[test]
    fn bound_core_native_names_are_exact_utf16_without_case_or_unicode_folding() {
        let expected: Vec<_> = "\\Device\\HarddiskVolume2\\Arkism.exe"
            .encode_utf16()
            .collect();
        assert!(exact_native_name(&expected, &expected));
        for different in [
            "\\Device\\HarddiskVolume2\\arkism.exe",
            "\\Device\\HarddiskVolume3\\Arkism.exe",
            "\\Device/HarddiskVolume2/Arkism.exe",
        ] {
            assert!(!exact_native_name(
                &expected,
                &different.encode_utf16().collect::<Vec<_>>()
            ));
        }
        assert!(!exact_native_name(&[0x00e9], &[0x0065, 0x0301]));
        let mut extra = expected.clone();
        extra.push(0);
        assert!(!exact_native_name(&expected, &extra));
    }
    #[test]
    fn bound_core_retained_drop_is_not_cleanup_or_termination() {
        fn require_send<T: Send>() {}
        require_send::<super::PendingInstaller>();
        let (owner, events) = fake("ok", "ok", Some(1));
        drop(owner);
        assert!(events.lock().unwrap().is_empty());
        let (owner, events) = fake("ok", "ok", Some(1));
        drop(owner.take());
        assert_eq!(*events.lock().unwrap(), ["release"]);
    }
}
