//! Storage owned by this user for SSH approvals, separate from ~/.ssh.
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Write};
use std::path::Path;

fn invalid() -> io::Error {
    io::Error::new(io::ErrorKind::PermissionDenied, "ssh_trust_store_invalid")
}

pub fn ordinary(path: &Path, directory: bool) -> io::Result<()> {
    let meta = fs::symlink_metadata(path)?;
    #[cfg(windows)]
    {
        use std::os::windows::fs::MetadataExt;
        if meta.file_attributes() & 0x400 != 0 {
            return Err(invalid());
        }
    }
    if meta.file_type().is_symlink()
        || meta.is_dir() != directory
        || (!directory && !meta.is_file())
    {
        return Err(invalid());
    }
    Ok(())
}

fn parents(path: &Path) -> io::Result<()> {
    if !path.is_absolute() {
        return Err(invalid());
    }
    for parent in path.ancestors() {
        ordinary(parent, true)?;
    }
    Ok(())
}

pub fn directory(path: &Path) -> io::Result<()> {
    let parent = path.parent().ok_or_else(invalid)?;
    for existing in parent.ancestors() {
        match fs::symlink_metadata(existing) {
            Ok(_) => ordinary(existing, true)?,
            Err(e) if e.kind() == io::ErrorKind::NotFound => {}
            Err(e) => return Err(e),
        }
    }
    fs::create_dir_all(parent)?;
    parents(parent)?;
    #[cfg(windows)]
    windows::directory(path)?;
    #[cfg(not(windows))]
    {
        use std::os::unix::fs::{DirBuilderExt, PermissionsExt};
        match fs::DirBuilder::new().mode(0o700).create(path) {
            Ok(()) => {}
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => {}
            Err(e) => return Err(e),
        }
        if fs::symlink_metadata(path)?.permissions().mode() & 0o077 != 0 {
            return Err(invalid());
        }
    }
    ordinary(path, true)
}

pub fn create(path: &Path, bytes: &[u8]) -> io::Result<()> {
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(path)?;
    file.write_all(bytes)?;
    file.sync_all()
}

pub fn read(path: &Path, limit: usize) -> io::Result<Vec<u8>> {
    ordinary(path, false)?;
    let file = File::open(path)?;
    let mut bytes = Vec::new();
    file.take(limit as u64 + 1).read_to_end(&mut bytes)?;
    if bytes.len() > limit {
        return Err(invalid());
    }
    Ok(bytes)
}

/// Windows opens one persistent lock file without sharing. A crashed owner
/// releases its handle, so no stale lock file is treated as an approval.
pub fn lock(path: &Path) -> io::Result<File> {
    match fs::symlink_metadata(path) {
        Ok(_) => ordinary(path, false)?,
        Err(e) if e.kind() == io::ErrorKind::NotFound => {}
        Err(e) => return Err(e),
    }
    let mut options = OpenOptions::new();
    options.read(true).write(true).create(true);
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        options.share_mode(0).custom_flags(0x0020_0000);
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path)
}

