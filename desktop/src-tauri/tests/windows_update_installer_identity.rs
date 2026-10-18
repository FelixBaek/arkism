//! Hosted-only inert PE and NSIS fixture; no download or product installation.
#![cfg(windows)]

#[path = "../src/update_installer.rs"]
mod update_installer;

use sha2::{Digest, Sha256};
use std::cell::RefCell;
use std::ffi::{c_void, OsString};
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::os::windows::ffi::OsStrExt;
use std::os::windows::fs::{MetadataExt, OpenOptionsExt};
use std::os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle};
use std::panic::{catch_unwind, resume_unwind, AssertUnwindSafe};
use std::path::{Path, PathBuf};
use std::rc::Rc;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use update_installer::test_diagnostics as diagnostic;
use update_installer::Installer;

const NAME: &str = "Arkism-1.2.3-setup.exe";
fn hash(bytes: &[u8]) -> String {
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}
fn wait_file(path: &Path) {
    let start = Instant::now();
    while !path.exists() {
        assert!(
            start.elapsed() < Duration::from_secs(15),
            "inert peer did not publish its marker"
        );
        std::thread::sleep(Duration::from_millis(10));
    }
}
fn require_hosted() -> PathBuf {
    assert_eq!(
        std::env::var("ARKISM_WINDOWS_UPDATE_IDENTITY_TEST").as_deref(),
        Ok("1")
    );
    assert_eq!(std::env::var("GITHUB_ACTIONS").as_deref(), Ok("true"));
    assert_eq!(std::env::var("RUNNER_OS").as_deref(), Ok("Windows"));
    assert_eq!(
        std::env::var("RUNNER_ENVIRONMENT").as_deref(),
        Ok("github-hosted")
    );
    let root =
        PathBuf::from(std::env::var_os("RUNNER_TEMP").expect("hosted temporary directory missing"));
    assert!(root.is_absolute() && root.is_dir());
    root
}
fn owned_root(parent: &Path) -> PathBuf {
    let mut nonce = [0; 16];
    getrandom::fill(&mut nonce).unwrap();
    let suffix: String = nonce.iter().map(|b| format!("{b:02x}")).collect();
    let root = parent.join(format!("arkism identity [fixture] {suffix}"));
    fs::create_dir(&root).unwrap();
    root
}
fn create_new(path: &Path, bytes: &[u8]) {
    OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)
        .unwrap()
        .write_all(bytes)
        .unwrap();
}
fn environment(changes: &[(&str, OsString)]) -> Vec<(OsString, OsString)> {
    let mut values: Vec<_> = std::env::vars_os()
        .filter(|(key, _)| !key.to_string_lossy().starts_with('='))
        .collect();
    for (key, value) in changes {
        values.retain(|(existing, _)| !existing.to_string_lossy().eq_ignore_ascii_case(key));
        values.push((key.into(), value.clone()));
    }
    values
}

#[repr(C)]
struct Security {
    length: u32,
    descriptor: *mut c_void,
    inherit: i32,
}
#[link(name = "kernel32")]
extern "system" {
    fn CreateFileMappingW(
        file: *mut c_void,
        security: *const Security,
        protection: u32,
        maximum_high: u32,
        maximum_low: u32,
        name: *const u16,
    ) -> *mut c_void;
    fn MapViewOfFile(
        mapping: *mut c_void,
        access: u32,
        offset_high: u32,
        offset_low: u32,
        size: usize,
    ) -> *mut c_void;
    fn FlushViewOfFile(view: *const c_void, size: usize) -> i32;
    fn UnmapViewOfFile(view: *const c_void) -> i32;
    fn CreateEventW(
        security: *const Security,
        manual: i32,
        initial: i32,
        name: *const u16,
    ) -> *mut c_void;
    fn SetEvent(event: *mut c_void) -> i32;
    fn WaitForSingleObject(handle: *mut c_void, milliseconds: u32) -> u32;
    fn DeviceIoControl(
        handle: *mut c_void,
        code: u32,
        input: *const c_void,
        input_size: u32,
        output: *mut c_void,
        output_size: u32,
        returned: *mut u32,
        overlapped: *mut c_void,
    ) -> i32;
}

struct NativeInputs {
    source: String,
    nsis_bytes: Vec<u8>,
    nsis_hash: String,
    compiler_hash: Option<String>,
    managed: [ManagedImage; 2],
    control_source_hash: String,
    control_manifest_hash: String,
    control_compiler_hash: String,
}

struct ManagedImage {
    bytes: Vec<u8>,
    hash: String,
}

