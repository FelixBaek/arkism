//! A server key is learned without target credentials, reviewed by the person,
//! and then used as the only host-key source for authenticated SSH commands.
use crate::ssh_exec;
use crate::ssh_private as storage;
use crate::ssh_tunnel::Target;
use base64::Engine;
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::io;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::{Mutex, OnceLock};
use std::time::{Duration, Instant};

const LIMIT: usize = 128 * 1024;
const REVIEW_TTL: Duration = Duration::from_secs(300);
const ALGORITHMS: &str = "ssh-ed25519,rsa-sha2-512,rsa-sha2-256,ecdsa-sha2-nistp256,ecdsa-sha2-nistp384,ecdsa-sha2-nistp521";

fn digest(bytes: &[u8]) -> String {
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}
fn nonce() -> Result<String, String> {
    let mut bytes = [0; 32];
    getrandom::fill(&mut bytes).map_err(|_| "ssh_host_key_probe_failed")?;
    Ok(bytes.iter().map(|b| format!("{b:02x}")).collect())
}
fn store_error(_: io::Error) -> String {
    "ssh_trust_store_failed".into()
}

#[derive(Clone, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
struct Endpoint {
    alias: String,
    host: String,
    port: u16,
    user: String,
}
impl Endpoint {
    fn id(&self) -> String {
        digest(&serde_json::to_vec(self).expect("SSH endpoint serializes"))
    }
}

#[derive(Clone, Debug, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
struct PeerKey {
    kind: String,
    public: String,
}
impl PeerKey {
    fn parse(kind: &str, public: &str) -> Result<Self, String> {
        let key = Self {
            kind: kind.into(),
            public: public.into(),
        };
        key.algorithms()?;
        let wire = base64::engine::general_purpose::STANDARD
            .decode(public)
            .or_else(|_| base64::engine::general_purpose::STANDARD_NO_PAD.decode(public))
            .map_err(|_| "ssh_host_key_probe_failed")?;
        if wire.len() < 12 || wire.len() > 16384 {
            return Err("ssh_host_key_probe_failed".into());
        }
        let length = u32::from_be_bytes(wire[..4].try_into().unwrap()) as usize;
        if length > 128 || wire.get(4..4 + length) != Some(kind.as_bytes()) {
            return Err("ssh_host_key_probe_failed".into());
        }
        Ok(Self {
            kind: kind.into(),
            public: base64::engine::general_purpose::STANDARD.encode(wire),
        })
    }
    fn bytes(&self) -> Vec<u8> {
        base64::engine::general_purpose::STANDARD
            .decode(&self.public)
            .expect("validated SSH public key")
    }
    fn fingerprint(&self) -> String {
        format!(
            "SHA256:{}",
            base64::engine::general_purpose::STANDARD_NO_PAD.encode(Sha256::digest(self.bytes()))
        )
    }
    fn algorithms(&self) -> Result<&'static str, String> {
        match self.kind.as_str() {
            "ssh-ed25519" => Ok("ssh-ed25519"),
            "ssh-rsa" => Ok("rsa-sha2-512,rsa-sha2-256"),
            "ecdsa-sha2-nistp256" => Ok("ecdsa-sha2-nistp256"),
            "ecdsa-sha2-nistp384" => Ok("ecdsa-sha2-nistp384"),
            "ecdsa-sha2-nistp521" => Ok("ecdsa-sha2-nistp521"),
            _ => Err("ssh_host_key_probe_failed".into()),
        }
    }
    fn known_host(&self, alias: &str) -> Vec<u8> {
        format!("{alias} {} {}\n", self.kind, self.public).into_bytes()
    }
}

#[derive(Clone, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Record {
    format: u32,
    endpoint: Endpoint,
    key: PeerKey,
}

/// This is constructed only from the approved store, never from WebView fields.
#[derive(Clone)]
pub struct Pin {
    pub file: PathBuf,
    pub alias: String,
    pub algorithms: String,
    expected: Vec<u8>,
}
impl Pin {
    pub fn apply(&self, command: &mut Command) -> Result<(), String> {
        if storage::read(&self.file, 32768).map_err(store_error)? != self.expected {
            return Err("ssh_trust_store_failed".into());
        }
        let path = known_hosts_option(&self.file)?;
        command.args([
            "-o",
            "StrictHostKeyChecking=yes",
            "-o",
            &path,
            "-o",
            &format!("HostKeyAlias={}", self.alias),
            "-o",
            &format!("HostKeyAlgorithms={}", self.algorithms),
        ]);
        isolation(command);
        Ok(())
    }
}

