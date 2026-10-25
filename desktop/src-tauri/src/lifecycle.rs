//! Update handoff coordination. The gate is held only while counting activities, never while
//! invoking a process, taking a runtime lock, or calling a recovery adapter.
use std::sync::{Arc, Mutex};
use std::collections::BTreeMap;

#[derive(Default)]
struct State {
    active: usize,
    updating: bool,
    blocked: bool,
}

#[derive(Default)]
pub(crate) struct Gate(Mutex<State>);

pub(crate) struct Activity(Arc<Gate>);

pub(crate) struct Lease {
    gate: Arc<Gate>,
    handed_off: bool,
}

impl Gate {
    pub(crate) fn activity(self: &Arc<Self>) -> Result<Activity, String> {
        let mut state = self.0.lock().map_err(|_| "update_runtime_busy")?;
        if state.updating || state.blocked {
            return Err("update_runtime_busy".into());
        }
        state.active = state.active.checked_add(1).ok_or("update_runtime_busy")?;
        Ok(Activity(Arc::clone(self)))
    }

    pub(crate) fn lease(self: &Arc<Self>) -> Result<Lease, String> {
        let mut state = self.0.lock().map_err(|_| "update_runtime_busy")?;
        if state.updating || state.blocked || state.active != 0 {
            return Err("update_runtime_busy".into());
        }
        state.updating = true;
        Ok(Lease { gate: Arc::clone(self), handed_off: false })
    }

    pub(crate) fn block(&self) {
        if let Ok(mut state) = self.0.lock() { state.blocked = true; }
    }
}

impl Drop for Activity {
    fn drop(&mut self) {
        if let Ok(mut state) = self.0.0.lock() {
            state.active -= 1;
        }
    }
}

impl Lease {
    pub(crate) fn hand_off(&mut self) {
        self.handed_off = true;
    }

    fn recovered(&mut self) {
        self.handed_off = false;
    }
}

impl Drop for Lease {
    fn drop(&mut self) {
        if !self.handed_off {
            if let Ok(mut state) = self.gate.0.lock() {
                state.updating = false;
            }
        }
    }
}

#[cfg(test)]
pub(crate) fn update(gate: &Arc<Gate>, action: impl FnOnce() -> Result<(), String>) -> Result<(), String> {
    let mut lease = gate.lease()?;
    action()?;
    lease.hand_off();
    Ok(())
}

// Dependency order for recovery; stopping uses the reverse order.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
pub(crate) enum Role { Postgres, Server, Worker, Gateway, Watcher }

impl Role {
    pub(crate) fn name(self) -> &'static str {
        match self {
            Self::Postgres => "postgres", Self::Server => "server", Self::Worker => "worker",
            Self::Gateway => "gateway", Self::Watcher => "watcher",
        }
    }
}

pub(crate) struct Stopped {
    pub(crate) confirmed: bool,
    pub(crate) error: Option<String>,
}

pub(crate) enum Pause { Absent, Busy, Idle(u64) }

pub(crate) trait RoleControl {
    fn pause_role(&mut self, role: Role) -> Result<Pause, String>;
    fn verify_idle_role(&mut self, role: Role, lease: u64) -> Result<(), String>;
    fn resume_role(&mut self, role: Role, lease: u64) -> Result<(), String>;
    fn control_failed(&mut self);
}

pub(crate) fn resume_roles(control: &mut impl RoleControl, leases: &mut BTreeMap<Role,u64>) -> Result<(),String> {
    let held: Vec<_> = leases.iter().map(|(role,lease)| (*role,*lease)).collect();
    // Verify every held owner before releasing any one of them. The following
    // sequential release is still not an atomic barrier across processes.
    for &(role,lease) in &held {
        if let Err(error) = control.verify_idle_role(role,lease) {
            control.control_failed();
            return Err(format!("{}: {error}",role.name()));
        }
    }
    for (role,lease) in held {
        match control.resume_role(role,lease) {
            Ok(()) => { leases.remove(&role); }
            Err(error) => {
                control.control_failed();
                return Err(format!("{}: {error}",role.name()));
            }
        }
    }
    Ok(())
}

