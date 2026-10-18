//! Parent sessions for the opt-in managed runtime control protocol.
//!
//! A role's `stopped` receipt never proves native descendants have exited; the
//! caller must also confirm its creation-time Job is empty. Busy is a receipt
//! that Go reopened admission without cancellation. An uncertain session keeps
//! its transport and last verified receipt, but grants no idle/resume authority.

use serde::{Deserialize, Deserializer, Serialize};
use std::fmt;
use std::time::Duration;

pub const MAX_FRAME: usize = 4096;
/// Covers the Go five-second shutdown and its final response-write allowance.
pub const RECOMMENDED_STOP_BUDGET: Duration = Duration::from_secs(7);

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Role {
    Server,
    Worker,
    Gateway,
    Watcher,
}

impl Role {
    fn label(self) -> &'static str {
        match self {
            Self::Server => "server",
            Self::Worker => "worker",
            Self::Gateway => "gateway",
            Self::Watcher => "watcher",
        }
    }
}

/// The PID must come from the owned native process handle, never a wire message.
/// No Debug/Serialize implementation can accidentally disclose the nonce.
pub struct Binding {
    role: Role,
    generation: u64,
    nonce: String,
    native_pid: u32,
}

fn valid_metadata(generation: u64, nonce: &str) -> bool {
    generation != 0
        && nonce.len() == 64
        && nonce
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn environment(role: Role, generation: u64, nonce: &str) -> Vec<(String, String)> {
    vec![
        ("ARKISM_DESKTOP_CONTROL".into(), "1".into()),
        ("ARKISM_DESKTOP_ROLE".into(), role.label().into()),
        ("ARKISM_DESKTOP_GENERATION".into(), generation.to_string()),
        ("ARKISM_DESKTOP_NONCE".into(), nonce.into()),
    ]
}

/// Build the opt-in environment before a native PID exists. Bind the resulting
/// process separately after creation; neither result should be logged.
pub fn bootstrap_environment(
    role: Role,
    generation: u64,
    nonce: &str,
) -> Result<Vec<(String, String)>, Error> {
    if !valid_metadata(generation, nonce) {
        return Err(Error::fixed(Code::InvalidBinding));
    }
    Ok(environment(role, generation, nonce))
}

impl Binding {
    pub fn new(role: Role, generation: u64, nonce: String, native_pid: u32) -> Result<Self, Error> {
        if native_pid == 0 || !valid_metadata(generation, &nonce) {
            return Err(Error::fixed(Code::InvalidBinding));
        }
        Ok(Self {
            role,
            generation,
            nonce,
            native_pid,
        })
    }

    /// Pass these to the full explicit child environment; never print this Vec.
    pub fn bootstrap_environment(&self) -> Vec<(String, String)> {
        environment(self.role, self.generation, &self.nonce)
    }

    pub fn role(&self) -> Role {
        self.role
    }
    pub fn generation(&self) -> u64 {
        self.generation
    }
    pub fn native_pid(&self) -> u32 {
        self.native_pid
    }
}

impl fmt::Debug for Binding {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Binding")
            .field("role", &self.role)
            .field("generation", &self.generation)
            .field("native_pid", &self.native_pid)
            .finish_non_exhaustive()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum State {
    AwaitReady,
    Running,
    Idle { lease_id: u64 },
    RoleStopped,
    Unverified,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Receipt {
    Ready,
    Busy,
    Idle { lease_id: u64 },
    Resumed,
    RoleStopped,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Operation {
    Pause,
    Resume,
    Stop,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Pending {
    pub operation: Operation,
    pub request_id: u64,
    pub lease_id: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RemoteError {
    InvalidContextOrReplay,
    InvalidLease,
    UnknownOperation,
    ShutdownTimeout,
    ShutdownFailed,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Code {
    InvalidBinding,
    InvalidState,
    InvalidLease,
    InvalidBudget,
    RequestExhausted,
    MalformedFrame,
    OversizeFrame,
    TrailingFrame,
    ContextMismatch,
    RequestMismatch,
    UnexpectedReceipt,
    ReceiptLeaseMismatch,
    Remote(RemoteError),
    Timeout,
    ClockRegressed,
    EndOfStream,
    InvalidProgress,
    InvalidPipe,
    PipeMode,
    PipeRead,
    PipeWrite,
    NativeOwnership,
    MissingPipe,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Error {
    pub code: Code,
    pub windows_code: Option<u32>,
}

impl Error {
    fn fixed(code: Code) -> Self {
        Self {
            code,
            windows_code: None,
        }
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        // Every value is a fixed enum or an OS number, never a parser message.
        write!(f, "desktop_control:{:?}", self.code)?;
        if let Some(code) = self.windows_code {
            write!(f, "({code})")?;
        }
        Ok(())
    }
}

impl std::error::Error for Error {}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ReadStep {
    Pending,
    Data(usize),
    End,
}

/// Each call must return without waiting for a peer. Only this session may read
/// or write the transport. A zero write means no progress, never successful EOF.
pub trait Transport {
    fn try_write(&mut self, bytes: &[u8]) -> Result<usize, Error>;
    fn try_read(&mut self, buffer: &mut [u8]) -> Result<ReadStep, Error>;
    fn clock(&mut self) -> Duration;
    fn wait_tick(&mut self, duration: Duration);
}

#[derive(Serialize)]
struct Request<'a> {
    v: u64,
    role: Role,
    generation: u64,
    nonce: &'a str,
    pid: u32,
    request_id: u64,
    op: Operation,
    lease_id: u64,
}

#[derive(Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
enum WireState {
    Ready,
    Busy,
    Idle,
    Resumed,
    Stopped,
    Error,
}

// Explicit null is not the same as omission. Struct deserialization also rejects
// duplicate fields, including duplicates of optional fields.
fn present<'de, D, T>(deserializer: D) -> Result<Option<T>, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de>,
{
    T::deserialize(deserializer).map(Some)
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Response {
    v: u64,
    role: Role,
    generation: u64,
    pid: u32,
    request_id: u64,
    state: WireState,
    #[serde(default, deserialize_with = "present")]
    error: Option<RemoteError>,
    #[serde(default, deserialize_with = "present")]
    lease_id: Option<u64>,
}

fn parse_response(frame: &[u8]) -> Result<Response, Error> {
    if frame.len() > MAX_FRAME {
        return Err(Error::fixed(Code::OversizeFrame));
    }
    if frame.last() != Some(&b'\n') || frame[..frame.len() - 1].contains(&b'\n') {
        return Err(Error::fixed(Code::MalformedFrame));
    }
    // Serde struct visitors can also accept positional arrays. The protocol
    // permits a JSON object only, including when every array slot would match.
    if frame
        .iter()
        .copied()
        .find(|byte| !byte.is_ascii_whitespace())
        != Some(b'{')
    {
        return Err(Error::fixed(Code::MalformedFrame));
    }
    let mut decoder = serde_json::Deserializer::from_slice(frame);
    let response =
        Response::deserialize(&mut decoder).map_err(|_| Error::fixed(Code::MalformedFrame))?;
    decoder
        .end()
        .map_err(|_| Error::fixed(Code::MalformedFrame))?;
    Ok(response)
}

struct Deadline {
    start: Duration,
    previous: Duration,
    budget: Duration,
}

impl Deadline {
    fn new(transport: &mut impl Transport, budget: Duration) -> Result<Self, Error> {
        if budget.is_zero() {
            return Err(Error::fixed(Code::InvalidBudget));
        }
        let start = transport.clock();
        Ok(Self {
            start,
            previous: start,
            budget,
        })
    }

    fn remaining(&mut self, transport: &mut impl Transport) -> Result<Duration, Error> {
        let now = transport.clock();
        if now < self.previous {
            return Err(Error::fixed(Code::ClockRegressed));
        }
        self.previous = now;
        let elapsed = now
            .checked_sub(self.start)
            .ok_or_else(|| Error::fixed(Code::ClockRegressed))?;
        self.budget
            .checked_sub(elapsed)
            .ok_or_else(|| Error::fixed(Code::Timeout))
    }

    fn pause(&mut self, transport: &mut impl Transport) -> Result<(), Error> {
        let remaining = self.remaining(transport)?;
        if remaining.is_zero() {
            return Err(Error::fixed(Code::Timeout));
        }
        transport.wait_tick(remaining.min(Duration::from_millis(2)));
        Ok(())
    }
}

fn write_frame(
    transport: &mut impl Transport,
    frame: &[u8],
    deadline: &mut Deadline,
) -> Result<(), Error> {
    let mut written = 0;
    while written < frame.len() {
        deadline.remaining(transport)?;
        let count = transport.try_write(&frame[written..])?;
        deadline.remaining(transport)?;
        if count > frame.len() - written {
            return Err(Error::fixed(Code::InvalidProgress));
        }
        if count == 0 {
            deadline.pause(transport)?;
        } else {
            written += count;
        }
    }
    Ok(())
}

fn read_frame(
    transport: &mut impl Transport,
    frame: &mut Vec<u8>,
    deadline: &mut Deadline,
) -> Result<(), Error> {
    let mut buffer = [0u8; 512];
    loop {
        deadline.remaining(transport)?;
        let capacity = buffer.len().min(MAX_FRAME + 1 - frame.len());
        let step = transport.try_read(&mut buffer[..capacity])?;
        deadline.remaining(transport)?;
        match step {
            ReadStep::End => return Err(Error::fixed(Code::EndOfStream)),
            ReadStep::Pending => deadline.pause(transport)?,
            ReadStep::Data(count) => {
                if count == 0 || count > capacity {
                    return Err(Error::fixed(Code::InvalidProgress));
                }
                frame.extend_from_slice(&buffer[..count]);
                if frame.len() > MAX_FRAME {
                    return Err(Error::fixed(Code::OversizeFrame));
                }
                if let Some(end) = frame.iter().position(|&b| b == b'\n') {
                    if end + 1 != frame.len() {
                        return Err(Error::fixed(Code::TrailingFrame));
                    }
                    return Ok(());
                }
                if frame.len() == MAX_FRAME {
                    return Err(Error::fixed(Code::OversizeFrame));
                }
            }
        }
    }
}

pub struct Session<T: Transport> {
    binding: Binding,
    transport: T,
    state: State,
    last_verified: State,
    next_id: u64,
    pending: Option<Pending>,
    partial_response: Vec<u8>,
}

impl<T: Transport> fmt::Debug for Session<T> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Session")
            .field("binding", &self.binding)
            .field("state", &self.state)
            .field("pending", &self.pending)
            .finish_non_exhaustive()
    }
}

impl<T: Transport> Session<T> {
    pub fn new(binding: Binding, transport: T) -> Self {
        Self {
            binding,
            transport,
            state: State::AwaitReady,
            last_verified: State::AwaitReady,
            next_id: 0,
            pending: None,
            partial_response: Vec::new(),
        }
    }

    pub fn state(&self) -> State {
        self.state
    }
    /// Historical evidence only; never use this as the current role permission.
    pub fn last_verified_state(&self) -> State {
        self.last_verified
    }
    pub fn pending(&self) -> Option<Pending> {
        self.pending
    }
    pub fn binding(&self) -> &Binding {
        &self.binding
    }

    pub fn ready(&mut self, budget: Duration) -> Result<Receipt, Error> {
        if self.state != State::AwaitReady {
            return Err(Error::fixed(Code::InvalidState));
        }
        let mut deadline = Deadline::new(&mut self.transport, budget)?;
        self.state = State::Unverified;
        read_frame(
            &mut self.transport,
            &mut self.partial_response,
            &mut deadline,
        )?;
        let response = parse_response(&self.partial_response)?;
        self.matches(&response, 0)?;
        if response.state != WireState::Ready
            || response.error.is_some()
            || response.lease_id.unwrap_or(0) != 0
        {
            return Err(Error::fixed(Code::UnexpectedReceipt));
        }
        self.confirm(State::Running);
        Ok(Receipt::Ready)
    }

    pub fn pause(&mut self, budget: Duration) -> Result<Receipt, Error> {
        if self.state != State::Running {
            return Err(Error::fixed(Code::InvalidState));
        }
        self.exchange(Operation::Pause, 0, budget)
    }

    pub fn resume(&mut self, lease_id: u64, budget: Duration) -> Result<Receipt, Error> {
        self.require_lease(lease_id)?;
        self.exchange(Operation::Resume, lease_id, budget)
    }

    /// Role ACK only. Caller must separately confirm the native Job is empty.
    pub fn stop(&mut self, lease_id: u64, budget: Duration) -> Result<Receipt, Error> {
        self.require_lease(lease_id)?;
        self.exchange(Operation::Stop, lease_id, budget)
    }

    fn require_lease(&self, lease_id: u64) -> Result<(), Error> {
        match self.state {
            State::Idle { lease_id: held } if held == lease_id && lease_id != 0 => Ok(()),
            State::Idle { .. } => Err(Error::fixed(Code::InvalidLease)),
            _ => Err(Error::fixed(Code::InvalidState)),
        }
    }

    fn exchange(
        &mut self,
        operation: Operation,
        lease_id: u64,
        budget: Duration,
    ) -> Result<Receipt, Error> {
        let mut deadline = Deadline::new(&mut self.transport, budget)?;
        let id = self
            .next_id
            .checked_add(1)
            .ok_or_else(|| Error::fixed(Code::RequestExhausted))?;
        let request = Request {
            v: 1,
            role: self.binding.role,
            generation: self.binding.generation,
            nonce: &self.binding.nonce,
            pid: self.binding.native_pid,
            request_id: id,
            op: operation,
            lease_id,
        };
        let mut frame =
            serde_json::to_vec(&request).map_err(|_| Error::fixed(Code::MalformedFrame))?;
        frame.push(b'\n');
        if frame.len() > MAX_FRAME {
            return Err(Error::fixed(Code::OversizeFrame));
        }
        self.next_id = id;
        self.pending = Some(Pending {
            operation,
            request_id: id,
            lease_id,
        });
        self.state = State::Unverified;
        write_frame(&mut self.transport, &frame, &mut deadline)?;
        read_frame(
            &mut self.transport,
            &mut self.partial_response,
            &mut deadline,
        )?;
        let response = parse_response(&self.partial_response)?;
        self.matches(&response, id)?;
        self.accept(response, operation, id, lease_id)
    }

    fn matches(&self, response: &Response, id: u64) -> Result<(), Error> {
        if response.v != 1
            || response.role != self.binding.role
            || response.generation != self.binding.generation
            || response.pid != self.binding.native_pid
        {
            return Err(Error::fixed(Code::ContextMismatch));
        }
        if response.request_id != id {
            return Err(Error::fixed(Code::RequestMismatch));
        }
        Ok(())
    }

    fn accept(
        &mut self,
        response: Response,
        operation: Operation,
        id: u64,
        lease: u64,
    ) -> Result<Receipt, Error> {
        let received_lease = response.lease_id.unwrap_or(0);
        if response.state == WireState::Error {
            let error = response
                .error
                .ok_or_else(|| Error::fixed(Code::UnexpectedReceipt))?;
            let expected = match (operation, error) {
                (Operation::Stop, RemoteError::ShutdownTimeout | RemoteError::ShutdownFailed) => {
                    lease
                }
                _ => 0,
            };
            if received_lease != expected {
                return Err(Error::fixed(Code::ReceiptLeaseMismatch));
            }
            return Err(Error::fixed(Code::Remote(error)));
        }
        if response.error.is_some() {
            return Err(Error::fixed(Code::UnexpectedReceipt));
        }
        let (state, receipt, expected_lease) = match (operation, response.state) {
            (Operation::Pause, WireState::Busy) => (State::Running, Receipt::Busy, 0),
            (Operation::Pause, WireState::Idle) => (
                State::Idle { lease_id: id },
                Receipt::Idle { lease_id: id },
                id,
            ),
            (Operation::Resume, WireState::Resumed) => (State::Running, Receipt::Resumed, 0),
            (Operation::Stop, WireState::Stopped) => {
                (State::RoleStopped, Receipt::RoleStopped, lease)
            }
            _ => return Err(Error::fixed(Code::UnexpectedReceipt)),
        };
        if received_lease != expected_lease {
            return Err(Error::fixed(Code::ReceiptLeaseMismatch));
        }
        self.confirm(state);
        Ok(receipt)
    }

    fn confirm(&mut self, state: State) {
        self.state = state;
        self.last_verified = state;
        self.pending = None;
        self.partial_response.clear();
    }
}

#[cfg(windows)]
mod windows {
    use super::{Binding, Code, Error, ReadStep, Role, Session, State, Transport, MAX_FRAME};
    use crate::windows_job::{JobChild, ResumeState};
    use std::ffi::c_void;
    use std::fmt;
    use std::fs::File;
    use std::os::windows::io::AsRawHandle;
    use std::ptr::{null, null_mut};
    use std::time::{Duration, Instant};

    #[link(name = "kernel32")]
    extern "system" {
        fn GetFileType(handle: *mut c_void) -> u32;
        fn GetLastError() -> u32;
        fn SetNamedPipeHandleState(
            handle: *mut c_void,
            mode: *const u32,
            maximum: *const u32,
            timeout: *const u32,
        ) -> i32;
        fn PeekNamedPipe(
            handle: *mut c_void,
            buffer: *mut c_void,
            size: u32,
            read: *mut u32,
            available: *mut u32,
            remaining: *mut u32,
        ) -> i32;
        fn ReadFile(
            handle: *mut c_void,
            buffer: *mut c_void,
            size: u32,
            read: *mut u32,
            overlapped: *mut c_void,
        ) -> i32;
        fn WriteFile(
            handle: *mut c_void,
            buffer: *const c_void,
            size: u32,
            written: *mut u32,
            overlapped: *mut c_void,
        ) -> i32;
    }

    fn os_error(code: Code) -> Error {
        Error {
            code,
            windows_code: Some(unsafe { GetLastError() }),
        }
    }

    /// No worker or detached reader thread. This object exclusively owns both
    /// parent endpoints taken directly from the newly created managed Job.
    /// NOWAIT applies only to the parent's write endpoint; the child's distinct
    /// stdin read endpoint keeps its normal blocking Go JSONL behavior.
    pub struct Pipes {
        input: Option<File>,
        output: Option<File>,
        configured: bool,
        start: Instant,
    }

    impl Pipes {
        fn configure(&mut self) -> Result<(), Error> {
            let input = self
                .input
                .as_ref()
                .ok_or_else(|| Error::fixed(Code::MissingPipe))?
                .as_raw_handle();
            let output = self
                .output
                .as_ref()
                .ok_or_else(|| Error::fixed(Code::MissingPipe))?
                .as_raw_handle();
            if input.is_null()
                || output.is_null()
                || input == output
                || input as usize == usize::MAX
                || output as usize == usize::MAX
                || unsafe { GetFileType(input) } != 3
                || unsafe { GetFileType(output) } != 3
            {
                return Err(Error::fixed(Code::InvalidPipe));
            }
            // CreatePipe's write handle has GENERIC_WRITE, sufficient to set its
            // wait mode. Do not assume a 4KiB write fits the default pipe buffer.
            // A byte-pipe NOWAIT write may make partial or zero progress.
            let mode = 1u32; // PIPE_READMODE_BYTE | PIPE_NOWAIT
            if unsafe { SetNamedPipeHandleState(input, &mode, null(), null()) } == 0 {
                return Err(os_error(Code::PipeMode));
            }
            self.configured = true;
            Ok(())
        }
    }

    impl Transport for Pipes {
        fn try_write(&mut self, bytes: &[u8]) -> Result<usize, Error> {
            if !self.configured {
                return Err(Error::fixed(Code::PipeMode));
            }
            if bytes.len() > MAX_FRAME {
                return Err(Error::fixed(Code::OversizeFrame));
            }
            let input = self
                .input
                .as_ref()
                .ok_or_else(|| Error::fixed(Code::MissingPipe))?;
            let size = u32::try_from(bytes.len()).map_err(|_| Error::fixed(Code::OversizeFrame))?;
            let mut written = 0;
            if unsafe {
                WriteFile(
                    input.as_raw_handle(),
                    bytes.as_ptr().cast(),
                    size,
                    &mut written,
                    null_mut(),
                )
            } == 0
            {
                return Err(os_error(Code::PipeWrite));
            }
            Ok(written as usize)
        }

        fn try_read(&mut self, buffer: &mut [u8]) -> Result<ReadStep, Error> {
            if !self.configured {
                return Err(Error::fixed(Code::PipeMode));
            }
            let output = self
                .output
                .as_ref()
                .ok_or_else(|| Error::fixed(Code::MissingPipe))?;
            read_available(output, buffer)
        }

        fn clock(&mut self) -> Duration {
            self.start.elapsed()
        }
        fn wait_tick(&mut self, duration: Duration) {
            std::thread::sleep(duration);
        }
    }

    /// One exclusive parent read endpoint from a managed Job, also reusable by a
    /// finite command's capture loop. Caller owns its bound/clock/size budget and
    /// retains the Job on errors; root exit alone never proves descendant exit.
    pub fn try_read_pipe(output: &mut File, buffer: &mut [u8]) -> Result<ReadStep, Error> {
        read_available(output, buffer)
    }

    fn read_available(output: &File, buffer: &mut [u8]) -> Result<ReadStep, Error> {
        if buffer.is_empty() {
            return Err(Error::fixed(Code::InvalidProgress));
        }
        if unsafe { GetFileType(output.as_raw_handle()) } != 3 {
            return Err(Error::fixed(Code::InvalidPipe));
        }
        let mut available = 0;
        if unsafe {
            PeekNamedPipe(
                output.as_raw_handle(),
                null_mut(),
                0,
                null_mut(),
                &mut available,
                null_mut(),
            )
        } == 0
        {
            let error = os_error(Code::PipeRead);
            return match error.windows_code {
                Some(109 | 233) => Ok(ReadStep::End),
                _ => Err(error),
            };
        }
        if available == 0 {
            return Ok(ReadStep::Pending);
        }
        // No other reader may drain this private endpoint between Peek and Read.
        // Read at most the already observed available bytes, without a worker.
        let count = buffer.len().min(available as usize) as u32;
        let mut read = 0;
        if unsafe {
            ReadFile(
                output.as_raw_handle(),
                buffer.as_mut_ptr().cast(),
                count,
                &mut read,
                null_mut(),
            )
        } == 0
        {
            let error = os_error(Code::PipeRead);
            return match error.windows_code {
                Some(109 | 233) => Ok(ReadStep::End),
                _ => Err(error),
            };
        }
        if read == 0 {
            Ok(ReadStep::End)
        } else {
            Ok(ReadStep::Data(read as usize))
        }
    }

    pub struct ConnectFailure {
        pub error: Error,
        pub session: Option<Session<Pipes>>,
    }

    impl fmt::Debug for ConnectFailure {
        fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
            f.debug_struct("ConnectFailure")
                .field("error", &self.error)
                .field("owns_transport", &self.session.is_some())
                .finish()
        }
    }

    /// Take each native parent endpoint once, before any other reader/writer.
    /// Failure retains a partially configured transport; it never stops the Job.
    /// Native availability and child behavior still require hosted Windows tests.
    pub fn for_job(
        job: &mut JobChild,
        role: Role,
        generation: u64,
        nonce: String,
    ) -> Result<Session<Pipes>, ConnectFailure> {
        let status = job.status();
        let binding = Binding::new(role, generation, nonce, job.root_pid()).map_err(|error| {
            ConnectFailure {
                error,
                session: None,
            }
        })?;
        if !status.assigned
            || !status.membership_verified
            || status.resume != ResumeState::ConfirmedOne
            || status.empty_confirmed
        {
            return Err(ConnectFailure {
                error: Error::fixed(Code::NativeOwnership),
                session: None,
            });
        }
        match job.root_exit_code() {
            Ok(None) => {}
            Ok(Some(_)) => {
                return Err(ConnectFailure {
                    error: Error::fixed(Code::NativeOwnership),
                    session: None,
                })
            }
            Err(error) => {
                return Err(ConnectFailure {
                    error: Error {
                        code: Code::NativeOwnership,
                        windows_code: error.causes.first().and_then(|cause| cause.windows_code),
                    },
                    session: None,
                })
            }
        }
        let pipes = Pipes {
            input: job.take_stdin(),
            output: job.take_stdout(),
            configured: false,
            start: Instant::now(),
        };
        let mut session = Session::new(binding, pipes);
        if let Err(error) = session.transport.configure() {
            session.state = State::Unverified;
            return Err(ConnectFailure {
                error,
                session: Some(session),
            });
        }
        Ok(session)
    }
}

#[cfg(windows)]
pub use windows::{for_job, try_read_pipe, ConnectFailure, Pipes};

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::{json, Value};
    use std::collections::VecDeque;
    use std::sync::{
        atomic::{AtomicUsize, Ordering},
        Arc,
    };

    struct Probe(Arc<AtomicUsize>);
    impl Drop for Probe {
        fn drop(&mut self) {
            self.0.fetch_add(1, Ordering::SeqCst);
        }
    }

    struct Input {
        after_requests: usize,
        bytes: VecDeque<u8>,
    }
    struct Fake {
        input: VecDeque<Input>,
        sent: Vec<u8>,
        read_limit: usize,
        write_limit: usize,
        now: Duration,
        times: VecDeque<Duration>,
        read_error: Option<Error>,
        write_error: Option<Error>,
        end: bool,
        read_step: Option<ReadStep>,
        write_step: Option<usize>,
        read_calls: usize,
        write_calls: usize,
        read_cost: Duration,
        write_cost: Duration,
        write_zeros: usize,
        _probe: Probe,
    }

    impl Default for Fake {
        fn default() -> Self {
            Self {
                input: VecDeque::new(),
                sent: Vec::new(),
                read_limit: usize::MAX,
                write_limit: usize::MAX,
                now: Duration::ZERO,
                times: VecDeque::new(),
                read_error: None,
                write_error: None,
                end: false,
                read_step: None,
                write_step: None,
                read_calls: 0,
                write_calls: 0,
                read_cost: Duration::ZERO,
                write_cost: Duration::ZERO,
                write_zeros: 0,
                _probe: Probe(Arc::new(AtomicUsize::new(0))),
            }
        }
    }

    impl Fake {
        fn frame(&mut self, after_requests: usize, bytes: Vec<u8>) {
            self.input.push_back(Input {
                after_requests,
                bytes: bytes.into(),
            });
        }
        fn requests(&self) -> Vec<Value> {
            self.sent
                .split(|&b| b == b'\n')
                .filter(|line| !line.is_empty())
                .map(|line| serde_json::from_slice(line).unwrap())
                .collect()
        }
    }

    impl Transport for Fake {
        fn try_write(&mut self, bytes: &[u8]) -> Result<usize, Error> {
            self.write_calls += 1;
            self.now += self.write_cost;
            if let Some(error) = self.write_error {
                return Err(error);
            }
            if let Some(count) = self.write_step {
                return Ok(count);
            }
            if self.write_zeros != 0 {
                self.write_zeros -= 1;
                return Ok(0);
            }
            let count = bytes.len().min(self.write_limit);
            self.sent.extend_from_slice(&bytes[..count]);
            Ok(count)
        }
        fn try_read(&mut self, buffer: &mut [u8]) -> Result<ReadStep, Error> {
            self.read_calls += 1;
            self.now += self.read_cost;
            if let Some(error) = self.read_error {
                return Err(error);
            }
            if let Some(step) = self.read_step {
                return Ok(step);
            }
            let requests = self.sent.iter().filter(|&&b| b == b'\n').count();
            if let Some(front) = self.input.front_mut() {
                if requests < front.after_requests {
                    return Ok(ReadStep::Pending);
                }
                let count = front.bytes.len().min(buffer.len()).min(self.read_limit);
                for byte in &mut buffer[..count] {
                    *byte = front.bytes.pop_front().unwrap();
                }
                if front.bytes.is_empty() {
                    self.input.pop_front();
                }
                return Ok(ReadStep::Data(count));
            }
            if self.end {
                Ok(ReadStep::End)
            } else {
                Ok(ReadStep::Pending)
            }
        }
        fn clock(&mut self) -> Duration {
            if let Some(now) = self.times.pop_front() {
                self.now = now;
            }
            self.now
        }
        fn wait_tick(&mut self, duration: Duration) {
            self.now += duration;
        }
    }

    fn binding() -> Binding {
        Binding::new(Role::Gateway, 7, "a".repeat(64), 42).unwrap()
    }
    fn budget() -> Duration {
        Duration::from_millis(20)
    }
    fn wire(id: u64, state: &str, lease: Option<u64>, error: Option<&str>) -> Vec<u8> {
        let mut value =
            json!({"v":1,"role":"gateway","generation":7,"pid":42,"request_id":id,"state":state});
        if let Some(lease) = lease {
            value["lease_id"] = json!(lease);
        }
        if let Some(error) = error {
            value["error"] = json!(error);
        }
        raw(value)
    }
    fn raw(value: Value) -> Vec<u8> {
        let mut bytes = serde_json::to_vec(&value).unwrap();
        bytes.push(b'\n');
        bytes
    }
    fn session(mut fake: Fake) -> Session<Fake> {
        fake.input.push_front(Input {
            after_requests: 0,
            bytes: wire(0, "ready", None, None).into(),
        });
        let mut session = Session::new(binding(), fake);
        assert_eq!(session.ready(budget()).unwrap(), Receipt::Ready);
        session
    }
    fn ready_failure(bytes: Vec<u8>) -> Session<Fake> {
        let mut fake = Fake::default();
        fake.frame(0, bytes);
        let mut session = Session::new(binding(), fake);
        assert!(session.ready(budget()).is_err());
        assert_eq!(session.state(), State::Unverified);
        assert_eq!(session.last_verified_state(), State::AwaitReady);
        session
    }

    fn malformed_ready(bytes: Vec<u8>) {
        let mut fake = Fake::default();
        fake.frame(0, bytes);
        let mut session = Session::new(binding(), fake);
        assert_eq!(
            session.ready(budget()).unwrap_err().code,
            Code::MalformedFrame
        );
        assert_eq!(session.state(), State::Unverified);
    }

    #[test]
    fn bootstrap_is_validated_before_pid_and_never_implicitly_inherits_environment() {
        let env = bootstrap_environment(Role::Watcher, u64::MAX, &"f".repeat(64)).unwrap();
        assert_eq!(env.len(), 4);
        assert_eq!(env[0], ("ARKISM_DESKTOP_CONTROL".into(), "1".into()));
        assert_eq!(env[1].1, "watcher");
        assert_eq!(env[2].1, u64::MAX.to_string());
        assert!(!env.iter().any(|(k, _)| k.contains("TOKEN") || k == "PATH"));
        let binding = binding();
        assert_eq!(
            binding.bootstrap_environment(),
            bootstrap_environment(Role::Gateway, 7, &"a".repeat(64)).unwrap()
        );
    }

    #[test]
    fn invalid_generation_pid_and_nonce_fail_closed() {
        assert_eq!(
            Binding::new(Role::Gateway, 0, "a".repeat(64), 42)
                .unwrap_err()
                .code,
            Code::InvalidBinding
        );
        assert_eq!(
            Binding::new(Role::Gateway, 7, "a".repeat(64), 0)
                .unwrap_err()
                .code,
            Code::InvalidBinding
        );
        for nonce in [
            "a".repeat(63),
            "a".repeat(65),
            "A".repeat(64),
            "g".repeat(64),
            "é".repeat(32),
            "\0".repeat(64),
        ] {
            assert_eq!(
                Binding::new(Role::Gateway, 7, nonce, 42).unwrap_err().code,
                Code::InvalidBinding
            );
        }
        assert!(bootstrap_environment(Role::Watcher, 0, &"a".repeat(64)).is_err());
    }

    #[test]
    fn actual_session_busy_idle_resume_and_role_stop_use_exact_monotonic_requests() {
        let mut fake = Fake::default();
        fake.frame(1, wire(1, "busy", None, None));
        fake.frame(2, wire(2, "idle", Some(2), None));
        fake.frame(3, wire(3, "resumed", None, None));
        fake.frame(4, wire(4, "idle", Some(4), None));
        fake.frame(5, wire(5, "stopped", Some(4), None));
        let mut s = session(fake);
        assert_eq!(s.pause(budget()).unwrap(), Receipt::Busy);
        assert_eq!(s.state(), State::Running);
        assert_eq!(s.pause(budget()).unwrap(), Receipt::Idle { lease_id: 2 });
        assert_eq!(s.resume(2, budget()).unwrap(), Receipt::Resumed);
        assert_eq!(s.pause(budget()).unwrap(), Receipt::Idle { lease_id: 4 });
        assert_eq!(s.stop(4, budget()).unwrap(), Receipt::RoleStopped);
        assert_eq!(s.state(), State::RoleStopped);
        assert_eq!(s.pending(), None);
        let requests = s.transport.requests();
        assert_eq!(requests.len(), 5);
        for (at, row) in requests.iter().enumerate() {
            assert_eq!(row.as_object().unwrap().len(), 8);
            assert_eq!(row["v"], 1);
            assert_eq!(row["role"], "gateway");
            assert_eq!(row["generation"], 7);
            assert_eq!(row["pid"], 42);
            assert_eq!(row["nonce"], "a".repeat(64));
            assert_eq!(row["request_id"], at + 1);
        }
        assert_eq!(requests[0]["lease_id"], 0);
        assert_eq!(requests[2]["lease_id"], 2);
        assert_eq!(requests[4]["lease_id"], 4);
        assert_eq!(s.pause(budget()).unwrap_err().code, Code::InvalidState);
        assert_eq!(s.transport.requests().len(), 5);
    }

    #[test]
    fn ready_context_comes_from_native_pid_role_generation_version_and_request_zero() {
        for (key, value) in [
            ("v", json!(2)),
            ("role", json!("watcher")),
            ("generation", json!(8)),
            ("pid", json!(43)),
            ("request_id", json!(1)),
        ] {
            let mut row: Value = serde_json::from_slice(&wire(0, "ready", None, None)).unwrap();
            row[key] = value;
            ready_failure(raw(row));
        }
    }

    #[test]
    fn missing_required_response_fields_are_not_defaults() {
        for key in ["v", "role", "generation", "pid", "request_id", "state"] {
            let mut row: Value = serde_json::from_slice(&wire(0, "ready", None, None)).unwrap();
            row.as_object_mut().unwrap().remove(key);
            malformed_ready(raw(row));
        }
    }

    #[test]
    fn unknown_null_and_duplicate_fields_are_rejected_including_optional_and_escaped_keys() {
        let base = String::from_utf8(wire(0, "ready", None, None)).unwrap();
        for addition in [
            r#", "extra":1"#,
            r#", "nonce":"hidden""#,
            r#", "lease_id":null"#,
            r#", "error":null"#,
            r#", "v":1"#,
            r#", "r\u006fle":"gateway"#,
            r#", "lease_id":0,"lease_id":0"#,
            r#", "error":"shutdown_failed","error":"shutdown_failed"#,
        ] {
            malformed_ready(base.replace('}', &format!("{addition}}}")).into_bytes());
        }
    }

    #[test]
    fn exact_integer_schema_rejects_float_exponent_bool_null_string_negative_and_overflow() {
        let base = String::from_utf8(wire(0, "ready", None, None)).unwrap();
        for value in [
            "1.0",
            "1e0",
            "true",
            "null",
            r#""7""#,
            "-1",
            "18446744073709551616",
        ] {
            malformed_ready(
                base.replace(r#""generation":7"#, &format!(r#""generation":{value}"#))
                    .into_bytes(),
            );
        }
        malformed_ready(
            base.replace(r#""pid":42"#, r#""pid":4294967296"#)
                .into_bytes(),
        );
        for value in ["false", "1.0", "-1"] {
            malformed_ready(
                base.replace(r#""request_id":0"#, &format!(r#""request_id":{value}"#))
                    .into_bytes(),
            );
        }
    }

    #[test]
    fn numerically_equal_floats_are_rejected_by_schema_before_context_matching() {
        let base = String::from_utf8(wire(0, "ready", Some(0), None)).unwrap();
        for (field, integer, float) in [
            ("v", "1", "1.0"),
            ("generation", "7", "7e0"),
            ("pid", "42", "42.0"),
            ("request_id", "0", "0.0"),
            ("lease_id", "0", "0e0"),
        ] {
            let bytes = base
                .replace(
                    &format!("\"{field}\":{integer}"),
                    &format!("\"{field}\":{float}"),
                )
                .into_bytes();
            malformed_ready(bytes);
        }
    }

    #[test]
    fn malformed_utf8_trailing_json_arrays_blank_and_unknown_enums_are_fixed_errors() {
        for bytes in [
            b"\xff\n".to_vec(),
            b"{}{}\n".to_vec(),
            b"[]\n".to_vec(),
            b"\n".to_vec(),
            b"{\n}\n".to_vec(),
            wire(0, "future_state", None, None),
            wire(0, "error", None, Some("future_error")),
        ] {
            ready_failure(bytes);
        }
    }

    #[test]
    fn positional_array_with_all_required_struct_fields_is_not_a_protocol_object() {
        malformed_ready(b"[1,\"gateway\",7,42,0,\"ready\"]\n".to_vec());
    }

    #[test]
    fn exact_4096_byte_frame_is_accepted_and_4097_or_unterminated_frame_rejected() {
        let original = wire(0, "ready", None, None);
        let mut exact = original[..original.len() - 1].to_vec();
        exact.resize(MAX_FRAME - 1, b' ');
        exact.push(b'\n');
        let mut fake = Fake::default();
        fake.frame(0, exact.clone());
        let mut s = Session::new(binding(), fake);
        assert_eq!(s.ready(budget()).unwrap(), Receipt::Ready);
        let mut oversized = exact.clone();
        oversized.insert(oversized.len() - 1, b' ');
        let mut s = ready_failure(oversized);
        assert_eq!(s.ready(budget()).unwrap_err().code, Code::InvalidState);
        exact[MAX_FRAME - 1] = b' ';
        ready_failure(exact);
    }

    #[test]
    fn byte_chunked_read_and_partial_writes_preserve_one_request_without_newline_replay() {
        let mut fake = Fake {
            read_limit: 1,
            write_limit: 3,
            ..Fake::default()
        };
        fake.frame(1, wire(1, "idle", Some(1), None));
        let mut s = session(fake);
        assert_eq!(s.pause(budget()).unwrap(), Receipt::Idle { lease_id: 1 });
        assert_eq!(s.transport.requests().len(), 1);
        assert!(s.transport.write_calls > 1 && s.transport.read_calls > 1);
    }

    #[test]
    fn zero_then_partial_progress_is_not_eof_and_does_not_duplicate_the_request() {
        let mut fake = Fake {
            write_zeros: 2,
            write_limit: 7,
            ..Fake::default()
        };
        fake.frame(1, wire(1, "busy", None, None));
        let mut s = session(fake);
        assert_eq!(s.pause(budget()).unwrap(), Receipt::Busy);
        assert_eq!(s.transport.requests().len(), 1);
        assert_eq!(s.transport.requests()[0]["request_id"], 1);
        assert_eq!(s.state(), State::Running);
    }

    #[test]
    fn write_and_read_share_one_budget_without_resetting_after_partial_progress() {
        let mut fake = Fake::default();
        fake.frame(1, wire(1, "idle", Some(1), None));
        let mut s = session(fake);
        s.transport.write_cost = Duration::from_millis(12);
        s.transport.read_cost = Duration::from_millis(9);
        assert_eq!(s.pause(budget()).unwrap_err().code, Code::Timeout);
        assert_eq!(s.state(), State::Unverified);
        assert_eq!(s.last_verified_state(), State::Running);
        assert_eq!(s.transport.requests().len(), 1);
    }

    #[test]
    fn actual_frame_writer_handles_a_full_four_kib_frame_in_partial_nonblocking_steps() {
        let mut fake = Fake {
            write_limit: 31,
            write_zeros: 2,
            ..Fake::default()
        };
        let frame = vec![b'x'; MAX_FRAME];
        let mut deadline = Deadline::new(&mut fake, budget()).unwrap();
        write_frame(&mut fake, &frame, &mut deadline).unwrap();
        assert_eq!(fake.sent, frame);
        assert!(fake.write_calls > MAX_FRAME / 31);
    }

    #[test]
    fn multiple_unsolicited_json_lines_in_one_observation_are_rejected() {
        let mut bytes = wire(0, "ready", None, None);
        bytes.extend(wire(1, "busy", None, None));
        let s = ready_failure(bytes);
        assert_eq!(s.state(), State::Unverified);
    }

    #[test]
    fn zero_progress_write_is_bounded_and_transport_remains_owned_without_retry() {
        let mut s = session(Fake {
            write_limit: 0,
            ..Fake::default()
        });
        let drops = s.transport._probe.0.clone();
        assert_eq!(s.pause(budget()).unwrap_err().code, Code::Timeout);
        assert_eq!(s.state(), State::Unverified);
        assert_eq!(s.last_verified_state(), State::Running);
        assert_eq!(
            s.pending(),
            Some(Pending {
                operation: Operation::Pause,
                request_id: 1,
                lease_id: 0
            })
        );
        assert_eq!(drops.load(Ordering::SeqCst), 0);
        assert!(s.transport.sent.is_empty());
        let calls = s.transport.write_calls;
        assert_eq!(s.pause(budget()).unwrap_err().code, Code::InvalidState);
        assert_eq!(s.transport.write_calls, calls);
        drop(s);
        assert_eq!(drops.load(Ordering::SeqCst), 1);
    }

    #[test]
    fn read_timeout_after_sent_request_never_invents_idle_or_resume_success() {
        let mut s = session(Fake::default());
        assert_eq!(s.pause(budget()).unwrap_err().code, Code::Timeout);
        assert_eq!(s.state(), State::Unverified);
        assert_eq!(s.last_verified_state(), State::Running);
        assert_eq!(s.transport.requests().len(), 1);
        assert!(s.pending().is_some());
        assert_eq!(s.resume(1, budget()).unwrap_err().code, Code::InvalidState);
    }

    #[test]
    fn partial_frame_then_eof_is_not_ready_or_idle() {
        let mut fake = Fake {
            end: true,
            ..Fake::default()
        };
        fake.frame(0, b"{\"v\":1".to_vec());
        let mut s = Session::new(binding(), fake);
        assert_eq!(s.ready(budget()).unwrap_err().code, Code::EndOfStream);
        assert_eq!(s.state(), State::Unverified);
        assert_eq!(s.partial_response, b"{\"v\":1");
    }

    #[test]
    fn pipe_read_and_write_failures_preserve_safe_codes_and_uncertain_ownership() {
        for write in [true, false] {
            let mut s = session(Fake::default());
            let drops = s.transport._probe.0.clone();
            let code = if write {
                Code::PipeWrite
            } else {
                Code::PipeRead
            };
            let error = Error {
                code,
                windows_code: Some(5),
            };
            if write {
                s.transport.write_error = Some(error);
            } else {
                s.transport.read_error = Some(error);
            }
            assert_eq!(s.pause(budget()).unwrap_err(), error);
            assert_eq!(s.state(), State::Unverified);
            assert_eq!(drops.load(Ordering::SeqCst), 0);
        }
    }

    #[test]
    fn invalid_zero_read_or_overreported_progress_is_not_success() {
        for step in [ReadStep::Data(0), ReadStep::Data(513)] {
            let mut s = session(Fake::default());
            s.transport.read_step = Some(step);
            assert_eq!(s.pause(budget()).unwrap_err().code, Code::InvalidProgress);
            assert_eq!(s.state(), State::Unverified);
        }
        let mut s = session(Fake::default());
        s.transport.write_step = Some(usize::MAX);
        assert_eq!(s.pause(budget()).unwrap_err().code, Code::InvalidProgress);
    }

    #[test]
    fn zero_budget_and_invalid_local_state_do_not_touch_pipes_or_consume_ids() {
        let mut s = Session::new(binding(), Fake::default());
        assert_eq!(
            s.ready(Duration::ZERO).unwrap_err().code,
            Code::InvalidBudget
        );
        assert_eq!((s.transport.read_calls, s.transport.write_calls), (0, 0));
        assert_eq!(s.state(), State::AwaitReady);
        assert_eq!(s.pause(budget()).unwrap_err().code, Code::InvalidState);
        let mut s = session(Fake::default());
        assert_eq!(
            s.pause(Duration::ZERO).unwrap_err().code,
            Code::InvalidBudget
        );
        assert!(s.transport.sent.is_empty());
        assert_eq!(s.next_id, 0);
        assert_eq!(s.state(), State::Running);
    }

    #[test]
    fn wrong_or_zero_lease_is_rejected_locally_with_no_request_or_state_change() {
        let mut fake = Fake::default();
        fake.frame(1, wire(1, "idle", Some(1), None));
        let mut s = session(fake);
        s.pause(budget()).unwrap();
        for lease in [0, 2, u64::MAX] {
            assert_eq!(
                s.resume(lease, budget()).unwrap_err().code,
                Code::InvalidLease
            );
            assert_eq!(
                s.stop(lease, budget()).unwrap_err().code,
                Code::InvalidLease
            );
        }
        assert_eq!(s.state(), State::Idle { lease_id: 1 });
        assert_eq!(s.transport.requests().len(), 1);
        assert_eq!(
            s.resume(1, Duration::ZERO).unwrap_err().code,
            Code::InvalidBudget
        );
        assert_eq!(s.state(), State::Idle { lease_id: 1 });
    }

    #[test]
    fn replay_stale_and_wrong_context_responses_cannot_advance_role_state() {
        for (key, value) in [
            ("request_id", json!(0)),
            ("request_id", json!(2)),
            ("pid", json!(43)),
            ("generation", json!(6)),
            ("role", json!("watcher")),
        ] {
            let mut row: Value = serde_json::from_slice(&wire(1, "idle", Some(1), None)).unwrap();
            row[key] = value;
            let mut fake = Fake::default();
            fake.frame(1, raw(row));
            let mut s = session(fake);
            assert!(s.pause(budget()).is_err());
            assert_eq!(s.state(), State::Unverified);
            assert_eq!(s.last_verified_state(), State::Running);
        }
    }

    #[test]
    fn unexpected_state_error_or_lease_never_grants_idle() {
        for bytes in [
            wire(1, "idle", Some(2), None),
            wire(1, "idle", None, None),
            wire(1, "busy", Some(1), None),
            wire(1, "resumed", None, None),
            wire(1, "stopped", Some(1), None),
            wire(1, "busy", None, Some("invalid_lease")),
            wire(1, "error", None, None),
        ] {
            let mut fake = Fake::default();
            fake.frame(1, bytes);
            let mut s = session(fake);
            assert!(s.pause(budget()).is_err());
            assert_eq!(s.state(), State::Unverified);
        }
    }

    #[test]
    fn stop_failure_or_timeout_preserves_held_lease_as_history_without_resume_authority() {
        for remote in ["shutdown_failed", "shutdown_timeout"] {
            let mut fake = Fake::default();
            fake.frame(1, wire(1, "idle", Some(1), None));
            fake.frame(2, wire(2, "error", Some(1), Some(remote)));
            let mut s = session(fake);
            s.pause(budget()).unwrap();
            let error = s.stop(1, budget()).unwrap_err();
            assert!(matches!(error.code, Code::Remote(_)));
            assert_eq!(s.state(), State::Unverified);
            assert_eq!(s.last_verified_state(), State::Idle { lease_id: 1 });
            assert_eq!(
                s.pending(),
                Some(Pending {
                    operation: Operation::Stop,
                    request_id: 2,
                    lease_id: 1
                })
            );
            assert_eq!(s.resume(1, budget()).unwrap_err().code, Code::InvalidState);
        }
    }

    #[test]
    fn stop_eof_or_mismatched_shutdown_lease_cannot_be_reported_as_stopped() {
        for bytes in [
            None,
            Some(wire(2, "stopped", None, None)),
            Some(wire(2, "error", Some(2), Some("shutdown_timeout"))),
        ] {
            let mut fake = Fake::default();
            fake.frame(1, wire(1, "idle", Some(1), None));
            if let Some(bytes) = bytes {
                fake.frame(2, bytes);
            } else {
                fake.end = true;
            }
            let mut s = session(fake);
            s.pause(budget()).unwrap();
            assert!(s.stop(1, budget()).is_err());
            assert_eq!(s.state(), State::Unverified);
            assert_eq!(s.last_verified_state(), State::Idle { lease_id: 1 });
        }
    }

    #[test]
    fn remote_error_vocabulary_matches_go_without_accepting_missing_context() {
        for error in [
            "invalid_context_or_replay",
            "invalid_lease",
            "unknown_operation",
        ] {
            let mut fake = Fake::default();
            fake.frame(1, wire(1, "error", None, Some(error)));
            let mut s = session(fake);
            assert!(matches!(
                s.pause(budget()).unwrap_err().code,
                Code::Remote(_)
            ));
        }
        let mut fake = Fake::default();
        fake.frame(1, wire(0, "error", None, Some("invalid_context_or_replay")));
        let mut s = session(fake);
        assert_eq!(s.pause(budget()).unwrap_err().code, Code::RequestMismatch);
    }

    #[test]
    fn request_ids_remain_exact_above_float_precision_and_never_wrap() {
        let high = (1u64 << 53) + 7;
        let mut fake = Fake::default();
        fake.frame(1, wire(high, "busy", None, None));
        let mut s = session(fake);
        s.next_id = high - 1;
        s.pause(budget()).unwrap();
        assert_eq!(s.transport.requests()[0]["request_id"].as_u64(), Some(high));
        let mut fake = Fake::default();
        fake.frame(1, wire(u64::MAX, "busy", None, None));
        let mut s = session(fake);
        s.next_id = u64::MAX - 1;
        s.pause(budget()).unwrap();
        assert_eq!(
            s.transport.requests()[0]["request_id"].as_u64(),
            Some(u64::MAX)
        );
        assert_eq!(s.pause(budget()).unwrap_err().code, Code::RequestExhausted);
        assert_eq!(s.state(), State::Running);
        assert_eq!(s.transport.requests().len(), 1);
    }

    #[test]
    fn response_at_exact_deadline_is_accepted_but_late_response_is_not() {
        for (time, accepted) in [(20, true), (21, false)] {
            let mut fake = Fake {
                times: VecDeque::from([
                    Duration::ZERO,
                    Duration::ZERO,
                    Duration::from_millis(time),
                ]),
                ..Fake::default()
            };
            fake.frame(0, wire(0, "ready", None, None));
            let mut s = Session::new(binding(), fake);
            assert_eq!(s.ready(budget()).is_ok(), accepted);
            assert_eq!(
                s.state(),
                if accepted {
                    State::Running
                } else {
                    State::Unverified
                }
            );
        }
    }

    #[test]
    fn clock_regression_during_read_rejects_even_valid_ready() {
        let mut fake = Fake {
            times: VecDeque::from([
                Duration::from_millis(5),
                Duration::from_millis(5),
                Duration::from_millis(4),
            ]),
            ..Fake::default()
        };
        fake.frame(0, wire(0, "ready", None, None));
        let mut s = Session::new(binding(), fake);
        assert_eq!(s.ready(budget()).unwrap_err().code, Code::ClockRegressed);
        assert_eq!(s.state(), State::Unverified);
    }

    #[test]
    fn clock_regression_before_write_cannot_send_a_partial_request() {
        let mut s = session(Fake::default());
        s.transport.times = VecDeque::from([Duration::from_millis(5), Duration::from_millis(4)]);
        assert_eq!(s.pause(budget()).unwrap_err().code, Code::ClockRegressed);
        assert!(s.transport.sent.is_empty());
        assert_eq!(s.transport.write_calls, 0);
        assert_eq!(s.state(), State::Unverified);
    }

    #[test]
    fn malformed_payloads_nonce_and_raw_parser_errors_never_reach_debug_or_error_output() {
        let secret = "a".repeat(64);
        let mut s = session(Fake::default());
        assert!(!format!("{:?}", s).contains(&secret));
        assert!(!format!("{:?}", s.binding()).contains(&secret));
        s.transport
            .frame(1, wire(1, "error", None, Some("sensitive-untrusted-text")));
        let error = s.pause(budget()).unwrap_err();
        assert_eq!(error.code, Code::MalformedFrame);
        for text in [format!("{error}"), format!("{error:?}"), format!("{s:?}")] {
            assert!(!text.contains(&secret));
            assert!(!text.contains("sensitive-untrusted-text"));
        }
    }

    #[test]
    fn watcher_and_max_generation_are_valid_exact_contexts() {
        let b = Binding::new(Role::Watcher, u64::MAX, "f".repeat(64), u32::MAX).unwrap();
        let mut fake = Fake::default();
        fake.frame(0,raw(json!({"v":1,"role":"watcher","generation":u64::MAX,"pid":u32::MAX,"request_id":0,"state":"ready"})));
        let mut s = Session::new(b, fake);
        assert_eq!(s.ready(budget()).unwrap(), Receipt::Ready);
        assert_eq!(s.binding().native_pid(), u32::MAX);
        assert_eq!(s.binding().generation(), u64::MAX);
    }

    fn runtime_role_frame(role: &str, id: u64, state: &str, lease: Option<u64>) -> Vec<u8> {
        let mut value: Value = serde_json::from_slice(&wire(id, state, lease, None)).unwrap();
        value["role"] = json!(role);
        raw(value)
    }

    #[test]
    fn runtime_roles_keep_exact_session_requests_and_leases() {
        for label in ["server", "worker", "gateway", "watcher"] {
            let role: Role = serde_json::from_value(json!(label)).unwrap();
            let binding = Binding::new(role, 7, "a".repeat(64), 42).unwrap();
            assert_eq!(binding.bootstrap_environment()[1].1, label);
            let mut fake = Fake::default();
            for (id, state, lease) in [
                (0, "ready", None),
                (1, "busy", None),
                (2, "idle", Some(2)),
                (3, "resumed", None),
                (4, "idle", Some(4)),
                (5, "stopped", Some(4)),
            ] {
                fake.frame(id as usize, runtime_role_frame(label, id, state, lease));
            }
            let mut session = Session::new(binding, fake);
            assert_eq!(session.ready(budget()).unwrap(), Receipt::Ready);
            assert_eq!(session.pause(budget()).unwrap(), Receipt::Busy);
            assert_eq!(
                session.pause(budget()).unwrap(),
                Receipt::Idle { lease_id: 2 }
            );
            assert_eq!(session.resume(2, budget()).unwrap(), Receipt::Resumed);
            assert_eq!(
                session.pause(budget()).unwrap(),
                Receipt::Idle { lease_id: 4 }
            );
            assert_eq!(session.stop(4, budget()).unwrap(), Receipt::RoleStopped);
            assert_eq!(session.state(), State::RoleStopped);
            for (index, request) in session.transport.requests().iter().enumerate() {
                assert_eq!(request.as_object().unwrap().len(), 8);
                assert_eq!(request["role"], label);
                assert_eq!(request["pid"], 42);
                assert_eq!(request["generation"], 7);
                assert_eq!(request["nonce"], "a".repeat(64));
                assert_eq!(request["request_id"], index + 1);
            }
        }
    }

    #[test]
    fn runtime_roles_reject_foreign_role_native_pid_and_generation() {
        for label in ["server", "worker"] {
            let role: Role = serde_json::from_value(json!(label)).unwrap();
            for (field, value) in [
                ("role", json!("gateway")),
                ("pid", json!(43)),
                ("generation", json!(6)),
                ("role", json!("unknown")),
            ] {
                let mut ready: Value =
                    serde_json::from_slice(&runtime_role_frame(label, 0, "ready", None)).unwrap();
                ready[field] = value;
                let mut fake = Fake::default();
                fake.frame(0, raw(ready));
                let binding = Binding::new(role, 7, "a".repeat(64), 42).unwrap();
                let mut session = Session::new(binding, fake);
                assert!(session.ready(budget()).is_err());
                assert_eq!(session.state(), State::Unverified);
                assert!(session.transport.requests().is_empty());
            }
        }
        for label in ["Server", "worker ", "postgres", "unknown", ""] {
            assert!(serde_json::from_value::<Role>(json!(label)).is_err());
        }
    }
}
