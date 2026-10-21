//! Per-user startup policy, shared by the settings commands and uninstaller.
//! Windows never delegates to an adapter that may select a machine-wide hive.
const APP_NAME: &str = "Arkism";

fn quoted_command(exe: &str) -> Result<String, String> {
    if exe.is_empty() || exe.chars().any(|c| c == '"' || c.is_control()) {
        return Err("autostart_path_invalid".into());
    }
    Ok(format!("\"{exe}\" --hidden"))
}

fn owns_command(value: &str, exe: &str) -> bool {
    [
        format!("\"{exe}\" --hidden"),
        format!("{exe} --hidden"),
        format!("\"{exe}\""),
        exe.to_owned(),
    ]
    .iter()
    .any(|expected| value.eq_ignore_ascii_case(expected))
}

#[cfg(windows)]
mod windows {
    use super::*;
    use windows_registry::{Key, Transaction, Type, CURRENT_USER};

    struct Keys {
        run: String,
        approved: String,
    }
    impl Keys {
        fn production() -> Self {
            Self {
                run: r"Software\Microsoft\Windows\CurrentVersion\Run".into(),
                approved: r"Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run"
                    .into(),
            }
        }
    }
    fn absent(code: i32) -> bool {
        matches!(code as u32, 0x80070002 | 0x80070003)
    }
    fn current_exe() -> Result<String, String> {
        std::env::current_exe()
            .map_err(|_| "autostart_path_invalid".to_string())?
            .into_os_string()
            .into_string()
            .map_err(|_| "autostart_path_invalid".into())
    }
    fn read_run(key: &Key) -> Result<Option<String>, String> {
        match key.get_value(APP_NAME) {
            Ok(value) if value.ty() == Type::String => key
                .get_string(APP_NAME)
                .map(Some)
                .map_err(|_| "autostart_value_invalid".into()),
            Ok(_) => Err("autostart_value_invalid".into()),
            Err(error) if absent(error.code().0) => Ok(None),
            Err(_) => Err("autostart_read_failed".into()),
        }
    }
    fn approved(keys: &Keys) -> Result<bool, String> {
        let key = match CURRENT_USER.open(&keys.approved) {
            Ok(key) => key,
            Err(error) if absent(error.code().0) => return Ok(true),
            Err(_) => return Err("autostart_read_failed".into()),
        };
        match key.get_value(APP_NAME) {
            Ok(value) => Ok(value.ty() == Type::Bytes
                && value.len() == 12
                && matches!(value[0], 2 | 6)
                && value[1..].iter().all(|b| *b == 0)),
            Err(error) if absent(error.code().0) => Ok(true),
            Err(_) => Err("autostart_read_failed".into()),
        }
    }
    fn get(keys: &Keys, exe: &str) -> Result<bool, String> {
        let key = match CURRENT_USER.open(&keys.run) {
            Ok(key) => key,
            Err(error) if absent(error.code().0) => return Ok(false),
            Err(_) => return Err("autostart_read_failed".into()),
        };
        match read_run(&key)? {
            Some(value) if owns_command(&value, exe) => approved(keys),
            _ => Ok(false),
        }
    }
    fn clear_approval(tx: &Transaction, keys: &Keys) -> Result<(), String> {
        let key = match CURRENT_USER
            .options()
            .read()
            .write()
            .transaction(tx)
            .open(&keys.approved)
        {
            Ok(key) => key,
            Err(error) if absent(error.code().0) => return Ok(()),
            Err(_) => return Err("autostart_write_failed".into()),
        };
        match key.remove_value(APP_NAME) {
            Ok(()) => Ok(()),
            Err(error) if absent(error.code().0) => Ok(()),
            Err(_) => Err("autostart_write_failed".into()),
        }
    }
    fn stage_change(tx: &Transaction, keys: &Keys, exe: &str, on: bool) -> Result<(), String> {
        let mut options = CURRENT_USER.options();
        options.read().write().transaction(tx);
        if on {
            options.create();
        }
        let key = match options.open(&keys.run) {
            Ok(key) => key,
            Err(error) if !on && absent(error.code().0) => return Ok(()),
            Err(_) => return Err("autostart_write_failed".into()),
        };
        if on {
            key.set_string(APP_NAME, quoted_command(exe)?)
                .map_err(|_| "autostart_write_failed")?;
        } else {
            if !read_run(&key)?.is_some_and(|value| owns_command(&value, exe)) {
                return Ok(());
            }
            key.remove_value(APP_NAME)
                .map_err(|_| "autostart_write_failed")?;
        }
        clear_approval(tx, keys)
    }
    fn set(keys: &Keys, exe: &str, on: bool) -> Result<(), String> {
        let tx = Transaction::new().map_err(|_| "autostart_transaction_failed")?;
        stage_change(&tx, keys, exe, on)?;
        tx.commit().map_err(|_| "autostart_commit_failed".into())
    }
    fn migrate(keys: &Keys, exe: &str) -> Result<(), String> {
        let tx = Transaction::new().map_err(|_| "autostart_transaction_failed")?;
        {
            let run = match CURRENT_USER
                .options()
                .read()
                .write()
                .transaction(&tx)
                .open(&keys.run)
            {
                Ok(key) => key,
                Err(error) if absent(error.code().0) => return Ok(()),
                Err(_) => return Err("autostart_read_failed".into()),
            };
            if let Some(value) = read_run(&run)? {
                let quoted = quoted_command(exe)?;
                if owns_command(&value, exe) && value != quoted {
                    run.set_string(APP_NAME, quoted)
                        .map_err(|_| "autostart_write_failed")?;
                }
            }
        }
        tx.commit().map_err(|_| "autostart_commit_failed".into())
    }
    pub(crate) fn is_enabled() -> Result<bool, String> {
        get(&Keys::production(), &current_exe()?)
    }
    pub(crate) fn set_enabled(on: bool) -> Result<(), String> {
        set(&Keys::production(), &current_exe()?, on)
    }
    pub(crate) fn migrate_owned() -> Result<(), String> {
        migrate(&Keys::production(), &current_exe()?)
    }