pub(crate) fn pause_roles(control: &mut impl RoleControl, leases: &mut BTreeMap<Role,u64>) -> Result<(),String> {
    for role in [Role::Server,Role::Worker,Role::Gateway,Role::Watcher] {
        let primary = match control.pause_role(role) {
            Ok(Pause::Absent) => continue,
            Ok(Pause::Idle(lease)) => { leases.insert(role,lease); continue; }
            Ok(Pause::Busy) => "update_runtime_busy".into(),
            Err(error) => { control.control_failed(); error },
        };
        return match resume_roles(control,leases) {
            Ok(()) => Err(primary),
            Err(recovery) => Err(format!("{primary}; update_runtime_recovery_failed: {recovery}")),
        };
    }
    Ok(())
}

pub(crate) trait Backend {
    type Spec: Clone;
    /// Read-only, including ownership, liveness and endpoint probes. No normal-start helpers.
    fn snapshot(&mut self) -> Result<Vec<(Role, Self::Spec)>, String>;
    fn pause(&mut self) -> Result<(), String> { Ok(()) }
    fn resume_remaining(&mut self) -> Result<(), String> { Ok(()) }
    fn hold_closed(&mut self) {}
    fn safe_to_reopen(&self) -> bool { true }
    /// An error may accompany a confirmed stop. Unconfirmed handles must remain owned.
    fn stop(&mut self, role: Role) -> Stopped;
    fn restore(&mut self, role: Role, spec: &Self::Spec) -> Result<(), String>;
}

fn validate<S>(snapshot: &[(Role, S)]) -> Result<(), String> {
    let roles: Vec<_> = snapshot.iter().map(|(r, _)| *r).collect();
    let pair = roles.contains(&Role::Server) && roles.contains(&Role::Worker);
    let partial = roles.contains(&Role::Server) != roles.contains(&Role::Worker);
    let duplicate = roles.windows(2).any(|w| w[0] == w[1]);
    if duplicate || partial || (pair && !roles.contains(&Role::Postgres)) || (roles.contains(&Role::Watcher) && !pair) {
        return Err("update_runtime_state_unknown".into());
    }
    Ok(())
}

fn recovered_error<B: Backend>(backend: &mut B, stopped: &mut [(Role, B::Spec)], primary: String) -> String {
    stopped.sort_by_key(|(role, _)| *role);
    let mut errors = Vec::new();
    let mut failed = Vec::new();
    for (role, spec) in stopped {
        let result = if dependency_failed(*role, &failed) { Err("update_runtime_dependency_unavailable".into()) } else { backend.restore(*role, spec) };
        if let Err(error) = result {
            failed.push(*role);
            errors.push(format!("{}: {error}", role.name()));
        }
    }
    if errors.is_empty() && backend.safe_to_reopen() {
        if let Err(error) = backend.resume_remaining() { errors.push(error); }
    }
    if !errors.is_empty() { backend.hold_closed(); }
    if errors.is_empty() { primary } else { format!("{primary}; update_runtime_recovery_failed: {}", errors.join("; ")) }
}

fn dependency_failed(role: Role, failed: &[Role]) -> bool {
    match role {
        Role::Server => failed.contains(&Role::Postgres),
        Role::Worker | Role::Watcher => failed.contains(&Role::Postgres) || failed.contains(&Role::Server),
        _ => false,
    }
}

pub(crate) enum Launch<O> {
    Started,
    Unstarted(String),
    Unverified { owner: O, error: String },
}

enum Handoff<O, S> {
    Started,
    Unstarted(String),
    Unverified { owner: O, error: String, stopped: Vec<(Role, S)> },
}

pub(crate) enum OwnedHandoff<O, S> {
    Started,
    Unstarted(String),
    Unverified { owner: O, error: String, stopped: Vec<(Role, S)>, lease: Lease },
}

