//! Opt-in inert native coverage; never run production roles or user data here.
//! The copied module unit tests and cross compilation are not native evidence.
#![cfg(windows)]

#[path = "../src/desktop_control.rs"]
pub mod desktop_control;
#[path = "../src/windows_job.rs"]
pub mod windows_job;

use desktop_control::{Receipt, Role, State};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::ffi::{OsStr, OsString};
use std::fs::{self, File, OpenOptions};
use std::io::{Read, Write};
use std::os::windows::ffi::OsStrExt;
use std::os::windows::fs::MetadataExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use windows_job::{JobChild, LaunchSpec, Stderr};

const PREFIX: &str = "ARKISM_WINDOWS_RUNTIME_CONTROL_";
const KIND: &str = "arkism_runtime_control_v1";
type Check<T> = Result<T, &'static str>;

fn require(condition: bool, code: &'static str) -> Check<()> {
    if condition {
        Ok(())
    } else {
        Err(code)
    }
}

fn fixed_env(name: &str) -> Check<String> {
    std::env::var(name).map_err(|_| "fixture_environment_missing")
}

fn hex_string(value: &str, length: usize) -> bool {
    value.len() == length
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn ordinary(path: &Path, directory: bool) -> Check<()> {
    let info = fs::symlink_metadata(path).map_err(|_| "fixture_path_inspection")?;
    require(
        info.file_attributes() & 0x400 == 0 && !info.file_type().is_symlink(),
        "fixture_reparse",
    )?;
    require(
        if directory {
            info.is_dir()
        } else {
            info.is_file()
        },
        "fixture_path_type",
    )
}

fn directory_chain(path: &Path) -> Check<()> {
    require(path.is_absolute(), "fixture_absolute_path")?;
    for ancestor in path.ancestors() {
        ordinary(ancestor, true)?;
    }
    Ok(())
}

fn absent(path: &Path) -> Check<()> {
    match fs::symlink_metadata(path) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        _ => Err("fixture_path_not_fresh"),
    }
}

fn within(path: &Path, root: &Path) -> bool {
    path != root && path.starts_with(root)
}

fn hash_file(path: &Path, max: u64) -> Check<String> {
    ordinary(path, false)?;
    let file = File::open(path).map_err(|_| "fixture_hash_open")?;
    require(
        file.metadata().map_err(|_| "fixture_hash_stat")?.len() <= max,
        "fixture_hash_limit",
    )?;
    let mut input = file.take(max + 1);
    let mut hasher = Sha256::new();
    let mut buffer = [0u8; 8192];
    let mut count = 0u64;
    loop {
        let read = input.read(&mut buffer).map_err(|_| "fixture_hash_read")?;
        if read == 0 {
            break;
        }
        count += read as u64;
        require(count <= max, "fixture_hash_limit")?;
        hasher.update(&buffer[..read]);
    }
    Ok(hasher
        .finalize()
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect())
}

fn create_file(path: &Path, bytes: &[u8]) -> Check<()> {
    directory_chain(path.parent().ok_or("fixture_parent_missing")?)?;
    absent(path)?;
    let mut file = OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)
        .map_err(|_| "fixture_create")?;
    file.write_all(bytes).map_err(|_| "fixture_write")
}

fn read_small(path: &Path) -> Check<Vec<u8>> {
    ordinary(path, false)?;
    let file = File::open(path).map_err(|_| "fixture_read")?;
    let mut bytes = Vec::new();
    file.take(65537)
        .read_to_end(&mut bytes)
        .map_err(|_| "fixture_read")?;
    require(bytes.len() <= 65536, "fixture_read_limit")?;
    Ok(bytes)
}

struct Hosted {
    source: String,
    runner_temp: PathBuf,
}