fn managed_image(
    parent: &Path,
    architecture: &str,
    fields: (u16, u16, u16),
    manifest: &str,
) -> ManagedImage {
    let prefix = format!("ARKISM_WINDOWS_INSTALLER_IDENTITY_MANAGED_{architecture}");
    let path =
        PathBuf::from(std::env::var_os(format!("{prefix}_EXE")).expect("comparison_input_missing"));
    assert!(
        path.is_absolute() && path.starts_with(parent),
        "comparison_input_scope"
    );
    let length = fs::metadata(&path)
        .expect("comparison_input_metadata")
        .len();
    assert!(
        length > 0 && length <= update_installer::MAX_BYTES,
        "comparison_input_length"
    );
    let bytes = fs::read(path).expect("comparison_input_read");
    let expected = hex_environment(&format!("{prefix}_SHA256"), 64);
    assert_eq!(hash(&bytes), expected, "comparison_input_hash");
    assert_eq!(
        diagnostic::pe_fields(&bytes),
        Some(fields),
        "comparison_pe_shape"
    );
    let embedded = diagnostic::embedded_manifest(&bytes).expect("comparison_manifest_missing");
    assert_eq!(
        hash(&embedded),
        manifest,
        "comparison_embedded_manifest_hash"
    );
    ManagedImage {
        bytes,
        hash: expected,
    }
}

fn hex_environment(name: &str, length: usize) -> String {
    let value = std::env::var(name).expect("fixture hash input missing");
    assert!(value.len() == length && value.bytes().all(|byte| byte.is_ascii_hexdigit()));
    value.to_ascii_lowercase()
}

fn safe_hex_environment(name: &str, length: usize) -> Option<String> {
    std::env::var(name)
        .ok()
        .filter(|value| value.len() == length && value.bytes().all(|byte| byte.is_ascii_hexdigit()))
        .map(|value| value.to_ascii_lowercase())
}

impl NativeInputs {
    fn load(parent: &Path) -> Self {
        let source = hex_environment("ARKISM_WINDOWS_INSTALLER_IDENTITY_SOURCE_SHA", 40);
        let nsis = PathBuf::from(
            std::env::var_os("ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_EXE")
                .expect("inert NSIS input missing"),
        );
        assert!(nsis.is_absolute() && nsis.starts_with(parent));
        let length = fs::metadata(&nsis).unwrap().len();
        assert!(length > 0 && length <= update_installer::MAX_BYTES);
        let nsis_bytes = fs::read(nsis).unwrap();
        let nsis_hash = hex_environment("ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_SHA256", 64);
        assert_eq!(
            hash(&nsis_bytes),
            nsis_hash,
            "inert NSIS input hash changed"
        );
        let compiler_hash =
            std::env::var_os("ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_COMPILER_SHA256").map(|_| {
                hex_environment("ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_COMPILER_SHA256", 64)
            });
        let control_source_hash = hex_environment(
            "ARKISM_WINDOWS_INSTALLER_IDENTITY_CONTROL_SOURCE_SHA256",
            64,
        );
        let control_manifest_hash = hex_environment(
            "ARKISM_WINDOWS_INSTALLER_IDENTITY_CONTROL_MANIFEST_SHA256",
            64,
        );
        let control_compiler_hash = hex_environment(
            "ARKISM_WINDOWS_INSTALLER_IDENTITY_CONTROL_COMPILER_SHA256",
            64,
        );
        let managed = [
            managed_image(parent, "X86", (0x14c, 0x10b, 2), &control_manifest_hash),
            managed_image(parent, "X64", (0x8664, 0x20b, 2), &control_manifest_hash),
        ];
        Self {
            source,
            nsis_bytes,
            nsis_hash,
            compiler_hash,
            managed,
            control_source_hash,
            control_manifest_hash,
            control_compiler_hash,
        }
    }
}

#[derive(Default)]
struct Report {
    source: String,
    nsis_hash: String,
    compiler_hash: Option<String>,
    mapping: &'static str,
    rust_peer: bool,
    rust_peer_long: bool,
    nsis: bool,
    preserved: bool,
    complete: bool,
    control_source_hash: String,
    control_manifest_hash: String,
    control_compiler_hash: String,
    cases: Vec<CaseRecord>,
}

const MATRIX: [(&str, bool); 4] = [
    ("managed_x86", false),
    ("managed_x64", false),
    ("inert_nsis", false),
    ("nsis_dos", true),
];

struct CaseRecord {
    target: &'static str,
    namespace: &'static str,
    phase: &'static str,
    outcome: &'static str,
    image_hash: String,
    created: bool,
    terminal: bool,
    exit_code: Option<u32>,
    os_code: Option<i32>,
    operation: &'static str,
    manifest_verified: bool,
}
impl CaseRecord {
    fn declared(target: &'static str, dos: bool) -> Self {
        Self {
            target,
            namespace: if dos { "physical_dos" } else { "volume_guid" },
            phase: "not_reached",
            outcome: "not_reached",
            image_hash: String::new(),
            created: false,
            terminal: false,
            exit_code: None,
            os_code: None,
            operation: "none",
            manifest_verified: false,
        }
    }
    fn json(&self) -> String {
        let exit = self
            .exit_code
            .map_or_else(|| "null".to_string(), |code| code.to_string());
        let code = self
            .os_code
            .map_or_else(|| "null".to_string(), |code| code.to_string());
        format!("{{\"target\":\"{}\",\"namespace\":\"{}\",\"phase\":\"{}\",\"outcome\":\"{}\",\"image_sha256\":\"{}\",\"manifest_verified\":{},\"created\":{},\"terminal\":{},\"exit_code\":{exit},\"operation\":\"{}\",\"os_code\":{code}}}",
            self.target, self.namespace, self.phase, self.outcome, self.image_hash,
            self.manifest_verified, self.created, self.terminal, self.operation)
    }
}
fn case_update(index: usize, update: impl FnOnce(&mut CaseRecord)) {
    REPORT.with(|report| update(&mut report.borrow_mut().cases[index]));
}
fn case_phase(index: usize, phase: &'static str) {
    case_update(index, |case| case.phase = phase);
    diagnostic::phase(phase);
}