    #[cfg(test)]
    mod tests {
        use super::*;
        struct Fixture {
            root: String,
            owner: String,
            keys: Keys,
        }
        impl Fixture {
            fn new() -> Self {
                let mut raw = [0u8; 16];
                getrandom::fill(&mut raw).unwrap();
                let owner = raw.iter().map(|b| format!("{b:02x}")).collect::<String>();
                let root = format!(r"Software\ArkismInstallerChecks\autostart-{owner}");
                assert!(CURRENT_USER.open(&root).is_err());
                CURRENT_USER
                    .create(&root)
                    .unwrap()
                    .set_string("owner", &owner)
                    .unwrap();
                Self {
                    keys: Keys {
                        run: format!(r"{root}\Run"),
                        approved: format!(r"{root}\Approved"),
                    },
                    root,
                    owner,
                }
            }
        }
        impl Drop for Fixture {
            fn drop(&mut self) {
                eprintln!("Only owned registry fixture cleanup: HKCU\\{}", self.root);
                if CURRENT_USER
                    .open(&self.root)
                    .and_then(|key| key.get_string("owner"))
                    .ok()
                    .as_deref()
                    == Some(self.owner.as_str())
                {
                    CURRENT_USER.remove_tree(&self.root).unwrap();
                }
            }
        }
        #[test]
        fn native_user_startup_quotes_and_removes_only_its_values() {
            let f = Fixture::new();
            let exe = r"C:\Synthetic User\Arkism\Arkism.exe";
            let run = CURRENT_USER.create(&f.keys.run).unwrap();
            run.set_string("OtherApp", "preserve neighbor").unwrap();
            let approval = CURRENT_USER.create(&f.keys.approved).unwrap();
            approval.set_bytes(APP_NAME, Type::Bytes, &[3; 12]).unwrap();
            set(&f.keys, exe, true).unwrap();
            assert_eq!(
                run.get_string(APP_NAME).unwrap(),
                format!("\"{exe}\" --hidden")
            );
            assert!(get(&f.keys, exe).unwrap());
            assert!(approval.get_value(APP_NAME).is_err());
            set(&f.keys, exe, false).unwrap();
            assert!(run.get_value(APP_NAME).is_err());
            assert_eq!(run.get_string("OtherApp").unwrap(), "preserve neighbor");
            assert!(!get(&f.keys, exe).unwrap());
        }
        #[test]
        fn native_foreign_startup_and_approval_are_preserved() {
            let f = Fixture::new();
            let run = CURRENT_USER.create(&f.keys.run).unwrap();
            run.set_string(APP_NAME, r"C:\Other\Arkism.exe --hidden")
                .unwrap();
            let approval = CURRENT_USER.create(&f.keys.approved).unwrap();
            approval.set_bytes(APP_NAME, Type::Bytes, &[3; 12]).unwrap();
            set(&f.keys, r"C:\Owned\Arkism.exe", false).unwrap();
            assert_eq!(
                run.get_string(APP_NAME).unwrap(),
                r"C:\Other\Arkism.exe --hidden"
            );
            assert_eq!(approval.get_value(APP_NAME).unwrap().as_ref(), &[3; 12]);
        }
        #[test]
        fn native_startup_transaction_rolls_back_before_commit() {
            let f = Fixture::new();
            let exe = r"C:\Owned\Arkism.exe";
            let run = CURRENT_USER.create(&f.keys.run).unwrap();
            run.set_string(APP_NAME, "old command").unwrap();
            let approval = CURRENT_USER.create(&f.keys.approved).unwrap();
            approval.set_bytes(APP_NAME, Type::Bytes, &[3; 12]).unwrap();
            {
                let tx = Transaction::new().unwrap();
                stage_change(&tx, &f.keys, exe, true).unwrap();
            }
            assert_eq!(run.get_string(APP_NAME).unwrap(), "old command");
            assert_eq!(approval.get_value(APP_NAME).unwrap().as_ref(), &[3; 12]);
        }
        #[test]
        fn native_task_manager_disabled_entry_is_not_reported_enabled() {
            let f = Fixture::new();
            let exe = r"C:\Owned\Arkism.exe";
            set(&f.keys, exe, true).unwrap();
            let approval = CURRENT_USER.create(&f.keys.approved).unwrap();
            approval.set_bytes(APP_NAME, Type::Bytes, &[3; 12]).unwrap();
            assert!(!get(&f.keys, exe).unwrap());
        }
        #[test]
        fn native_legacy_quote_migration_preserves_disabled_choice() {
            let f = Fixture::new();
            let exe = r"C:\Synthetic User\Arkism\Arkism.exe";
            let run = CURRENT_USER.create(&f.keys.run).unwrap();
            run.set_string(APP_NAME, format!("{exe} --hidden")).unwrap();
            let approval = CURRENT_USER.create(&f.keys.approved).unwrap();
            approval.set_bytes(APP_NAME, Type::Bytes, &[3; 12]).unwrap();
            migrate(&f.keys, exe).unwrap();
            assert_eq!(
                run.get_string(APP_NAME).unwrap(),
                quoted_command(exe).unwrap()
            );
            assert_eq!(approval.get_value(APP_NAME).unwrap().as_ref(), &[3; 12]);
            assert!(!get(&f.keys, exe).unwrap());
        }
        #[test]
        fn native_missing_startup_keys_are_not_created_by_disable() {
            let f = Fixture::new();
            let exe = r"C:\Owned\Arkism.exe";
            set(&f.keys, exe, false).unwrap();
            migrate(&f.keys, exe).unwrap();
            assert!(CURRENT_USER.open(&f.keys.run).is_err());
            assert!(CURRENT_USER.open(&f.keys.approved).is_err());
        }
        #[test]
        fn native_legacy_owned_forms_remove_the_approval_without_neighbors() {
            let f = Fixture::new();
            let exe = r"C:\Owned\Arkism.exe";
            for command in [
                exe.to_owned(),
                format!("{exe} --hidden"),
                format!("\"{exe}\""),
            ] {
                let run = CURRENT_USER.create(&f.keys.run).unwrap();
                run.set_string(APP_NAME, command).unwrap();
                let approval = CURRENT_USER.create(&f.keys.approved).unwrap();
                approval.set_bytes(APP_NAME, Type::Bytes, &[3; 12]).unwrap();
                set(&f.keys, exe, false).unwrap();
                assert!(run.get_value(APP_NAME).is_err());
                assert!(approval.get_value(APP_NAME).is_err());
            }
        }
        #[test]
        fn native_disable_abort_retains_both_values() {
            let f = Fixture::new();
            let exe = r"C:\Owned\Arkism.exe";
            set(&f.keys, exe, true).unwrap();
            let run = CURRENT_USER.open(&f.keys.run).unwrap();
            let approval = CURRENT_USER.create(&f.keys.approved).unwrap();
            approval.set_bytes(APP_NAME, Type::Bytes, &[3; 12]).unwrap();
            {
                let tx = Transaction::new().unwrap();
                stage_change(&tx, &f.keys, exe, false).unwrap();
            }
            assert_eq!(
                run.get_string(APP_NAME).unwrap(),
                quoted_command(exe).unwrap()
            );
            assert_eq!(approval.get_value(APP_NAME).unwrap().as_ref(), &[3; 12]);
        }
    }
}

#[cfg(windows)]
pub(crate) use windows::{is_enabled, migrate_owned, set_enabled};

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn startup_ownership_accepts_standard_legacy_forms_and_preserves_foreign_arguments() {
        let exe = r"C:\Synthetic User\Arkism\Arkism.exe";
        for value in [
            exe.to_owned(),
            format!("{exe} --hidden"),
            format!("\"{exe}\""),
            quoted_command(exe).unwrap(),
        ] {
            assert!(owns_command(&value, exe));
        }
        for value in [
            r"C:\Other\Arkism.exe --hidden".to_owned(),
            format!("{exe} --other"),
            format!("\"{exe}.other\" --hidden"),
            String::new(),
        ] {
            assert!(!owns_command(&value, exe));
        }
        assert!(quoted_command("bad\"path").is_err());
    }
    #[test]
    fn startup_name_matches_the_packaged_product() {
        let config: serde_json::Value =
            serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
        assert_eq!(config["productName"], APP_NAME);
    }
}