fn hosted() -> Check<Hosted> {
    require(
        fixed_env("GITHUB_ACTIONS")? == "true"
            && fixed_env("RUNNER_ENVIRONMENT")? == "github-hosted"
            && fixed_env("RUNNER_OS")? == "Windows"
            && fixed_env(&format!("{PREFIX}NATIVE"))? == "1",
        "fixture_hosted_opt_in",
    )?;
    let source = fixed_env("GITHUB_SHA")?;
    require(
        hex_string(&source, 40) && fixed_env(&format!("{PREFIX}SOURCE_SHA"))? == source,
        "fixture_source_binding",
    )?;
    let temp = PathBuf::from(fixed_env("RUNNER_TEMP")?);
    directory_chain(&temp)?;
    Ok(Hosted {
        source,
        runner_temp: fs::canonicalize(temp).map_err(|_| "fixture_temp_canonical")?,
    })
}

fn owner(root: &Path, context: &Hosted) -> Check<()> {
    directory_chain(root)?;
    let canonical = fs::canonicalize(root).map_err(|_| "fixture_root_canonical")?;
    require(
        within(&canonical, &context.runner_temp),
        "fixture_root_scope",
    )?;
    let expected = json!({"kind":KIND,"source":context.source});
    let actual: Value = serde_json::from_slice(&read_small(&root.join("owner.json"))?)
        .map_err(|_| "fixture_owner_schema")?;
    require(actual == expected, "fixture_owner_changed")
}

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Expected {
    arguments: Vec<Vec<u16>>,
    environment: Vec<(Vec<u16>, Vec<u16>)>,
    directory: Vec<u16>,
}

fn wide(value: &OsStr) -> Vec<u16> {
    value.encode_wide().collect()
}
fn environment_wide(env: &[(OsString, OsString)]) -> Vec<(Vec<u16>, Vec<u16>)> {
    let mut env = env
        .iter()
        .map(|(key, value)| (wide(key), wide(value)))
        .collect::<Vec<_>>();
    env.sort();
    env
}

fn child_arguments() -> Vec<OsString> {
    [
        "--ignored",
        "--exact",
        "inert_child_entry",
        "--nocapture",
        "--test-threads=1",
        "--",
        "",
        "two words",
        "quote\"tail",
        "C:\\tail\\",
        "wide-한글-😀",
        "& ; $(literal)",
    ]
    .into_iter()
    .map(OsString::from)
    .collect()
}

fn expected_file(
    root: &Path,
    name: &str,
    exe: &Path,
    args: &[OsString],
    env: &[(OsString, OsString)],
    cwd: &Path,
) -> Check<()> {
    let mut arguments = vec![wide(exe.as_os_str())];
    arguments.extend(args.iter().map(|s| wide(s)));
    let directory = fs::canonicalize(cwd).map_err(|_| "fixture_directory_canonical")?;
    let value = Expected {
        arguments,
        environment: environment_wide(env),
        directory: wide(directory.as_os_str()),
    };
    create_file(
        &root.join(name),
        &serde_json::to_vec(&value).map_err(|_| "fixture_expectation_encode")?,
    )
}

fn verify_child_environment(root: &Path) -> Check<()> {
    let name = fixed_env(&format!("{PREFIX}EXPECTATION"))?;
    require(
        matches!(
            name.as_str(),
            "expect_control.json" | "expect_tree.json" | "expect_grandchild.json"
        ),
        "fixture_expectation_name",
    )?;
    let expected: Expected = serde_json::from_slice(&read_small(&root.join(name))?)
        .map_err(|_| "fixture_expectation_schema")?;
    let actual_args = std::env::args_os().map(|s| wide(&s)).collect::<Vec<_>>();
    require(
        actual_args == expected.arguments,
        "fixture_argument_mismatch",
    )?;
    let actual_env = environment_wide(&std::env::vars_os().collect::<Vec<_>>());
    require(
        actual_env == expected.environment,
        "fixture_environment_mismatch",
    )?;
    let directory =
        fs::canonicalize(std::env::current_dir().map_err(|_| "fixture_current_directory")?)
            .map_err(|_| "fixture_directory_canonical")?;
    require(
        wide(directory.as_os_str()) == expected.directory,
        "fixture_directory_mismatch",
    )
}

