//! Owned runtime processes. Role control receipts and native descendant accounting are
//! separate evidence; neither root exit nor a failed operation releases ownership.
use crate::lifecycle::Stopped;
use std::process::{Child, Command};
#[cfg(any(windows, test))]
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

#[cfg(windows)]
pub(crate) const COMMAND_BUDGET: Duration = Duration::from_secs(30 * 60);
#[cfg(windows)]
const OUTPUT_LIMIT: usize = 8 * 1024 * 1024;

pub(crate) struct Output {
    pub code: u32,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

pub(crate) enum Process {
    Plain(Child),
    #[cfg(windows)]
    Owned(Native),
    #[cfg(test)]
    Injected(Box<dyn ProcessBoundary>),
}

#[cfg(test)]
pub(crate) trait ProcessBoundary: Send {
    fn live(&mut self) -> Result<Option<u32>, String>;
    fn pause(&mut self) -> Result<Option<u64>, String>;
    fn verify_idle(&mut self, lease: u64) -> Result<(), String>;
    fn resume(&mut self, lease: u64) -> Result<(), String>;
    fn stop(&mut self, lease: Option<u64>) -> Stopped;
}

pub(crate) struct Failure {
    pub error: String,
    pub process: Option<Process>,
}

pub(crate) fn checked_live(root: Option<u32>, active: u32) -> Result<Option<u32>, String> {
    match (root, active) {
        (None, 0) => Err("runtime_job_accounting_unknown".into()),
        (None, _) => Ok(None),
        (Some(code), 0) => Ok(Some(code)),
        (Some(_), _) => Err("runtime_descendants_still_running".into()),
    }
}

impl Process {
    pub(crate) fn live(&mut self) -> Result<Option<u32>, String> {
        match self {
            Self::Plain(child) => child
                .try_wait()
                .map(|s| s.map(|s| s.code().unwrap_or(1) as u32))
                .map_err(|_| "runtime_process_probe_failed".into()),
            #[cfg(windows)]
            Self::Owned(child) => child.live(),
            #[cfg(test)]
            Self::Injected(child) => child.live(),
        }
    }
    pub(crate) fn contained(&self) -> bool {
        match self {
            Self::Plain(_) => false,
            #[cfg(windows)]
            Self::Owned(_) => true,
            #[cfg(test)]
            Self::Injected(_) => true,
        }
    }
    pub(crate) fn pause(&mut self) -> Result<Option<u64>, String> {
        match self {
            #[cfg(windows)]
            Self::Owned(child) => child.pause(),
            #[cfg(test)]
            Self::Injected(child) => child.pause(),
            _ => Err("runtime_control_unavailable".into()),
        }
    }
    pub(crate) fn resume(&mut self, lease: u64) -> Result<(), String> {
        match self {
            #[cfg(windows)]
            Self::Owned(child) => child.resume(lease),
            #[cfg(test)]
            Self::Injected(child) => child.resume(lease),
            _ => {
                let _ = lease;
                Err("runtime_control_unavailable".into())
            }
        }
    }
    pub(crate) fn verify_idle(&mut self, lease: u64) -> Result<(), String> {
        match self {
            #[cfg(windows)]
            Self::Owned(child) => child.verify_idle(lease),
            #[cfg(test)]
            Self::Injected(child) => child.verify_idle(lease),
            _ => {
                let _ = lease;
                Err("runtime_control_unavailable".into())
            }
        }
    }
    pub(crate) fn stop(&mut self, lease: Option<u64>) -> Stopped {
        #[cfg(windows)]
        if let Self::Owned(child) = self {
            return child.stop(lease);
        }
        #[cfg(test)]
        if let Self::Injected(child) = self {
            return child.stop(lease);
        }
        let _ = lease;
        let primary = self.kill().err();
        let wait = self.wait().err();
        match self.live() {
            Ok(Some(_)) => Stopped {
                confirmed: true,
                error: joined(primary, wait),
            },
            probe => Stopped {
                confirmed: false,
                error: joined(
                    joined(primary, wait),
                    probe.err().or(Some("runtime_stop_unconfirmed".into())),
                ),
            },
        }
    }
    pub(crate) fn kill(&mut self) -> Result<(), String> {
        match self {
            Self::Plain(child) => child
                .kill()
                .map_err(|_| "runtime_process_kill_failed".into()),
            #[cfg(windows)]
            Self::Owned(child) => child
                .job
                .terminate_and_wait(Duration::from_secs(5))
                .map(|_| ())
                .map_err(|e| e.to_string()),
            #[cfg(test)]
            Self::Injected(_) => Err("inert_boundary_force_stop_forbidden".into()),
        }
    }
    pub(crate) fn wait(&mut self) -> Result<(), String> {
        match self {
            Self::Plain(child) => child
                .wait()
                .map(|_| ())
                .map_err(|_| "runtime_process_wait_failed".into()),
            #[cfg(windows)]
            Self::Owned(child) => child.empty(Duration::from_secs(5)),
            #[cfg(test)]
            Self::Injected(_) => Err("inert_boundary_wait_forbidden".into()),
        }
    }
}

pub(crate) trait StopEvidence {
    fn role_stopped(&mut self, lease: u64) -> Result<(), String>;
    fn job_empty(&mut self) -> Result<(), String>;
}

pub(crate) fn checked_role_stop(evidence: &mut impl StopEvidence, lease: Option<u64>) -> Stopped {
    let result = lease
        .ok_or_else(|| "runtime_control_lease_missing".to_string())
        .and_then(|lease| evidence.role_stopped(lease))
        .and_then(|_| evidence.job_empty());
    Stopped {
        confirmed: result.is_ok(),
        error: result.err(),
    }
}

fn unclosed_owner<T>(owner: Option<T>, empty_confirmed: bool, cleanup_failed: bool) -> Option<T> {
    if empty_confirmed && !cleanup_failed {
        None
    } else {
        owner
    }
}

fn joined(left: Option<String>, right: Option<String>) -> Option<String> {
    match (left, right) {
        (Some(a), Some(b)) => Some(format!("{a}; {b}")),
        (a, b) => a.or(b),
    }
}

pub(crate) enum Read {
    Pending,
    Data(usize),
    End,
}
trait Collect {
    fn read(&mut self, stderr: bool, buffer: &mut [u8]) -> Result<Read, String>;
    fn state(&mut self) -> Result<(Option<u32>, u32), String>;
    fn clock(&mut self) -> Duration;
    fn sleep(&mut self, budget: Duration);
}

fn collect(io: &mut impl Collect, budget: Duration, limit: usize) -> Result<Output, String> {
    if budget.is_zero() {
        return Err("runtime_command_timeout".into());
    }
    let start = io.clock();
    let mut previous = start;
    let (mut stdout, mut stderr) = (Vec::new(), Vec::new());
    let (mut out_end, mut err_end) = (false, false);
    loop {
        read_available(
            io,
            false,
            &mut stdout,
            &mut out_end,
            limit.saturating_sub(stderr.len()),
        )?;
        read_available(
            io,
            true,
            &mut stderr,
            &mut err_end,
            limit.saturating_sub(stdout.len()),
        )?;
        let (root, active) = io.state()?;
        let now = io.clock();
        let elapsed = now
            .checked_sub(start)
            .filter(|_| now >= previous)
            .ok_or("runtime_command_clock_invalid")?;
        previous = now;
        if elapsed > budget {
            return Err("runtime_command_timeout".into());
        }
        if active == 0 {
            let code = root.ok_or("runtime_job_accounting_unknown")?;
            if out_end && err_end {
                return Ok(Output {
                    code,
                    stdout,
                    stderr,
                });
            }
        }
        if elapsed == budget {
            return Err("runtime_command_timeout".into());
        }
        io.sleep((budget - elapsed).min(Duration::from_millis(10)));
    }
}

fn read_available(
    io: &mut impl Collect,
    stderr: bool,
    output: &mut Vec<u8>,
    ended: &mut bool,
    limit: usize,
) -> Result<(), String> {
    if *ended {
        return Ok(());
    }
    let mut buffer = [0u8; 8192];
    let capacity = limit
        .saturating_sub(output.len())
        .saturating_add(1)
        .min(buffer.len());
    match io.read(stderr, &mut buffer[..capacity])? {
        Read::Pending => (),
        Read::End => *ended = true,
        Read::Data(count) => {
            if count == 0 || count > capacity {
                return Err("runtime_command_progress_invalid".into());
            }
            let total = output
                .len()
                .checked_add(count)
                .ok_or("runtime_command_output_limit")?;
            if total > limit {
                return Err("runtime_command_output_limit".into());
            }
            output.extend_from_slice(&buffer[..count]);
        }
    }
    Ok(())
}

#[cfg(any(windows, test))]
fn take_log_pipe<R, W>(
    take: impl FnOnce() -> Option<R>,
    open: impl FnOnce() -> Result<W, String>,
) -> Result<(R, W), String> {
    // Keep the child's read endpoint in its owner if opening the log fails.
    let destination = open()?;
    let source = take().ok_or("runtime_stdout_unavailable")?;
    Ok((source, destination))
}

#[cfg(any(windows, test))]
fn start_log_drain<R, W>(
    source: R,
    destination: W,
    held: &mut Vec<R>,
    failed: &AtomicBool,
    duplicate: impl FnOnce(&R) -> Result<R, String>,
    start: impl FnOnce(R, W) -> Result<(), String>,
) -> Result<(), String> {
    let reader = duplicate(&source);
    // The worker may fail to start or read. Its handle must not be the last reader
    // while the native Job still owns work whose completion is unconfirmed.
    held.push(source);
    let result = reader.and_then(|reader| start(reader, destination));
    if result.is_err() {
        failed.store(true, Ordering::SeqCst);
    }
    result
}

#[cfg(any(windows, test))]
fn drain_log_core(
    destination: &mut impl std::io::Write,
    failed: &AtomicBool,
    mut read: impl FnMut(&mut [u8]) -> Result<Read, String>,
    mut sleep: impl FnMut(Duration),
) {
    let mut buffer = [0u8; 8192];
    let mut sink_failed = false;
    loop {
        match read(&mut buffer) {
            Ok(Read::Pending) => sleep(Duration::from_millis(10)),
            Ok(Read::End) => return,
            Ok(Read::Data(n)) if n > 0 && n <= buffer.len() => {
                if !sink_failed && destination.write_all(&buffer[..n]).is_err() {
                    failed.store(true, Ordering::SeqCst);
                    sink_failed = true;
                }
                // After a sink failure, keep consuming without retaining output. A
                // broken log must not cancel the child through its stdout pipe.
            }
            _ => {
                failed.store(true, Ordering::SeqCst);
                return;
            }
        }
    }
}

#[cfg(windows)]
use crate::{desktop_control as control, windows_job as jobs};
#[cfg(windows)]
use std::{
    fs::{File, OpenOptions},
    path::Path,
    sync::Arc,
    time::Instant,
};

#[cfg(windows)]
pub(crate) struct Native {
    job: jobs::JobChild,
    session: Option<control::Session<control::Pipes>>,
    drain_error: Arc<AtomicBool>,
    held_io: Vec<File>,
}

#[cfg(windows)]
fn role_error(error: control::Error) -> String {
    format!("runtime_control_failed: {error}")
}

#[cfg(windows)]
impl Native {
    fn verify_idle(&mut self, lease: u64) -> Result<(), String> {
        let session = self.session.as_ref().ok_or("runtime_control_unavailable")?;
        if lease == 0
            || session.state() != (control::State::Idle { lease_id: lease })
            || session.pending().is_some()
        {
            return Err("runtime_control_idle_unverified".into());
        }
        if self.live()?.is_some() {
            return Err("runtime_control_owner_exited".into());
        }
        Ok(())
    }
    fn live(&mut self) -> Result<Option<u32>, String> {
        if self.drain_error.load(Ordering::SeqCst) {
            return Err("runtime_output_unavailable".into());
        }
        let root = self.job.root_exit_code().map_err(|e| e.to_string())?;
        let active = self
            .job
            .query()
            .map_err(|e| e.to_string())?
            .active_processes;
        let result = checked_live(root, active)?;
        if result.is_some() {
            self.empty(Duration::from_secs(1))?;
        }
        Ok(result)
    }
    fn pause(&mut self) -> Result<Option<u64>, String> {
        match self
            .session
            .as_mut()
            .ok_or("runtime_control_unavailable")?
            .pause(Duration::from_secs(3))
            .map_err(role_error)?
        {
            control::Receipt::Busy => Ok(None),
            control::Receipt::Idle { lease_id } => Ok(Some(lease_id)),
            _ => Err("runtime_control_receipt_invalid".into()),
        }
    }
    fn resume(&mut self, lease: u64) -> Result<(), String> {
        match self
            .session
            .as_mut()
            .ok_or("runtime_control_unavailable")?
            .resume(lease, Duration::from_secs(3))
            .map_err(role_error)?
        {
            control::Receipt::Resumed => Ok(()),
            _ => Err("runtime_control_receipt_invalid".into()),
        }
    }
    fn empty(&mut self, budget: Duration) -> Result<(), String> {
        self.job
            .wait_empty(budget)
            .map(|_| ())
            .map_err(|e| e.to_string())
    }
    fn stop(&mut self, lease: Option<u64>) -> Stopped {
        // Update shutdown always needs the role's same-session idle lease/receipt.
        // Native Job termination remains an explicit normal/terminal teardown API.
        checked_role_stop(self, lease)
    }
}

#[cfg(windows)]
impl StopEvidence for Native {
    fn role_stopped(&mut self, lease: u64) -> Result<(), String> {
        let receipt = self
            .session
            .as_mut()
            .ok_or("runtime_control_unavailable")?
            .stop(lease, control::RECOMMENDED_STOP_BUDGET)
            .map_err(role_error)?;
        if receipt == control::Receipt::RoleStopped {
            Ok(())
        } else {
            Err("runtime_control_receipt_invalid".into())
        }
    }
    fn job_empty(&mut self) -> Result<(), String> {
        self.empty(Duration::from_secs(7))
    }
}

#[cfg(windows)]
fn native_spec(command: &Command, stderr: jobs::Stderr) -> jobs::LaunchSpec {
    jobs::LaunchSpec {
        executable: command.get_program().into(),
        arguments: command.get_args().map(ToOwned::to_owned).collect(),
        environment: command
            .get_envs()
            .filter_map(|(k, v)| v.map(|v| (k.to_owned(), v.to_owned())))
            .collect(),
        directory: command
            .get_current_dir()
            .expect("captured launch directory")
            .into(),
        stderr,
    }
}

#[cfg(windows)]
fn native_spawn(command: &Command, stderr: jobs::Stderr) -> Result<Native, Failure> {
    match jobs::JobChild::spawn(native_spec(command, stderr)) {
        Ok(job) => Ok(Native {
            job,
            session: None,
            drain_error: Arc::new(AtomicBool::new(false)),
            held_io: Vec::new(),
        }),
        Err(mut failure) => {
            let primary_causes = failure.error.causes.len();
            let cleanup = failure.cleanup(Duration::from_secs(5));
            let cleanup_failed = cleanup.causes.len() != primary_causes;
            let empty = failure
                .child
                .as_ref()
                .is_some_and(|child| child.status().empty_confirmed);
            let error = cleanup.to_string();
            let process = unclosed_owner(failure.child, empty, cleanup_failed).map(|job| {
                Process::Owned(Native {
                    job,
                    session: None,
                    drain_error: Arc::new(AtomicBool::new(false)),
                    held_io: Vec::new(),
                })
            });
            Err(Failure { error, process })
        }
    }
}

#[cfg(windows)]
pub(crate) fn spawn(
    command: &mut Command,
    log: Option<&Path>,
    bootstrap: Option<(control::Role, u64, String)>,
) -> Result<Process, Failure> {
    let stderr = match log {
        Some(path) => OpenOptions::new().append(true).open(path),
        None => OpenOptions::new().write(true).open("NUL"),
    }
    .map_err(|_| Failure {
        error: "runtime_log_unavailable".into(),
        process: None,
    })?;
    let mut child = native_spawn(command, jobs::Stderr::File(stderr))?;
    let connected = (|| -> Result<(), String> {
        if let Some((role, generation, nonce)) = bootstrap {
            match control::for_job(&mut child.job, role, generation, nonce) {
                Ok(session) => child.session = Some(session),
                Err(failure) => {
                    child.session = failure.session;
                    return Err(role_error(failure.error));
                }
            }
            child
                .session
                .as_mut()
                .expect("connected session")
                .ready(Duration::from_secs(5))
                .map_err(role_error)?;
        } else {
            drop(child.job.take_stdin());
            let (stdout, destination) = take_log_pipe(
                || child.job.take_stdout(),
                || {
                    match log {
                        Some(path) => OpenOptions::new().append(true).open(path),
                        None => OpenOptions::new().write(true).open("NUL"),
                    }
                    .map_err(|_| "runtime_log_unavailable".into())
                },
            )?;
            drain_log(
                stdout,
                destination,
                Arc::clone(&child.drain_error),
                &mut child.held_io,
            )?;
        }
        Ok(())
    })();
    match connected {
        Ok(()) => Ok(Process::Owned(child)),
        Err(error) => Err(Failure {
            error,
            process: Some(Process::Owned(child)),
        }),
    }
}

#[cfg(windows)]
fn drain_log(
    source: File,
    destination: File,
    failed: Arc<AtomicBool>,
    held: &mut Vec<File>,
) -> Result<(), String> {
    start_log_drain(
        source,
        destination,
        held,
        failed.as_ref(),
        |source| {
            source
                .try_clone()
                .map_err(|_| "runtime_stdout_duplicate_failed".into())
        },
        |mut source, mut destination| {
            let failed = Arc::clone(&failed);
            std::thread::Builder::new()
                .name("arkism-runtime-log".into())
                .spawn(move || {
                    drain_log_core(
                        &mut destination,
                        failed.as_ref(),
                        |buffer| {
                            control::try_read_pipe(&mut source, buffer)
                                .map(|read| match read {
                                    control::ReadStep::Pending => Read::Pending,
                                    control::ReadStep::End => Read::End,
                                    control::ReadStep::Data(n) => Read::Data(n),
                                })
                                .map_err(role_error)
                        },
                        std::thread::sleep,
                    );
                })
                .map(|_| ())
                .map_err(|_| "runtime_log_drain_start_failed".into())
        },
    )
}

#[cfg(not(windows))]
pub(crate) fn spawn(
    command: &mut Command,
    _log: Option<&std::path::Path>,
    _bootstrap: Option<()>,
) -> Result<Process, Failure> {
    command.spawn().map(Process::Plain).map_err(|_| Failure {
        error: "runtime_process_start_failed".into(),
        process: None,
    })
}

#[cfg(windows)]
struct NativeCollect<'a> {
    job: &'a mut jobs::JobChild,
    out: &'a mut File,
    err: &'a mut File,
    started: Instant,
}
#[cfg(windows)]
impl Collect for NativeCollect<'_> {
    fn read(&mut self, stderr: bool, buffer: &mut [u8]) -> Result<Read, String> {
        control::try_read_pipe(if stderr { &mut self.err } else { &mut self.out }, buffer)
            .map(|r| match r {
                control::ReadStep::Pending => Read::Pending,
                control::ReadStep::Data(n) => Read::Data(n),
                control::ReadStep::End => Read::End,
            })
            .map_err(role_error)
    }
    fn state(&mut self) -> Result<(Option<u32>, u32), String> {
        Ok((
            self.job.root_exit_code().map_err(|e| e.to_string())?,
            self.job
                .query()
                .map_err(|e| e.to_string())?
                .active_processes,
        ))
    }
    fn clock(&mut self) -> Duration {
        self.started.elapsed()
    }
    fn sleep(&mut self, budget: Duration) {
        std::thread::sleep(budget);
    }
}

