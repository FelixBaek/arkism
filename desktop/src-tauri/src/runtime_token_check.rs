//! Real CLI/database checks confined to a fresh, explicitly isolated diagnostic
//! directory. No IPC command exposes these destructive fixture operations.
use super::*;

pub(crate) const MARKER: &str = "arkism-fresh-runtime-check-v2";

fn ordinary_ancestor(path: &Path) -> Result<(), String> {
    let info = match fs::symlink_metadata(path) {
        Ok(info) => info,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(_) => return Err("runtime_check_directory_inspection_failed".into()),
    };
    #[cfg(windows)]
    {
        use std::os::windows::fs::MetadataExt;
        require(
            info.file_attributes() & 0x400 == 0,
            "runtime_check_reparse_directory",
        )?;
    }
    require(
        !info.file_type().is_symlink() && info.is_dir(),
        "runtime_check_ordinary_directory_required",
    )
}

pub(crate) fn fresh_directory(path: &Path) -> Result<(), String> {
    if !path.is_absolute() {
        return Err("runtime_check_absolute_directory_required".into());
    }
    for ancestor in path.ancestors() {
        ordinary_ancestor(ancestor)?;
    }
    if path.exists()
        && fs::read_dir(path)
            .map_err(|_| "runtime_check_directory_inspection_failed")?
            .next()
            .is_some()
    {
        return Err("runtime_check_fresh_directory_required".into());
    }
    fs::create_dir_all(path).map_err(|_| "runtime_check_directory_create_failed")?;
    use std::io::Write;
    let mut marker = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path.join(".runtime-check-owned"))
        .map_err(|_| "runtime_check_marker_create_failed")?;
    marker
        .write_all(MARKER.as_bytes())
        .map_err(|_| "runtime_check_marker_write_failed")?;
    marker
        .sync_all()
        .map_err(|_| "runtime_check_marker_write_failed".to_string())
}

fn require(ok: bool, code: &str) -> Result<(), String> {
    if ok {
        Ok(())
    } else {
        Err(code.into())
    }
}

fn capabilities(server: &str, token: &str, expected: u16) -> Result<(), String> {
    let reply = http_agent(Duration::from_secs(5))
        .get(&format!("{server}/v1/capabilities?workspace_id=default&project_id=default&user_id=management%3Aowner"))
        .header("Authorization", &format!("Bearer {token}"))
        .config().http_status_as_error(false).build().call().map_err(|_| "token_check_http_unavailable")?;
    require(
        reply.status().as_u16() == expected,
        "token_check_http_status",
    )
}

impl Runtime {
    fn pending_token_fixture(
        &self,
        principal: ManagedPrincipal,
        commit: bool,
    ) -> Result<(), String> {
        let mut secrets = self.secrets()?;
        let old = principal.current(&secrets).to_owned();
        let next = secret()?;
        principal.stage(&mut secrets, next.clone());
        self.save(&secrets)?;
        if commit {
            let url = self.url("arkism_init", &secrets.init, secrets.pg_port);
            let mut cmd = Command::new(self.programs()?.join(exe("memory-principal")));
            quiet(&mut cmd)
                .args(["--rotate-token", "--principal", principal.id()])
                .env("MEMORY_DATABASE_URL", url)
                .env("MEMORY_PRINCIPAL_TOKEN", next)
                .env("MEMORY_PREVIOUS_PRINCIPAL_TOKEN", old);
            self.host_status(&mut cmd, "token_check_cli_rotation_failed")?;
        }
        Ok(())
    }

