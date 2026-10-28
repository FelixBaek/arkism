//! Test-only entry point for an authenticated published-installer upgrade fixture.
//! It cannot be invoked through the product CLI or IPC.
use super::{identity, loopback, request, snapshot, DurableFixture, Runtime, QUERY};
use serde_json::{json, Value};
use std::fs::{self, OpenOptions};
use std::io::{Read, Write};
use std::path::{Path, PathBuf};

fn require(ok: bool, code: &'static str) -> Result<(), String> {
    if ok {
        Ok(())
    } else {
        Err(code.into())
    }
}

fn hex(value: &str, length: usize) -> bool {
    value.len() == length
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn environment(name: &str) -> Result<String, String> {
    std::env::var(name).map_err(|_| "upgrade_fixture_environment_missing".into())
}

fn ordinary(path: &Path, directory: bool) -> Result<(), String> {
    let info = fs::symlink_metadata(path).map_err(|_| "upgrade_fixture_path_inspection")?;
    #[cfg(windows)]
    {
        use std::os::windows::fs::MetadataExt;
        require(
            info.file_attributes() & 0x400 == 0,
            "upgrade_fixture_reparse",
        )?;
    }
    require(
        !info.file_type().is_symlink()
            && if directory {
                info.is_dir()
            } else {
                info.is_file()
            },
        "upgrade_fixture_path_type",
    )
}

fn directory_chain(path: &Path) -> Result<(), String> {
    require(path.is_absolute(), "upgrade_fixture_absolute_path")?;
    for ancestor in path.ancestors() {
        ordinary(ancestor, true)?;
    }
    Ok(())
}

fn ordinary_tree(root: &Path) -> Result<(), String> {
    directory_chain(root)?;
    let mut pending = vec![root.to_owned()];
    let mut count = 0;
    while let Some(directory) = pending.pop() {
        for entry in fs::read_dir(directory).map_err(|_| "upgrade_fixture_tree_read")? {
            let path = entry.map_err(|_| "upgrade_fixture_tree_read")?.path();
            let info =
                fs::symlink_metadata(&path).map_err(|_| "upgrade_fixture_path_inspection")?;
            ordinary(&path, info.is_dir())?;
            count += 1;
            require(count <= 100000, "upgrade_fixture_tree_limit")?;
            if info.is_dir() {
                pending.push(path);
            }
        }
    }
    Ok(())
}

fn read_small(path: &Path, limit: u64) -> Result<Vec<u8>, String> {
    ordinary(path, false)?;
    let file = fs::File::open(path).map_err(|_| "upgrade_fixture_read")?;
    let mut bytes = Vec::new();
    file.take(limit + 1)
        .read_to_end(&mut bytes)
        .map_err(|_| "upgrade_fixture_read")?;
    require(bytes.len() as u64 <= limit, "upgrade_fixture_read_limit")?;
    Ok(bytes)
}

fn validate_hosted(
    actions: &str,
    runner: &str,
    os: &str,
    repository: &str,
    source: &str,
    requested: &str,
    compiled: Option<&str>,
) -> Result<(), String> {
    require(
        actions == "true"
            && runner == "github-hosted"
            && os == "Windows"
            && repository == "FelixBaek/arkism",
        "upgrade_fixture_hosted_windows_required",
    )?;
    require(
        hex(source, 40) && source == requested && compiled == Some(source),
        "upgrade_fixture_source_binding",
    )
}

struct OwnedUpgrade {
    root: PathBuf,
    data: PathBuf,
    programs: PathBuf,
    expected: PathBuf,
    result: PathBuf,
    owner: String,
    source: String,
}

impl OwnedUpgrade {
    fn from_environment() -> Result<Self, String> {
        require(cfg!(windows), "upgrade_fixture_windows_required")?;
        let source = environment("GITHUB_SHA")?;
        validate_hosted(
            &environment("GITHUB_ACTIONS")?,
            &environment("RUNNER_ENVIRONMENT")?,
            &environment("RUNNER_OS")?,
            &environment("GITHUB_REPOSITORY")?,
            &source,
            &environment("ARKISM_UPGRADE_SOURCE_SHA")?,
            option_env!("GITHUB_SHA"),
        )?;
        let temp = PathBuf::from(environment("RUNNER_TEMP")?);
        let root = PathBuf::from(environment("ARKISM_UPGRADE_ROOT")?);
        let owner = environment("ARKISM_UPGRADE_OWNER")?;
        directory_chain(&temp)?;
        directory_chain(&root)?;
        let parent = root.parent().ok_or("upgrade_fixture_root_scope")?;
        require(
            fs::canonicalize(parent).map_err(|_| "upgrade_fixture_root_scope")?
                == fs::canonicalize(&temp).map_err(|_| "upgrade_fixture_root_scope")?
                && hex(&owner, 32)
                && root.file_name().and_then(|name| name.to_str())
                    == Some(format!("arkism version-upgrade {owner}").as_str()),
            "upgrade_fixture_root_scope",
        )?;
        let fixture = Self {
            data: root.join("retained data").join("runtime data"),
            programs: root.join("installed Arkism").join("runtime"),
            expected: root.join("upgrade checks").join("legacy-fixture.json"),
            result: root.join("upgrade checks").join("candidate-result.json"),
            root,
            owner,
            source,
        };
        fixture.assert_owned()?;
        Ok(fixture)
    }

    fn assert_owned(&self) -> Result<(), String> {
        directory_chain(&self.root)?;
        require(
            read_small(&self.root.join(".installed-smoke-owner"), 32)? == self.owner.as_bytes(),
            "upgrade_fixture_owner_changed",
        )?;
        ordinary_tree(&self.data)?;
        ordinary_tree(&self.programs)?;
        directory_chain(self.expected.parent().ok_or("upgrade_fixture_parent")?)?;
        require(!self.result.exists(), "upgrade_fixture_result_not_fresh")?;
        // Opening a previously running cluster could reconnect to an unrelated
        // process. This fixture only accepts the stopped, retained synthetic DB.
        require(
            fs::symlink_metadata(self.data.join("pg/postmaster.pid"))
                .is_err_and(|err| err.kind() == std::io::ErrorKind::NotFound),
            "upgrade_fixture_database_not_stopped",
        )?;
        require(
            read_small(&self.data.join("pg/PG_VERSION"), 8)?.as_slice() == b"17\n"
                || read_small(&self.data.join("pg/PG_VERSION"), 8)?.as_slice() == b"17\r\n",
            "upgrade_fixture_legacy_pg_version",
        )?;
        for name in [
            "memoryd.exe",
            "memory-principal.exe",
            "roles.sql",
            "pgsql/bin/postgres.exe",
            "pgsql/bin/pg_ctl.exe",
            "pgsql/bin/initdb.exe",
            "pgsql/bin/psql.exe",
        ] {
            let file = self.programs.join(name);
            directory_chain(file.parent().ok_or("upgrade_fixture_parent")?)?;
            ordinary(&file, false)?;
        }
        let models: Value =
            serde_json::from_slice(&read_small(&self.data.join("models.json"), 65536)?)
                .map_err(|_| "upgrade_fixture_models_json")?;
        require(
            models["embedder"] == "none" && models["judge"] == "fixed",
            "upgrade_fixture_external_models_disabled",
        )?;
        let settings: Value =
            serde_json::from_slice(&read_small(&self.data.join("runtime.json"), 65536)?)
                .map_err(|_| "upgrade_fixture_settings_json")?;
        require(
            settings["registered"] == true,
            "upgrade_fixture_registered_profile",
        )?;
        Ok(())
    }

    fn assert_ports_free(&self) -> Result<(), String> {
        let settings: Value =
            serde_json::from_slice(&read_small(&self.data.join("runtime.json"), 65536)?)
                .map_err(|_| "upgrade_fixture_settings_json")?;
        for key in ["pg_port", "api_port", "gateway_port"] {
            let port = match settings.get(key) {
                Some(value) => value.as_u64().ok_or("upgrade_fixture_settings_port")?,
                None if key == "gateway_port" => 0, // Same legacy default as Runtime::Secrets.
                None => return Err("upgrade_fixture_settings_port".into()),
            };
            require(
                port <= u16::MAX as u64 && (port > 0 || key == "gateway_port"),
                "upgrade_fixture_settings_port",
            )?;
            if port != 0 {
                require(
                    std::net::TcpListener::bind(("127.0.0.1", port as u16)).is_ok(),
                    "upgrade_fixture_port_already_in_use",
                )?;
            }
        }
        Ok(())
    }
}

fn existing_upgrade() -> Result<(), String> {
    let owned = OwnedUpgrade::from_environment()?;
    let expected: Value = serde_json::from_slice(&read_small(&owned.expected, 256 << 10)?)
        .map_err(|_| "upgrade_fixture_expected_json")?;
    let fixture = DurableFixture {
        source_id: identity(&expected["source"])?,
        memory_id: identity(&expected["memory"])?,
        snapshot: snapshot(&expected["source"], &expected["memory"])?,
    };
    owned.assert_owned()?;
    owned.assert_ports_free()?;
    // The normal runtime startup reads this existing profile and runs the actual
    // candidate server/schema migrations. No diagnostic fresh guard is relaxed.
    let runtime = Runtime::new(Some(owned.programs.clone()), owned.data.clone()).isolated();
    let outcome = (|| {
        let started = runtime
            .start()
            .map_err(|_| "upgrade_fixture_candidate_start")?;
        loopback(&started.server)?;
        let source = request(
            &started,
            &format!("/v1/sources/{}?{QUERY}", fixture.source_id),
            None,
            200,
        )?;
        let memory = request(
            &started,
            &format!("/v1/memories/{}?{QUERY}", fixture.memory_id),
            None,
            200,
        )?;
        require(
            snapshot(&source, &memory)? == fixture.snapshot,
            "upgrade_fixture_content_changed",
        )?;
        let mut report = fixture.report()?;
        report
            .as_object_mut()
            .ok_or("upgrade_fixture_report")?
            .remove("backup_restore_content");
        report["ok"] = json!(true);
        report["existing_profile_upgrade"] = json!(true);
        report["source_sha"] = json!(owned.source);
        Ok::<Value, String>(report)
    })();
    runtime.stop();
    let report = outcome?;
    owned.assert_owned()?;
    let mut output = OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(&owned.result)
        .map_err(|_| "upgrade_fixture_result_create")?;
    output
        .write_all(report.to_string().as_bytes())
        .map_err(|_| "upgrade_fixture_result_write")?;
    output
        .sync_all()
        .map_err(|_| "upgrade_fixture_result_write".into())
}

#[test]
#[ignore = "requires an owned published 0.1.4 profile and candidate installer on actual GitHub hosted Windows"]
fn existing_published_profile_upgrade() {
    if let Err(code) = existing_upgrade() {
        panic!("{code}");
    }
}

#[test]
fn upgrade_hosted_policy_rejects_local_wrong_source_and_wrong_repository() {
    let source = "a".repeat(40);
    assert!(validate_hosted(
        "true",
        "github-hosted",
        "Windows",
        "FelixBaek/arkism",
        &source,
        &source,
        Some(&source)
    )
    .is_ok());
    for (runner, os, repository, requested, compiled) in [
        (
            "self-hosted",
            "Windows",
            "FelixBaek/arkism",
            source.as_str(),
            Some(source.as_str()),
        ),
        (
            "github-hosted",
            "Linux",
            "FelixBaek/arkism",
            source.as_str(),
            Some(source.as_str()),
        ),
        (
            "github-hosted",
            "Windows",
            "other/repo",
            source.as_str(),
            Some(source.as_str()),
        ),
        (
            "github-hosted",
            "Windows",
            "FelixBaek/arkism",
            "wrong",
            Some(source.as_str()),
        ),
        (
            "github-hosted",
            "Windows",
            "FelixBaek/arkism",
            source.as_str(),
            None,
        ),
    ] {
        assert!(
            validate_hosted("true", runner, os, repository, &source, requested, compiled).is_err()
        );
    }
}

#[cfg(unix)]
#[test]
fn upgrade_tree_rejects_linked_profile_content_before_start() {
    let root = std::env::temp_dir().join(format!(
        "arkism-upgrade-guard-{}",
        super::super::secret().unwrap()
    ));
    let data = root.join("data");
    let target = root.join("outside");
    fs::create_dir_all(data.join("pg")).unwrap();
    fs::create_dir(&target).unwrap();
    fs::write(data.join("pg/PG_VERSION"), "17\n").unwrap();
    ordinary_tree(&data).unwrap();
    std::os::unix::fs::symlink(&target, data.join("pg/alias")).unwrap();
    assert!(ordinary_tree(&data).is_err());
    assert_eq!(fs::read_dir(&target).unwrap().count(), 0);
    fs::remove_file(data.join("pg/alias")).unwrap();
    fs::remove_dir_all(root).unwrap();
}