fn wait_file(root: &Path, name: &str, context: &Hosted, budget: Duration) -> Check<()> {
    let start = Instant::now();
    while start.elapsed() < budget {
        owner(root, context)?;
        match fs::symlink_metadata(root.join(name)) {
            Ok(_) => return ordinary(&root.join(name), false),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(_) => return Err("fixture_marker_inspection"),
        }
        std::thread::sleep(Duration::from_millis(5));
    }
    Err("fixture_marker_timeout")
}

fn child_run() -> Check<()> {
    let context = hosted()?;
    let root = PathBuf::from(fixed_env(&format!("{PREFIX}ROOT"))?);
    owner(&root, &context)?;
    verify_child_environment(&root)?;
    match fixed_env(&format!("{PREFIX}CHILD"))?.as_str() {
        "control" => {
            create_file(&root.join("control_ready"), b"ok")?;
            wait_file(&root, "control_release", &context, Duration::from_secs(30))
        }
        "grandchild" => {
            create_file(&root.join("grandchild_ready"), b"ok")?;
            wait_file(
                &root,
                "grandchild_release",
                &context,
                Duration::from_secs(30),
            )
        }
        "tree" => {
            let exe = std::env::current_exe().map_err(|_| "fixture_current_image")?;
            let args = child_arguments();
            let mut env = std::env::vars_os().collect::<Vec<_>>();
            env.retain(|(key, _)| {
                key != &OsString::from(format!("{PREFIX}CHILD"))
                    && key != &OsString::from(format!("{PREFIX}EXPECTATION"))
            });
            env.push((format!("{PREFIX}CHILD").into(), "grandchild".into()));
            env.push((
                format!("{PREFIX}EXPECTATION").into(),
                "expect_grandchild.json".into(),
            ));
            let cwd = std::env::current_dir().map_err(|_| "fixture_current_directory")?;
            expected_file(&root, "expect_grandchild.json", &exe, &args, &env, &cwd)?;
            // Ordinary CreateProcess descendants inherit the already owned Job;
            // no shell, breakaway flag, service, or detached broker is involved.
            let _child = Command::new(exe)
                .args(args)
                .env_clear()
                .envs(env)
                .current_dir(cwd)
                .stdin(Stdio::null())
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .spawn()
                .map_err(|_| "fixture_grandchild_spawn")?;
            create_file(&root.join("tree_arguments_env_cwd_exact"), b"ok")
        }
        _ => Err("fixture_child_kind"),
    }
}

#[test]
#[ignore = "inert self-execution entry; parent supplies the guarded child environment"]
fn inert_child_entry() {
    // Running all ignored tests without the internal child selector has no effect.
    if std::env::var_os(format!("{PREFIX}CHILD")).is_none() {
        return;
    }
    if let Err(code) = child_run() {
        if let Ok(context) = hosted() {
            if let Ok(root) = fixed_env(&format!("{PREFIX}ROOT")) {
                let root = PathBuf::from(root);
                if owner(&root, &context).is_ok() {
                    if let Ok(bytes) = serde_json::to_vec(&json!({"code":code})) {
                        let _ = create_file(&root.join("child_failure.json"), &bytes);
                    }
                }
            }
        }
        panic!("{code}");
    }
}

struct TrackedJob {
    job: JobChild,
    closed: bool,
}
struct Fixture {
    context: Hosted,
    root: PathBuf,
    exe: PathBuf,
    exe_hash: String,
    peer: PathBuf,
    peer_hash: String,
    jobs: Vec<TrackedJob>,
    diagnostics: Vec<Value>,
}

impl Fixture {
    fn new(context: Hosted, peer: PathBuf, peer_hash: String) -> Check<Self> {
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_err(|_| "fixture_clock")?
            .as_nanos();
        let root = context.runner_temp.join(format!(
            "arkism-runtime-control-{}-{now}",
            std::process::id()
        ));
        absent(&root)?;
        fs::create_dir(&root).map_err(|_| "fixture_root_create")?;
        create_file(
            &root.join("owner.json"),
            &serde_json::to_vec(&json!({"kind":KIND,"source":context.source}))
                .map_err(|_| "fixture_owner_encode")?,
        )?;
        fs::create_dir(root.join("working directory")).map_err(|_| "fixture_directory_create")?;
        let exe = std::env::current_exe().map_err(|_| "fixture_current_image")?;
        directory_chain(exe.parent().ok_or("fixture_image_parent")?)?;
        let exe_hash = hash_file(&exe, 128 << 20)?;
        Ok(Self {
            context,
            root,
            exe,
            exe_hash,
            peer,
            peer_hash,
            jobs: Vec::new(),
            diagnostics: Vec::new(),
        })
    }

