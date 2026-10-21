//! Independent publisher authentication for the Windows update feed.
//! The key is compiled in, never learned from the feed. Both the manifest and
//! installer are authenticated; hashing alone cannot establish the publisher.
use base64::{engine::general_purpose::STANDARD, Engine};
use minisign_verify::{PublicKey, Signature};
use std::{io::Read, time::Duration};

pub const PUBLIC_KEY: &str = match option_env!("ARKISM_UPDATE_PUBLIC_KEY") { Some(key) => key, None => "" };
pub const CHANNEL: &str = match option_env!("ARKISM_UPDATE_CHANNEL") { Some(channel) => channel, None => "stable" };

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Latest {
    pub version: String,
    pub file: String,
    pub sha256: String,
    pub channel: String,
    // Base64 of the complete minisign signature file, as used by Tauri.
    pub signature: String,
}

fn public_key(encoded: &str) -> Result<PublicKey, String> {
    let bytes = STANDARD.decode(encoded.trim()).map_err(|_| "update_signing_key_invalid")?;
    let text = std::str::from_utf8(&bytes).map_err(|_| "update_signing_key_invalid")?;
    PublicKey::decode(text).map_err(|_| "update_signing_key_invalid".into())
}

pub fn validate_configuration(feed: &str, key: &str, channel: &str) -> Result<(), String> {
    // URI parsing rejects malformed authorities. Credentials, queries and
    // fragments are not meaningful for a static release directory.
    let uri: ureq::http::Uri = feed.parse().map_err(|_| "update_feed_invalid")?;
    if uri.scheme_str() != Some("https") || uri.host().is_none_or(str::is_empty)
        || uri.authority().is_some_and(|a| a.as_str().contains('@'))
        || uri.query().is_some() || feed.contains('#') || feed.ends_with('/')
        || !matches!(channel, "stable" | "beta")
    {
        return Err("update_feed_invalid".into());
    }
    public_key(key)?;
    Ok(())
}

pub fn agent(timeout: Duration) -> ureq::Agent {
    // Applies to the initial URL AND every redirect (including GitHub's CDN).
    ureq::Agent::config_builder().https_only(true).timeout_global(Some(timeout)).build().into()
}

pub fn read_bounded(reader: impl Read, limit: u64) -> Result<Vec<u8>, String> {
    let mut bytes = Vec::new();
    reader.take(limit + 1).read_to_end(&mut bytes).map_err(|_| "update_feed_unreachable")?;
    if bytes.len() as u64 > limit { return Err("update_feed_too_large".into()); }
    Ok(bytes)
}

pub fn verify(data: &[u8], signature: &str, key: &str) -> Result<(), String> {
    let key = public_key(key)?;
    if signature.lines().count() != 4 { return Err("update_signature_invalid".into()); }
    let signature = Signature::decode(signature).map_err(|_| "update_signature_invalid")?;
    // Only modern prehashed minisign signatures; also verifies the signed comment.
    key.verify(data, &signature, false).map_err(|_| "update_signature_invalid".into())
}

pub fn decode_manifest(data: &[u8], signature: &str, key: &str, channel: &str) -> Result<Latest, String> {
    // Authenticate the exact bytes before interpreting version, file or hash.
    verify(data, signature, key)?;
    let latest: Latest = serde_json::from_slice(data).map_err(|_| "update_feed_invalid")?;
    if !matches!(channel, "stable" | "beta") || latest.channel != channel
        || parse_version(&latest.version).is_none()
        || latest.file != format!("Arkism-{}-setup.exe", latest.version)
        || latest.sha256.len() != 64
        || !latest.sha256.bytes().all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
    {
        return Err("update_feed_invalid".into());
    }
    Ok(latest)
}