#[cfg(windows)]
pub(crate) fn output(command: &mut Command) -> Result<Output, Failure> {
    let mut child = native_spawn(command, jobs::Stderr::Capture)?;
    drop(child.job.take_stdin());
    let mut out = child.job.take_stdout();
    let mut err = child.job.take_stderr();
    let result = (|| {
        let stdout = out.as_mut().ok_or("runtime_stdout_unavailable")?;
        let stderr = err.as_mut().ok_or("runtime_stderr_unavailable")?;
        let output = collect(
            &mut NativeCollect {
                job: &mut child.job,
                out: stdout,
                err: stderr,
                started: Instant::now(),
            },
            COMMAND_BUDGET,
            OUTPUT_LIMIT,
        )?;
        child
            .job
            .wait_empty(Duration::from_secs(1))
            .map_err(|e| e.to_string())?;
        child.job.close_empty().map_err(|e| e.to_string())?;
        Ok(output)
    })();
    match result {
        Ok(output) => Ok(output),
        Err(error) => {
            // Retain both read endpoints on uncertainty: dropping them could send broken-pipe
            // cancellation to work whose descendants are still owned and unconfirmed.
            child.held_io.extend(out);
            child.held_io.extend(err);
            Err(Failure {
                error,
                process: Some(Process::Owned(child)),
            })
        }
    }
}