thread_local! { static REPORT: RefCell<Report> = RefCell::new(Report::default()); }

fn evidence(inputs: &NativeInputs, ok: bool, mapping: &'static str, nsis: bool, preserved: bool) {
    REPORT.with(|report| {
        let mut report = report.borrow_mut();
        report.source = inputs.source.clone();
        report.nsis_hash = inputs.nsis_hash.clone();
        report.compiler_hash = inputs.compiler_hash.clone();
        report.control_source_hash = inputs.control_source_hash.clone();
        report.control_manifest_hash = inputs.control_manifest_hash.clone();
        report.control_compiler_hash = inputs.control_compiler_hash.clone();
        report.mapping = mapping;
        report.nsis = nsis;
        report.preserved = preserved;
        report.complete = ok;
    });
}

fn checked<T>(result: Result<T, update_installer::Error>) -> T {
    match result {
        Ok(value) => value,
        Err(error) => {
            diagnostic::error(&error);
            panic!("identity_guard_operation_failed");
        }
    }
}

fn write_evidence(passed: bool) -> std::io::Result<()> {
    if let Some(path) = std::env::var_os("ARKISM_UPDATE_IDENTITY_EVIDENCE") {
        let parent = require_hosted();
        let path = PathBuf::from(path);
        assert_eq!(path.parent(), Some(parent.as_path()));
        let body = REPORT.with(|report| {
        let report = report.borrow();
        let ok = passed && report.complete;
        let compiler = report
            .compiler_hash
            .as_ref()
            .map_or_else(|| "null".to_string(), |value| format!("\"{value}\""));
        let peers = report.rust_peer && report.rust_peer_long;
        let cases = report.cases.iter().map(CaseRecord::json).collect::<Vec<_>>().join(",");
        let matrix_complete = report.cases.len() == MATRIX.len()
            && report.cases.iter().all(|case| case.outcome == "passed");
        format!("{{\"ok\":{ok},\"scope\":\"inert_images_only\",\"native_windows\":true,\"source_sha\":\"{}\",\"module_sha256\":\"{}\",\"fixture_sha256\":\"{}\",\"writable_mapping_outcome\":\"{}\",\"inert_nsis_self_archive\":{},\"inert_nsis_sha256\":\"{}\",\"nsis_compiler_sha256\":{compiler},\"rust_peer_complete\":{},\"rust_peer_long_complete\":{},\"parent_drop_delayed_read\":{peers},\"selective_inheritance_checked\":{peers},\"owned_root_preserved\":{},\"production_nsis_executed\":false,\"comparison_source_sha256\":\"{}\",\"comparison_manifest_sha256\":\"{}\",\"comparison_compiler_sha256\":\"{}\",\"comparison_all_required_passed\":{matrix_complete},\"comparison_cases\":[{cases}],\"dos_device_mapping_atomicity_proven\":false,\"diagnostic\":{}}}\n",
            report.source, hash(include_bytes!("../src/update_installer.rs")),
            hash(include_bytes!("windows_update_installer_identity.rs")), report.mapping,
            report.nsis, report.nsis_hash, report.rust_peer, report.rust_peer_long,
            report.preserved, report.control_source_hash, report.control_manifest_hash, report.control_compiler_hash, diagnostic::json())
        });
        OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(path)?
            .write_all(body.as_bytes())?;
    }
    Ok(())
}

struct WritableView {
    pointer: *mut c_void,
    length: usize,
}

impl WritableView {
    fn open(path: &Path, length: usize) -> Self {
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .share_mode(7)
            .open(path)
            .unwrap();
        let raw = unsafe {
            CreateFileMappingW(
                file.as_raw_handle(),
                std::ptr::null(),
                4,
                0,
                0,
                std::ptr::null(),
            )
        };
        assert!(!raw.is_null(), "writable mapping creation failed");
        let mapping = unsafe { OwnedHandle::from_raw_handle(raw) };
        let pointer = unsafe { MapViewOfFile(mapping.as_raw_handle(), 2, 0, 0, length) };
        assert!(!pointer.is_null(), "writable view creation failed");
        // The regression retains only the view across the readonly guard open.
        drop(mapping);
        drop(file);
        Self { pointer, length }
    }

