//! Synthetic durable records for the fresh-directory runtime diagnostic only.
//! Responses stay in memory; reports contain only counts and a digest.
use super::{http_agent, ordinary_ancestor, require, Duration, Runtime, Started, MARKER};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};

const QUERY: &str = "workspace_id=default&project_id=default&user_id=management%3Aowner";
const SOURCE_TEXT: &str = "Synthetic runtime source for backup verification.";
const MEMORY_TEXT: &str = "Synthetic runtime memory survives both backup restores.";
const SOURCE_REF: &str = "runtime-check/durable-fixture";

// Deliberately not Serialize/Debug: IDs, text and provenance never enter check.json.
pub(crate) struct DurableFixture {
    source_id: String,
    memory_id: String,
    snapshot: Value,
}

impl DurableFixture {
    pub(crate) fn report(&self) -> Result<Value, String> {
        let raw = serde_json::to_vec(&self.snapshot).map_err(|_| "runtime_fixture_digest")?;
        let digest: String = Sha256::digest(raw)
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect();
        Ok(json!({"fixture_source_rows":1,"fixture_memory_rows":1,
            "fixture_sha256":digest,"backup_restore_content":true}))
    }
}

fn scope() -> Value {
    json!({"workspace_id":"default","project_id":"default","user_id":"management:owner"})
}

fn loopback(server: &str) -> Result<(), String> {
    require(
        server
            .strip_prefix("http://127.0.0.1:")
            .and_then(|port| port.parse::<u16>().ok())
            .is_some_and(|port| port != 0),
        "runtime_fixture_loopback_required",
    )
}

fn request(
    started: &Started,
    path: &str,
    body: Option<Value>,
    expected: u16,
) -> Result<Value, String> {
    loopback(&started.server)?;
    let agent = http_agent(Duration::from_secs(15));
    let url = format!("{}{path}", started.server);
    let auth = format!("Bearer {}", started.token);
    let mut reply = match body {
        Some(body) => agent
            .post(&url)
            .header("Authorization", &auth)
            .config()
            .http_status_as_error(false)
            .max_redirects(0)
            .build()
            .send_json(body),
        None => agent
            .get(&url)
            .header("Authorization", &auth)
            .config()
            .http_status_as_error(false)
            .max_redirects(0)
            .build()
            .call(),
    }
    .map_err(|_| "runtime_fixture_http_unavailable")?;
    if reply.status().as_u16() != expected {
        return Err(format!(
            "runtime_fixture_http_status_{}",
            reply.status().as_u16()
        ));
    }
    reply
        .body_mut()
        .with_config()
        .limit(256 << 10)
        .read_json()
        .map_err(|_| "runtime_fixture_http_json".into())
}

fn identity(record: &Value) -> Result<String, String> {
    let id = record["id"].as_str().ok_or("runtime_fixture_record_id")?;
    require(
        !id.is_empty()
            && id.len() <= 128
            && id
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-'),
        "runtime_fixture_record_id",
    )?;
    Ok(id.to_owned())
}

fn snapshot(source: &Value, memory: &Value) -> Result<Value, String> {
    require(
        source["text"] == SOURCE_TEXT
            && source["kind"] == "document"
            && source["origin_product"] == "brain"
            && source["source_ref"] == SOURCE_REF
            && source["status"] == "active",
        "runtime_fixture_source_mismatch",
    )?;
    require(
        memory["text"] == MEMORY_TEXT
            && memory["kind"] == "semantic"
            && memory["status"] == "active"
            && memory["review"]["person"] == true
            && memory["sources"]
                .as_array()
                .is_some_and(|refs| refs.len() == 1 && refs[0]["source_id"] == source["id"]),
        "runtime_fixture_memory_mismatch",
    )?;
    for record in [source, memory] {
        identity(record)?;
        require(
            record["scope"]["workspace_id"] == "default"
                && record["scope"]["project_id"] == "default"
                && record["scope"]["user_id"] == "management:owner"
                && record["scope"]["agent_id"]
                    .as_str()
                    .unwrap_or("")
                    .is_empty()
                && record["revision"]
                    .as_u64()
                    .is_some_and(|revision| revision > 0),
            "runtime_fixture_record_scope",
        )?;
    }
    // Exclude operational fields (access/embedding/job state); preserve content,
    // source identity, revision and explicit human approval across both restores.
    let project = |record: &Value| {
        let mut value = serde_json::Map::new();
        for key in [
            "id",
            "scope",
            "kind",
            "status",
            "revision",
            "text",
            "summary",
            "keywords",
            "sources",
            "origin_product",
            "source_ref",
            "collected_at",
            "trust_level",
            "sensitivity",
            "content_hash",
            "created_by",
            "created_by_type",
            "approved_by",
            "review",
        ] {
            if let Some(field) = record.get(key) {
                value.insert(key.into(), field.clone());
            }
        }
        Value::Object(value)
    };
    Ok(json!({"source":project(source),"memory":project(memory)}))
}