#[cfg(not(windows))]
pub(crate) fn output(command: &mut Command) -> Result<Output, Failure> {
    command
        .output()
        .map(|out| Output {
            code: out.status.code().unwrap_or(1) as u32,
            stdout: out.stdout,
            stderr: out.stderr,
        })
        .map_err(|_| Failure {
            error: "runtime_command_failed".into(),
            process: None,
        })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::Cell;
    use std::collections::VecDeque;
    use std::sync::{atomic::AtomicUsize, Arc};
    enum Step {
        Bytes(Vec<u8>),
        Pending,
        End,
        Invalid(usize),
        Error,
    }
    struct Io {
        out: VecDeque<Step>,
        err: VecDeque<Step>,
        states: VecDeque<(Option<u32>, u32)>,
        times: VecDeque<Duration>,
        now: Duration,
        state_calls: usize,
        read_bytes: usize,
        query_error: bool,
    }
    impl Io {
        fn new() -> Self {
            Self {
                out: VecDeque::new(),
                err: VecDeque::new(),
                states: VecDeque::new(),
                times: VecDeque::new(),
                now: Duration::ZERO,
                state_calls: 0,
                read_bytes: 0,
                query_error: false,
            }
        }
    }
    impl Collect for Io {
        fn read(&mut self, stderr: bool, buffer: &mut [u8]) -> Result<Read, String> {
            match if stderr {
                self.err.pop_front()
            } else {
                self.out.pop_front()
            }
            .unwrap_or(Step::End)
            {
                Step::Pending => Ok(Read::Pending),
                Step::End => Ok(Read::End),
                Step::Invalid(n) => Ok(Read::Data(n)),
                Step::Error => Err("inert_read_failed".into()),
                Step::Bytes(data) => {
                    assert!(data.len() <= buffer.len());
                    buffer[..data.len()].copy_from_slice(&data);
                    self.read_bytes += data.len();
                    Ok(Read::Data(data.len()))
                }
            }
        }
        fn state(&mut self) -> Result<(Option<u32>, u32), String> {
            self.state_calls += 1;
            if self.query_error {
                return Err("inert_query_failed".into());
            }
            Ok(self.states.pop_front().unwrap_or((Some(0), 0)))
        }
        fn clock(&mut self) -> Duration {
            self.times.pop_front().unwrap_or(self.now)
        }
        fn sleep(&mut self, duration: Duration) {
            self.now += duration;
        }
    }
    #[test]
    fn runtime_collect_reads_both_pipes_and_waits_for_descendants_after_root_exit() {
        let mut io = Io::new();
        io.out.extend([Step::Bytes(b"a".to_vec()), Step::End]);
        io.err.extend([Step::Bytes(b"b".to_vec()), Step::End]);
        io.states.extend([(Some(0), 2), (Some(0), 1), (Some(0), 0)]);
        let output = collect(&mut io, Duration::from_secs(1), 8).unwrap();
        assert_eq!(output.code, 0);
        assert_eq!(output.stdout, b"a");
        assert_eq!(output.stderr, b"b");
        assert_eq!(io.state_calls, 3);
    }
    #[test]
    fn runtime_collect_combined_output_cap_allows_exact_and_reads_only_one_extra() {
        let mut io = Io::new();
        io.out.push_back(Step::Bytes(b"a".to_vec()));
        io.err.push_back(Step::Bytes(b"bc".to_vec()));
        assert!(collect(&mut io, Duration::from_secs(1), 3).is_ok());
        let mut io = Io::new();
        io.out
            .extend([Step::Bytes(b"a".to_vec()), Step::Bytes(b"d".to_vec())]);
        io.err.push_back(Step::Bytes(b"bc".to_vec()));
        io.states.push_back((Some(0), 1));
        assert_eq!(
            collect(&mut io, Duration::from_secs(1), 3).err().as_deref(),
            Some("runtime_command_output_limit")
        );
        assert_eq!(io.read_bytes, 4);
    }
    #[test]
    fn runtime_collect_deadline_and_monotonic_clock_are_checked_even_when_job_empty() {
        for late in [false, true] {
            let mut io = Io::new();
            let end = Duration::from_millis(100)
                + if late {
                    Duration::from_nanos(1)
                } else {
                    Duration::ZERO
                };
            io.times.extend([Duration::ZERO, end]);
            let result = collect(&mut io, Duration::from_millis(100), 1);
            assert_eq!(result.is_ok(), !late);
        }
        let mut io = Io::new();
        io.times
            .extend([Duration::from_millis(50), Duration::from_millis(49)]);
        assert_eq!(
            collect(&mut io, Duration::from_secs(1), 1).err().as_deref(),
            Some("runtime_command_clock_invalid")
        );
    }
    #[test]
    fn runtime_collect_pending_or_ended_pipes_never_replace_job_accounting() {
        let mut io = Io::new();
        io.out.push_back(Step::Pending);
        io.err.push_back(Step::Pending);
        io.states.extend(std::iter::repeat((Some(0), 1)).take(8));
        assert_eq!(
            collect(&mut io, Duration::from_millis(30), 1)
                .err()
                .as_deref(),
            Some("runtime_command_timeout")
        );
        let mut io = Io::new();
        io.states.push_back((None, 0));
        assert_eq!(
            collect(&mut io, Duration::from_secs(1), 1).err().as_deref(),
            Some("runtime_job_accounting_unknown")
        );
        assert_eq!(
            checked_live(Some(0), 1).err().as_deref(),
            Some("runtime_descendants_still_running")
        );
        assert!(checked_live(None, 0).is_err());
    }
    #[test]
    fn runtime_collect_rejects_read_query_and_zero_progress_errors() {
        for (step, error) in [
            (Step::Error, "inert_read_failed"),
            (Step::Invalid(0), "runtime_command_progress_invalid"),
            (Step::Invalid(9), "runtime_command_progress_invalid"),
        ] {
            let mut io = Io::new();
            io.out.push_back(step);
            assert_eq!(
                collect(&mut io, Duration::from_secs(1), 3).err().as_deref(),
                Some(error)
            );
            assert_eq!(io.state_calls, 0);
        }
        let mut io = Io::new();
        io.query_error = true;
        assert_eq!(
            collect(&mut io, Duration::from_secs(1), 3).err().as_deref(),
            Some("inert_query_failed")
        );
    }
    struct Evidence {
        ack: bool,
        empty: bool,
        calls: Vec<&'static str>,
    }
    impl StopEvidence for Evidence {
        fn role_stopped(&mut self, lease: u64) -> Result<(), String> {
            assert_eq!(lease, 7);
            self.calls.push("receipt");
            if self.ack {
                Ok(())
            } else {
                Err("receipt_unknown".into())
            }
        }
        fn job_empty(&mut self) -> Result<(), String> {
            self.calls.push("job0");
            if self.empty {
                Ok(())
            } else {
                Err("job_unknown".into())
            }
        }
    }
    #[test]
    fn runtime_controlled_stop_needs_exact_receipt_then_native_job_zero() {
        for (ack, empty, confirmed) in [
            (false, true, false),
            (true, false, false),
            (true, true, true),
        ] {
            let mut evidence = Evidence {
                ack,
                empty,
                calls: vec![],
            };
            assert_eq!(
                checked_role_stop(&mut evidence, Some(7)).confirmed,
                confirmed
            );
            assert_eq!(
                evidence.calls,
                if ack {
                    vec!["receipt", "job0"]
                } else {
                    vec!["receipt"]
                }
            );
        }
        let mut evidence = Evidence {
            ack: true,
            empty: true,
            calls: vec![],
        };
        assert!(!checked_role_stop(&mut evidence, None).confirmed);
        assert!(evidence.calls.is_empty());
    }

    #[test]
    fn runtime_spawn_failure_releases_only_fully_cleaned_owner() {
        use std::sync::{
            atomic::{AtomicUsize, Ordering},
            Arc,
        };
        struct Owned(Arc<AtomicUsize>);
        impl Drop for Owned {
            fn drop(&mut self) {
                self.0.fetch_add(1, Ordering::SeqCst);
            }
        }
        for (empty, failed, released) in [
            (true, false, true),
            (false, false, false),
            (true, true, false),
            (false, true, false),
        ] {
            let drops = Arc::new(AtomicUsize::new(0));
            let owner = unclosed_owner(Some(Owned(Arc::clone(&drops))), empty, failed);
            assert_eq!(owner.is_none(), released);
            assert_eq!(drops.load(Ordering::SeqCst), usize::from(released));
            drop(owner);
            assert_eq!(drops.load(Ordering::SeqCst), 1);
        }
    }

    struct ReaderHandle(Arc<AtomicUsize>);
    impl ReaderHandle {
        fn new(handles: &Arc<AtomicUsize>) -> Self {
            handles.fetch_add(1, Ordering::SeqCst);
            Self(Arc::clone(handles))
        }
        fn duplicate(&self) -> Self {
            Self::new(&self.0)
        }
    }
    impl Drop for ReaderHandle {
        fn drop(&mut self) {
            self.0.fetch_sub(1, Ordering::SeqCst);
        }
    }

    #[test]
    fn runtime_log_open_failure_keeps_stdout_in_child_before_take() {
        let handles = Arc::new(AtomicUsize::new(0));
        let mut stdout = Some(ReaderHandle::new(&handles));
        let result = take_log_pipe(
            || stdout.take(),
            || Err::<(), _>("inert_log_open_failed".into()),
        );
        assert_eq!(result.err().as_deref(), Some("inert_log_open_failed"));
        assert!(stdout.is_some());
        assert_eq!(handles.load(Ordering::SeqCst), 1);

        let order = Cell::new(0);
        let attached = take_log_pipe(
            || {
                assert_eq!(order.get(), 1);
                order.set(2);
                stdout.take()
            },
            || {
                order.set(1);
                Ok(())
            },
        )
        .unwrap();
        assert_eq!(order.get(), 2);
        assert_eq!(handles.load(Ordering::SeqCst), 1);
        drop(attached);
        assert_eq!(handles.load(Ordering::SeqCst), 0);
    }

    #[test]
    fn runtime_log_sink_partial_write_failure_keeps_draining_without_retrying_sink() {
        struct BrokenLog {
            writes: usize,
            accepted: Vec<u8>,
        }
        impl std::io::Write for BrokenLog {
            fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
                self.writes += 1;
                if self.writes == 1 {
                    self.accepted.push(bytes[0]);
                    Ok(1)
                } else {
                    Err(std::io::Error::other("inert_log_write_failed"))
                }
            }
            fn flush(&mut self) -> std::io::Result<()> {
                Ok(())
            }
        }
        let mut log = BrokenLog {
            writes: 0,
            accepted: vec![],
        };
        let mut io = Io::new();
        io.out.extend([
            Step::Bytes(b"first".to_vec()),
            Step::Pending,
            Step::Bytes(b"second".to_vec()),
            Step::End,
        ]);
        let failed = AtomicBool::new(false);
        let mut slept = Duration::ZERO;
        drain_log_core(
            &mut log,
            &failed,
            |buffer| io.read(false, buffer),
            |d| slept += d,
        );
        assert!(failed.load(Ordering::SeqCst));
        assert_eq!(io.read_bytes, 11);
        assert!(io.out.is_empty());
        assert_eq!(io.state_calls, 0);
        assert_eq!(log.writes, 2);
        assert_eq!(log.accepted, b"f");
        assert_eq!(slept, Duration::from_millis(10));
    }

    #[test]
    fn runtime_log_clone_and_worker_start_failures_keep_owner_read_endpoint() {
        for duplicate_fails in [false, true] {
            let handles = Arc::new(AtomicUsize::new(0));
            let source = ReaderHandle::new(&handles);
            let mut held = Vec::new();
            let failed = AtomicBool::new(false);
            let starts = Cell::new(0);
            let result = start_log_drain(
                source,
                (),
                &mut held,
                &failed,
                |source| {
                    if duplicate_fails {
                        Err("inert_duplicate_failed".into())
                    } else {
                        Ok(source.duplicate())
                    }
                },
                |_reader, ()| {
                    starts.set(starts.get() + 1);
                    Err("inert_worker_start_failed".into())
                },
            );
            assert_eq!(
                result.err().as_deref(),
                Some(if duplicate_fails {
                    "inert_duplicate_failed"
                } else {
                    "inert_worker_start_failed"
                }),
            );
            assert_eq!(starts.get(), usize::from(!duplicate_fails));
            assert!(failed.load(Ordering::SeqCst));
            assert_eq!(held.len(), 1);
            assert_eq!(handles.load(Ordering::SeqCst), 1);
            drop(held);
            assert_eq!(handles.load(Ordering::SeqCst), 0);
        }
    }

    #[test]
    fn runtime_log_read_failure_keeps_owner_endpoint_after_worker_returns() {
        for step in [Step::Error, Step::Invalid(0), Step::Invalid(8193)] {
            let handles = Arc::new(AtomicUsize::new(0));
            let mut held = Vec::new();
            let failed = AtomicBool::new(false);
            let mut io = Io::new();
            io.out.push_back(step);
            start_log_drain(
                ReaderHandle::new(&handles),
                Vec::<u8>::new(),
                &mut held,
                &failed,
                |source| Ok(source.duplicate()),
                |_reader, mut log| {
                    assert_eq!(handles.load(Ordering::SeqCst), 2);
                    drain_log_core(
                        &mut log,
                        &failed,
                        |buffer| io.read(false, buffer),
                        |_| panic!("read failure must return to its owner"),
                    );
                    Ok(())
                },
            )
            .unwrap();
            assert!(failed.load(Ordering::SeqCst));
            assert_eq!(handles.load(Ordering::SeqCst), 1);
            assert_eq!(io.state_calls, 0);
            drop(held);
            assert_eq!(handles.load(Ordering::SeqCst), 0);
        }
    }
}
