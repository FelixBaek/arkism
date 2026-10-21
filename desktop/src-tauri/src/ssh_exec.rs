//! Bounded SSH processes. Windows owns the child and its normal descendants in
//! a Job before resuming it, including a configured ProxyJump/ProxyCommand.
#[cfg(windows)]
use std::fs::File;
use std::io::{Read, Write};
#[cfg(windows)]
use std::path::PathBuf;
use std::process::Command;
#[cfg(not(windows))]
use std::process::Stdio;
use std::sync::mpsc::{self, Receiver};
use std::time::{Duration, Instant};

#[cfg(windows)]
pub struct Process {
    inner: crate::windows_job::JobChild,
    closed: bool,
}
#[cfg(not(windows))]
pub struct Process {
    inner: std::process::Child,
    closed: bool,
}

#[cfg(windows)]
type Input = File;
#[cfg(not(windows))]
type Input = std::process::ChildStdin;
#[cfg(windows)]
type Output = File;
#[cfg(not(windows))]
type Output = std::process::ChildStdout;
#[cfg(windows)]
type ErrorOutput = File;
#[cfg(not(windows))]
type ErrorOutput = std::process::ChildStderr;

#[cfg(windows)]
fn executable(command: &Command) -> Result<PathBuf, String> {
    let program = PathBuf::from(command.get_program());
    if program.is_absolute() {
        return Ok(program);
    }
    if program.components().count() != 1 {
        return std::env::current_dir()
            .map(|cwd| cwd.join(program))
            .map_err(|_| "ssh_missing".into());
    }
    let path = std::env::var_os("PATH").ok_or("ssh_missing")?;
    for directory in std::env::split_paths(&path) {
        for name in [program.clone(), program.with_extension("exe")] {
            let candidate = directory.join(name);
            if candidate.is_absolute() && candidate.is_file() {
                return Ok(candidate);
            }
        }
    }
    Err("ssh_missing".into())
}

impl Process {
    pub fn spawn(command: Command) -> Result<Self, String> {
        #[cfg(windows)]
        {
            use crate::windows_job::{JobChild, LaunchSpec, Stderr};
            let mut environment: Vec<_> = std::env::vars_os()
                .filter(|(k, _)| !k.to_string_lossy().starts_with('='))
                .collect();
            for (key, value) in command.get_envs() {
                environment.retain(|(k, _)| {
                    !k.to_string_lossy()
                        .eq_ignore_ascii_case(&key.to_string_lossy())
                });
                if let Some(value) = value {
                    environment.push((key.to_owned(), value.to_owned()));
                }
            }
            let directory = command
                .get_current_dir()
                .map(PathBuf::from)
                .unwrap_or(std::env::current_dir().map_err(|_| "ssh_missing")?);
            let spec = LaunchSpec {
                executable: executable(&command)?,
                arguments: command.get_args().map(Into::into).collect(),
                environment,
                directory,
                stderr: Stderr::Capture,
            };
            match JobChild::spawn(spec) {
                Ok(child) => Ok(Self {
                    inner: child,
                    closed: false,
                }),
                Err(mut failure) => {
                    let _ = failure.cleanup(Duration::from_secs(3));
                    Err("ssh_process_failed".into())
                }
            }
        }
        #[cfg(not(windows))]
        {
            let mut command = command;
            command
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .stderr(Stdio::piped())
                .spawn()
                .map(|inner| Self {
                    inner,
                    closed: false,
                })
                .map_err(|_| "ssh_missing".into())
        }
    }
    pub fn input(&mut self) -> Option<Input> {
        #[cfg(windows)]
        {
            self.inner.take_stdin()
        }
        #[cfg(not(windows))]
        {
            self.inner.stdin.take()
        }
    }
    pub fn output(&mut self) -> Option<Output> {
        #[cfg(windows)]
        {
            self.inner.take_stdout()
        }
        #[cfg(not(windows))]
        {
            self.inner.stdout.take()
        }
    }
    pub fn errors(&mut self) -> Option<ErrorOutput> {
        #[cfg(windows)]
        {
            self.inner.take_stderr()
        }
        #[cfg(not(windows))]
        {
            self.inner.stderr.take()
        }
    }
    pub fn status(&mut self) -> Result<Option<i32>, String> {
        #[cfg(windows)]
        {
            self.inner
                .root_exit_code()
                .map(|v| v.map(|n| n as i32))
                .map_err(|_| "ssh_process_failed".into())
        }
        #[cfg(not(windows))]
        {
            self.inner
                .try_wait()
                .map(|v| v.map(|n| n.code().unwrap_or(-1)))
                .map_err(|_| "ssh_process_failed".into())
        }
    }
    pub fn finish(&mut self) -> Result<(), String> {
        if self.closed {
            return Ok(());
        }
        #[cfg(windows)]
        {
            self.inner
                .wait_empty(Duration::from_secs(3))
                .map_err(|_| "ssh_process_cleanup_failed")?;
            self.inner
                .close_empty()
                .map_err(|_| "ssh_process_cleanup_failed")?;
        }
        self.closed = true;
        Ok(())
    }
    pub fn stop(&mut self) -> Result<(), String> {
        if self.closed {
            return Ok(());
        }
        #[cfg(windows)]
        {
            self.inner
                .terminate_and_wait(Duration::from_secs(3))
                .map_err(|_| "ssh_process_cleanup_failed")?;
            self.inner
                .close_empty()
                .map_err(|_| "ssh_process_cleanup_failed")?;
        }
        #[cfg(not(windows))]
        {
            if self
                .inner
                .try_wait()
                .map_err(|_| "ssh_process_cleanup_failed")?
                .is_none()
            {
                self.inner
                    .kill()
                    .map_err(|_| "ssh_process_cleanup_failed")?;
            }
            self.inner
                .wait()
                .map_err(|_| "ssh_process_cleanup_failed")?;
        }
        self.closed = true;
        Ok(())
    }
}