    fn environment(&self) -> Check<Vec<(OsString, OsString)>> {
        owner(&self.root, &self.context)?;
        Ok(vec![
            ("SystemRoot".into(), fixed_env("SystemRoot")?.into()),
            ("TEMP".into(), self.root.as_os_str().to_owned()),
            ("TMP".into(), self.root.as_os_str().to_owned()),
            ("GITHUB_ACTIONS".into(), "true".into()),
            ("RUNNER_ENVIRONMENT".into(), "github-hosted".into()),
            ("RUNNER_OS".into(), "Windows".into()),
            ("GITHUB_SHA".into(), self.context.source.clone().into()),
            (
                "RUNNER_TEMP".into(),
                self.context.runner_temp.as_os_str().to_owned(),
            ),
            (format!("{PREFIX}NATIVE").into(), "1".into()),
            (
                format!("{PREFIX}SOURCE_SHA").into(),
                self.context.source.clone().into(),
            ),
            (
                format!("{PREFIX}ROOT").into(),
                self.root.as_os_str().to_owned(),
            ),
            (
                "ARKISM_FIXTURE_VALUE_Ü".into(),
                "synthetic value with spaces".into(),
            ),
        ])
    }

    fn spec(
        &self,
        exe: PathBuf,
        args: Vec<OsString>,
        env: Vec<(OsString, OsString)>,
    ) -> Check<LaunchSpec> {
        owner(&self.root, &self.context)?;
        require(
            hash_file(&self.exe, 128 << 20)? == self.exe_hash,
            "fixture_test_image_changed",
        )?;
        require(
            hash_file(&self.peer, 128 << 20)? == self.peer_hash,
            "fixture_peer_image_changed",
        )?;
        Ok(LaunchSpec {
            executable: exe,
            arguments: args,
            environment: env,
            directory: self.root.join("working directory"),
            stderr: Stderr::File(
                OpenOptions::new()
                    .write(true)
                    .open("NUL")
                    .map_err(|_| "fixture_stderr")?,
            ),
        })
    }

    fn spawn(
        &mut self,
        exe: PathBuf,
        args: Vec<OsString>,
        env: Vec<(OsString, OsString)>,
    ) -> Check<usize> {
        let spec = self.spec(exe, args, env)?;
        match JobChild::spawn(spec) {
            Ok(job) => {
                let index = self.jobs.len();
                self.jobs.push(TrackedJob { job, closed: false });
                Ok(index)
            }
            Err(mut failure) => {
                self.diagnostics
                    .push(json!({"stage":"spawn","causes":native_causes(&failure.error)}));
                if let Some(job) = failure.child.take() {
                    self.jobs.push(TrackedJob { job, closed: false });
                }
                Err("fixture_native_spawn")
            }
        }
    }

    fn spawn_self(&mut self, kind: &str, expectation: &str) -> Check<usize> {
        let mut env = self.environment()?;
        env.push((format!("{PREFIX}CHILD").into(), kind.into()));
        env.push((format!("{PREFIX}EXPECTATION").into(), expectation.into()));
        let args = child_arguments();
        expected_file(
            &self.root,
            expectation,
            &self.exe,
            &args,
            &env,
            &self.root.join("working directory"),
        )?;
        self.spawn(self.exe.clone(), args, env)
    }

    fn terminate(&mut self, index: usize) -> Check<()> {
        let tracked = &mut self.jobs[index];
        let empty = match tracked.job.terminate_and_wait(Duration::from_secs(5)) {
            Ok(empty) => empty,
            Err(error) => {
                self.diagnostics
                    .push(json!({"stage":"terminate","causes":native_causes(&error)}));
                return Err("fixture_terminate_wait");
            }
        };
        require(
            empty.active_processes == 0 && tracked.job.status().empty_confirmed,
            "fixture_not_empty",
        )?;
        tracked.job.close_empty().map_err(|_| "fixture_close")?;
        tracked.closed = true;
        Ok(())
    }

