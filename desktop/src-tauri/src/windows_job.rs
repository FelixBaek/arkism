//! Creation-time ownership for one managed Windows component or finite command.
//!
//! Admission and graceful draining belong to the role's control protocol. Job
//! accounting only establishes native descendant ownership and confirmed exit.
//! Queries and bounded waits borrow the owner; neither terminates a live job.
//! Dropping the owner closes its kill-on-close job, so callers must retain the
//! owner returned with a launch failure until explicit recovery is complete.
//! This is not a sandbox for services, shell delegation, or breakaway processes.

use std::cmp::Ordering;
use std::fmt;
use std::time::Duration;

/// Fixed diagnostics only: never include paths, command lines or environment.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Cause {
    pub operation: &'static str,
    pub windows_code: Option<u32>,
}

impl Cause {
    fn fixed(operation: &'static str) -> Self {
        Self {
            operation,
            windows_code: None,
        }
    }
}

#[derive(Debug)]
pub struct JobError {
    pub causes: Vec<Cause>,
}

impl JobError {
    fn one(cause: Cause) -> Self {
        Self {
            causes: vec![cause],
        }
    }
}

impl fmt::Display for JobError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "windows_job")?;
        for cause in &self.causes {
            write!(f, ":{}", cause.operation)?;
            if let Some(code) = cause.windows_code {
                write!(f, "({code})")?;
            }
        }
        Ok(())
    }
}