fn quiesce<B: Backend>(backend: &mut B, on_mutating: impl FnOnce()) -> Result<Vec<(Role, B::Spec)>, String> {
    let mut snapshot = backend.snapshot()?;
    snapshot.sort_by_key(|(role, _)| *role);
    validate(&snapshot)?;
    on_mutating();
    backend.pause()?;
    let mut stopped = Vec::new();
    for (role, spec) in snapshot.iter().rev() {
        let result = backend.stop(*role);
        if result.confirmed {
            stopped.push((*role, spec.clone()));
        }
        let error = result.error.or_else(|| (!result.confirmed).then(|| "update_runtime_stop_unconfirmed".into()));
        if let Some(error) = error {
            if !result.confirmed { backend.hold_closed(); }
            return Err(recovered_error(backend, &mut stopped, format!("update_runtime_stop_failed: {}: {error}", role.name())));
        }
    }
    Ok(stopped)
}

/// All launch outcomes share the same snapshot, stop and confirmed-subset recovery logic.
fn handoff_core<B: Backend, O>(backend: &mut B, on_mutating: impl FnOnce(), launch: impl FnOnce() -> Launch<O>) -> Handoff<O, B::Spec> {
    let mut stopped = match quiesce(backend, on_mutating) {
        Ok(stopped) => stopped,
        Err(error) => return Handoff::Unstarted(error),
    };
    match launch() {
        Launch::Started => Handoff::Started,
        Launch::Unstarted(error) => Handoff::Unstarted(recovered_error(backend, &mut stopped, error)),
        Launch::Unverified { owner, error } => Handoff::Unverified { owner, error, stopped },
    }
}

#[cfg(test)]
fn ordinary_launch(result: Result<(), String>) -> Launch<std::convert::Infallible> {
    match result {
        Ok(()) => Launch::Started,
        Err(error) => Launch::Unstarted(error),
    }
}

/// The ordinary Result boundary cannot produce an unresolved child owner.
#[cfg(test)]
pub(crate) fn handoff<B: Backend>(backend: &mut B, launch: impl FnOnce() -> Result<(), String>) -> Result<(), String> {
    match handoff_core(backend, || {}, || ordinary_launch(launch())) {
        Handoff::Started => Ok(()),
        Handoff::Unstarted(error) => Err(error),
        Handoff::Unverified { owner, .. } => match owner {},
    }
}

pub(crate) fn coordinated_owned<B: Backend, O>(gate: &Arc<Gate>, backend: &mut B, launch: impl FnOnce() -> Launch<O>) -> OwnedHandoff<O, B::Spec> {
    let mut lease = match gate.lease() {
        Ok(lease) => lease,
        Err(error) => return OwnedHandoff::Unstarted(error),
    };
    match handoff_core(backend, || lease.hand_off(), launch) {
        Handoff::Started => OwnedHandoff::Started,
        Handoff::Unstarted(error) => {
            if backend.safe_to_reopen() { lease.recovered(); } else { lease.hand_off(); }
            OwnedHandoff::Unstarted(error)
        }
        Handoff::Unverified { owner, error, stopped } => OwnedHandoff::Unverified { owner, error, stopped, lease },
    }
}