    pub(crate) fn check_managed_tokens(
        &self,
        initial: &Started,
    ) -> Result<(Started, serde_json::Value), String> {
        require(self.home.is_none(), "token_check_requires_isolation")?;
        require(
            fs::read(self.data.join(".runtime-check-owned"))
                .map_err(|_| "token_check_requires_fresh_marker")?
                == MARKER.as_bytes(),
            "token_check_requires_fresh_marker",
        )?;
        let original = self.secrets()?;
        run(self.psql(&original, DATABASE)?.args(["-c",
            "UPDATE arkism.principal_token_history SET issued_at=statement_timestamp()-interval '80 days',expires_at=statement_timestamp()+interval '1 hour' WHERE token_sha256 IN (SELECT token_sha256 FROM arkism.principals WHERE id IN ('management:owner','worker'))"]), "token_check_age_failed")?;
        let rotated = self.start()?;
        let fresh = self.secrets()?;
        require(
            rotated.token != original.owner && fresh.worker != original.worker,
            "token_check_expiry_not_rotated",
        )?;
        capabilities(&rotated.server, &original.owner, 403)?;
        capabilities(&rotated.server, &original.worker, 403)?;
        capabilities(&rotated.server, &fresh.owner, 200)?;
        capabilities(&rotated.server, &fresh.worker, 200)?;

        // Reconstruct persisted states on either side of a process crash. This
        // does not simulate a whole-machine crash or a power failure.
        self.pending_token_fixture(ManagedPrincipal::Worker, false)?;
        let before_commit = self.secrets()?;
        let resumed = self.start()?;
        let after_commit = self.secrets()?;
        require(
            after_commit.pending_worker.is_none()
                && after_commit.worker
                    == before_commit
                        .pending_worker
                        .ok_or("token_check_pending_missing")?,
            "token_check_pending_before_commit",
        )?;
        capabilities(&resumed.server, &before_commit.worker, 403)?;
        capabilities(&resumed.server, &after_commit.worker, 200)?;

        self.pending_token_fixture(ManagedPrincipal::Owner, true)?;
        let pending = self.secrets()?;
        let backups = self.data.join("token-backups");
        let backup = self.backup_into(&backups)?;
        let info: BackupInfo = serde_json::from_slice(
            &fs::read(backups.join(&backup.name).join("backup.json"))
                .map_err(|_| "token_check_backup_read")?,
        )
        .map_err(|_| "token_check_backup_metadata")?;
        require(
            info.format == 2
                && info.pending_owner == pending.pending_owner
                && info.owner == pending.owner,
            "token_check_pending_backup",
        )?;
        self.erase()?;
        let restored = self.restore_from(&backups, &backup.name)?;
        let complete = self.secrets()?;
        require(
            complete.pending_owner.is_none()
                && complete.owner == pending.pending_owner.ok_or("token_check_pending_missing")?,
            "token_check_pending_after_commit",
        )?;
        capabilities(&restored.started.server, &pending.owner, 403)?;
        capabilities(&restored.started.server, &complete.owner, 200)?;
        capabilities(&restored.started.server, &complete.worker, 200)?;
        require(
            restored.started.server.starts_with("http://127.0.0.1:")
                && initial.server.starts_with("http://127.0.0.1:"),
            "token_check_loopback_required",
        )?;
        Ok((
            restored.started,
            serde_json::json!({"expiry_rotation":true,"pending_before_commit":true,
            "pending_after_commit":true,"pending_backup_restore":true,"retired_token_http_403":true}),
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn runtime_check_refuses_existing_data_and_relative_paths() {
        assert!(fresh_directory(Path::new("relative-check")).is_err());
        let root = std::env::temp_dir().join(format!("arkism-fresh-check-{}", secret().unwrap()));
        fs::create_dir_all(&root).unwrap();
        fs::write(root.join("keep"), b"existing data").unwrap();
        assert!(fresh_directory(&root).is_err());
        assert_eq!(fs::read(root.join("keep")).unwrap(), b"existing data");
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn runtime_check_owns_only_a_fresh_directory() {
        let root = std::env::temp_dir().join(format!("arkism-fresh-check-{}", secret().unwrap()));
        fresh_directory(&root).unwrap();
        assert_eq!(
            fs::read(root.join(".runtime-check-owned")).unwrap(),
            MARKER.as_bytes()
        );
        assert!(fresh_directory(&root).is_err());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn runtime_check_refuses_linked_ancestors_without_writing_target() {
        let root = std::env::temp_dir().join(format!("arkism-fresh-check-{}", secret().unwrap()));
        let target = root.join("physical");
        let link = root.join("link");
        fs::create_dir_all(&target).unwrap();
        #[cfg(unix)]
        std::os::unix::fs::symlink(&target, &link).unwrap();
        #[cfg(windows)]
        {
            use base64::Engine;
            let script=format!("$ErrorActionPreference='Stop'; New-Item -ItemType Junction -Path '{}' -Target '{}' | Out-Null",link.display().to_string().replace('\'',"''"),target.display().to_string().replace('\'',"''"));
            let encoded = base64::engine::general_purpose::STANDARD.encode(
                script
                    .encode_utf16()
                    .flat_map(u16::to_le_bytes)
                    .collect::<Vec<_>>(),
            );
            let shell = PathBuf::from(std::env::var_os("SystemRoot").unwrap())
                .join("System32/WindowsPowerShell/v1.0/powershell.exe");
            assert!(
                Command::new(shell)
                    .args(["-NoProfile", "-NonInteractive", "-EncodedCommand", &encoded])
                    .output()
                    .unwrap()
                    .status
                    .success(),
                "junction fixture creation failed"
            );
        }
        assert!(fresh_directory(&link.join("new")).is_err());
        assert_eq!(fs::read_dir(&target).unwrap().count(), 0);
        #[cfg(windows)]
        fs::remove_dir(&link).unwrap();
        #[cfg(unix)]
        fs::remove_file(&link).unwrap();
        fs::remove_dir_all(root).unwrap();
    }
}