impl std::error::Error for JobError {}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Accounting {
    pub active_processes: u32,
    pub total_processes: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ResumeState {
    NotAttempted,
    ConfirmedOne,
    Failed,
    UnexpectedCount,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Status {
    pub root_pid: u32,
    pub assigned: bool,
    pub membership_verified: bool,
    pub resume: ResumeState,
    pub may_have_run: bool,
    pub empty_confirmed: bool,
}

#[derive(Default)]
struct Created {
    process: usize,
    thread: usize,
    pid: u32,
    errors: Vec<Cause>,
}

// The production adapter and fake tests execute the same ownership state machine.
trait Api {
    fn create_job(&mut self) -> Result<usize, Cause>;
    fn configure_job(&mut self, job: usize) -> Result<(), Cause>;
    fn create_suspended(&mut self) -> Created;
    fn assign(&mut self, job: usize, process: usize) -> Result<(), Cause>;
    fn member(&mut self, job: usize, process: usize) -> Result<bool, Cause>;
    fn verify_image(&mut self, process: usize) -> Result<(), Cause>;
    fn resume(&mut self, thread: usize) -> Result<u32, Cause>;
    fn accounting(&mut self, job: usize) -> Result<Accounting, Cause>;
    fn root_exited(&mut self, process: usize) -> Result<bool, Cause>;
    fn terminate_job(&mut self, job: usize) -> Result<(), Cause>;
    fn terminate_root(&mut self, process: usize) -> Result<(), Cause>;
    fn close(&mut self, handle: usize) -> Result<(), Cause>;
    fn clock(&mut self) -> Duration;
    fn sleep(&mut self, duration: Duration);
}

struct Owner<A: Api> {
    api: A,
    job: usize,
    process: usize,
    thread: usize,
    status: Status,
}

struct LaunchFailure<A: Api> {
    error: JobError,
    owner: Owner<A>,
}

impl<A: Api> Owner<A> {
    fn launch(api: A) -> Result<Self, LaunchFailure<A>> {
        let mut owner = Self {
            api,
            job: 0,
            process: 0,
            thread: 0,
            status: Status {
                root_pid: 0,
                assigned: false,
                membership_verified: false,
                resume: ResumeState::NotAttempted,
                may_have_run: false,
                empty_confirmed: false,
            },
        };
        match owner.launch_inner() {
            Ok(()) => Ok(owner),
            Err(error) => Err(LaunchFailure { error, owner }),
        }
    }

    fn launch_inner(&mut self) -> Result<(), JobError> {
        self.job = self.api.create_job().map_err(JobError::one)?;
        if !valid_handle(self.job) {
            return Err(JobError::one(Cause::fixed("invalid_job")));
        }
        self.api.configure_job(self.job).map_err(JobError::one)?;
        let created = self.api.create_suspended();
        self.process = created.process;
        self.thread = created.thread;
        self.status.root_pid = created.pid;
        if !created.errors.is_empty() {
            return Err(JobError {
                causes: created.errors,
            });
        }
        if !valid_handle(self.process) || !valid_handle(self.thread) || created.pid == 0 {
            return Err(JobError::one(Cause::fixed("invalid_created_process")));
        }
        self.api
            .assign(self.job, self.process)
            .map_err(JobError::one)?;
        self.status.assigned = true;
        if !self
            .api
            .member(self.job, self.process)
            .map_err(JobError::one)?
        {
            return Err(JobError::one(Cause::fixed("membership_missing")));
        }
        self.status.membership_verified = true;
        self.api.verify_image(self.process).map_err(JobError::one)?;
        // Once ResumeThread is attempted, a failed/ambiguous result cannot prove
        // that user code never ran. Preserve the owner for explicit recovery.
        self.status.may_have_run = true;
        let previous = match self.api.resume(self.thread) {
            Ok(value) => value,
            Err(error) => {
                self.status.resume = ResumeState::Failed;
                return Err(JobError::one(error));
            }
        };
        if previous != 1 {
            self.status.resume = ResumeState::UnexpectedCount;
            return Err(JobError::one(Cause::fixed("resume_count")));
        }
        self.status.resume = ResumeState::ConfirmedOne;
        Ok(())
    }

    fn query(&mut self) -> Result<Accounting, JobError> {
        if !valid_handle(self.job) || !self.status.assigned {
            return Err(JobError::one(Cause::fixed("job_not_assigned")));
        }
        let accounting = self.api.accounting(self.job).map_err(JobError::one)?;
        if accounting.active_processes > accounting.total_processes
            || (self.status.membership_verified && accounting.total_processes == 0)
        {
            return Err(JobError::one(Cause::fixed("invalid_accounting")));
        }
        Ok(accounting)
    }

    fn wait_empty(&mut self, timeout: Duration) -> Result<Accounting, JobError> {
        if timeout.is_zero() {
            return Err(JobError::one(Cause::fixed("invalid_wait_budget")));
        }
        self.status.empty_confirmed = false;
        let start = self.api.clock();
        let mut previous = start;
        loop {
            let accounting = self.observe()?;
            let now = self.api.clock();
            if now < previous {
                return Err(JobError::one(Cause::fixed("clock_regressed")));
            }
            previous = now;
            let elapsed = now
                .checked_sub(start)
                .ok_or_else(|| JobError::one(Cause::fixed("clock_regressed")))?;
            if elapsed <= timeout && accounting.active_processes == 0 {
                self.status.empty_confirmed = true;
                return Ok(accounting);
            }
            if elapsed >= timeout {
                return Err(JobError::one(Cause::fixed("wait_timeout")));
            }
            self.api
                .sleep((timeout - elapsed).min(Duration::from_millis(10)));
        }
    }

    fn observe(&mut self) -> Result<Accounting, JobError> {
        if self.status.assigned {
            let accounting = self.query()?;
            if accounting.active_processes == 0
                && valid_handle(self.process)
                && !self.api.root_exited(self.process).map_err(JobError::one)?
            {
                return Err(JobError::one(Cause::fixed("empty_root_unsignaled")));
            }
            Ok(accounting)
        } else if valid_handle(self.process) {
            let exited = self.api.root_exited(self.process).map_err(JobError::one)?;
            Ok(Accounting {
                active_processes: u32::from(!exited),
                total_processes: 1,
            })
        } else {
            Ok(Accounting::default())
        }
    }

    fn terminate_and_wait(&mut self, timeout: Duration) -> Result<Accounting, JobError> {
        if timeout.is_zero() {
            return Err(JobError::one(Cause::fixed("invalid_wait_budget")));
        }
        let mut causes = Vec::new();
        if !self.status.membership_verified && valid_handle(self.process) {
            if let Err(error) = self.api.terminate_root(self.process) {
                causes.push(error);
            }
        }
        if self.status.assigned && valid_handle(self.job) {
            if let Err(error) = self.api.terminate_job(self.job) {
                causes.push(error);
            }
        }
        let waited = self.wait_empty(timeout);
        match waited {
            Ok(accounting) if causes.is_empty() => Ok(accounting),
            Ok(_) => Err(JobError { causes }),
            Err(error) => {
                causes.extend(error.causes);
                Err(JobError { causes })
            }
        }
    }

    fn close_empty(&mut self) -> Result<(), JobError> {
        if !self.status.empty_confirmed {
            return Err(JobError::one(Cause::fixed(
                "close_requires_confirmed_empty",
            )));
        }
        let mut causes = Vec::new();
        close_slot(&mut self.api, &mut self.thread, &mut causes);
        close_slot(&mut self.api, &mut self.process, &mut causes);
        close_slot(&mut self.api, &mut self.job, &mut causes);
        if causes.is_empty() {
            Ok(())
        } else {
            Err(JobError { causes })
        }
    }
}

fn valid_handle(handle: usize) -> bool {
    handle != 0 && handle != usize::MAX
}

fn inheritance_list(child: [usize; 3], forbidden: &[usize]) -> Result<[usize; 3], Cause> {
    if child
        .iter()
        .any(|h| !valid_handle(*h) || forbidden.contains(h))
        || child[0] == child[1]
        || child[0] == child[2]
        || child[1] == child[2]
    {
        return Err(Cause::fixed("invalid_inheritance_list"));
    }
    Ok(child)
}

fn close_slot<A: Api>(api: &mut A, slot: &mut usize, causes: &mut Vec<Cause>) {
    if valid_handle(*slot) {
        match api.close(*slot) {
            Ok(()) => *slot = 0,
            Err(error) => causes.push(error),
        }
    }
}

impl<A: Api> Drop for Owner<A> {
    fn drop(&mut self) {
        // Explicit close_empty reports close failures and retains failed handles.
        // This last-resort lifetime boundary closes the kill-on-close job last.
        if !self.status.membership_verified && valid_handle(self.process) {
            // An unassigned suspended root is not covered by kill-on-close.
            let _ = self.api.terminate_root(self.process);
        }
        for handle in [self.thread, self.process, self.job] {
            if valid_handle(handle) {
                let _ = self.api.close(handle);
            }
        }
    }
}

fn no_nul(value: &[u16]) -> Result<(), Cause> {
    if value.contains(&0) {
        Err(Cause::fixed("embedded_nul"))
    } else {
        Ok(())
    }
}

// Microsoft CRT argv quoting. argv[0] is always the explicit application path;
// CreateProcessW also receives lpApplicationName, so spaces cannot select an exe.
fn quote_argument(value: &[u16]) -> Result<Vec<u16>, Cause> {
    no_nul(value)?;
    let mut quoted = vec![u16::from(b'"')];
    let mut slashes = 0;
    for &unit in value {
        if unit == u16::from(b'\\') {
            slashes += 1;
            continue;
        }
        let count = if unit == u16::from(b'"') {
            slashes * 2 + 1
        } else {
            slashes
        };
        quoted.extend(std::iter::repeat(u16::from(b'\\')).take(count));
        quoted.push(unit);
        slashes = 0;
    }
    quoted.extend(std::iter::repeat(u16::from(b'\\')).take(slashes * 2));
    quoted.push(u16::from(b'"'));
    Ok(quoted)
}

fn command_line(application: &[u16], arguments: &[Vec<u16>]) -> Result<Vec<u16>, Cause> {
    if application.is_empty() || application.contains(&u16::from(b'"')) {
        return Err(Cause::fixed("invalid_application"));
    }
    let mut line = quote_argument(application)?;
    for arg in arguments {
        line.push(u16::from(b' '));
        line.extend(quote_argument(arg)?);
    }
    line.push(0);
    if line.len() > 32767 {
        return Err(Cause::fixed("command_line_too_long"));
    }
    Ok(line)
}

fn environment_block(
    entries: &[(Vec<u16>, Vec<u16>)],
    mut compare: impl FnMut(&[u16], &[u16]) -> Result<Ordering, Cause>,
) -> Result<Vec<u16>, Cause> {
    let mut sorted: Vec<usize> = Vec::with_capacity(entries.len());
    for (index, (key, value)) in entries.iter().enumerate() {
        no_nul(key)?;
        no_nul(value)?;
        if key.is_empty() || key.contains(&u16::from(b'=')) {
            return Err(Cause::fixed("invalid_environment_key"));
        }
        let mut position = sorted.len();
        for (at, &other) in sorted.iter().enumerate() {
            match compare(key, &entries[other].0)? {
                Ordering::Equal => return Err(Cause::fixed("duplicate_environment_key")),
                Ordering::Less => {
                    position = at;
                    break;
                }
                Ordering::Greater => {}
            }
        }
        sorted.insert(position, index);
    }
    let mut block = Vec::new();
    for index in sorted {
        block.extend_from_slice(&entries[index].0);
        block.push(u16::from(b'='));
        block.extend_from_slice(&entries[index].1);
        block.push(0);
    }
    if block.is_empty() {
        block.push(0);
    }
    block.push(0);
    Ok(block)
}

// Win32 types are pointer-sized where required; BOOL/DWORD stay 32-bit.
mod abi {
    use std::ffi::c_void;
    pub type Handle = *mut c_void;
    #[repr(C)]
    #[derive(Default)]
    pub struct BasicLimit {
        pub process_time: i64,
        pub job_time: i64,
        pub flags: u32,
        pub min_working_set: usize,
        pub max_working_set: usize,
        pub active_limit: u32,
        pub affinity: usize,
        pub priority: u32,
        pub scheduling: u32,
    }
    #[repr(C)]
    #[derive(Default)]
    pub struct IoCounters {
        pub counters: [u64; 6],
    }
    #[repr(C)]
    #[derive(Default)]
    pub struct ExtendedLimit {
        pub basic: BasicLimit,
        pub io: IoCounters,
        pub process_memory: usize,
        pub job_memory: usize,
        pub peak_process: usize,
        pub peak_job: usize,
    }
    #[repr(C)]
    #[derive(Default)]
    pub struct BasicAccounting {
        pub times: [i64; 4],
        pub faults: u32,
        pub total: u32,
        pub active: u32,
        pub terminated: u32,
    }
    #[repr(C)]
    pub struct StartupInfo {
        pub cb: u32,
        pub reserved: *mut u16,
        pub desktop: *mut u16,
        pub title: *mut u16,
        pub x: u32,
        pub y: u32,
        pub x_size: u32,
        pub y_size: u32,
        pub x_chars: u32,
        pub y_chars: u32,
        pub fill: u32,
        pub flags: u32,
        pub show: u16,
        pub reserved_count: u16,
        pub reserved_bytes: *mut u8,
        pub stdin: Handle,
        pub stdout: Handle,
        pub stderr: Handle,
    }
    #[repr(C)]
    pub struct StartupEx {
        pub startup: StartupInfo,
        pub attributes: *mut c_void,
    }
    #[repr(C)]
    #[derive(Default)]
    pub struct ProcessInfo {
        pub process: Handle,
        pub thread: Handle,
        pub pid: u32,
        pub tid: u32,
    }
    #[repr(C)]
    pub struct SecurityAttributes {
        pub length: u32,
        pub descriptor: *mut c_void,
        pub inherit: i32,
    }
}

#[cfg(windows)]
mod native {
    use super::abi::*;
    use super::*;
    use std::alloc::{alloc_zeroed, dealloc, Layout};
    use std::ffi::{c_void, OsString};
    use std::fs::File;
    use std::mem::{size_of, zeroed};
    use std::os::windows::ffi::{OsStrExt, OsStringExt};
    use std::os::windows::io::{AsRawHandle, FromRawHandle, IntoRawHandle};
    use std::path::PathBuf;
    use std::ptr::{null, null_mut};
    use std::time::Instant;

    const KILL_ON_CLOSE: u32 = 0x2000;
    const STARTF_USESTDHANDLES: u32 = 0x100;
    const HANDLE_LIST: usize = 0x0002_0002;
    const CREATE_SUSPENDED: u32 = 0x4;
    const CREATE_UNICODE_ENVIRONMENT: u32 = 0x400;
    const EXTENDED_STARTUPINFO_PRESENT: u32 = 0x0008_0000;
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    const WAIT_OBJECT_0: u32 = 0;
    const WAIT_TIMEOUT: u32 = 258;

    #[link(name = "kernel32")]
    extern "system" {
        fn CreateJobObjectW(attributes: *const SecurityAttributes, name: *const u16) -> Handle;
        fn SetInformationJobObject(
            job: Handle,
            class: u32,
            info: *const c_void,
            length: u32,
        ) -> i32;
        fn QueryInformationJobObject(
            job: Handle,
            class: u32,
            info: *mut c_void,
            length: u32,
            returned: *mut u32,
        ) -> i32;
        fn AssignProcessToJobObject(job: Handle, process: Handle) -> i32;
        fn IsProcessInJob(process: Handle, job: Handle, result: *mut i32) -> i32;
        fn TerminateJobObject(job: Handle, code: u32) -> i32;
        fn CreateProcessW(
            application: *const u16,
            command: *mut u16,
            process_attributes: *const SecurityAttributes,
            thread_attributes: *const SecurityAttributes,
            inherit: i32,
            flags: u32,
            environment: *const c_void,
            directory: *const u16,
            startup: *mut StartupEx,
            process: *mut ProcessInfo,
        ) -> i32;
        fn ResumeThread(thread: Handle) -> u32;
        fn QueryFullProcessImageNameW(
            process: Handle,
            flags: u32,
            buffer: *mut u16,
            size: *mut u32,
        ) -> i32;
        fn WaitForSingleObject(handle: Handle, milliseconds: u32) -> u32;
        fn TerminateProcess(process: Handle, code: u32) -> i32;
        fn GetExitCodeProcess(process: Handle, code: *mut u32) -> i32;
        fn CloseHandle(handle: Handle) -> i32;
        fn GetLastError() -> u32;
        fn CreatePipe(
            read: *mut Handle,
            write: *mut Handle,
            attributes: *const SecurityAttributes,
            size: u32,
        ) -> i32;
        fn SetHandleInformation(handle: Handle, mask: u32, flags: u32) -> i32;
        fn GetCurrentProcess() -> Handle;
        fn DuplicateHandle(
            source_process: Handle,
            source: Handle,
            target_process: Handle,
            target: *mut Handle,
            access: u32,
            inherit: i32,
            options: u32,
        ) -> i32;
        fn InitializeProcThreadAttributeList(
            list: *mut c_void,
            count: u32,
            flags: u32,
            bytes: *mut usize,
        ) -> i32;
        fn UpdateProcThreadAttribute(
            list: *mut c_void,
            flags: u32,
            attribute: usize,
            value: *const c_void,
            bytes: usize,
            previous: *mut c_void,
            returned: *mut usize,
        ) -> i32;
        fn DeleteProcThreadAttributeList(list: *mut c_void);
        fn CompareStringOrdinal(
            first: *const u16,
            first_len: i32,
            second: *const u16,
            second_len: i32,
            ignore_case: i32,
        ) -> i32;
    }

    fn last(operation: &'static str) -> Cause {
        Cause {
            operation,
            windows_code: Some(unsafe { GetLastError() }),
        }
    }

    fn checked_bool(result: i32, operation: &'static str) -> Result<(), Cause> {
        if result == 0 {
            Err(last(operation))
        } else {
            Ok(())
        }
    }

    fn compare_ordinal(first: &[u16], second: &[u16]) -> Result<Ordering, Cause> {
        let a = i32::try_from(first.len()).map_err(|_| Cause::fixed("string_too_long"))?;
        let b = i32::try_from(second.len()).map_err(|_| Cause::fixed("string_too_long"))?;
        match unsafe { CompareStringOrdinal(first.as_ptr(), a, second.as_ptr(), b, 1) } {
            1 => Ok(Ordering::Less),
            2 => Ok(Ordering::Equal),
            3 => Ok(Ordering::Greater),
            _ => Err(last("compare_ordinal")),
        }
    }

    pub enum Stderr {
        Capture,
        /// Duplicate this already opened file; do not create or truncate a log.
        File(File),
    }

    /// Full environment, never implicitly inherited. No Debug implementation:
    /// this value can contain control nonces and other credentials.
    pub struct LaunchSpec {
        pub executable: PathBuf,
        pub arguments: Vec<OsString>,
        pub environment: Vec<(OsString, OsString)>,
        pub directory: PathBuf,
        pub stderr: Stderr,
    }

    struct Prepared {
        application: Vec<u16>,
        command: Vec<u16>,
        environment: Vec<u16>,
        directory: Vec<u16>,
        expected_image: PathBuf,
    }

    impl Prepared {
        fn new(spec: &LaunchSpec) -> Result<Self, Cause> {
            if !spec.executable.is_absolute() || !spec.directory.is_absolute() {
                return Err(Cause::fixed("absolute_launch_paths_required"));
            }
            let expected_image = std::fs::canonicalize(&spec.executable)
                .map_err(|_| Cause::fixed("image_unavailable"))?;
            if !std::fs::metadata(&expected_image)
                .map_err(|_| Cause::fixed("image_unavailable"))?
                .is_file()
                || !std::fs::metadata(&spec.directory)
                    .map_err(|_| Cause::fixed("directory_unavailable"))?
                    .is_dir()
            {
                return Err(Cause::fixed("invalid_launch_paths"));
            }
            let mut application: Vec<u16> = spec.executable.as_os_str().encode_wide().collect();
            let arguments = spec
                .arguments
                .iter()
                .map(|arg| arg.encode_wide().collect())
                .collect::<Vec<_>>();
            let command = command_line(&application, &arguments)?;
            no_nul(&application)?;
            application.push(0);
            let mut directory: Vec<u16> = spec.directory.as_os_str().encode_wide().collect();
            no_nul(&directory)?;
            directory.push(0);
            let entries = spec
                .environment
                .iter()
                .map(|(key, value)| (key.encode_wide().collect(), value.encode_wide().collect()))
                .collect::<Vec<_>>();
            let environment = environment_block(&entries, compare_ordinal)?;
            Ok(Self {
                application,
                command,
                environment,
                directory,
                expected_image,
            })
        }
    }

    // Opaque attribute list storage must remain aligned and alive until after
    // CreateProcess. The handle array also remains alive until list deletion.
    struct Attributes {
        pointer: *mut c_void,
        layout: Layout,
        initialized: bool,
        handles: [usize; 3],
    }

    impl Attributes {
        fn new(handles: [usize; 3]) -> Result<Self, Cause> {
            inheritance_list(handles, &[])?;
            let mut bytes = 0;
            let sized = unsafe { InitializeProcThreadAttributeList(null_mut(), 1, 0, &mut bytes) };
            if sized != 0 || unsafe { GetLastError() } != 122 || bytes == 0 {
                return Err(Cause::fixed("attribute_size"));
            }
            let layout =
                Layout::from_size_align(bytes, 16).map_err(|_| Cause::fixed("attribute_layout"))?;
            let pointer = unsafe { alloc_zeroed(layout) } as *mut c_void;
            if pointer.is_null() {
                return Err(Cause::fixed("attribute_allocation"));
            }
            let mut attributes = Self {
                pointer,
                layout,
                initialized: false,
                handles,
            };
            checked_bool(
                unsafe { InitializeProcThreadAttributeList(pointer, 1, 0, &mut bytes) },
                "attribute_initialize",
            )?;
            attributes.initialized = true;
            // The array is installed after Self has reached its final address.
            Ok(attributes)
        }

        fn install(&mut self) -> Result<(), Cause> {
            checked_bool(
                unsafe {
                    UpdateProcThreadAttribute(
                        self.pointer,
                        0,
                        HANDLE_LIST,
                        self.handles.as_ptr().cast(),
                        size_of::<[usize; 3]>(),
                        null_mut(),
                        null_mut(),
                    )
                },
                "attribute_handles",
            )
        }
    }

    impl Drop for Attributes {
        fn drop(&mut self) {
            if self.initialized {
                unsafe {
                    DeleteProcThreadAttributeList(self.pointer);
                }
            }
            unsafe {
                dealloc(self.pointer.cast(), self.layout);
            }
        }
    }

    struct NativeApi {
        prepared: Prepared,
        stderr: Stderr,
        start: Instant,
        stdin: Option<File>,
        stdout: Option<File>,
        captured_stderr: Option<File>,
        auxiliary: Vec<usize>,
        job: usize,
    }

    impl NativeApi {
        fn new(spec: LaunchSpec) -> Result<Self, Cause> {
            let prepared = Prepared::new(&spec)?;
            Ok(Self {
                prepared,
                stderr: spec.stderr,
                start: Instant::now(),
                stdin: None,
                stdout: None,
                captured_stderr: None,
                auxiliary: Vec::new(),
                job: 0,
            })
        }

        fn parent_file(&mut self, raw: Handle) -> Result<File, Cause> {
            checked_bool(
                unsafe { SetHandleInformation(raw, 1, 0) },
                "parent_noninherit",
            )?;
            let position = self
                .auxiliary
                .iter()
                .position(|&h| h == raw as usize)
                .ok_or_else(|| Cause::fixed("pipe_ownership"))?;
            self.auxiliary.swap_remove(position);
            Ok(unsafe { File::from_raw_handle(raw) })
        }

        fn pipe(&mut self) -> Result<(Handle, Handle), Cause> {
            let sa = SecurityAttributes {
                length: size_of::<SecurityAttributes>() as u32,
                descriptor: null_mut(),
                inherit: 1,
            };
            let mut read = null_mut();
            let mut write = null_mut();
            let result = unsafe { CreatePipe(&mut read, &mut write, &sa, 0) };
            // Retain partial handles even on an API failure for RAII cleanup.
            if valid_handle(read as usize) {
                self.auxiliary.push(read as usize);
            }
            if valid_handle(write as usize) {
                self.auxiliary.push(write as usize);
            }
            checked_bool(result, "create_pipe")?;
            if !valid_handle(read as usize) || !valid_handle(write as usize) {
                return Err(Cause::fixed("invalid_pipe"));
            }
            Ok((read, write))
        }

        fn child_handles(&mut self) -> Result<[usize; 3], Cause> {
            let (child_in, parent_in) = self.pipe()?;
            self.stdin = Some(self.parent_file(parent_in)?);
            let (parent_out, child_out) = self.pipe()?;
            self.stdout = Some(self.parent_file(parent_out)?);
            let child_err = match &self.stderr {
                Stderr::Capture => {
                    let (parent_err, child_err) = self.pipe()?;
                    self.captured_stderr = Some(self.parent_file(parent_err)?);
                    child_err
                }
                Stderr::File(file) => {
                    let mut child_err = null_mut();
                    let process = unsafe { GetCurrentProcess() };
                    checked_bool(
                        unsafe {
                            DuplicateHandle(
                                process,
                                file.as_raw_handle(),
                                process,
                                &mut child_err,
                                0,
                                1,
                                2,
                            )
                        },
                        "duplicate_stderr",
                    )?;
                    if !valid_handle(child_err as usize) {
                        return Err(Cause::fixed("invalid_stderr"));
                    }
                    self.auxiliary.push(child_err as usize);
                    child_err
                }
            };
            Ok([child_in as usize, child_out as usize, child_err as usize])
        }

        fn create(&mut self) -> Result<Created, Cause> {
            let handles = self.child_handles()?;
            let parent_handle = |file: &Option<File>| {
                file.as_ref()
                    .map(|f| f.as_raw_handle() as usize)
                    .unwrap_or(0)
            };
            let forbidden = [
                self.job,
                parent_handle(&self.stdin),
                parent_handle(&self.stdout),
                parent_handle(&self.captured_stderr),
            ];
            let handles = inheritance_list(handles, &forbidden)?;
            let mut attributes = Attributes::new(handles)?;
            attributes.install()?;
            let mut startup: StartupEx = unsafe { zeroed() };
            startup.startup.cb = size_of::<StartupEx>() as u32;
            startup.startup.flags = STARTF_USESTDHANDLES;
            startup.startup.stdin = handles[0] as Handle;
            startup.startup.stdout = handles[1] as Handle;
            startup.startup.stderr = handles[2] as Handle;
            startup.attributes = attributes.pointer;
            let mut process = ProcessInfo::default();
            let flags = CREATE_SUSPENDED
                | CREATE_UNICODE_ENVIRONMENT
                | EXTENDED_STARTUPINFO_PRESENT
                | CREATE_NO_WINDOW;
            // No BREAKAWAY flag; only the explicit three standard handles inherit.
            let result = unsafe {
                CreateProcessW(
                    self.prepared.application.as_ptr(),
                    self.prepared.command.as_mut_ptr(),
                    null(),
                    null(),
                    1,
                    flags,
                    self.prepared.environment.as_ptr().cast(),
                    self.prepared.directory.as_ptr(),
                    &mut startup,
                    &mut process,
                )
            };
            let mut errors = if result == 0 {
                vec![last("create_process")]
            } else {
                Vec::new()
            };
            // Closing the parent's copies is required for EOF on control/capture.
            // Do not discard a created root if a pipe close itself fails.
            for handle in handles {
                if let Some(position) = self.auxiliary.iter().position(|&h| h == handle) {
                    match self.close(handle) {
                        Ok(()) => {
                            self.auxiliary.swap_remove(position);
                        }
                        Err(cause) => errors.push(cause),
                    }
                }
            }
            Ok(Created {
                process: process.process as usize,
                thread: process.thread as usize,
                pid: process.pid,
                errors,
            })
        }

        fn close_auxiliary(&mut self) -> Vec<Cause> {
            let mut errors = Vec::new();
            let mut at = 0;
            while at < self.auxiliary.len() {
                match self.close(self.auxiliary[at]) {
                    Ok(()) => {
                        self.auxiliary.swap_remove(at);
                    }
                    Err(error) => {
                        errors.push(error);
                        at += 1;
                    }
                }
            }
            errors
        }

        fn close_streams(&mut self) -> Vec<Cause> {
            let mut errors = Vec::new();
            for slot in [&mut self.stdin, &mut self.stdout, &mut self.captured_stderr] {
                if let Some(file) = slot.take() {
                    if let Err((file, cause)) = close_file(file) {
                        *slot = Some(file);
                        errors.push(cause);
                    }
                }
            }
            if let Stderr::File(file) = std::mem::replace(&mut self.stderr, Stderr::Capture) {
                if let Err((file, cause)) = close_file(file) {
                    self.stderr = Stderr::File(file);
                    errors.push(cause);
                }
            }
            errors
        }
    }

    fn close_file(file: File) -> Result<(), (File, Cause)> {
        let raw = file.into_raw_handle();
        match checked_bool(unsafe { CloseHandle(raw) }, "close_stream") {
            Ok(()) => Ok(()),
            Err(cause) => Err((unsafe { File::from_raw_handle(raw) }, cause)),
        }
    }

    impl Api for NativeApi {
        fn create_job(&mut self) -> Result<usize, Cause> {
            // NULL security attributes create a noninheritable, unnamed own job.
            let job = unsafe { CreateJobObjectW(null(), null()) } as usize;
            if valid_handle(job) {
                self.job = job;
                Ok(job)
            } else {
                Err(last("create_job"))
            }
        }
        fn configure_job(&mut self, job: usize) -> Result<(), Cause> {
            let mut limit = ExtendedLimit::default();
            limit.basic.flags = KILL_ON_CLOSE;
            checked_bool(
                unsafe {
                    SetInformationJobObject(
                        job as Handle,
                        9,
                        (&limit as *const ExtendedLimit).cast(),
                        size_of::<ExtendedLimit>() as u32,
                    )
                },
                "configure_job",
            )
        }
        fn create_suspended(&mut self) -> Created {
            match self.create() {
                Ok(created) => created,
                Err(error) => Created {
                    errors: vec![error],
                    ..Created::default()
                },
            }
        }
        fn assign(&mut self, job: usize, process: usize) -> Result<(), Cause> {
            checked_bool(
                unsafe { AssignProcessToJobObject(job as Handle, process as Handle) },
                "assign_job",
            )
        }
        fn member(&mut self, job: usize, process: usize) -> Result<bool, Cause> {
            let mut member = 0;
            checked_bool(
                unsafe { IsProcessInJob(process as Handle, job as Handle, &mut member) },
                "query_membership",
            )?;
            Ok(member != 0)
        }
        fn verify_image(&mut self, process: usize) -> Result<(), Cause> {
            let mut image = vec![0u16; 32768];
            let mut length = image.len() as u32;
            checked_bool(
                unsafe {
                    QueryFullProcessImageNameW(
                        process as Handle,
                        0,
                        image.as_mut_ptr(),
                        &mut length,
                    )
                },
                "query_image",
            )?;
            if length == 0 || length as usize >= image.len() {
                return Err(Cause::fixed("invalid_image_length"));
            }
            image.truncate(length as usize);
            no_nul(&image)?;
            let actual = std::fs::canonicalize(PathBuf::from(OsString::from_wide(&image)))
                .map_err(|_| Cause::fixed("image_canonicalization"))?;
            // Recheck the requested path as well as the actual created HANDLE.
            let requested = OsString::from_wide(
                &self.prepared.application[..self.prepared.application.len() - 1],
            );
            let requested = std::fs::canonicalize(PathBuf::from(requested))
                .map_err(|_| Cause::fixed("image_recheck"))?;
            let expected: Vec<u16> = self
                .prepared
                .expected_image
                .as_os_str()
                .encode_wide()
                .collect();
            for path in [actual, requested] {
                let path: Vec<u16> = path.as_os_str().encode_wide().collect();
                if compare_ordinal(&path, &expected)? != Ordering::Equal {
                    return Err(Cause::fixed("image_changed"));
                }
            }
            Ok(())
        }
        fn resume(&mut self, thread: usize) -> Result<u32, Cause> {
            let result = unsafe { ResumeThread(thread as Handle) };
            if result == u32::MAX {
                Err(last("resume_thread"))
            } else {
                Ok(result)
            }
        }
        fn accounting(&mut self, job: usize) -> Result<Accounting, Cause> {
            let mut accounting = BasicAccounting::default();
            let mut returned = 0;
            checked_bool(
                unsafe {
                    QueryInformationJobObject(
                        job as Handle,
                        1,
                        (&mut accounting as *mut BasicAccounting).cast(),
                        size_of::<BasicAccounting>() as u32,
                        &mut returned,
                    )
                },
                "query_accounting",
            )?;
            if returned != size_of::<BasicAccounting>() as u32
                || accounting.active > accounting.total
            {
                return Err(Cause::fixed("invalid_accounting"));
            }
            Ok(Accounting {
                active_processes: accounting.active,
                total_processes: accounting.total,
            })
        }
        fn root_exited(&mut self, process: usize) -> Result<bool, Cause> {
            match unsafe { WaitForSingleObject(process as Handle, 0) } {
                WAIT_OBJECT_0 => Ok(true),
                WAIT_TIMEOUT => Ok(false),
                _ => Err(last("wait_root")),
            }
        }
        fn terminate_job(&mut self, job: usize) -> Result<(), Cause> {
            checked_bool(
                unsafe { TerminateJobObject(job as Handle, 1) },
                "terminate_job",
            )
        }
        fn terminate_root(&mut self, process: usize) -> Result<(), Cause> {
            checked_bool(
                unsafe { TerminateProcess(process as Handle, 1) },
                "terminate_root",
            )
        }
        fn close(&mut self, handle: usize) -> Result<(), Cause> {
            checked_bool(unsafe { CloseHandle(handle as Handle) }, "close_handle")
        }
        fn clock(&mut self) -> Duration {
            self.start.elapsed()
        }
        fn sleep(&mut self, duration: Duration) {
            std::thread::sleep(duration);
        }
    }

    impl Drop for NativeApi {
        fn drop(&mut self) {
            for handle in self.auxiliary.drain(..) {
                let _ = unsafe { CloseHandle(handle as Handle) };
            }
        }
    }

    /// The owner must outlive every control request and failed stop/recovery.
    /// Its final drop closes the kill-on-close job, which terminates descendants.
    pub struct JobChild {
        owner: Owner<NativeApi>,
    }

    pub struct SpawnFailure {
        pub error: JobError,
        pub child: Option<JobChild>,
    }

    // Debug intentionally excludes LaunchSpec, NativeApi and pipe contents.
    impl fmt::Debug for SpawnFailure {
        fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
            f.debug_struct("SpawnFailure")
                .field("error", &self.error)
                .field("owns_child", &self.child.is_some())
                .finish()
        }
    }

    impl SpawnFailure {
        /// Explicit recovery preserves the original launch error and every
        /// termination/wait/close error. Unconfirmed ownership stays in `child`.
        pub fn cleanup(&mut self, timeout: Duration) -> JobError {
            let mut causes = self.error.causes.clone();
            if let Some(child) = &mut self.child {
                if !child.status().empty_confirmed {
                    if let Err(error) = child.terminate_and_wait(timeout) {
                        causes.extend(error.causes);
                    }
                }
                if child.status().empty_confirmed {
                    if let Err(error) = child.close_empty() {
                        causes.extend(error.causes);
                    }
                }
            }
            JobError { causes }
        }
    }

    impl JobChild {
        pub fn spawn(spec: LaunchSpec) -> Result<Self, SpawnFailure> {
            let api = NativeApi::new(spec).map_err(|cause| SpawnFailure {
                error: JobError::one(cause),
                child: None,
            })?;
            Owner::launch(api)
                .map(|owner| Self { owner })
                .map_err(|failure| SpawnFailure {
                    error: failure.error,
                    child: Some(Self {
                        owner: failure.owner,
                    }),
                })
        }
        pub fn status(&self) -> Status {
            self.owner.status
        }
        pub fn root_pid(&self) -> u32 {
            self.owner.status.root_pid
        }
        pub fn take_stdin(&mut self) -> Option<File> {
            self.owner.api.stdin.take()
        }
        pub fn take_stdout(&mut self) -> Option<File> {
            self.owner.api.stdout.take()
        }
        pub fn take_stderr(&mut self) -> Option<File> {
            self.owner.api.captured_stderr.take()
        }
        pub fn query(&mut self) -> Result<Accounting, JobError> {
            self.owner.query()
        }
        /// A root exit code is not proof that its descendants have exited.
        pub fn root_exit_code(&mut self) -> Result<Option<u32>, JobError> {
            if !valid_handle(self.owner.process) {
                return Err(JobError::one(Cause::fixed("root_unavailable")));
            }
            if !self
                .owner
                .api
                .root_exited(self.owner.process)
                .map_err(JobError::one)?
            {
                return Ok(None);
            }
            let mut code = 0;
            checked_bool(
                unsafe { GetExitCodeProcess(self.owner.process as Handle, &mut code) },
                "root_exit_code",
            )
            .map_err(JobError::one)?;
            Ok(Some(code))
        }
        pub fn wait_empty(&mut self, timeout: Duration) -> Result<Accounting, JobError> {
            self.owner.wait_empty(timeout)
        }
        pub fn terminate_and_wait(&mut self, timeout: Duration) -> Result<Accounting, JobError> {
            self.owner.terminate_and_wait(timeout)
        }
        pub fn close_empty(&mut self) -> Result<(), JobError> {
            if !self.owner.status.empty_confirmed {
                return Err(JobError::one(Cause::fixed(
                    "close_requires_confirmed_empty",
                )));
            }
            let mut causes = Vec::new();
            if let Err(error) = self.owner.close_empty() {
                causes.extend(error.causes);
            }
            causes.extend(self.owner.api.close_auxiliary());
            causes.extend(self.owner.api.close_streams());
            if causes.is_empty() {
                Ok(())
            } else {
                Err(JobError { causes })
            }
        }
    }
}

#[cfg(windows)]
pub use native::{JobChild, LaunchSpec, SpawnFailure, Stderr};

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::{BTreeSet, VecDeque};
    use std::sync::{Arc, Mutex};