    fn close_stopped(&mut self, index: usize) -> Check<()> {
        let tracked = &mut self.jobs[index];
        require(
            tracked
                .job
                .wait_empty(Duration::from_secs(5))
                .map_err(|_| "fixture_stopped_not_empty")?
                .active_processes
                == 0,
            "fixture_stopped_not_empty",
        )?;
        require(
            tracked
                .job
                .root_exit_code()
                .map_err(|_| "fixture_exit_query")?
                == Some(0),
            "fixture_root_exit_nonzero",
        )?;
        tracked.job.close_empty().map_err(|_| "fixture_close")?;
        tracked.closed = true;
        Ok(())
    }

    fn cleanup(&mut self) -> Check<()> {
        let mut failed = false;
        for index in 0..self.jobs.len() {
            if !self.jobs[index].closed && self.terminate(index).is_err() {
                failed = true;
            }
        }
        require(!failed, "fixture_cleanup_job_unknown")?;
        owner(&self.root, &self.context)?;
        let mut count = 0;
        check_tree(&self.root, 0, &mut count)?;
        fs::remove_dir_all(&self.root).map_err(|_| "fixture_owned_cleanup")
    }
}

fn native_causes(error: &windows_job::JobError) -> Vec<Value> {
    error
        .causes
        .iter()
        .map(|cause| json!({"operation":cause.operation,"windows_code":cause.windows_code}))
        .collect()
}

fn child_diagnostic(root: &Path) -> Option<&'static str> {
    let bytes = read_small(&root.join("child_failure.json")).ok()?;
    let row: Value = serde_json::from_slice(&bytes).ok()?;
    Some(match row["code"].as_str() {
        Some("fixture_argument_mismatch") => "argument_mismatch",
        Some("fixture_environment_mismatch") => "environment_mismatch",
        Some("fixture_directory_mismatch") => "directory_mismatch",
        Some("fixture_grandchild_spawn") => "grandchild_spawn",
        Some("fixture_marker_timeout") => "marker_timeout",
        _ => "other_child_failure",
    })
}

fn control_result<T>(
    result: Result<T, desktop_control::Error>,
    report: &mut Value,
    phase: &'static str,
) -> Check<T> {
    result.map_err(|error| {
        report["control_error"] =
            json!({"code":format!("{:?}",error.code),"windows_code":error.windows_code});
        phase
    })
}

fn environment_case(fixture: &mut Fixture, report: &mut Value) -> Check<()> {
    for (key_a, key_b) in [("ARKISM_CASE", "arkism_case"), ("ARKISM_Ä", "ARKISM_ä")] {
        let mut env = fixture.environment()?;
        env.push((key_a.into(), "first".into()));
        env.push((key_b.into(), "second".into()));
        let spec = fixture.spec(fixture.exe.clone(), child_arguments(), env)?;
        match JobChild::spawn(spec) {
            Ok(job) => {
                fixture.jobs.push(TrackedJob { job, closed: false });
                return Err("fixture_duplicate_environment_launched");
            }
            Err(mut failure) => {
                let rejected = failure.child.is_none()
                    && failure
                        .error
                        .causes
                        .iter()
                        .any(|cause| cause.operation == "duplicate_environment_key");
                if let Some(job) = failure.child.take() {
                    fixture.jobs.push(TrackedJob { job, closed: false });
                }
                require(rejected, "fixture_duplicate_environment_not_rejected")?;
            }
        }
    }
    report["ascii_and_unicode_environment_duplicates_rejected_before_spawn"] = json!(true);
    Ok(())
}