#[cfg(windows)]
mod windows {
    use super::{invalid, ordinary};
    use std::ffi::c_void;
    use std::io;
    use std::mem::size_of;
    use std::os::windows::ffi::OsStrExt;
    use std::os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle};
    use std::path::Path;
    use std::ptr::null_mut;
    use windows_sys::Win32::Foundation::{GetLastError, LocalFree, ERROR_ALREADY_EXISTS};
    use windows_sys::Win32::Security::Authorization::{
        ConvertSidToStringSidW, ConvertStringSecurityDescriptorToSecurityDescriptorW,
        GetNamedSecurityInfoW, SE_FILE_OBJECT,
    };
    use windows_sys::Win32::Security::{
        EqualSid, GetAce, GetSecurityDescriptorControl, GetSecurityDescriptorDacl,
        GetSecurityDescriptorOwner, GetTokenInformation, TokenUser, ACE_HEADER, ACL,
        DACL_SECURITY_INFORMATION, OWNER_SECURITY_INFORMATION, SECURITY_ATTRIBUTES,
        SE_DACL_PROTECTED, TOKEN_QUERY, TOKEN_USER,
    };
    use windows_sys::Win32::Storage::FileSystem::CreateDirectoryW;
    use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};

    struct Local(*mut c_void);
    impl Drop for Local {
        fn drop(&mut self) {
            unsafe {
                LocalFree(self.0);
            }
        }
    }
    fn checked(result: i32) -> io::Result<()> {
        if result == 0 {
            Err(io::Error::last_os_error())
        } else {
            Ok(())
        }
    }

    fn current_sid() -> io::Result<String> {
        let mut raw = null_mut();
        checked(unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut raw) })?;
        let token = unsafe { OwnedHandle::from_raw_handle(raw) };
        let mut needed = 0;
        unsafe {
            GetTokenInformation(token.as_raw_handle(), TokenUser, null_mut(), 0, &mut needed);
        }
        if needed < size_of::<TOKEN_USER>() as u32 || needed > 65536 {
            return Err(invalid());
        }
        let mut buffer =
            vec![0usize; (needed as usize).div_ceil(size_of::<usize>())];
        checked(unsafe {
            GetTokenInformation(
                token.as_raw_handle(),
                TokenUser,
                buffer.as_mut_ptr().cast(),
                needed,
                &mut needed,
            )
        })?;
        let user = unsafe { &*buffer.as_ptr().cast::<TOKEN_USER>() };
        let mut sid = null_mut();
        checked(unsafe { ConvertSidToStringSidW(user.User.Sid, &mut sid) })?;
        let _allocation = Local(sid.cast());
        let mut length = 0;
        while length < 256 && unsafe { *sid.add(length) } != 0 {
            length += 1;
        }
        if length == 256 {
            return Err(invalid());
        }
        let text = String::from_utf16(unsafe { std::slice::from_raw_parts(sid, length) })
            .map_err(|_| invalid())?;
        if !text.starts_with("S-1-")
            || !text
                .bytes()
                .all(|b| b.is_ascii_digit() || b"S-".contains(&b))
        {
            return Err(invalid());
        }
        Ok(text)
    }

    fn descriptor() -> io::Result<Local> {
        let sid = current_sid()?;
        let text = format!("O:{sid}D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;{sid})");
        let wide: Vec<u16> = text.encode_utf16().chain([0]).collect();
        let mut result = null_mut();
        checked(unsafe {
            ConvertStringSecurityDescriptorToSecurityDescriptorW(
                wide.as_ptr(),
                1,
                &mut result,
                null_mut(),
            )
        })?;
        Ok(Local(result))
    }

    fn acl(sd: *mut c_void) -> io::Result<*mut ACL> {
        let mut present = 0;
        let mut defaulted = 0;
        let mut value = null_mut();
        checked(unsafe {
            GetSecurityDescriptorDacl(sd, &mut present, &mut value, &mut defaulted)
        })?;
        if present == 0 || value.is_null() {
            return Err(invalid());
        }
        Ok(value)
    }

    fn verify(actual: *mut c_void, expected: *mut c_void) -> io::Result<()> {
        let mut control = 0;
        let mut revision = 0;
        checked(unsafe { GetSecurityDescriptorControl(actual, &mut control, &mut revision) })?;
        if control & SE_DACL_PROTECTED == 0 {
            return Err(invalid());
        }
        let mut owner = null_mut();
        let mut expected_owner = null_mut();
        let mut defaulted = 0;
        checked(unsafe { GetSecurityDescriptorOwner(actual, &mut owner, &mut defaulted) })?;
        checked(unsafe {
            GetSecurityDescriptorOwner(expected, &mut expected_owner, &mut defaulted)
        })?;
        if unsafe { EqualSid(owner, expected_owner) } == 0 {
            return Err(invalid());
        }
        let actual = acl(actual)?;
        let expected = acl(expected)?;
        if unsafe { (*actual).AceCount != 2 || (*expected).AceCount != 2 } {
            return Err(invalid());
        }
        for index in 0..2 {
            let mut a = null_mut();
            let mut e = null_mut();
            checked(unsafe { GetAce(actual, index, &mut a) })?;
            checked(unsafe { GetAce(expected, index, &mut e) })?;
            let size = unsafe { (*a.cast::<ACE_HEADER>()).AceSize } as usize;
            if size < size_of::<ACE_HEADER>()
                || size > 1024
                || size != unsafe { (*e.cast::<ACE_HEADER>()).AceSize } as usize
            {
                return Err(invalid());
            }
            if unsafe {
                std::slice::from_raw_parts(a.cast::<u8>(), size)
                    != std::slice::from_raw_parts(e.cast::<u8>(), size)
            } {
                return Err(invalid());
            }
        }
        Ok(())
    }

    pub fn directory(path: &Path) -> io::Result<()> {
        let expected = descriptor()?;
        let wide: Vec<u16> = path.as_os_str().encode_wide().chain([0]).collect();
        let attributes = SECURITY_ATTRIBUTES {
            nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
            lpSecurityDescriptor: expected.0,
            bInheritHandle: 0,
        };
        if unsafe { CreateDirectoryW(wide.as_ptr(), &attributes) } == 0
            && unsafe { GetLastError() } != ERROR_ALREADY_EXISTS
        {
            return Err(io::Error::last_os_error());
        }
        ordinary(path, true)?;
        let mut actual = null_mut();
        let error = unsafe {
            GetNamedSecurityInfoW(
                wide.as_ptr(),
                SE_FILE_OBJECT,
                OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION,
                null_mut(),
                null_mut(),
                null_mut(),
                null_mut(),
                &mut actual,
            )
        };
        if error != 0 {
            return Err(io::Error::from_raw_os_error(error as i32));
        }
        let actual = Local(actual);
        verify(actual.0, expected.0)
    }
}