impl Latest {
    pub fn verify_installer(&self, bytes: &[u8], key: &str) -> Result<(), String> {
        let signature = STANDARD.decode(&self.signature).map_err(|_| "update_signature_invalid")?;
        let signature = std::str::from_utf8(&signature).map_err(|_| "update_signature_invalid")?;
        verify(bytes, signature, key)?;
        // Inspect comments only after the global minisign signature passed.
        let signature = Signature::decode(signature).map_err(|_| "update_signature_invalid")?;
        let fields: Vec<_> = signature.trusted_comment().split('\t').collect();
        if fields.iter().filter(|v| v.starts_with("version:")).count() != 1
            || !fields.contains(&format!("version:{}", self.version).as_str())
            || fields.iter().filter(|v| v.starts_with("file:")).count() != 1
            || !fields.contains(&format!("file:{}", self.file).as_str())
        { return Err("update_signed_version_mismatch".into()); }
        Ok(())
    }
}

pub fn parse_version(value: &str) -> Option<(u32, u32, u32)> {
    let mut parts = value.split('.');
    fn number(part: Option<&str>) -> Option<u32> {
        let part = part?;
        if part.is_empty() || !part.bytes().all(|b| b.is_ascii_digit())
            || (part.len() > 1 && part.starts_with('0')) { return None; }
        part.parse().ok()
    }
    let version = (number(parts.next())?, number(parts.next())?, number(parts.next())?);
    parts.next().is_none().then_some(version)
}

#[cfg(test)]
mod tests {
    use super::*;
    const KEY: &str = include_str!("../tests/fixtures/update/public-key-base64.txt");
    const MANIFEST: &[u8] = include_bytes!("../tests/fixtures/update/latest.json");
    const SIGNATURE: &str = include_str!("../tests/fixtures/update/latest.json.minisig");
    const INSTALLER: &[u8] = b"inert Windows installer fixture\n";

    #[test]
    fn authentic_manifest_and_tauri_wrapped_installer_signature() {
        let mut latest = decode_manifest(MANIFEST, SIGNATURE, KEY, "stable").unwrap();
        latest.verify_installer(INSTALLER, KEY).unwrap();
        assert!(latest.verify_installer(b"replaced installer", KEY).is_err());
        latest.version = "9.9.9".into();
        assert!(latest.verify_installer(INSTALLER, KEY).is_err());
        let mut tampered = MANIFEST.to_vec();
        tampered[0] ^= 1;
        assert!(decode_manifest(&tampered, SIGNATURE, KEY, "stable").is_err());
        assert!(decode_manifest(MANIFEST, SIGNATURE, "", "stable").is_err());
        assert!(decode_manifest(MANIFEST, SIGNATURE, KEY, "beta").is_err());
        assert!(decode_manifest(MANIFEST, "", KEY, "stable").is_err());
        // An attacker cannot relabel an older signed installer as a new version.
        let changed = String::from_utf8(MANIFEST.to_vec()).unwrap().replace("1.2.3", "9.9.9");
        assert!(decode_manifest(changed.as_bytes(), SIGNATURE, KEY, "stable").is_err());
    }

    #[test]
    fn https_and_independent_key_are_required() {
        validate_configuration("https://example.test/feed", KEY, "stable").unwrap();
        for url in ["http://example.test/feed", "file:///tmp/feed", "https://user@example.test/feed",
                    "https://example.test/feed?q=1", "https://example.test/feed#x", "https://example.test/feed/"] {
            assert!(validate_configuration(url, KEY, "stable").is_err(), "{url}");
        }
        assert!(validate_configuration("https://example.test/feed", "", "stable").is_err());
        assert!(matches!(agent(Duration::from_secs(1)).get("http://127.0.0.1:1/").call(), Err(ureq::Error::RequireHttpsOnly(_))));
    }

    #[test]
    fn unsigned_legacy_and_oversized_inputs_fail() {
        assert!(decode_manifest(br#"{"version":"9.9.9"}"#, "", KEY, "stable").is_err());
        assert_eq!(read_bounded(&b"abc"[..], 3).unwrap(), b"abc");
        assert!(read_bounded(&b"abcd"[..], 3).is_err());
        for version in ["1.2", "1.2.3.4", "+1.2.3", "01.2.3", "1.2.3/bad", "4294967296.0.0"] {
            assert!(parse_version(version).is_none(), "{version}");
        }
    }
}