    fn mutate(&self, original: &[u8]) {
        let offset = self.length / 2;
        unsafe {
            (self.pointer as *mut u8)
                .add(offset)
                .write_volatile(original[offset] ^ 0x80);
        }
        assert_ne!(
            unsafe { (self.pointer as *const u8).add(offset).read_volatile() },
            original[offset]
        );
        assert_ne!(
            unsafe { FlushViewOfFile(self.pointer, self.length) },
            0,
            "writable view flush failed"
        );
    }
}

impl Drop for WritableView {
    fn drop(&mut self) {
        unsafe {
            UnmapViewOfFile(self.pointer);
        }
    }
}

fn set_junction(path: &Path, target: &Path, attributes_only: bool) -> std::io::Result<()> {
    let target = fs::canonicalize(target)?;
    let target = target.as_os_str().encode_wide().collect::<Vec<_>>();
    let prefix = [92u16, 92, 63, 92];
    assert!(target.starts_with(&prefix));
    let substitute: Vec<_> = [92u16, 63, 63, 92]
        .into_iter()
        .chain(target[4..].iter().copied())
        .collect();
    let mut names = substitute.clone();
    names.push(0);
    let print_offset = (names.len() * 2) as u16;
    names.extend_from_slice(&target);
    names.push(0);
    let mut buffer = Vec::new();
    buffer.extend_from_slice(&0xa0000003u32.to_le_bytes());
    buffer.extend_from_slice(&((8 + names.len() * 2) as u16).to_le_bytes());
    buffer.extend_from_slice(&0u16.to_le_bytes());
    for value in [
        0,
        (substitute.len() * 2) as u16,
        print_offset,
        (target.len() * 2) as u16,
    ] {
        buffer.extend_from_slice(&value.to_le_bytes());
    }
    for value in names {
        buffer.extend_from_slice(&value.to_le_bytes());
    }
    let handle = OpenOptions::new()
        .access_mode(if attributes_only { 0x100 } else { 0x40000000 })
        .share_mode(7)
        .custom_flags(0x02000000 | 0x00200000)
        .open(path)?;
    let mut returned = 0;
    if unsafe {
        DeviceIoControl(
            handle.as_raw_handle(),
            0x000900a4,
            buffer.as_ptr() as *const c_void,
            buffer.len() as u32,
            std::ptr::null_mut(),
            0,
            &mut returned,
            std::ptr::null_mut(),
        )
    } == 0
    {
        return Err(std::io::Error::last_os_error());
    }
    Ok(())
}

fn reparse_boundary(root: &Path, bytes: &[u8], expected: &str) {
    let target = root.join("junction target");
    fs::create_dir(&target).unwrap();
    let alias = root.join("junction alias");
    fs::create_dir(&alias).unwrap();
    set_junction(&alias, &target, false).unwrap();
    assert!(Installer::prepare_in(&alias, bytes, expected, NAME).is_err());
    assert_eq!(fs::read_dir(&target).unwrap().count(), 0);
    fs::remove_dir(&alias).unwrap();
    let held = root.join("held ordinary");
    fs::create_dir(&held).unwrap();
    let guard = Installer::prepare_in(&held, bytes, expected, NAME).unwrap();
    // Sharing does not forbid WRITE_ATTRIBUTES opens. A fixed existing child
    // keeps this directory nonempty, which forbids setting a reparse point.
    assert!(set_junction(&held, &target, true).is_err());
    assert_eq!(
        fs::symlink_metadata(&held).unwrap().file_attributes() & 0x400,
        0
    );
    guard.cleanup().unwrap();
    fs::remove_dir(held).unwrap();
    fs::remove_dir(target).unwrap();
}

fn empty_parent_reparse_race(root: &Path, bytes: &[u8], expected: &str) -> bool {
    let source = root.join("empty source");
    let target = root.join("empty target");
    fs::create_dir(&source).unwrap();
    fs::create_dir(&target).unwrap();
    let changed = Arc::new(Mutex::new(false));
    let observed = changed.clone();
    let destination = target.clone();
    let result =
        Installer::prepare_before_create(&source, bytes, expected, move |path| match set_junction(
            path,
            &destination,
            true,
        ) {
            Ok(()) => {
                *observed.lock().unwrap() = true;
            }
            Err(error) => assert!(
                matches!(error.raw_os_error(), Some(5) | Some(32)),
                "unexpected reparse setup error"
            ),
        });
    assert_eq!(
        fs::read_dir(&target).unwrap().count(),
        0,
        "relative creation traversed the substituted parent name"
    );
    if *changed.lock().unwrap() {
        assert!(result.is_err(), "changed parent was accepted");
        // Preserve both exact owned fixture directories for VM teardown. A
        // created object behind a changed namespace is not deleted by guessing.
        true
    } else {
        result.unwrap().cleanup().unwrap();
        fs::remove_dir(&source).unwrap();
        fs::remove_dir(target).unwrap();
        false
    }
}