impl Runtime {
    fn fixture_guard(&self) -> Result<(), String> {
        require(self.home.is_none(), "runtime_fixture_requires_isolation")?;
        for ancestor in self.data_dir().ancestors() {
            ordinary_ancestor(ancestor)?;
        }
        require(
            std::fs::read(self.data_dir().join(".runtime-check-owned"))
                .map_err(|_| "runtime_fixture_requires_marker")?
                == MARKER.as_bytes(),
            "runtime_fixture_requires_marker",
        )
    }

    pub(super) fn seed_fixture_data(&self, started: &Started) -> Result<DurableFixture, String> {
        self.fixture_guard()?;
        let source = request(
            started,
            "/v1/sources",
            Some(json!({"scope":scope(),"kind":"document",
            "text":SOURCE_TEXT,"origin_product":"brain","source_ref":SOURCE_REF,
            "collected_at":"2026-01-01T00:00:00Z","trust_level":"user_authored"})),
            201,
        )?;
        let source_id = identity(&source)?;
        let reference = source["sources"]
            .as_array()
            .and_then(|refs| refs.first())
            .filter(|reference| {
                reference["source_id"] == source_id && reference["span"].is_object()
            })
            .ok_or("runtime_fixture_source_span")?
            .clone();
        let candidate = request(
            started,
            "/v1/memory_candidates",
            Some(json!({"scope":scope(),
            "kind":"semantic","text":MEMORY_TEXT,"sources":[reference]})),
            201,
        )?;
        let candidate_id = identity(&candidate)?;
        let approved = request(
            started,
            &format!("/v1/memory_candidates/{candidate_id}/review"),
            Some(
                json!({"scope":scope(),"expected_revision":candidate["revision"],"action":"approve"}),
            ),
            200,
        )?;
        // Read durable records back from the normal owner API rather than treating
        // a successful POST or capabilities response as evidence of stored content.
        let memory_id = identity(&approved)?;
        let source = request(
            started,
            &format!("/v1/sources/{source_id}?{QUERY}"),
            None,
            200,
        )?;
        let memory = request(
            started,
            &format!("/v1/memories/{memory_id}?{QUERY}"),
            None,
            200,
        )?;
        Ok(DurableFixture {
            source_id,
            memory_id,
            snapshot: snapshot(&source, &memory)?,
        })
    }

    pub(crate) fn check_fixture_data(
        &self,
        started: &Started,
        fixture: &DurableFixture,
    ) -> Result<(), String> {
        self.fixture_guard()?;
        let source = request(
            started,
            &format!("/v1/sources/{}?{QUERY}", fixture.source_id),
            None,
            200,
        )?;
        let memory = request(
            started,
            &format!("/v1/memories/{}?{QUERY}", fixture.memory_id),
            None,
            200,
        )?;
        require(
            snapshot(&source, &memory)? == fixture.snapshot,
            "runtime_fixture_restore_mismatch",
        )
    }
}