fn known_hosts_option(path: &Path) -> Result<String, String> {
    let text = path.to_str().ok_or("ssh_trust_store_failed")?;
    if !path.is_absolute()
        || text.chars().any(|c| c.is_control() || c == '"')
        || text.contains("${")
    {
        return Err("ssh_trust_store_failed".into());
    }
    let text = text.replace('\\', "/").replace('%', "%%");
    Ok(format!("UserKnownHostsFile=\"{text}\""))
}

fn isolation(command: &mut Command) {
    command.args([
        "-o",
        "GlobalKnownHostsFile=none",
        "-o",
        "KnownHostsCommand=none",
        "-o",
        "UpdateHostKeys=no",
        "-o",
        "VerifyHostKeyDNS=no",
        "-o",
        "CheckHostIP=no",
    ]);
}

#[derive(Clone)]
pub struct Client {
    program: std::ffi::OsString,
    config: Option<PathBuf>,
}
impl Client {
    pub fn system() -> Self {
        Self {
            program: crate::ssh_tunnel::ssh_program(),
            config: None,
        }
    }
    pub fn command(&self, target: &Target) -> Command {
        let mut command = Command::new(&self.program);
        // A password login needs ssh to ask (askpass); it is answered only after the host is pinned.
        if target.password.is_none() {
            command.args(["-o", "BatchMode=yes"]);
        }
        command.args([
            "-o",
            "ConnectTimeout=10",
            "-o",
            "ServerAliveInterval=15",
            "-o",
            "ServerAliveCountMax=3",
            "-o",
            "ControlMaster=no",
            "-o",
            "ControlPath=none",
            "-o",
            "ControlPersist=no",
            "-o",
            "ForkAfterAuthentication=no",
            "-o",
            "ForwardAgent=no",
            "-o",
            "ForwardX11=no",
            "-o",
            "PermitLocalCommand=no",
            "-o",
            "RemoteCommand=none",
        ]);
        command.env("SSH_ASKPASS_REQUIRE", "never");
        if let Some(config) = &self.config {
            command.arg("-F").arg(config);
        }
        if let Some(port) = target.port {
            command.arg("-p").arg(port.to_string());
        }
        if let Some(key) = &target.key {
            command
                .arg("-i")
                .arg(key)
                .args(["-o", "IdentitiesOnly=yes"]);
        }
        command
    }
    fn endpoint(&self, target: &Target) -> Result<Endpoint, String> {
        let mut command = self.command(target);
        command.arg("-G").arg(&target.host);
        let result = ssh_exec::run(command, Vec::new(), LIMIT, Duration::from_secs(10))?;
        if result.code != 0 {
            return Err("ssh_address_invalid".into());
        }
        let text = std::str::from_utf8(&result.out).map_err(|_| "ssh_address_invalid")?;
        let field = |name: &str| {
            text.lines()
                .find_map(|line| line.strip_prefix(name))
                .map(str::trim)
        };
        let host = field("hostname ")
            .filter(|s| !s.is_empty() && s.len() <= 1024 && !s.chars().any(char::is_control))
            .ok_or("ssh_address_invalid")?;
        let port = field("port ")
            .and_then(|s| s.parse::<u16>().ok())
            .filter(|p| *p != 0)
            .ok_or("ssh_address_invalid")?;
        let user = field("user ")
            .filter(|s| !s.is_empty() && s.len() <= 256 && !s.chars().any(char::is_control))
            .ok_or("ssh_address_invalid")?;
        let alias = target
            .host
            .rsplit('@')
            .next()
            .unwrap_or(&target.host)
            .to_ascii_lowercase();
        Ok(Endpoint {
            alias,
            host: host.to_ascii_lowercase(),
            port,
            user: user.into(),
        })
    }
}

#[derive(serde::Serialize)]
pub struct Review {
    pub state: &'static str,
    pub host: String,
    pub port: u16,
    pub key_type: String,
    pub fingerprint: String,
    pub previous_fingerprint: Option<String>,
    pub challenge: Option<String>,
}