/// One production boundary owns both the mutation lease and recovery safety decision.
#[cfg(test)]
pub(crate) fn coordinated<B: Backend>(gate: &Arc<Gate>, backend: &mut B, launch: impl FnOnce() -> Result<(), String>) -> Result<(), String> {
    match coordinated_owned(gate, backend, || ordinary_launch(launch())) {
        OwnedHandoff::Started => Ok(()),
        OwnedHandoff::Unstarted(error) => Err(error),
        OwnedHandoff::Unverified { owner, .. } => match owner {},
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::BTreeMap;

    #[derive(Clone, PartialEq, Eq, Debug)]
    struct Spec { endpoint: u16, argv: String }

    struct Fake {
        running: BTreeMap<Role, Spec>,
        events: Arc<Mutex<Vec<String>>>,
        stop_failure: Option<(Role, bool)>,
        restore_failure: Option<Role>,
        probe_failure: bool,
        configuration: Vec<u8>,
        held_closed: bool,
        pause_calls: usize,
        resume_calls: usize,
        panic_on: Option<&'static str>,
    }

    impl Fake {
        fn new(roles: &[Role]) -> Self {
            Self {
                running: roles.iter().map(|role| (*role, Spec { endpoint: 12000 + *role as u16, argv: format!("original-{}", role.name()) })).collect(),
                events: Arc::new(Mutex::new(Vec::new())), stop_failure: None, restore_failure: None,
                probe_failure: false, configuration: b"remote-routing;token-presence=absent;settings=original".to_vec(), held_closed:false,
                pause_calls:0, resume_calls:0, panic_on:None,
            }
        }
        fn record(&self, action: &str, role: Role) { self.events.lock().unwrap().push(format!("{action}:{}", role.name())); }
        fn events(&self) -> Vec<String> { self.events.lock().unwrap().clone() }
    }

    impl Backend for Fake {
        type Spec = Spec;
        fn snapshot(&mut self) -> Result<Vec<(Role, Spec)>, String> {
            assert_ne!(self.panic_on, Some("snapshot"), "inert snapshot panic");
            self.events.lock().unwrap().push("snapshot".into());
            if self.probe_failure { return Err("probe_failed".into()); }
            Ok(self.running.iter().map(|(r, s)| (*r, s.clone())).collect())
        }
        fn pause(&mut self) -> Result<(), String> {
            self.pause_calls += 1;
            assert_ne!(self.panic_on, Some("pause"), "inert pause panic");
            Ok(())
        }
        fn resume_remaining(&mut self) -> Result<(), String> {
            self.resume_calls += 1;
            Ok(())
        }
        fn stop(&mut self, role: Role) -> Stopped {
            assert_ne!(self.panic_on, Some("stop"), "inert stop panic");
            self.record("stop", role);
            let failed = self.stop_failure.filter(|(r, _)| *r == role);
            let confirmed = failed.is_none_or(|(_, confirmed)| confirmed);
            if confirmed { self.running.remove(&role); }
            Stopped { confirmed, error: failed.map(|_| "stop_fault".into()) }
        }
        fn hold_closed(&mut self) { self.held_closed = true; }
        fn safe_to_reopen(&self) -> bool { !self.held_closed }
        fn restore(&mut self, role: Role, spec: &Spec) -> Result<(), String> {
            self.record("restore", role);
            assert!(!self.running.contains_key(&role), "unconfirmed or untouched role must not be replaced");
            if self.restore_failure == Some(role) { return Err("replay_fault".into()); }
            self.running.insert(role, spec.clone());
            Ok(())
        }
    }

    const ALL: [Role; 5] = [Role::Postgres, Role::Server, Role::Worker, Role::Gateway, Role::Watcher];

    #[test]
    fn update_lifecycle_restores_exact_roles_endpoints_and_remote_routing() {
        let states = [vec![], vec![Role::Postgres], vec![Role::Gateway], vec![Role::Postgres, Role::Gateway],
            ALL[..3].to_vec(), ALL[..4].to_vec(), vec![Role::Postgres, Role::Server, Role::Worker, Role::Watcher], ALL.to_vec()];
        for roles in states {
            let gate = Arc::new(Gate::default());
            let mut fake = Fake::new(&roles);
            let original = fake.running.clone();
            let config = fake.configuration.clone();
            let events = Arc::clone(&fake.events);
            let error = coordinated(&gate, &mut fake, || {
                events.lock().unwrap().push("launch".into());
                assert!(gate.activity().is_err(), "no reentrant mutation during launch");
                Err("update_start_failed: injected".into())
            }).unwrap_err();
            assert_eq!(error, "update_start_failed: injected");
            assert_eq!(fake.running, original);
            assert_eq!(fake.configuration, config);
            let mut expected = vec!["snapshot".to_string()];
            expected.extend(roles.iter().rev().map(|r| format!("stop:{}", r.name())));
            expected.push("launch".into());
            expected.extend(roles.iter().map(|r| format!("restore:{}", r.name())));
            assert_eq!(fake.events(), expected);
            assert!(gate.activity().is_ok(), "failure reopens the gate after recovery");
        }
    }

    #[test]
    fn update_lifecycle_stop_fault_restores_only_confirmed_subset_and_never_launches() {
        for role in ALL {
            for confirmed in [false, true] {
                let gate = Arc::new(Gate::default());
                let mut fake = Fake::new(&ALL);
                let original = fake.running.clone();
                fake.stop_failure = Some((role, confirmed));
                let error = coordinated(&gate, &mut fake, || panic!("stop error must prevent launch")).unwrap_err();
                assert!(error.contains("update_runtime_stop_failed"));
                assert!(error.contains("stop_fault"));
                assert_eq!(fake.running, original);
                let restored: Vec<_> = fake.events().into_iter().filter(|e| e.starts_with("restore:")).collect();
                let expected: Vec<_> = ALL.iter().filter(|r| **r > role || (**r == role && confirmed)).map(|r| format!("restore:{}", r.name())).collect();
                assert_eq!(restored, expected);
                assert_eq!(gate.activity().is_ok(),confirmed);
            }
        }
    }

    #[test]
    fn update_lifecycle_preserves_primary_and_recovery_errors_and_keeps_gate_closed() {
        for role in ALL {
            let gate = Arc::new(Gate::default());
            let mut fake = Fake::new(&ALL);
            fake.restore_failure = Some(role);
            let error = coordinated(&gate, &mut fake, || Err("update_start_failed: injected".into())).unwrap_err();
            assert!(error.contains("update_start_failed: injected"));
            assert!(error.contains("update_runtime_recovery_failed"));
            assert!(error.contains(&format!("{}: replay_fault", role.name())));
            assert!(gate.activity().is_err());
        }
        let mut fake = Fake::new(&ALL);
        fake.stop_failure = Some((Role::Worker, true));
        fake.restore_failure = Some(Role::Worker);
        let error = handoff(&mut fake, || panic!("no launch after stop failure")).unwrap_err();
        assert!(error.contains("stop_fault") && error.contains("replay_fault"));
    }

    #[test]
    fn update_lifecycle_rejects_partial_or_unknown_state_before_stop_or_launch() {
        let invalid = [vec![Role::Server], vec![Role::Worker], vec![Role::Server, Role::Worker], vec![Role::Watcher], vec![Role::Postgres, Role::Watcher]];
        for roles in invalid {
            let mut fake = Fake::new(&roles);
            assert_eq!(handoff(&mut fake, || panic!("invalid state cannot launch")), Err("update_runtime_state_unknown".into()));
            assert_eq!(fake.events(), vec!["snapshot"]);
        }
        let mut fake = Fake::new(&ALL);
        fake.probe_failure = true;
        assert_eq!(handoff(&mut fake, || panic!("failed probe cannot launch")), Err("probe_failed".into()));
        assert_eq!(fake.events(), vec!["snapshot"]);
    }

    #[test]
    fn update_lifecycle_success_keeps_terminal_gate_closed_and_never_restores() {
        let gate = Arc::new(Gate::default());
        let mut fake = Fake::new(&ALL);
        coordinated(&gate, &mut fake, || {
            assert!(gate.activity().is_err());
            assert!(gate.lease().is_err());
            Ok(())
        }).unwrap();
        assert!(fake.running.is_empty());
        assert!(!fake.events().iter().any(|e| e.starts_with("restore:")));
        assert!(gate.activity().is_err());
        assert!(gate.lease().is_err());
    }

    #[test]
    fn update_lifecycle_activity_lifetime_includes_already_running_work() {
        let gate = Arc::new(Gate::default());
        let (entered, active) = std::sync::mpsc::channel();
        let (resume, waiting) = std::sync::mpsc::channel();
        let worker_gate = Arc::clone(&gate);
        let thread = std::thread::spawn(move || {
            let _work = worker_gate.activity().unwrap();
            let _nested = worker_gate.activity().unwrap();
            entered.send(()).unwrap();
            waiting.recv().unwrap();
        });
        active.recv().unwrap();
        assert_eq!(update(&gate, || panic!("active external work is not quiescent")), Err("update_runtime_busy".into()));
        resume.send(()).unwrap();
        thread.join().unwrap();
        assert!(gate.lease().is_ok());
    }

    struct DropOwner(Arc<std::sync::atomic::AtomicUsize>);
    impl Drop for DropOwner {
        fn drop(&mut self) { self.0.fetch_add(1, std::sync::atomic::Ordering::SeqCst); }
    }

    #[test]
    fn update_owned_unverified_retains_owner_checkpoint_and_terminal_lease() {
        let gate = Arc::new(Gate::default());
        let mut fake = Fake::new(&ALL);
        let original = fake.running.clone();
        let drops = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let outcome = coordinated_owned(&gate, &mut fake, || Launch::Unverified {
            owner: DropOwner(Arc::clone(&drops)), error:"update_installer_unverified".into(),
        });
        let OwnedHandoff::Unverified { error, stopped, lease, .. } = &outcome else { panic!("owner must be retained"); };
        assert_eq!(error, "update_installer_unverified");
        let retained: BTreeMap<_,_> = stopped.iter().cloned().collect();
        assert_eq!(retained, original);
        assert!(lease.handed_off);
        assert!(fake.running.is_empty());
        assert_eq!(fake.resume_calls, 0);
        assert!(!fake.events().iter().any(|e| e.starts_with("restore:")));
        assert_eq!(drops.load(std::sync::atomic::Ordering::SeqCst), 0);
        let followup = coordinated_owned(&gate, &mut fake, || -> Launch<DropOwner> { panic!("pending owner prevents another launch") });
        assert!(matches!(followup, OwnedHandoff::Unstarted(_)));
        assert!(gate.activity().is_err());
        drop(outcome);
        assert_eq!(drops.load(std::sync::atomic::Ordering::SeqCst), 1);
        assert!(gate.activity().is_err(), "dropping the checkpoint is not recovery evidence");
    }

    #[test]
    fn update_owned_unstarted_uses_confirmed_recovery_and_releases_own_lease() {
        let gate = Arc::new(Gate::default());
        let mut fake = Fake::new(&ALL);
        let original = fake.running.clone();
        let result = coordinated_owned(&gate, &mut fake, || Launch::<DropOwner>::Unstarted("inert_unstarted".into()));
        assert!(matches!(result, OwnedHandoff::Unstarted(ref e) if e == "inert_unstarted"));
        assert_eq!(fake.running, original);
        assert_eq!(fake.resume_calls, 1);
        assert!(gate.activity().is_ok());
    }

    #[test]
    fn update_owned_panic_arms_only_after_read_only_validation() {
        for phase in ["snapshot", "pause", "stop", "launch"] {
            let gate = Arc::new(Gate::default());
            let mut fake = Fake::new(&ALL);
            fake.panic_on = Some(phase);
            let panic = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                coordinated_owned(&gate, &mut fake, || -> Launch<DropOwner> { panic!("inert launch panic") })
            }));
            assert!(panic.is_err());
            assert_eq!(gate.activity().is_ok(), phase == "snapshot");
            assert!(!fake.events().iter().any(|e| e.starts_with("restore:")));
            assert_eq!(fake.resume_calls, 0);
        }
        let gate = Arc::new(Gate::default());
        let mut fake = Fake::new(&[Role::Server]);
        assert!(matches!(coordinated_owned(&gate, &mut fake, || -> Launch<DropOwner> { panic!("invalid snapshot cannot launch") }), OwnedHandoff::Unstarted(_)));
        assert_eq!(fake.pause_calls, 0);
        assert!(gate.activity().is_ok());
    }

    #[test]
    fn update_owned_recovery_preserves_unrelated_blocked_state() {
        let gate = Arc::new(Gate::default());
        let mut fake = Fake::new(&ALL);
        let result = coordinated_owned(&gate, &mut fake, || {
            gate.block();
            Launch::<DropOwner>::Unstarted("inert_unstarted".into())
        });
        assert!(matches!(result, OwnedHandoff::Unstarted(_)));
        let state = gate.0.lock().unwrap();
        assert!(!state.updating, "only the recovered updating lease is released");
        assert!(state.blocked, "recovery must not clear another blocked owner");
    }
}