#[cfg(test)]
#[path = "runtime_upgrade_check.rs"]
mod published_upgrade;

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::net::TcpListener;

    fn records() -> (Value, Value) {
        let source = json!({"id":"fixture-source","scope":scope(),"kind":"document","status":"active",
            "revision":1,"text":SOURCE_TEXT,"origin_product":"brain","source_ref":SOURCE_REF,
            "sources":[{"source_id":"fixture-source","locator":"original","span":{"content_hash":"synthetic-span","start":0,"end":SOURCE_TEXT.len()}}]});
        let memory = json!({"id":"fixture-memory","scope":scope(),"kind":"semantic","status":"active",
            "revision":1,"text":MEMORY_TEXT,"sources":[{"source_id":"fixture-source","locator":SOURCE_REF}],
            "review":{"person":true,"by":"management:owner"}});
        (source, memory)
    }

    #[test]
    fn durable_snapshot_detects_content_provenance_and_revision_loss() {
        let (source, memory) = records();
        let baseline = snapshot(&source, &memory).unwrap();
        for mode in [
            "source_text",
            "memory_text",
            "source_link",
            "approval",
            "scope",
            "status",
            "revision",
            "missing",
        ] {
            let (mut source, mut memory) = (source.clone(), memory.clone());
            match mode {
                "source_text" => source["text"] = json!("changed"),
                "memory_text" => memory["text"] = json!("changed"),
                "source_link" => memory["sources"][0]["source_id"] = json!("other"),
                "approval" => memory["review"]["person"] = json!(false),
                "scope" => memory["scope"]["project_id"] = json!("other"),
                "status" => memory["status"] = json!("expired"),
                "revision" => memory["revision"] = json!(2),
                "missing" => memory = Value::Null,
                _ => unreachable!(),
            }
            assert!(
                match snapshot(&source, &memory) {
                    Err(_) => true,
                    Ok(value) => value != baseline,
                },
                "{mode}"
            );
        }
        let mut operational = memory.clone();
        operational["access_count"] = json!(12);
        operational["embedding"] = json!({"status":"ready"});
        assert_eq!(snapshot(&source, &operational).unwrap(), baseline);
    }

    #[test]
    fn durable_report_contains_only_counts_digest_and_verified_flag() {
        let (source, memory) = records();
        let fixture = DurableFixture {
            source_id: "fixture-source".into(),
            memory_id: "fixture-memory".into(),
            snapshot: snapshot(&source, &memory).unwrap(),
        };
        let report = fixture.report().unwrap();
        assert_eq!(report.as_object().unwrap().len(), 4);
        assert_eq!(report["fixture_source_rows"], 1);
        assert_eq!(report["fixture_memory_rows"], 1);
        assert_eq!(report["backup_restore_content"], true);
        assert_eq!(report["fixture_sha256"].as_str().unwrap().len(), 64);
        let raw = report.to_string();
        for private in [
            SOURCE_TEXT,
            MEMORY_TEXT,
            "fixture-source",
            "fixture-memory",
            "Bearer",
        ] {
            assert!(!raw.contains(private));
        }
    }

    #[test]
    fn durable_http_rejects_non_loopback_and_untrusted_record_ids() {
        for server in [
            "https://127.0.0.1:8091",
            "http://example.invalid:8091",
            "http://127.0.0.1:8091/redirect",
            "http://127.0.0.1:8091@evil.invalid",
            "http://127.0.0.1:0",
        ] {
            assert!(loopback(server).is_err());
        }
        for id in ["", "../sources", "value?secret", "other/path"] {
            assert!(identity(&json!({"id":id})).is_err());
        }
    }

    #[test]
    fn durable_seed_and_restore_readback_use_owner_api_in_owned_fixture_only() {
        let root = std::env::temp_dir().join(format!(
            "arkism-durable-check-{}",
            super::super::secret().unwrap()
        ));
        super::super::fresh_directory(&root).unwrap();
        let runtime = Runtime::new(None, root.clone()).isolated();
        let (source, memory) = records();
        let expected_reference = source["sources"][0].clone();
        let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let port = listener.local_addr().unwrap().port();
        let paths = vec![
            ("POST", "/v1/sources".into(), source.clone()),
            (
                "POST",
                "/v1/memory_candidates".into(),
                json!({"id":"fixture-candidate","revision":1}),
            ),
            (
                "POST",
                "/v1/memory_candidates/fixture-candidate/review".into(),
                memory.clone(),
            ),
            (
                "GET",
                format!("/v1/sources/fixture-source?{QUERY}"),
                source.clone(),
            ),
            (
                "GET",
                format!("/v1/memories/fixture-memory?{QUERY}"),
                memory.clone(),
            ),
            ("GET", format!("/v1/sources/fixture-source?{QUERY}"), source),
            (
                "GET",
                format!("/v1/memories/fixture-memory?{QUERY}"),
                memory,
            ),
        ];
        let server = std::thread::spawn(move || {
            for (index, (method, path, value)) in paths.into_iter().enumerate() {
                let (mut stream, _) = listener.accept().unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(5)))
                    .unwrap();
                let mut raw = Vec::new();
                let mut buf = [0; 2048];
                let end = loop {
                    let n = stream.read(&mut buf).unwrap();
                    assert!(n > 0 && raw.len() < 65536);
                    raw.extend_from_slice(&buf[..n]);
                    if let Some(pos) = raw.windows(4).position(|part| part == b"\r\n\r\n") {
                        break pos + 4;
                    }
                };
                let header = String::from_utf8(raw[..end].to_vec()).unwrap();
                assert!(header.starts_with(&format!("{method} {path} HTTP/1.1\r\n")));
                assert!(header
                    .to_ascii_lowercase()
                    .contains("authorization: bearer fixture-owner\r\n"));
                let length = header
                    .lines()
                    .find_map(|line| {
                        line.to_ascii_lowercase()
                            .strip_prefix("content-length:")
                            .and_then(|n| n.trim().parse::<usize>().ok())
                    })
                    .unwrap_or(0);
                while raw.len() - end < length {
                    let n = stream.read(&mut buf).unwrap();
                    assert!(n > 0);
                    raw.extend_from_slice(&buf[..n]);
                }
                if index == 1 || index == 2 {
                    let body: Value = serde_json::from_slice(&raw[end..end + length]).unwrap();
                    assert_eq!(body["scope"], scope());
                    assert!(body.get("automated").is_none());
                    if index == 1 {
                        assert_eq!(body["sources"], json!([expected_reference]));
                    } else {
                        assert_eq!(body["action"], "approve");
                    }
                }
                let body = value.to_string();
                let status = if index < 2 { "201 Created" } else { "200 OK" };
                write!(stream,"HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",body.len()).unwrap();
            }
        });
        let started = Started {
            server: format!("http://127.0.0.1:{port}"),
            token: "fixture-owner".into(),
        };
        let fixture = runtime.seed_fixture_data(&started).unwrap();
        runtime.check_fixture_data(&started, &fixture).unwrap();
        server.join().unwrap();
        std::fs::remove_file(root.join(".runtime-check-owned")).unwrap();
        assert!(runtime.check_fixture_data(&started, &fixture).is_err());
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn durable_guard_rejects_nonisolated_runtime_before_api_writes() {
        let root = std::env::temp_dir().join(format!(
            "arkism-durable-check-{}",
            super::super::secret().unwrap()
        ));
        super::super::fresh_directory(&root).unwrap();
        let runtime = Runtime::new(None, root.clone());
        let started = Started {
            server: "http://127.0.0.1:1".into(),
            token: "fixture-only".into(),
        };
        assert!(
            matches!(runtime.seed_fixture_data(&started), Err(code) if code == "runtime_fixture_requires_isolation")
        );
        assert_eq!(std::fs::read_dir(&root).unwrap().count(), 1);
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn durable_http_failure_does_not_include_response_body_or_token() {
        let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let port = listener.local_addr().unwrap().port();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            stream
                .set_read_timeout(Some(Duration::from_secs(5)))
                .unwrap();
            let mut bytes = [0; 4096];
            let mut header = Vec::new();
            while !header.windows(4).any(|part| part == b"\r\n\r\n") {
                let n = stream.read(&mut bytes).unwrap();
                assert!(n > 0 && header.len() < 65536);
                header.extend_from_slice(&bytes[..n]);
            }
            let body = "private response contents and fixture-only token";
            write!(stream,"HTTP/1.1 500 Internal Server Error\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",body.len()).unwrap();
        });
        let started = Started {
            server: format!("http://127.0.0.1:{port}"),
            token: "fixture-only".into(),
        };
        assert_eq!(
            request(&started, "/v1/sources", None, 200).unwrap_err(),
            "runtime_fixture_http_status_500"
        );
        server.join().unwrap();
    }
}