impl Drop for Process {
    fn drop(&mut self) {
        let _ = self.stop();
    }
}

pub struct Completed {
    pub code: i32,
    pub out: Vec<u8>,
    pub err: Vec<u8>,
}

pub fn read_pipe(
    mut reader: impl Read + Send + 'static,
    limit: usize,
) -> Receiver<Result<Vec<u8>, String>> {
    let (sender, receiver) = mpsc::channel();
    std::thread::spawn(move || {
        let mut bytes = Vec::new();
        let result = reader
            .by_ref()
            .take(limit as u64 + 1)
            .read_to_end(&mut bytes)
            .map_err(|_| "ssh_output_invalid".into())
            .and_then(|_| {
                if bytes.len() > limit {
                    Err("ssh_output_invalid".into())
                } else {
                    Ok(bytes)
                }
            });
        let _ = sender.send(result);
    });
    receiver
}

pub fn run(
    command: Command,
    input: Vec<u8>,
    limit: usize,
    timeout: Duration,
) -> Result<Completed, String> {
    let mut child = Process::spawn(command)?;
    let mut pipe = child.input().ok_or("ssh_process_failed")?;
    if input.is_empty() {
        drop(pipe);
    } else {
        std::thread::spawn(move || {
            let _ = pipe.write_all(&input);
        });
    }
    let out = read_pipe(child.output().ok_or("ssh_process_failed")?, limit);
    let err = read_pipe(child.errors().ok_or("ssh_process_failed")?, limit);
    let deadline = Instant::now() + timeout;
    let mut stdout = None;
    let mut stderr = None;
    loop {
        if stdout.is_none() {
            if let Ok(value) = out.try_recv() {
                stdout = Some(value?);
            }
        }
        if stderr.is_none() {
            if let Ok(value) = err.try_recv() {
                stderr = Some(value?);
            }
        }
        if let Some(code) = child.status()? {
            child.finish()?;
            let stdout = match stdout {
                Some(v) => v,
                None => out
                    .recv_timeout(Duration::from_secs(1))
                    .map_err(|_| "ssh_output_invalid")??,
            };
            let stderr = match stderr {
                Some(v) => v,
                None => err
                    .recv_timeout(Duration::from_secs(1))
                    .map_err(|_| "ssh_output_invalid")??,
            };
            return Ok(Completed {
                code,
                out: stdout,
                err: stderr,
            });
        }
        if Instant::now() >= deadline {
            child.stop()?;
            return Err("ssh_unreachable".into());
        }
        std::thread::sleep(Duration::from_millis(20));
    }
}