    struct Fake {
        events: Arc<Mutex<Vec<String>>>,
        faults: BTreeSet<&'static str>,
        created: (usize, usize, u32),
        member: bool,
        resume: u32,
        accounting: VecDeque<Result<Accounting, Cause>>,
        active: u32,
        root_exited: bool,
        times: VecDeque<Duration>,
        now: Duration,
    }

    impl Default for Fake {
        fn default() -> Self {
            Self {
                events: Arc::new(Mutex::new(Vec::new())),
                faults: BTreeSet::new(),
                created: (20, 30, 42),
                member: true,
                resume: 1,
                accounting: VecDeque::new(),
                active: 1,
                root_exited: true,
                times: VecDeque::new(),
                now: Duration::ZERO,
            }
        }
    }

    impl Fake {
        fn call(&mut self, name: &'static str) -> Result<(), Cause> {
            self.events.lock().unwrap().push(name.to_owned());
            if self.faults.contains(name) {
                Err(Cause::fixed(name))
            } else {
                Ok(())
            }
        }
    }

    impl Api for Fake {
        fn create_job(&mut self) -> Result<usize, Cause> {
            self.call("create_job")?;
            Ok(10)
        }
        fn configure_job(&mut self, _: usize) -> Result<(), Cause> {
            self.call("configure")
        }
        fn create_suspended(&mut self) -> Created {
            let errors = self.call("create_suspended").err().into_iter().collect();
            Created {
                process: self.created.0,
                thread: self.created.1,
                pid: self.created.2,
                errors,
            }
        }
        fn assign(&mut self, j: usize, p: usize) -> Result<(), Cause> {
            assert_eq!((j, p), (10, 20));
            self.call("assign")
        }
        fn member(&mut self, j: usize, p: usize) -> Result<bool, Cause> {
            assert_eq!((j, p), (10, 20));
            self.call("member")?;
            Ok(self.member)
        }
        fn verify_image(&mut self, p: usize) -> Result<(), Cause> {
            assert_eq!(p, 20);
            self.call("image")
        }
        fn resume(&mut self, t: usize) -> Result<u32, Cause> {
            assert_eq!(t, 30);
            self.call("resume")?;
            Ok(self.resume)
        }
        fn accounting(&mut self, j: usize) -> Result<Accounting, Cause> {
            assert_eq!(j, 10);
            self.call("accounting")?;
            self.accounting.pop_front().unwrap_or(Ok(Accounting {
                active_processes: self.active,
                total_processes: 3,
            }))
        }
        fn root_exited(&mut self, p: usize) -> Result<bool, Cause> {
            assert_eq!(p, 20);
            self.call("root_exited")?;
            Ok(self.root_exited)
        }
        fn terminate_job(&mut self, j: usize) -> Result<(), Cause> {
            assert_eq!(j, 10);
            self.call("terminate_job")?;
            self.active = 0;
            Ok(())
        }
        fn terminate_root(&mut self, p: usize) -> Result<(), Cause> {
            assert_eq!(p, 20);
            self.call("terminate_root")?;
            self.root_exited = true;
            Ok(())
        }
        fn close(&mut self, h: usize) -> Result<(), Cause> {
            self.events.lock().unwrap().push(format!("close:{h}"));
            if self.faults.contains("close") {
                Err(Cause::fixed("close"))
            } else {
                Ok(())
            }
        }
        fn clock(&mut self) -> Duration {
            if let Some(now) = self.times.pop_front() {
                self.now = now;
            }
            self.now
        }
        fn sleep(&mut self, d: Duration) {
            self.now += d;
        }
    }