fn check_tree(path: &Path, depth: usize, count: &mut usize) -> Check<()> {
    *count += 1;
    require(depth <= 8 && *count <= 128, "fixture_cleanup_limit")?;
    let info = fs::symlink_metadata(path).map_err(|_| "fixture_cleanup_inspection")?;
    require(
        info.file_attributes() & 0x400 == 0 && !info.file_type().is_symlink(),
        "fixture_cleanup_reparse",
    )?;
    if info.is_dir() {
        for item in fs::read_dir(path).map_err(|_| "fixture_cleanup_read")? {
            check_tree(
                &item.map_err(|_| "fixture_cleanup_read")?.path(),
                depth + 1,
                count,
            )?;
        }
    } else {
        require(
            info.is_file() && info.len() <= 65536,
            "fixture_cleanup_type",
        )?;
    }
    Ok(())
}

fn wait_root_exit(job: &mut JobChild, stdout: &mut File) -> Check<usize> {
    let start = Instant::now();
    let mut captured = 0usize;
    let mut buffer = [0u8; 512];
    while start.elapsed() < Duration::from_secs(5) {
        match desktop_control::try_read_pipe(stdout, &mut buffer)
            .map_err(|_| "fixture_capture_read")?
        {
            desktop_control::ReadStep::Data(count) => {
                captured += count;
                require(captured <= 16384, "fixture_capture_limit")?;
            }
            desktop_control::ReadStep::Pending | desktop_control::ReadStep::End => {}
        }
        if let Some(code) = job.root_exit_code().map_err(|_| "fixture_exit_query")? {
            require(code == 0, "fixture_root_exit_nonzero")?;
            return Ok(captured);
        }
        std::thread::sleep(Duration::from_millis(5));
    }
    Err("fixture_root_exit_timeout")
}

fn tree_case(fixture: &mut Fixture, report: &mut Value) -> Check<()> {
    let control = fixture.spawn_self("control", "expect_control.json")?;
    wait_file(
        &fixture.root,
        "control_ready",
        &fixture.context,
        Duration::from_secs(5),
    )?;
    let tree = fixture.spawn_self("tree", "expect_tree.json")?;
    let mut stdout = fixture.jobs[tree]
        .job
        .take_stdout()
        .ok_or("fixture_capture_missing")?;
    let captured = wait_root_exit(&mut fixture.jobs[tree].job, &mut stdout)?;
    report["root_output_observed_bytes"] = json!(captured);
    wait_file(
        &fixture.root,
        "grandchild_ready",
        &fixture.context,
        Duration::from_secs(5),
    )?;
    wait_file(
        &fixture.root,
        "tree_arguments_env_cwd_exact",
        &fixture.context,
        Duration::from_secs(5),
    )?;
    let live = fixture.jobs[tree]
        .job
        .query()
        .map_err(|_| "fixture_tree_accounting")?;
    require(
        live.active_processes > 0 && live.total_processes >= 2,
        "fixture_tree_not_live",
    )?;
    report["tree_total_processes"] = json!(live.total_processes);
    report["root_exited_with_live_grandchild"] = json!(true);
    report["creation_time_native_job_core_completed"] = json!(true);
    let error = fixture.jobs[tree]
        .job
        .wait_empty(Duration::from_millis(50))
        .err()
        .ok_or("fixture_wait_should_timeout")?;
    require(
        error
            .causes
            .iter()
            .any(|cause| cause.operation == "wait_timeout"),
        "fixture_wait_wrong_error",
    )?;
    require(
        fixture.jobs[tree]
            .job
            .query()
            .map_err(|_| "fixture_tree_accounting")?
            .active_processes
            > 0,
        "fixture_timeout_killed_job",
    )?;
    report["wait_timeout_non_destructive"] = json!(true);
    fixture.terminate(tree)?;
    require(
        fixture.jobs[control]
            .job
            .root_exit_code()
            .map_err(|_| "fixture_control_query")?
            .is_none()
            && fixture.jobs[control]
                .job
                .query()
                .map_err(|_| "fixture_control_query")?
                .active_processes
                > 0,
        "fixture_control_not_preserved",
    )?;
    report["separate_control_survived_termination"] = json!(true);
    report["tree_termination_empty_confirmed"] = json!(true);
    report["exact_arguments_environment_directory"] = json!(true);
    fixture.terminate(control)
}