#[test]
fn windows_update_installer_peer() {
    if std::env::var("ARKISM_UPDATE_IDENTITY_PEER").as_deref() != Ok("1") {
        return;
    }
    let root = require_hosted();
    let ready = PathBuf::from(std::env::var_os("ARKISM_UPDATE_IDENTITY_READY").unwrap());
    let release = PathBuf::from(std::env::var_os("ARKISM_UPDATE_IDENTITY_RELEASE").unwrap());
    let done = PathBuf::from(std::env::var_os("ARKISM_UPDATE_IDENTITY_DONE").unwrap());
    for path in [&ready, &release, &done] {
        assert!(path.starts_with(&root));
    }
    let count = Installer::inherited_guards_for_test().expect("inherited guard identity mismatch");
    let unrelated: usize = std::env::var("ARKISM_UPDATE_IDENTITY_UNRELATED")
        .unwrap()
        .parse()
        .unwrap();
    assert_eq!(
        unsafe { SetEvent(unrelated as *mut c_void) },
        0,
        "unrelated inheritable event reached child"
    );
    create_new(&ready, b"ready");
    wait_file(&release);
    let image = std::env::current_exe().unwrap();
    let expected = std::env::var("ARKISM_UPDATE_IDENTITY_EXPECTED").unwrap();
    assert_eq!(
        hash(&fs::read(image).unwrap()),
        expected,
        "delayed own-image bytes changed"
    );
    create_new(&done, format!("{{\"self_read\":true,\"inherited_guard_count\":{count},\"unrelated_inherited\":false}}\n").as_bytes());
}

fn locked_file_and_ancestors(root: &Path, bytes: &[u8], expected: &str) {
    let guard = Installer::prepare_in(root, bytes, expected, NAME).unwrap();
    let path = guard.path().to_path_buf();
    assert_eq!(
        OpenOptions::new()
            .write(true)
            .open(&path)
            .unwrap_err()
            .raw_os_error(),
        Some(32)
    );
    assert!(fs::remove_file(&path).is_err());
    assert!(fs::rename(&path, root.join("renamed.exe")).is_err());
    assert!(fs::rename(root, root.with_extension("moved")).is_err());
    guard.cleanup().unwrap();
    assert!(!path.exists());
}

fn replacement_and_writer_conflicts(root: &Path, bytes: &[u8], expected: &str) {
    let observed = Arc::new(Mutex::new(None));
    let path = observed.clone();
    let result = Installer::prepare_interleaved(
        root,
        bytes,
        expected,
        Box::new(move |file| {
            *path.lock().unwrap() = Some(file.to_path_buf());
            fs::rename(file, file.with_extension("original")).unwrap();
            create_new(file, b"unrelated replacement");
        }),
    );
    let error = result.err().expect("replacement was accepted");
    assert!(error
        .causes
        .iter()
        .any(|c| c.operation == "file_identity_changed"));
    assert!(error
        .causes
        .iter()
        .any(|c| c.operation == "cleanup_identity_changed"));
    let path = observed.lock().unwrap().take().unwrap();
    assert_eq!(fs::read(&path).unwrap(), b"unrelated replacement");
    assert_eq!(
        hash(&fs::read(path.with_extension("original")).unwrap()),
        expected
    );
    fs::remove_file(&path).unwrap();
    fs::remove_file(path.with_extension("original")).unwrap();

    let writer = Arc::new(Mutex::new(None));
    let held = writer.clone();
    let observed = Arc::new(Mutex::new(None));
    let path = observed.clone();
    let result = Installer::prepare_interleaved(
        root,
        bytes,
        expected,
        Box::new(move |file| {
            *path.lock().unwrap() = Some(file.to_path_buf());
            *held.lock().unwrap() = Some(OpenOptions::new().write(true).open(file).unwrap());
        }),
    );
    assert!(result
        .err()
        .unwrap()
        .causes
        .iter()
        .any(|c| c.operation == "lock_file"));
    writer.lock().unwrap().take();
    let path = observed.lock().unwrap().take().unwrap();
    assert_eq!(hash(&fs::read(&path).unwrap()), expected);
    fs::remove_file(path).unwrap();
}

fn changed_same_object_and_collision(root: &Path, bytes: &[u8], expected: &str) {
    let observed = Arc::new(Mutex::new(None));
    let path = observed.clone();
    let result = Installer::prepare_interleaved(
        root,
        bytes,
        expected,
        Box::new(move |file| {
            *path.lock().unwrap() = Some(file.to_path_buf());
            let mut changed = bytes_for_mutation();
            changed[0] ^= 1;
            fs::write(file, changed).unwrap();
        }),
    );
    assert!(result
        .err()
        .unwrap()
        .causes
        .iter()
        .any(|c| c.operation == "hash_changed"));
    assert!(!observed.lock().unwrap().as_ref().unwrap().exists());
    let name = "Arkism-update-collision-setup.exe";
    let collision = root.join(name);
    create_new(&collision, b"existing");
    assert!(Installer::prepare_named_for_test(root, bytes, expected, name).is_err());
    assert_eq!(fs::read(&collision).unwrap(), b"existing");
    fs::remove_file(collision).unwrap();
}