    fn launch(fake: Fake) -> Owner<Fake> {
        match Owner::launch(fake) {
            Ok(owner) => owner,
            Err(failure) => panic!("{}", failure.error),
        }
    }

    fn launch_failure(fake: Fake) -> LaunchFailure<Fake> {
        match Owner::launch(fake) {
            Ok(_) => panic!("expected failure"),
            Err(failure) => failure,
        }
    }

    fn operations(owner: &Owner<Fake>) -> Vec<String> {
        owner.api.events.lock().unwrap().clone()
    }
    fn cause(error: JobError) -> Vec<&'static str> {
        error.causes.iter().map(|e| e.operation).collect()
    }
    fn wide(s: &str) -> Vec<u16> {
        s.encode_utf16().collect()
    }
    fn text(s: &[u16]) -> String {
        String::from_utf16(s).unwrap()
    }

    #[test]
    fn actual_core_establishes_ownership_before_any_resume() {
        let owner = launch(Fake::default());
        assert_eq!(
            operations(&owner),
            [
                "create_job",
                "configure",
                "create_suspended",
                "assign",
                "member",
                "image",
                "resume"
            ]
        );
        assert_eq!(owner.status.root_pid, 42);
        assert!(owner.status.assigned && owner.status.may_have_run);
        assert_eq!(owner.status.resume, ResumeState::ConfirmedOne);
    }

    #[test]
    fn every_pre_resume_failure_retains_handles_without_running_or_cancelling() {
        for stage in [
            "create_job",
            "configure",
            "create_suspended",
            "assign",
            "member",
            "image",
        ] {
            let mut fake = Fake::default();
            fake.faults.insert(stage);
            let failure = launch_failure(fake);
            assert_eq!(failure.error.causes[0].operation, stage);
            assert!(!failure.owner.status.may_have_run);
            assert_eq!(failure.owner.status.resume, ResumeState::NotAttempted);
            let calls = operations(&failure.owner);
            assert!(!calls
                .iter()
                .any(|c| c == "resume" || c.starts_with("terminate") || c.starts_with("close")));
            if stage != "create_job" {
                assert_eq!(failure.owner.job, 10);
            }
        }
    }

    #[test]
    fn membership_false_is_not_permission_to_resume() {
        let fake = Fake {
            member: false,
            ..Fake::default()
        };
        let mut failure = launch_failure(fake);
        assert_eq!(failure.error.causes[0].operation, "membership_missing");
        assert!(!operations(&failure.owner).contains(&"image".into()));
        assert!(!failure.owner.status.membership_verified);
        failure
            .owner
            .terminate_and_wait(Duration::from_millis(10))
            .unwrap();
        let calls = operations(&failure.owner);
        assert!(calls.contains(&"terminate_root".into()));
        assert!(calls.contains(&"terminate_job".into()));
    }

    #[test]
    fn invalid_partial_creation_keeps_only_actual_handles_owned() {
        let fake = Fake {
            created: (20, 0, 42),
            ..Fake::default()
        };
        let mut failure = launch_failure(fake);
        assert_eq!(failure.error.causes[0].operation, "invalid_created_process");
        assert!(!failure.owner.status.assigned);
        failure
            .owner
            .terminate_and_wait(Duration::from_millis(10))
            .unwrap();
        failure.owner.close_empty().unwrap();
        let calls = operations(&failure.owner);
        assert!(calls.contains(&"terminate_root".into()));
        assert!(!calls.contains(&"terminate_job".into()));
        assert_eq!(calls.iter().filter(|&x| x == "close:20").count(), 1);
        assert!(!calls.contains(&"close:0".into()));
    }

    #[test]
    fn resume_failure_and_unexpected_counts_never_claim_zero_effects() {
        let mut fake = Fake::default();
        fake.faults.insert("resume");
        let failure = launch_failure(fake);
        assert!(failure.owner.status.may_have_run);
        assert_eq!(failure.owner.status.resume, ResumeState::Failed);
        for previous in [0, 2, u32::MAX] {
            let failure = launch_failure(Fake {
                resume: previous,
                ..Fake::default()
            });
            assert!(failure.owner.status.may_have_run && failure.owner.status.assigned);
            assert_eq!(failure.owner.status.resume, ResumeState::UnexpectedCount);
        }
    }

    #[test]
    fn observation_timeout_does_not_terminate_close_or_consume_owner() {
        let mut owner = launch(Fake::default());
        assert_eq!(
            cause(owner.wait_empty(Duration::from_millis(25)).unwrap_err()),
            ["wait_timeout"]
        );
        assert_eq!((owner.job, owner.process, owner.thread), (10, 20, 30));
        assert!(!operations(&owner)
            .iter()
            .any(|c| c.starts_with("terminate") || c.starts_with("close")));
        owner.api.active = 0;
        assert_eq!(
            owner
                .wait_empty(Duration::from_millis(10))
                .unwrap()
                .active_processes,
            0
        );
        assert!(owner.status.empty_confirmed);
    }

    #[test]
    fn query_failure_does_not_close_or_terminate_live_job() {
        let mut owner = launch(Fake::default());
        owner.api.faults.insert("accounting");
        assert_eq!(cause(owner.query().unwrap_err()), ["accounting"]);
        assert!(!operations(&owner)
            .iter()
            .any(|c| c.starts_with("terminate") || c.starts_with("close")));
        owner.api.faults.clear();
        assert_eq!(owner.query().unwrap().active_processes, 1);
    }

    #[test]
    fn inconsistent_or_never_assigned_accounting_cannot_confirm_empty() {
        for accounting in [
            Accounting {
                active_processes: 0,
                total_processes: 0,
            },
            Accounting {
                active_processes: 2,
                total_processes: 1,
            },
        ] {
            let mut owner = launch(Fake {
                accounting: VecDeque::from([Ok(accounting)]),
                ..Fake::default()
            });
            assert_eq!(
                cause(owner.wait_empty(Duration::from_millis(10)).unwrap_err()),
                ["invalid_accounting"]
            );
            assert!(!owner.status.empty_confirmed);
            assert!(!operations(&owner)
                .iter()
                .any(|op| op.starts_with("terminate") || op.starts_with("close")));
        }
    }

    #[test]
    fn empty_accounting_requires_root_handle_signaled() {
        let mut owner = launch(Fake {
            active: 0,
            root_exited: false,
            ..Fake::default()
        });
        assert_eq!(
            cause(owner.wait_empty(Duration::from_millis(10)).unwrap_err()),
            ["empty_root_unsignaled"]
        );
        assert!(!owner.status.empty_confirmed);
    }

    #[test]
    fn exact_deadline_empty_is_accepted_but_late_empty_is_not() {
        for (elapsed, accept) in [(10, true), (11, false)] {
            let fake = Fake {
                active: 0,
                times: VecDeque::from([Duration::ZERO, Duration::from_millis(elapsed)]),
                ..Fake::default()
            };
            let mut owner = launch(fake);
            assert_eq!(owner.wait_empty(Duration::from_millis(10)).is_ok(), accept);
        }
    }

    #[test]
    fn regressing_clock_cannot_turn_empty_into_success() {
        let fake = Fake {
            active: 0,
            times: VecDeque::from([Duration::from_millis(20), Duration::from_millis(19)]),
            ..Fake::default()
        };
        let mut owner = launch(fake);
        assert_eq!(
            cause(owner.wait_empty(Duration::from_millis(10)).unwrap_err()),
            ["clock_regressed"]
        );
        assert!(!owner.status.empty_confirmed);
    }

    #[test]
    fn zero_budget_performs_no_accounting_or_termination() {
        let mut owner = launch(Fake::default());
        assert_eq!(
            cause(owner.wait_empty(Duration::ZERO).unwrap_err()),
            ["invalid_wait_budget"]
        );
        assert_eq!(operations(&owner).last().unwrap(), "resume");
        assert_eq!(
            cause(owner.terminate_and_wait(Duration::ZERO).unwrap_err()),
            ["invalid_wait_budget"]
        );
        assert_eq!(operations(&owner).last().unwrap(), "resume");
    }

    #[test]
    fn explicit_termination_preserves_both_primary_and_query_failure() {
        let mut owner = launch(Fake::default());
        owner.api.faults.extend(["terminate_job", "accounting"]);
        assert_eq!(
            cause(
                owner
                    .terminate_and_wait(Duration::from_millis(10))
                    .unwrap_err()
            ),
            ["terminate_job", "accounting"]
        );
        assert_eq!(owner.job, 10);
        assert!(!owner.status.empty_confirmed);
        assert!(!operations(&owner).iter().any(|c| c.starts_with("close")));
    }

    #[test]
    fn termination_error_is_preserved_even_when_exit_is_confirmed() {
        let mut owner = launch(Fake {
            active: 0,
            ..Fake::default()
        });
        owner.api.faults.insert("terminate_job");
        assert_eq!(
            cause(
                owner
                    .terminate_and_wait(Duration::from_millis(10))
                    .unwrap_err()
            ),
            ["terminate_job"]
        );
        assert!(owner.status.empty_confirmed);
    }

    #[test]
    fn explicit_close_requires_empty_and_closes_each_handle_once() {
        let mut owner = launch(Fake::default());
        assert_eq!(
            cause(owner.close_empty().unwrap_err()),
            ["close_requires_confirmed_empty"]
        );
        owner.terminate_and_wait(Duration::from_millis(10)).unwrap();
        owner.close_empty().unwrap();
        owner.close_empty().unwrap();
        let events = owner.api.events.clone();
        drop(owner);
        let calls = events.lock().unwrap();
        for handle in ["close:30", "close:20", "close:10"] {
            assert_eq!(calls.iter().filter(|&x| x == handle).count(), 1);
        }
    }

    #[test]
    fn close_failures_are_reported_together_and_handles_stay_recoverable() {
        let mut owner = launch(Fake {
            active: 0,
            ..Fake::default()
        });
        owner.wait_empty(Duration::from_millis(10)).unwrap();
        owner.api.faults.insert("close");
        assert_eq!(
            cause(owner.close_empty().unwrap_err()),
            ["close", "close", "close"]
        );
        assert_eq!((owner.job, owner.process, owner.thread), (10, 20, 30));
        owner.api.faults.clear();
        owner.close_empty().unwrap();
        assert_eq!((owner.job, owner.process, owner.thread), (0, 0, 0));
    }

    #[test]
    fn job_is_never_queried_through_zero_or_outer_job_handle() {
        let mut fake = Fake::default();
        fake.faults.insert("assign");
        let mut failure = launch_failure(fake);
        assert_eq!(
            cause(failure.owner.query().unwrap_err()),
            ["job_not_assigned"]
        );
        assert!(!operations(&failure.owner).contains(&"accounting".into()));
    }

    #[test]
    fn drop_of_unassigned_partial_root_does_not_orphan_a_suspended_process() {
        let fake = Fake {
            created: (20, 0, 42),
            ..Fake::default()
        };
        let events = fake.events.clone();
        drop(launch_failure(fake));
        let calls = events.lock().unwrap();
        assert_eq!(
            &calls[calls.len() - 3..],
            ["terminate_root", "close:20", "close:10"]
        );
    }

    #[test]
    fn argv_empty_space_quote_and_trailing_backslashes_are_literal() {
        let vectors = [
            ("", r#""""#),
            ("plain", r#""plain""#),
            ("two words", r#""two words""#),
            ("a\"b", r#""a\"b""#),
            ("C:\\tail\\", r#""C:\tail\\""#),
            ("\\\"", r#""\\\"""#),
        ];
        for (input, expected) in vectors {
            assert_eq!(text(&quote_argument(&wide(input)).unwrap()), expected);
        }
    }

    #[test]
    fn argv_preserves_utf16_code_units_including_unpaired_surrogate() {
        let input = [0xd800, 0xd83d, 0xde00, 92, 34, 0xdc00];
        let quoted = quote_argument(&input).unwrap();
        assert_eq!(
            quoted,
            [34, 0xd800, 0xd83d, 0xde00, 92, 92, 92, 34, 0xdc00, 34]
        );
    }

    #[test]
    fn command_has_explicit_argv0_no_shell_and_exact_terminator() {
        let command = command_line(
            &wide("C:\\Program Files\\app.exe"),
            &[wide(""), wide("& ; $ value")],
        )
        .unwrap();
        assert_eq!(
            text(&command[..command.len() - 1]),
            r#""C:\Program Files\app.exe" "" "& ; $ value""#
        );
        assert_eq!(command.last(), Some(&0));
        assert_eq!(command.iter().filter(|&&x| x == 0).count(), 1);
    }

    #[test]
    fn nul_and_oversized_command_are_rejected_before_process_creation() {
        assert_eq!(
            quote_argument(&[65, 0, 66]).unwrap_err().operation,
            "embedded_nul"
        );
        assert_eq!(
            command_line(&wide("x.exe"), &[vec![65; 32760]])
                .unwrap_err()
                .operation,
            "command_line_too_long"
        );
        assert!(command_line(&[], &[]).is_err());
        assert!(command_line(&wide("a\".exe"), &[]).is_err());
    }

    fn ascii_case_compare(a: &[u16], b: &[u16]) -> Result<Ordering, Cause> {
        let fold = |v: &[u16]| {
            v.iter()
                .map(|&c| if (97..=122).contains(&c) { c - 32 } else { c })
                .collect::<Vec<_>>()
        };
        Ok(fold(a).cmp(&fold(b)))
    }

    #[test]
    fn explicit_environment_sorted_and_double_nul_without_parent_inheritance() {
        let env = [
            (wide("z"), wide("")),
            (wide("a"), wide("has=equals")),
            (wide("Path"), wide("owned")),
        ];
        assert_eq!(
            text(&environment_block(&env, ascii_case_compare).unwrap()),
            "a=has=equals\0Path=owned\0z=\0\0"
        );
        assert_eq!(environment_block(&[], ascii_case_compare).unwrap(), [0, 0]);
    }

    #[test]
    fn duplicate_env_case_keys_and_invalid_names_are_rejected() {
        let duplicate = [(wide("Path"), wide("a")), (wide("PATH"), wide("b"))];
        assert_eq!(
            environment_block(&duplicate, ascii_case_compare)
                .unwrap_err()
                .operation,
            "duplicate_environment_key"
        );
        for key in [vec![], wide("a=b"), vec![65, 0]] {
            assert!(environment_block(&[(key, wide("v"))], ascii_case_compare).is_err());
        }
        assert!(environment_block(&[(wide("A"), vec![0])], ascii_case_compare).is_err());
    }

    #[test]
    fn unicode_env_keys_are_passed_unchanged_to_platform_comparator() {
        let env = [
            (vec![0x0131], wide("first")),
            (vec![0x0049], wide("second")),
        ];
        let mut seen = false;
        let result = environment_block(&env, |a, b| {
            assert_eq!(a, [0x0049]);
            assert_eq!(b, [0x0131]);
            seen = true;
            Ok(Ordering::Equal)
        });
        assert!(seen);
        assert_eq!(result.unwrap_err().operation, "duplicate_environment_key");
        let result = environment_block(&env, |_, _| Err(Cause::fixed("compare_failure")));
        assert_eq!(result.unwrap_err().operation, "compare_failure");
    }

    #[test]
    fn fixed_error_format_contains_only_codes_and_no_payload() {
        let error = JobError {
            causes: vec![
                Cause::fixed("image_changed"),
                Cause {
                    operation: "close_handle",
                    windows_code: Some(5),
                },
            ],
        };
        assert_eq!(
            error.to_string(),
            "windows_job:image_changed:close_handle(5)"
        );
    }

    #[test]
    #[cfg(target_pointer_width = "64")]
    fn win64_abi_sizes_alignment_and_field_offsets_match_sdk() {
        use super::abi::*;
        use std::mem::{align_of, offset_of, size_of};
        assert_eq!(size_of::<BasicLimit>(), 64);
        assert_eq!(align_of::<BasicLimit>(), 8);
        assert_eq!(size_of::<ExtendedLimit>(), 144);
        assert_eq!(size_of::<BasicAccounting>(), 48);
        assert_eq!(size_of::<StartupInfo>(), 104);
        assert_eq!(size_of::<StartupEx>(), 112);
        assert_eq!(size_of::<ProcessInfo>(), 24);
        assert_eq!(size_of::<SecurityAttributes>(), 24);
        assert_eq!(offset_of!(BasicLimit, min_working_set), 24);
        assert_eq!(offset_of!(BasicLimit, affinity), 48);
        assert_eq!(offset_of!(ExtendedLimit, io), 64);
        assert_eq!(offset_of!(ExtendedLimit, process_memory), 112);
        assert_eq!(offset_of!(StartupInfo, flags), 60);
        assert_eq!(offset_of!(StartupInfo, stdin), 80);
        assert_eq!(offset_of!(StartupEx, attributes), 104);
        assert_eq!(offset_of!(ProcessInfo, pid), 16);
        assert_eq!(offset_of!(BasicAccounting, active), 40);
    }

    #[test]
    fn inheritance_allowlist_rejects_parent_job_invalid_and_duplicate_handles() {
        assert_eq!(
            inheritance_list([11, 12, 13], &[10, 20, 21, 22]).unwrap(),
            [11, 12, 13]
        );
        for handles in [
            [0, 12, 13],
            [usize::MAX, 12, 13],
            [11, 11, 13],
            [10, 12, 13],
            [11, 20, 13],
            [11, 12, 22],
        ] {
            assert_eq!(
                inheritance_list(handles, &[10, 20, 21, 22])
                    .unwrap_err()
                    .operation,
                "invalid_inheritance_list"
            );
        }
    }

    #[test]
    fn root_exit_alone_does_not_confirm_descendant_exit() {
        let fake = Fake {
            root_exited: true,
            accounting: VecDeque::from([
                Ok(Accounting {
                    active_processes: 2,
                    total_processes: 3,
                }),
                Ok(Accounting {
                    active_processes: 1,
                    total_processes: 3,
                }),
                Ok(Accounting {
                    active_processes: 0,
                    total_processes: 3,
                }),
            ]),
            ..Fake::default()
        };
        let mut owner = launch(fake);
        let empty = owner.wait_empty(Duration::from_millis(50)).unwrap();
        assert_eq!(empty.total_processes, 3);
        assert_eq!(
            operations(&owner)
                .iter()
                .filter(|&c| c == "accounting")
                .count(),
            3
        );
        assert_eq!(
            operations(&owner)
                .iter()
                .filter(|&c| c == "root_exited")
                .count(),
            1
        );
    }
}