struct Pending {
    root: PathBuf,
    endpoint: Endpoint,
    key: PeerKey,
    previous: Option<Vec<u8>>,
    created: Instant,
}
static REVIEWS: OnceLock<Mutex<HashMap<String, Pending>>> = OnceLock::new();
fn reviews() -> &'static Mutex<HashMap<String, Pending>> {
    REVIEWS.get_or_init(|| Mutex::new(HashMap::new()))
}

#[derive(Clone)]
pub struct Store {
    root: PathBuf,
    client: Client,
}
impl Store {
    pub fn new(data: &Path) -> Self {
        Self {
            root: data.join("ssh-hosts"),
            client: Client::system(),
        }
    }
    fn ready(&self) -> Result<(), String> {
        storage::directory(&self.root).map_err(store_error)
    }
    fn metadata(&self, endpoint: &Endpoint) -> PathBuf {
        self.root.join(format!("{}.json", endpoint.id()))
    }
    fn load(&self, endpoint: &Endpoint) -> Result<Option<(Record, Vec<u8>)>, String> {
        let bytes = match storage::read(&self.metadata(endpoint), 32768) {
            Ok(bytes) => bytes,
            Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
            Err(e) => return Err(store_error(e)),
        };
        let record: Record =
            serde_json::from_slice(&bytes).map_err(|_| "ssh_trust_store_failed")?;
        let key = PeerKey::parse(&record.key.kind, &record.key.public)?;
        if record.format != 1 || record.endpoint != *endpoint || record.key != key {
            return Err("ssh_trust_store_failed".into());
        }
        Ok(Some((record, bytes)))
    }
    fn pin(&self, record: &Record) -> Result<Pin, String> {
        let id = record.endpoint.id();
        let alias = format!("arkism-{id}");
        let expected = record.key.known_host(&alias);
        let file = self
            .root
            .join(format!("{id}.{}.known_hosts", digest(&record.key.bytes())));
        if storage::read(&file, 32768).map_err(store_error)? != expected {
            return Err("ssh_trust_store_failed".into());
        }
        Ok(Pin {
            file,
            alias,
            algorithms: record.key.algorithms()?.into(),
            expected,
        })
    }
    pub fn required(&self, target: &Target) -> Result<Pin, String> {
        self.ready()?;
        let endpoint = self.client.endpoint(target)?;
        let (record, _) = self
            .load(&endpoint)?
            .ok_or("ssh_host_key_confirmation_required")?;
        self.pin(&record)
    }
    fn review(
        endpoint: &Endpoint,
        key: &PeerKey,
        old: Option<&PeerKey>,
        challenge: Option<String>,
    ) -> Review {
        Review {
            state: if challenge.is_none() {
                "trusted"
            } else if old.is_some() {
                "changed"
            } else {
                "unknown"
            },
            host: endpoint.host.clone(),
            port: endpoint.port,
            key_type: key.kind.clone(),
            fingerprint: key.fingerprint(),
            previous_fingerprint: old.map(PeerKey::fingerprint),
            challenge,
        }
    }
    pub fn inspect(&self, target: &Target, force: bool) -> Result<Review, String> {
        self.ready()?;
        let endpoint = self.client.endpoint(target)?;
        let previous = self.load(&endpoint)?;
        if !force {
            if let Some((record, _)) = &previous {
                self.pin(record)?;
                return Ok(Self::review(&endpoint, &record.key, None, None));
            }
        }
        let key = self.probe(target, &endpoint, previous.as_ref().map(|(r, _)| &r.key))?;
        if let Some((record, _)) = &previous {
            if record.key == key {
                self.pin(record)?;
                return Ok(Self::review(&endpoint, &key, None, None));
            }
        }
        let challenge = nonce()?;
        let mut pending = reviews().lock().map_err(|_| "ssh_host_key_probe_failed")?;
        pending.retain(|_, p| p.created.elapsed() < REVIEW_TTL);
        if pending.len() >= 16 {
            return Err("ssh_host_key_review_busy".into());
        }
        pending.insert(
            challenge.clone(),
            Pending {
                root: self.root.clone(),
                endpoint: endpoint.clone(),
                key: key.clone(),
                previous: previous.as_ref().map(|(_, bytes)| bytes.clone()),
                created: Instant::now(),
            },
        );
        Ok(Self::review(
            &endpoint,
            &key,
            previous.as_ref().map(|(r, _)| &r.key),
            Some(challenge),
        ))
    }
    fn probe(
        &self,
        target: &Target,
        endpoint: &Endpoint,
        previous: Option<&PeerKey>,
    ) -> Result<PeerKey, String> {
        let file = self.root.join(format!("probe-{}.known_hosts", nonce()?));
        storage::create(&file, b"").map_err(store_error)?;
        let alias = format!("arkism-{}", endpoint.id());
        let result = (|| {
            let mut command = self.client.command(target);
            let path = known_hosts_option(&file)?;
            let algorithms = previous
                .map(PeerKey::algorithms)
                .transpose()?
                .map(|a| format!("{a},{ALGORITHMS}"))
                .unwrap_or(ALGORITHMS.into());
            command.args([
                "-o",
                &path,
                "-o",
                &format!("HostKeyAlias={alias}"),
                "-o",
                &format!("HostKeyAlgorithms={algorithms}"),
                "-o",
                "StrictHostKeyChecking=accept-new",
                "-o",
                "HashKnownHosts=no",
                "-o",
                "PreferredAuthentications=none",
                "-o",
                "PubkeyAuthentication=no",
                "-o",
                "PasswordAuthentication=no",
                "-o",
                "KbdInteractiveAuthentication=no",
                "-o",
                "HostbasedAuthentication=no",
                "-o",
                "GSSAPIAuthentication=no",
                "-o",
                "IdentityAgent=none",
                "-o",
                "AddKeysToAgent=no",
                "-o",
                "NumberOfPasswordPrompts=0",
                "-o",
                "ClearAllForwardings=yes",
                "-N",
            ]);
            isolation(&mut command);
            command.arg(&target.host);
            match ssh_exec::run(command, Vec::new(), LIMIT, Duration::from_secs(12)) {
                Ok(_) => {}
                Err(e) if e == "ssh_unreachable" => {}
                Err(e) => return Err(e),
            }
            let bytes = storage::read(&file, 32768).map_err(|_| "ssh_host_key_probe_failed")?;
            let text = std::str::from_utf8(&bytes).map_err(|_| "ssh_host_key_probe_failed")?;
            let fields: Vec<_> = text.split_whitespace().collect();
            if fields.len() != 3 || fields[0] != alias {
                return Err("ssh_host_key_probe_failed".into());
            }
            PeerKey::parse(fields[1], fields[2])
        })();
        // Only the generated file inside this already verified private directory.
        let cleanup = std::fs::remove_file(&file).map_err(store_error);
        match (result, cleanup) {
            (Ok(key), Ok(())) => Ok(key),
            (Err(e), _) => Err(e),
            (_, Err(e)) => Err(e),
        }
    }
    pub fn approve(&self, challenge: &str, replace: bool) -> Result<Review, String> {
        self.approve_at(challenge, replace, Instant::now())
    }
    fn approve_at(&self, challenge: &str, replace: bool, now: Instant) -> Result<Review, String> {
        let pending = reviews()
            .lock()
            .map_err(|_| "ssh_host_key_review_busy")?
            .remove(challenge)
            .ok_or("ssh_host_key_review_expired")?;
        if pending.root != self.root || now.saturating_duration_since(pending.created) >= REVIEW_TTL
        {
            return Err("ssh_host_key_review_expired".into());
        }
        self.ready()?;
        let _lock = storage::lock(&self.root.join(format!("{}.lock", pending.endpoint.id())))
            .map_err(|_| "ssh_host_key_review_busy")?;
        let current = self.load(&pending.endpoint)?;
        if current.as_ref().map(|(_, b)| b) != pending.previous.as_ref() {
            return Err("ssh_host_key_review_stale".into());
        }
        if current.is_some() && !replace {
            return Err("ssh_host_key_change_confirmation_required".into());
        }
        let record = Record {
            format: 1,
            endpoint: pending.endpoint,
            key: pending.key,
        };
        let id = record.endpoint.id();
        let host_file = self
            .root
            .join(format!("{id}.{}.known_hosts", digest(&record.key.bytes())));
        let expected = record.key.known_host(&format!("arkism-{id}"));
        match storage::create(&host_file, &expected) {
            Ok(()) => {}
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => {
                if storage::read(&host_file, 32768).map_err(store_error)? != expected {
                    return Err("ssh_trust_store_failed".into());
                }
            }
            Err(e) => return Err(store_error(e)),
        }
        let temporary = self.root.join(format!("approval-{}.tmp", nonce()?));
        let bytes = serde_json::to_vec(&record).map_err(|_| "ssh_trust_store_failed")?;
        storage::create(&temporary, &bytes).map_err(store_error)?;
        if let Err(e) = std::fs::rename(&temporary, self.metadata(&record.endpoint)) {
            let _ = std::fs::remove_file(temporary);
            return Err(store_error(e));
        }
        Ok(Self::review(&record.endpoint, &record.key, None, None))
    }
    pub fn cancel(&self, challenge: &str) {
        if let Ok(mut values) = reviews().lock() {
            if values.get(challenge).is_some_and(|p| p.root == self.root) {
                values.remove(challenge);
            }
        }
    }
    pub fn command(&self, target: &Target, pin: &Pin) -> Result<Command, String> {
        self.ready()?;
        let mut command = self.client.command(target);
        pin.apply(&mut command)?;
        if let Some(password) = &target.password {
            crate::ssh_tunnel::password_login(&mut command, password);
        }
        Ok(command)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    struct Fixture {
        parent: PathBuf,
        owner: String,
        store: Store,
    }
    impl Fixture {
        fn new() -> Self {
            let owner = nonce().unwrap();
            let parent = std::env::temp_dir().join(format!("ssh-pin-{}", &owner[..16]));
            std::fs::create_dir(&parent).unwrap();
            storage::create(&parent.join("TASK-OWNER"), owner.as_bytes()).unwrap();
            let store = Store {
                root: parent.join("ssh-hosts"),
                client: Client {
                    program: "inert-not-run".into(),
                    config: None,
                },
            };
            store.ready().unwrap();
            Self {
                parent,
                owner,
                store,
            }
        }
        fn pending(
            &self,
            endpoint: &Endpoint,
            key: PeerKey,
            previous: Option<Vec<u8>>,
            created: Instant,
        ) -> String {
            let challenge = nonce().unwrap();
            reviews().lock().unwrap().insert(
                challenge.clone(),
                Pending {
                    root: self.store.root.clone(),
                    endpoint: endpoint.clone(),
                    key,
                    previous,
                    created,
                },
            );
            challenge
        }
    }
    impl Drop for Fixture {
        fn drop(&mut self) {
            reviews()
                .lock()
                .unwrap()
                .retain(|_, p| p.root != self.store.root);
            assert_eq!(
                storage::read(&self.parent.join("TASK-OWNER"), 128).unwrap(),
                self.owner.as_bytes()
            );
            fn tree(path: &Path) {
                storage::ordinary(path, true).unwrap();
                for entry in std::fs::read_dir(path).unwrap() {
                    let entry = entry.unwrap();
                    if entry.file_type().unwrap().is_dir() {
                        tree(&entry.path());
                    } else {
                        storage::ordinary(&entry.path(), false).unwrap();
                    }
                }
            }
            tree(&self.parent);
            println!(
                "Only owned SSH test cleanup root: {}",
                self.parent.display()
            );
            std::fs::remove_dir_all(&self.parent).unwrap();
        }
    }
    fn endpoint() -> Endpoint {
        Endpoint {
            alias: "fixture".into(),
            host: "127.0.0.1".into(),
            port: 2200,
            user: "fixture".into(),
        }
    }
    fn key(byte: u8) -> PeerKey {
        let mut bytes = Vec::new();
        bytes.extend_from_slice(&11u32.to_be_bytes());
        bytes.extend_from_slice(b"ssh-ed25519");
        bytes.extend_from_slice(&32u32.to_be_bytes());
        bytes.extend_from_slice(&[byte; 32]);
        PeerKey::parse(
            "ssh-ed25519",
            &base64::engine::general_purpose::STANDARD.encode(bytes),
        )
        .unwrap()
    }
    #[test]
    fn peer_wire_type_and_base64_are_checked_before_fingerprinting() {
        let k = key(42);
        assert!(k.fingerprint().starts_with("SHA256:"));
        assert!(!k.fingerprint().ends_with('='));
        assert_eq!(k.fingerprint().len(), 50);
        assert_ne!(k.fingerprint(), key(43).fingerprint());
        for (kind, public) in [
            ("ssh-rsa", k.public.as_str()),
            ("ssh-dss", k.public.as_str()),
            ("ssh-ed25519", "x\n"),
            ("ssh-ed25519", "AAAA"),
        ] {
            assert!(PeerKey::parse(kind, public).is_err());
        }
    }
    #[test]
    fn approval_is_one_use_and_cancel_does_not_create_a_trust_record() {
        let f = Fixture::new();
        let ep = endpoint();
        let challenge = f.pending(&ep, key(1), None, Instant::now());
        f.store.cancel(&challenge);
        assert!(f.store.approve(&challenge, false).is_err());
        assert!(f.store.load(&ep).unwrap().is_none());
        let challenge = f.pending(&ep, key(1), None, Instant::now());
        assert_eq!(f.store.approve(&challenge, false).unwrap().state, "trusted");
        let before = f.store.load(&ep).unwrap().unwrap().1;
        assert!(f.store.approve(&challenge, false).is_err());
        assert_eq!(f.store.load(&ep).unwrap().unwrap().1, before);
    }
    #[test]
    fn expired_and_stale_reviews_cannot_create_or_replace_approvals() {
        let f = Fixture::new();
        let ep = endpoint();
        let created = Instant::now();
        let expired = f.pending(&ep, key(2), None, created);
        assert!(f
            .store
            .approve_at(&expired, false, created + REVIEW_TTL)
            .is_err());
        assert!(f.store.load(&ep).unwrap().is_none());
        let stale = f.pending(&ep, key(3), None, created);
        let first = f.pending(&ep, key(2), None, created);
        f.store.approve(&first, false).unwrap();
        assert!(f.store.approve(&stale, true).is_err());
        assert_eq!(f.store.load(&ep).unwrap().unwrap().0.key, key(2));
    }
    #[test]
    fn rotation_requires_explicit_confirmation_and_keeps_active_pin_immutable() {
        let f = Fixture::new();
        let ep = endpoint();
        let first = f.pending(&ep, key(4), None, Instant::now());
        f.store.approve(&first, false).unwrap();
        let (old, bytes) = f.store.load(&ep).unwrap().unwrap();
        let active = f.store.pin(&old).unwrap();
        let denied = f.pending(&ep, key(5), Some(bytes.clone()), Instant::now());
        assert!(f.store.approve(&denied, false).is_err());
        assert_eq!(f.store.load(&ep).unwrap().unwrap().1, bytes);
        let next = f.pending(&ep, key(5), Some(bytes), Instant::now());
        f.store.approve(&next, true).unwrap();
        let (new, _) = f.store.load(&ep).unwrap().unwrap();
        assert_eq!(new.key, key(5));
        assert_ne!(active.file, f.store.pin(&new).unwrap().file);
        let mut command = Command::new("inert-not-run");
        active.apply(&mut command).unwrap();
        let args: Vec<_> = command
            .get_args()
            .map(|v| v.to_string_lossy().into_owned())
            .collect();
        for required in [
            "StrictHostKeyChecking=yes",
            "GlobalKnownHostsFile=none",
            "KnownHostsCommand=none",
            "UpdateHostKeys=no",
            "VerifyHostKeyDNS=no",
        ] {
            assert!(args.iter().any(|a| a == required));
        }
        assert!(!args.iter().any(|a| a.contains("accept-new")));
    }
    #[test]
    fn altered_pin_or_record_fails_closed_and_user_known_hosts_is_untouched() {
        let f = Fixture::new();
        let ep = endpoint();
        let unrelated = f.parent.join("unrelated-known-hosts");
        storage::create(&unrelated, b"keep user state").unwrap();
        let first = f.pending(&ep, key(7), None, Instant::now());
        f.store.approve(&first, false).unwrap();
        let (record, _) = f.store.load(&ep).unwrap().unwrap();
        let pin = f.store.pin(&record).unwrap();
        std::fs::write(&pin.file, b"attacker key").unwrap();
        assert!(pin.apply(&mut Command::new("inert-not-run")).is_err());
        std::fs::write(f.store.metadata(&ep), b"{bad}").unwrap();
        assert!(f.store.load(&ep).is_err());
        assert_eq!(storage::read(&unrelated, 64).unwrap(), b"keep user state");
    }
    #[test]
    fn store_rejects_an_existing_directory_without_its_owner_only_policy() {
        let owner = nonce().unwrap();
        let root = std::env::temp_dir().join(format!("ssh-foreign-{}", &owner[..16]));
        std::fs::create_dir(&root).unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&root, std::fs::Permissions::from_mode(0o755)).unwrap();
        }
        assert!(storage::directory(&root).is_err());
        println!(
            "Only owned foreign-policy fixture cleanup: {}",
            root.display()
        );
        std::fs::remove_dir(&root).unwrap();
    }
    /// Real OpenSSH handshakes against a fresh loopback-only Go server, with its
    /// own -F config and client key. No user SSH files or external hosts.
    #[test]
    #[cfg(windows)]
    #[ignore = "requires ARKISM_SSH_FIXTURE pointing to the inert Windows fixture"]
    fn native_openssh_approval_and_changed_key_roundtrip() {
        let mut f = Fixture::new();
        let server_root = f.parent.join("fixture with spaces % path");
        storage::directory(&server_root).unwrap();
        storage::create(&server_root.join("TASK-OWNER"), b"arkism-ssh-fixture-v1").unwrap();
        let mut command = Command::new(
            std::env::var_os("ARKISM_SSH_FIXTURE").expect("owned SSH fixture executable"),
        );
        command.arg(&server_root);
        let mut server = crate::ssh_exec::Process::spawn(command).unwrap();
        drop(server.input());
        drop(server.output());
        let errors = crate::ssh_exec::read_pipe(server.errors().unwrap(), 16384);
        let deadline = Instant::now() + Duration::from_secs(10);
        let ready_file = server_root.join("ready.json");
        while !ready_file.exists() {
            assert!(Instant::now() < deadline, "fixture startup timed out");
            assert!(
                server.status().unwrap().is_none(),
                "fixture exited: {:?}",
                errors.try_recv()
            );
            std::thread::sleep(Duration::from_millis(20));
        }
        let ready: serde_json::Value =
            serde_json::from_slice(&storage::read(&ready_file, 8192).unwrap()).unwrap();
        f.store.client = Client {
            program: crate::ssh_tunnel::ssh_program(),
            config: Some(server_root.join("config")),
        };
        let target = crate::ssh_tunnel::parse_target("fixture").unwrap();
        let auth_count = || {
            std::fs::read_to_string(server_root.join("auth.log"))
                .unwrap_or_default()
                .lines()
                .count()
        };
        assert!(
            matches!(f.store.required(&target), Err(e) if e == "ssh_host_key_confirmation_required")
        );
        let cancelled = f.store.inspect(&target, false).unwrap();
        assert_eq!(cancelled.fingerprint, ready["first"].as_str().unwrap());
        assert_eq!(cancelled.port as u64, ready["port"].as_u64().unwrap());
        assert_eq!(auth_count(), 0, "probe offered a target credential");
        f.store.cancel(cancelled.challenge.as_deref().unwrap());
        assert!(f.store.required(&target).is_err());
        let first = f.store.inspect(&target, false).unwrap();
        f.store
            .approve(first.challenge.as_deref().unwrap(), false)
            .unwrap();
        let pin = f.store.required(&target).unwrap();
        let run = |pin: &Pin| {
            let mut command = f.store.command(&target, pin).unwrap();
            command.args(["-T", "fixture", "owned-fixture"]);
            ssh_exec::run(command, Vec::new(), 32768, Duration::from_secs(10)).unwrap()
        };
        let first_result = run(&pin);
        assert_eq!(
            first_result.code,
            0,
            "{}",
            String::from_utf8_lossy(&first_result.err)
        );
        assert_eq!(
            serde_json::from_slice::<serde_json::Value>(&first_result.out).unwrap()["fixture"],
            true
        );
        let before_rotation = auth_count();
        assert!(before_rotation > 0);
        storage::create(&server_root.join("rotate"), b"owned key rotation").unwrap();
        let blocked = run(&pin);
        assert_ne!(blocked.code, 0);
        assert_eq!(
            auth_count(),
            before_rotation,
            "changed key reached credential authentication"
        );
        let changed = f.store.inspect(&target, true).unwrap();
        assert_eq!(changed.state, "changed");
        assert_eq!(changed.fingerprint, ready["second"].as_str().unwrap());
        assert_eq!(
            changed.previous_fingerprint.as_deref(),
            ready["first"].as_str()
        );
        assert_eq!(auth_count(), before_rotation);
        f.store
            .approve(changed.challenge.as_deref().unwrap(), true)
            .unwrap();
        let next = f.store.required(&target).unwrap();
        assert_eq!(run(&next).code, 0);
        assert_ne!(
            run(&pin).code,
            0,
            "active immutable old pin silently rotated"
        );
        server.stop().unwrap();
    }
}