fn writable_mapping_transition(
    root: &Path,
    bytes: &[u8],
    expected: &str,
    inputs: &NativeInputs,
) -> &'static str {
    let view = Rc::new(RefCell::new(None));
    let retained = view.clone();
    let staged = Rc::new(RefCell::new(None));
    let observed = staged.clone();
    let length = bytes.len();
    let prepared = Installer::prepare_interleaved(
        root,
        bytes,
        expected,
        Box::new(move |path| {
            *observed.borrow_mut() = Some(path.to_path_buf());
            *retained.borrow_mut() = Some(WritableView::open(path, length));
        }),
    );
    let view = view
        .borrow_mut()
        .take()
        .expect("writable view was not established");
    match prepared {
        Err(error) => {
            assert_eq!(
                error
                    .causes
                    .first()
                    .map(|cause| (cause.operation, cause.code)),
                Some(("lock_file", Some(32))),
                "mapping was rejected without the expected sharing violation"
            );
            drop(view);
            let path = staged.borrow_mut().take().unwrap();
            if path.exists() {
                // This fixture did not replace/rename the object; only its view
                // survived. It is safe to remove this exact owned test file.
                fs::remove_file(path).unwrap();
            }
            "strict_read_rejected"
        }
        Ok(mut guard) => {
            view.mutate(bytes);
            let observation = guard.locked_hash_for_test(bytes.len() as u64, expected);
            let outcome = match observation {
                Err(error)
                    if error.causes.first().map(|cause| cause.operation)
                        == Some("hash_changed") =>
                {
                    "post_hash_mutation_observed"
                }
                Err(_) => "post_hash_observation_failed",
                Ok(()) => "accepted_view_mutation_not_observed",
            };
            // A stale or incoherent ReadFile result does not prove a writable
            // mapping became immutable. Preserve evidence without launching it.
            evidence(inputs, false, outcome, false, true);
            panic!("writable_mapping_transition_unsealed:{outcome}");
        }
    }
}

// An unknown observer is retained until this hosted test process/VM exits. Its
// Drop never kills the child, and an unknown case must stop the entire matrix.
struct PendingObserver(Option<update_installer::Launched>);
impl Drop for PendingObserver {
    fn drop(&mut self) {
        if let Some(observer) = self.0.take() {
            std::mem::forget(observer);
        }
    }
}

fn record_create_failure(index: usize, error: &update_installer::Error) -> bool {
    diagnostic::error(error);
    let cause = error.causes.first();
    let known_no_child =
        cause.is_some_and(|cause| cause.operation == "spawn" && cause.code.is_some());
    case_update(index, |case| {
        case.outcome = if known_no_child {
            "create_failed"
        } else {
            "guard_failed"
        };
        case.operation = if known_no_child {
            "spawn"
        } else {
            "guard_operation"
        };
        case.os_code = cause.and_then(|cause| cause.code);
    });
    known_no_child
}

fn wait_comparison(index: usize, mut pending: PendingObserver) -> bool {
    case_phase(index, "wait");
    match pending.0.as_mut().unwrap().wait(Duration::from_secs(30)) {
        Ok(Some(code)) => {
            case_update(index, |case| {
                case.terminal = true;
                case.exit_code = Some(code);
            });
            drop(pending.0.take());
            code == 0
        }
        Ok(None) => {
            case_update(index, |case| case.outcome = "wait_timeout");
            panic!("comparison_live_owner_matrix_stopped");
        }
        Err(error) => {
            diagnostic::error(&error);
            case_update(index, |case| {
                case.outcome = "wait_unknown";
                case.operation = "wait";
                case.os_code = error.causes.first().and_then(|cause| cause.code);
            });
            panic!("comparison_unknown_owner_matrix_stopped");
        }
    }
}

fn payload_matches(output: &Path) -> bool {
    let bytes = fs::read(output.join("embedded.fixture"));
    let count = fs::read_dir(output).map(|entries| entries.collect::<Result<Vec<_>, _>>());
    matches!(bytes, Ok(bytes) if bytes == b"Arkism inert installer fixture.\n")
        && matches!(count, Ok(Ok(entries)) if entries.len() == 1)
}

