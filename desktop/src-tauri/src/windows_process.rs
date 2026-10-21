//! Process creation ABI shared by managed roles and update installers.
//!
//! Use the pinned Windows SDK bindings instead of locally redeclaring the same
//! symbols with different nominal types. Both callers pass the STARTUPINFOW
//! prefix of STARTUPINFOEXW, with its full size and extended-startup flag.

pub(crate) use windows_sys::Win32::Security::SECURITY_ATTRIBUTES;
pub(crate) use windows_sys::Win32::System::Threading::{
    CreateProcessW, DeleteProcThreadAttributeList, InitializeProcThreadAttributeList,
    UpdateProcThreadAttribute, PROCESS_INFORMATION, STARTUPINFOEXW,
};

#[cfg(target_pointer_width = "64")]
const _: () = {
    use std::mem::{offset_of, size_of};
    use windows_sys::Win32::System::Threading::STARTUPINFOW;
    // These offsets are the Win64 contract behind the extended-startup upcast.
    assert!(size_of::<STARTUPINFOW>() == 104);
    assert!(size_of::<STARTUPINFOEXW>() == 112);
    assert!(offset_of!(STARTUPINFOEXW, StartupInfo) == 0);
    assert!(offset_of!(STARTUPINFOEXW, lpAttributeList) == 104);
    assert!(size_of::<PROCESS_INFORMATION>() == 24);
    assert!(size_of::<SECURITY_ATTRIBUTES>() == 24);
};

#[cfg(test)]
mod tests {
    /// Only the owning Job test supplies this process-local nonce. Normal test
    /// discovery returns without running a child mode or touching user data.
    #[test]
    fn inert_pipe_peer() {
        use std::io::Write;
        let Ok(nonce) = std::env::var("ARKISM_SDK_PIPE_PEER") else {
            return;
        };
        assert!(nonce.len() == 32 && nonce.bytes().all(|b| b.is_ascii_hexdigit()));
        writeln!(std::io::stdout(), "sdk-stdout:{nonce}").unwrap();
        writeln!(std::io::stderr(), "sdk-stderr:{nonce}").unwrap();
        std::process::exit(7);
    }
}