fn nonce() -> Check<String> {
    let mut bytes = [0u8; 32];
    getrandom::fill(&mut bytes).map_err(|_| "fixture_nonce")?;
    Ok(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
}

fn peer_case(fixture: &mut Fixture, mode: &str, generation: u64, report: &mut Value) -> Check<()> {
    let nonce = nonce()?;
    let mut env = fixture.environment()?;
    env.extend(
        desktop_control::bootstrap_environment(Role::Gateway, generation, &nonce)
            .map_err(|_| "fixture_bootstrap")?
            .into_iter()
            .map(|(key, value)| (key.into(), value.into())),
    );
    env.push((format!("{PREFIX}PEER_MODE").into(), mode.into()));
    let index = fixture.spawn(fixture.peer.clone(), Vec::new(), env)?;
    let mut session = match desktop_control::for_job(
        &mut fixture.jobs[index].job,
        Role::Gateway,
        generation,
        nonce,
    ) {
        Ok(session) => session,
        Err(failure) => {
            report["control_error"] = json!({"code":format!("{:?}",failure.error.code),"windows_code":failure.error.windows_code});
            let _owned_failure = failure;
            return Err("fixture_pipe_connect");
        }
    };
    if mode == "silent" {
        require(
            session
                .ready(Duration::from_millis(50))
                .err()
                .map(|error| error.code)
                == Some(desktop_control::Code::Timeout),
            "fixture_pipe_timeout_expected",
        )?;
        require(
            session.state() == State::Unverified
                && fixture.jobs[index]
                    .job
                    .root_exit_code()
                    .map_err(|_| "fixture_silent_query")?
                    .is_none(),
            "fixture_pipe_timeout_changed_owner",
        )?;
        report["pipe_timeout_retains_unverified_live_owner"] = json!(true);
        fixture.terminate(index)?;
        drop(session);
        return Ok(());
    }
    require(
        control_result(
            session.ready(Duration::from_secs(5)),
            report,
            "fixture_go_ready",
        )? == Receipt::Ready,
        "fixture_go_ready",
    )?;
    // A real Go bufio reader must remain blocked on its distinct stdin endpoint.
    std::thread::sleep(Duration::from_millis(100));
    require(
        fixture.jobs[index]
            .job
            .root_exit_code()
            .map_err(|_| "fixture_go_alive")?
            .is_none(),
        "fixture_go_reader_not_blocking",
    )?;
    require(
        control_result(
            session.pause(Duration::from_secs(2)),
            report,
            "fixture_go_busy",
        )? == Receipt::Busy,
        "fixture_go_busy",
    )?;
    create_file(&fixture.root.join("release_activity"), b"ok")?;
    wait_file(
        &fixture.root,
        "activity_released",
        &fixture.context,
        Duration::from_secs(5),
    )?;
    let lease = match control_result(
        session.pause(Duration::from_secs(2)),
        report,
        "fixture_go_pause",
    )? {
        Receipt::Idle { lease_id } => lease_id,
        _ => return Err("fixture_go_pause"),
    };
    require(
        control_result(
            session.resume(lease, Duration::from_secs(2)),
            report,
            "fixture_go_resume",
        )? == Receipt::Resumed,
        "fixture_go_resume",
    )?;
    let lease = match control_result(
        session.pause(Duration::from_secs(2)),
        report,
        "fixture_go_repause",
    )? {
        Receipt::Idle { lease_id } => lease_id,
        _ => return Err("fixture_go_repause"),
    };
    require(
        control_result(
            session.stop(lease, desktop_control::RECOMMENDED_STOP_BUDGET),
            report,
            "fixture_go_stop",
        )? == Receipt::RoleStopped,
        "fixture_go_stop",
    )?;
    wait_file(
        &fixture.root,
        "go_stop",
        &fixture.context,
        Duration::from_secs(1),
    )?;
    fixture.close_stopped(index)?;
    drop(session);
    report["real_go_gate_busy_pause_resume_stop"] = json!(true);
    report["go_role_ack_and_native_job_empty"] = json!(true);
    report["anonymous_nowait_peek_and_blocking_go_reader"] = json!(true);
    Ok(())
}

fn native_run(fixture: &mut Fixture, report: &mut Value) -> Check<()> {
    report["phase"] = json!("environment_validation");
    environment_case(fixture, report)?;
    report["phase"] = json!("tree");
    tree_case(fixture, report)?;
    report["phase"] = json!("go_protocol");
    peer_case(fixture, "protocol", 1, report)?;
    report["phase"] = json!("pipe_timeout");
    peer_case(fixture, "silent", 2, report)?;
    require(
        hash_file(&fixture.exe, 128 << 20)? == fixture.exe_hash
            && hash_file(&fixture.peer, 128 << 20)? == fixture.peer_hash,
        "fixture_image_changed",
    )?;
    report["artifact_hashes_unchanged"] = json!(true);
    report["phase"] = json!("complete");
    Ok(())
}

#[test]
#[ignore = "requires explicit disposable hosted-Windows opt-in and a source-bound inert Go peer"]
fn hosted_windows_runtime_control() {
    let context = hosted().expect("fixture_hosted_guard_failed");
    let peer =
        PathBuf::from(fixed_env(&format!("{PREFIX}PEER_EXE")).expect("fixture_peer_missing"));
    directory_chain(peer.parent().expect("fixture_peer_parent_missing"))
        .expect("fixture_peer_directory_unsafe");
    ordinary(&peer, false).expect("fixture_peer_leaf_unsafe");
    let peer = fs::canonicalize(peer).expect("fixture_peer_canonical_failed");
    require(within(&peer, &context.runner_temp), "fixture_peer_scope").expect("fixture_peer_scope");
    let peer_hash =
        fixed_env(&format!("{PREFIX}PEER_SHA256")).expect("fixture_peer_digest_missing");
    require(
        hex_string(&peer_hash, 64)
            && hash_file(&peer, 128 << 20).expect("fixture_peer_hash_failed") == peer_hash,
        "fixture_peer_digest",
    )
    .expect("fixture_peer_digest");
    let evidence =
        PathBuf::from(fixed_env(&format!("{PREFIX}EVIDENCE")).expect("fixture_evidence_missing"));
    directory_chain(evidence.parent().expect("fixture_evidence_parent"))
        .expect("fixture_evidence_directory_unsafe");
    let evidence_parent =
        fs::canonicalize(evidence.parent().unwrap()).expect("fixture_evidence_canonical_failed");
    require(
        evidence_parent.starts_with(&context.runner_temp),
        "fixture_evidence_scope",
    )
    .expect("fixture_evidence_scope");
    absent(&evidence).expect("fixture_evidence_not_fresh");
    let mut fixture =
        Fixture::new(context, peer, peer_hash).expect("fixture_initialization_failed");
    let mut report = json!({"schema":1,"source_sha":fixture.context.source,"peer_sha256":fixture.peer_hash,
        "test_executable_sha256":fixture.exe_hash,"scope":"inert_native_only","phase":"start","ok":false,
        "production_roles_executed":false,"models_db_ssh_installers_executed":false});
    let outcome = native_run(&mut fixture, &mut report);
    if let Err(code) = outcome {
        report["error"] = json!(code);
    }
    if let Some(code) = child_diagnostic(&fixture.root) {
        report["child_error"] = json!(code);
    }
    let cleanup = fixture.cleanup();
    report["cleanup"] = json!(if cleanup.is_ok() {
        "owned_jobs_empty_and_owned_root_removed"
    } else {
        "unconfirmed_owned_root_retained"
    });
    if let Err(code) = cleanup {
        report["cleanup_error"] = json!(code);
    }
    report["native_diagnostics"] = json!(fixture.diagnostics);
    report["ok"] = json!(outcome.is_ok() && cleanup.is_ok());
    create_file(
        &evidence,
        &serde_json::to_vec_pretty(&report).expect("fixture_evidence_encode"),
    )
    .expect("fixture_evidence_write");
    assert!(
        outcome.is_ok() && cleanup.is_ok(),
        "inert_runtime_control_failed_see_safe_evidence"
    );
}