fn comparison_case(index: usize, parent: &Path, inputs: &NativeInputs) {
    let (target, dos) = MATRIX[index];
    let (bytes, expected) = match index {
        0 | 1 => (&inputs.managed[index].bytes, &inputs.managed[index].hash),
        _ => (&inputs.nsis_bytes, &inputs.nsis_hash),
    };
    case_update(index, |case| {
        case.image_hash = expected.clone();
        case.manifest_verified = index < 2;
        case.outcome = "in_progress";
    });
    diagnostic::target(target);
    case_phase(index, "prepare");
    // All four owners have the same fixed prefix and 128-bit hex nonce length.
    let root = owned_root(parent);
    let mut guard = checked(Installer::prepare_in(&root, bytes, expected, NAME));
    if dos {
        checked(guard.select_physical_dos_for_test(bytes.len() as u64, expected));
    }
    let staged = guard.path().to_path_buf();
    let output = root.join("inert NSIS output [owned]");
    fs::create_dir(&output).expect("comparison_output_create");
    let env = environment(&[(
        "ARKISM_INSTALLER_FIXTURE_OUTPUT",
        output.clone().into_os_string(),
    )]);
    case_phase(index, "create_process");
    let observer = match guard.spawn_inert(&[OsString::from("/S")], &env) {
        Ok(observer) => PendingObserver(Some(observer)),
        Err(error) => {
            // No cleanup/retry: preserve this exact failed case and original code.
            // A recorded failed CreateProcess has no child and allows the next
            // separately declared case. Every other uncertainty ends the matrix.
            assert!(
                record_create_failure(index, &error),
                "comparison_guard_matrix_stopped"
            );
            return;
        }
    };
    case_update(index, |case| case.created = true);
    drop(guard);
    if !wait_comparison(index, observer) {
        case_update(index, |case| case.outcome = "exit_failed");
        return;
    }
    case_phase(index, "payload");
    if !payload_matches(&output) {
        case_update(index, |case| case.outcome = "payload_failed");
        return;
    }
    case_update(index, |case| case.outcome = "payload_verified");
    if index == 2 {
        REPORT.with(|report| report.borrow_mut().nsis = true);
    }
    // Successful inert-only cases remove their exact files after terminal exit.
    // Failed/unknown cases retain their complete owners for VM teardown.
    case_phase(index, "cleanup");
    fs::remove_file(output.join("embedded.fixture")).expect("comparison_payload_cleanup");
    fs::remove_dir(output).expect("comparison_output_cleanup");
    fs::remove_file(staged).expect("comparison_image_cleanup");
    fs::remove_dir(root).expect("comparison_root_cleanup");
    case_update(index, |case| {
        case.outcome = "passed";
        case.phase = "complete";
    });
}

fn architecture_namespace_matrix(parent: &Path, inputs: &NativeInputs) {
    for index in 0..MATRIX.len() {
        comparison_case(index, parent, inputs);
    }
    let passed = REPORT.with(|report| {
        report
            .borrow()
            .cases
            .iter()
            .all(|case| case.outcome == "passed")
    });
    diagnostic::phase("comparison_required");
    assert!(passed, "comparison_required_case_failed");
}

fn bytes_for_mutation() -> Vec<u8> {
    fs::read(std::env::current_exe().unwrap()).unwrap()
}

fn inherited_lifetime(root: &Path, bytes: &[u8], expected: &str) {
    diagnostic::phase("prepare");
    let mut guard = checked(Installer::prepare_in(root, bytes, expected, NAME));
    let path = guard.path().to_path_buf();
    let ready = root.join("ready");
    let release = root.join("release");
    let done = root.join("done");
    let security = Security {
        length: std::mem::size_of::<Security>() as u32,
        descriptor: std::ptr::null_mut(),
        inherit: 1,
    };
    let raw = unsafe { CreateEventW(&security, 1, 0, std::ptr::null()) };
    assert!(!raw.is_null());
    let event = unsafe { OwnedHandle::from_raw_handle(raw) };
    let env = environment(&[
        ("ARKISM_UPDATE_IDENTITY_PEER", "1".into()),
        (
            "ARKISM_UPDATE_IDENTITY_READY",
            ready.clone().into_os_string(),
        ),
        (
            "ARKISM_UPDATE_IDENTITY_RELEASE",
            release.clone().into_os_string(),
        ),
        ("ARKISM_UPDATE_IDENTITY_DONE", done.clone().into_os_string()),
        ("ARKISM_UPDATE_IDENTITY_EXPECTED", expected.into()),
        (
            "ARKISM_UPDATE_IDENTITY_UNRELATED",
            (raw as usize).to_string().into(),
        ),
    ]);
    let args = ["--exact", "windows_update_installer_peer", "--nocapture"].map(OsString::from);
    diagnostic::phase("create_process");
    let mut process = checked(guard.spawn_inert(&args, &env));
    // Parent originals and temporary inheritable duplicates are all closed now.
    drop(guard);
    wait_file(&ready);
    assert_eq!(
        unsafe { WaitForSingleObject(event.as_raw_handle(), 0) },
        258
    );
    assert_eq!(
        OpenOptions::new()
            .write(true)
            .open(&path)
            .unwrap_err()
            .raw_os_error(),
        Some(32)
    );
    assert!(fs::remove_file(&path).is_err());
    assert!(fs::rename(&path, root.join("renamed-child.exe")).is_err());
    assert!(fs::rename(root, root.with_extension("moved-child")).is_err());
    create_new(&release, b"release");
    diagnostic::phase("wait");
    assert_eq!(checked(process.wait(Duration::from_secs(30))), Some(0));
    diagnostic::phase("payload");
    assert!(
        done.exists(),
        "peer completion was absent when process wait returned"
    );
    let observed = fs::read_to_string(&done).unwrap();
    assert!(
        observed.contains("\"self_read\":true")
            && observed.contains("\"unrelated_inherited\":false")
    );
    assert_eq!(hash(&fs::read(&path).unwrap()), expected);
    fs::remove_file(path).unwrap();
    for file in [ready, release, done] {
        fs::remove_file(file).unwrap();
    }
}

fn invalid_image_launch_preserves_cleanup_contract(root: &Path) {
    diagnostic::target("invalid_image");
    diagnostic::phase("prepare");
    let mut guard = Installer::prepare_in(
        root,
        b"not an executable",
        &hash(b"not an executable"),
        NAME,
    )
    .unwrap();
    let path = guard.path().to_path_buf();
    assert!(guard
        .spawn()
        .err()
        .unwrap()
        .causes
        .iter()
        .any(|c| c.operation == "spawn"));
    guard.cleanup().unwrap();
    assert!(!path.exists());
}

#[test]
#[ignore = "requires explicit opt-in on a disposable GitHub-hosted Windows runner"]
fn windows_update_installer_identity() {
    let parent = require_hosted();
    REPORT.with(|report| {
        *report.borrow_mut() = Report {
            source: safe_hex_environment("ARKISM_WINDOWS_INSTALLER_IDENTITY_SOURCE_SHA", 40)
                .unwrap_or_else(|| "unavailable".to_string()),
            nsis_hash: safe_hex_environment("ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_SHA256", 64)
                .unwrap_or_else(|| "unavailable".to_string()),
            compiler_hash: safe_hex_environment(
                "ARKISM_WINDOWS_INSTALLER_IDENTITY_NSIS_COMPILER_SHA256",
                64,
            ),
            mapping: "not_reached",
            cases: MATRIX
                .iter()
                .map(|(target, dos)| CaseRecord::declared(target, *dos))
                .collect(),
            ..Report::default()
        };
    });
    preserve_failure(
        || {
            run_identity_fixture(&parent);
            assert!(
                REPORT.with(|report| report.borrow().complete),
                "identity_fixture_incomplete"
            );
            assert!(diagnostic::healthy(), "identity_diagnostic_overflow");
        },
        write_evidence,
    );
}

fn preserve_failure(body: impl FnOnce(), writer: impl FnOnce(bool) -> std::io::Result<()>) {
    let execution = catch_unwind(AssertUnwindSafe(body));
    let written = catch_unwind(AssertUnwindSafe(|| writer(execution.is_ok())));
    match execution {
        Err(original) => resume_unwind(original),
        Ok(()) => match written {
            Err(failure) => resume_unwind(failure),
            Ok(Err(_)) => panic!("identity_evidence_write_failed"),
            Ok(Ok(())) => (),
        },
    }
}

fn run_identity_fixture(parent: &Path) {
    diagnostic::phase("inputs");
    let inputs = NativeInputs::load(parent);
    evidence(&inputs, false, "not_reached", false, false);
    let root = owned_root(parent);
    REPORT.with(|report| report.borrow_mut().preserved = true);
    let bytes = bytes_for_mutation();
    assert!(!bytes.is_empty() && bytes.len() as u64 <= update_installer::MAX_BYTES);
    let expected = hash(&bytes);
    diagnostic::phase("mapping");
    let mapping = writable_mapping_transition(&root, &bytes, &expected, &inputs);
    evidence(&inputs, false, mapping, false, true);
    diagnostic::phase("file_guards");
    locked_file_and_ancestors(&root, &bytes, &expected);
    diagnostic::phase("replacement");
    replacement_and_writer_conflicts(&root, &bytes, &expected);
    diagnostic::phase("same_object");
    changed_same_object_and_collision(&root, &bytes, &expected);
    diagnostic::phase("reparse");
    reparse_boundary(&root, &bytes, &expected);
    invalid_image_launch_preserves_cleanup_contract(&root);
    diagnostic::target("rust_peer");
    inherited_lifetime(&root, &bytes, &expected);
    REPORT.with(|report| report.borrow_mut().rust_peer = true);
    let long = root.join(format!("long {}", "a".repeat(170)));
    fs::create_dir(&long).unwrap();
    assert!(long.as_os_str().encode_wide().count() + 64 > 260);
    diagnostic::target("rust_peer_long");
    inherited_lifetime(&long, &bytes, &expected);
    REPORT.with(|report| report.borrow_mut().rust_peer_long = true);
    fs::remove_dir(long).unwrap();
    architecture_namespace_matrix(parent, &inputs);
    diagnostic::target("other");
    diagnostic::phase("empty_parent");
    let preserved = empty_parent_reparse_race(&root, &bytes, &expected);
    if preserved {
        let mut names: Vec<_> = fs::read_dir(&root)
            .unwrap()
            .map(|entry| entry.unwrap().file_name())
            .collect();
        names.sort();
        assert_eq!(
            names,
            [
                OsString::from("empty source"),
                OsString::from("empty target")
            ]
        );
    } else {
        assert_eq!(fs::read_dir(&root).unwrap().count(), 0);
        fs::remove_dir(&root).unwrap();
    }
    evidence(&inputs, true, mapping, true, preserved);
    diagnostic::phase("complete");
    println!("inert installer identity: locked bytes, replacement, writer conflict, exact cleanup, inherited lifetime and selective inheritance passed");
}
