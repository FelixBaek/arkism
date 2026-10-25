package crew

// Agent-runner connections (arkism crew backends): an agent runner the owner already uses, reached over the
// arkism.backend.v1 HTTP contract (guide/agent-backends.md), that an expert or a workflow step may hand its work to.
// A connection is not a model provider: the runner does the whole task with its own tools, and it says what it can
// enforce (capabilities). Connections live on this host only (backends.json, the token in the credential store);
// an expert names one by its name (crew.backend), so one connection serves many experts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/FelixBaek/arkism/internal/host/crew/keystore"
)

const backendService = "arkism-backends" // credential-store service for connection tokens

// The credential store (fakes in tests, so a test never writes the host's keyring).
var (
	secretPut    = keystore.PutIn
	secretGet    = keystore.Get
	secretDelete = keystore.Delete
)

var backendName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// backendCaps are the capabilities Arkism reads; a runner that does not say one does not have it.
var backendCaps = []string{"model_choice", "instructions", "status", "cancel", "idempotency", "tool_restriction", "usage",
	"approval", "events", "session_reuse", "attachments", "structured_result"}

// backend is one connection. Local allows a loopback or private address (and plain http on loopback only);
// Isolated is the owner's word that the runner works in its own sandbox, which lets a read-only task run on a runner
// that cannot enforce read-only itself; OwnModel accepts that a runner without model choice uses its own model.
type backend struct {
	Name      string          `json:"name"`
	URL       string          `json:"url"`
	Token     string          `json:"token,omitempty"` // only when no credential store answers (file 0600)
	SecretRef string          `json:"secret_ref,omitempty"`
	Local     bool            `json:"local,omitempty"`
	Isolated  bool            `json:"isolated,omitempty"`
	OwnModel  bool            `json:"own_model,omitempty"`
	Caps      map[string]bool `json:"capabilities,omitempty"` // from the last check
	CheckedAt string          `json:"checked_at,omitempty"`
	Status    string          `json:"status,omitempty"` // ok | failed: <reason>
	CreatedAt string          `json:"created_at"`
}

func backendsFile() string { return stateFile("backends.json") }

func loadBackends() (map[string]*backend, error) {
	raw, err := os.ReadFile(backendsFile())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*backend{}, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []*backend
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, crewErr("backends.json을 읽지 못했어요")
	}
	out := map[string]*backend{}
	for _, b := range rows {
		out[b.Name] = b
	}
	return out, nil
}

func backendNames(all map[string]*backend) []string {
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func saveBackends(all map[string]*backend) error {
	names := backendNames(all)
	rows := make([]*backend, len(names))
	for i, n := range names {
		rows[i] = all[n]
	}
	raw, _ := json.MarshalIndent(rows, "", "  ")
	return writePrivate(backendsFile(), raw)
}

// writePrivate replaces path atomically with a 0600 file.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// token is the connection's secret, from the credential store or the file.
func (b *backend) token() (string, error) {
	if b.SecretRef == "" {
		return b.Token, nil
	}
	v, err := secretGet(b.SecretRef)
	if err != nil {
		return "", crewErr("연결 " + b.Name + "의 토큰을 자격 증명 저장소에서 읽지 못했어요")
	}
	return v, nil
}

// public is what list and check show: never the token.
func (b *backend) public() map[string]any {
	storage := "none"
	switch {
	case b.SecretRef != "":
		storage = keystore.StorageKeyring
	case b.Token != "":
		storage = keystore.StorageFile
	}
	return map[string]any{"name": b.Name, "url": b.URL, "token": storage, "local": b.Local, "isolated": b.Isolated, "own_model": b.OwnModel,
		"capabilities": b.Caps, "checked_at": b.CheckedAt, "status": b.Status, "limits": b.limits()}
}

// limits says, in plain words, what this connection will refuse or cannot do.
func (b *backend) limits() []string {
	out := []string{}
	if b.Caps == nil {
		return append(out, "아직 확인하지 않았어요: arkism crew backends check "+b.Name)
	}
	if !b.Caps["model_choice"] {
		out = append(out, map[bool]string{true: "모델 선택을 못 해요 (러너의 모델로 돌아요)", false: "모델 선택을 못 해요: 모델을 정한 전문가는 거절돼요"}[b.OwnModel])
	}
	if !b.Caps["tool_restriction"] {
		out = append(out, map[bool]string{true: "도구 제한을 못 해요 (격리된 러너로 표시됨)", false: "도구 제한을 못 해요: 읽기 전용 작업도 거절돼요"}[b.Isolated])
	}
	if !b.Caps["status"] {
		out = append(out, "상태 조회를 못 해요: 오래 걸리는 작업과 재시작 뒤 확인이 안 돼요")
	}
	if !b.Caps["cancel"] {
		out = append(out, "취소를 못 해요")
	}
	if !b.Caps["idempotency"] {
		out = append(out, "중복 접수 방지를 못 해요: 시작 응답을 못 받으면 결과를 알 수 없음으로 둬요")
	}
	if !b.Caps["usage"] {
		out = append(out, "사용량을 알려 주지 않아요: 비용을 알 수 없어요")
	}
	if !b.Caps["instructions"] {
		out = append(out, "지시를 따로 받지 못해요: 지시가 있는 전문가는 거절돼요")
	}
	return out
}

// ---------------------------------------------------------------- addresses

// checkBackendURL accepts https, or http on a loopback host of a local connection; no credentials, query or fragment.
func checkBackendURL(raw string, local bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, crewErr("연결 주소는 https://호스트[/경로] 모양이어야 해요")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !local || !loopbackHost(u.Hostname()) {
			return nil, crewErr("http 주소는 --local 연결의 이 컴퓨터(127.0.0.1·localhost)만 돼요. 그 밖에는 https를 써 주세요")
		}
	default:
		return nil, crewErr("연결 주소는 https://로 시작해야 해요")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		if err := allowBackendIP(ip, local); err != nil {
			return nil, err
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func loopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

var (
	cgnat = mustCIDR("100.64.0.0/10") // Tailscale and carrier NAT
	// Cloud metadata endpoints outside link-local, refused even for a local connection.
	metadataIPs = []net.IP{net.ParseIP("100.100.100.200"), net.ParseIP("fd00:ec2::254")}
	thisNet     = mustCIDR("0.0.0.0/8")
)

func mustCIDR(s string) *net.IPNet { _, n, _ := net.ParseCIDR(s); return n }

// allowBackendIP is the address check every connection is dialled through (so a DNS answer cannot move it):
// link-local (169.254.0.0/16 metadata included), multicast, unspecified and metadata addresses never; loopback,
// private and CGNAT only for a local connection.
func allowBackendIP(ip net.IP, local bool) error {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, m := range metadataIPs {
		if ip.Equal(m) {
			return crewErr("메타데이터 서버 주소에는 연결하지 않아요")
		}
	}
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || thisNet.Contains(ip) {
		return crewErr("이 주소(" + ip.String() + ")에는 연결하지 않아요")
	}
	if !local && (ip.IsLoopback() || ip.IsPrivate() || cgnat.Contains(ip)) {
		return crewErr("내부망 주소(" + ip.String() + ")는 --local로 만든 연결만 쓸 수 있어요")
	}
	return nil
}

// backendClient dials only allowed addresses (checked after DNS, at connect time), ignores proxy settings so the
// check sees the real destination, and never follows a redirect (the token would travel with it).
func backendClient(local bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		h, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(h)
		if ip == nil {
			return crewErr("주소를 확인하지 못했어요")
		}
		return allowBackendIP(ip, local)
	}}
	tr := &http.Transport{Proxy: nil, DialContext: d.DialContext, TLSHandshakeTimeout: 10 * time.Second, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// ---------------------------------------------------------------- add, remove, check

// insideArkismRun: a CLI Arkism started (ARKISM_CAPTURE_CHILD) must not change where Arkism sends work.
func insideArkismRun() bool { return os.Getenv("ARKISM_CAPTURE_CHILD") == "1" }

func addBackend(name, rawURL, token string, local, isolated, ownModel bool) (*backend, error) {
	if insideArkismRun() {
		return nil, crewErr("Arkism이 실행한 에이전트 안에서는 연결을 바꿀 수 없어요")
	}
	if !backendName.MatchString(name) || name == "local" {
		return nil, crewErr("연결 이름은 영어 소문자·숫자·-로 1~40자예요 (local은 Arkism 기본 실행이라 못 써요)")
	}
	u, err := checkBackendURL(rawURL, local)
	if err != nil {
		return nil, err
	}
	token = strings.TrimSpace(token)
	if strings.ContainsAny(token, " \t\r\n") {
		return nil, crewErr("토큰에 공백이 있어요")
	}
	all, err := loadBackends()
	if err != nil {
		return nil, err
	}
	if all[name] != nil {
		return nil, crewErr("같은 이름의 연결이 이미 있어요: 지우고 다시 만들어 주세요")
	}
	b := &backend{Name: name, URL: u.String(), Local: local, Isolated: isolated, OwnModel: ownModel, CreatedAt: nowZ()}
	if token != "" {
		if ref, err := secretPut(backendService, name, token); err == nil {
			b.SecretRef = ref
		} else {
			b.Token = token // no credential store: the 0600 file keeps it, like the keys file
		}
	}
	all[name] = b
	if err := saveBackends(all); err != nil {
		if b.SecretRef != "" {
			_ = secretDelete(b.SecretRef)
		}
		return nil, err
	}
	return b, nil
}

func removeBackend(name string) error {
	if insideArkismRun() {
		return crewErr("Arkism이 실행한 에이전트 안에서는 연결을 바꿀 수 없어요")
	}
	all, err := loadBackends()
	if err != nil {
		return err
	}
	b := all[name]
	if b == nil {
		return crewErr("그 연결을 찾지 못했어요")
	}
	delete(all, name)
	if err := saveBackends(all); err != nil {
		return err
	}
	if b.SecretRef != "" && secretDelete(b.SecretRef) != nil {
		return crewErr("연결은 지웠지만 자격 증명 저장소의 토큰을 지우지 못했어요")
	}
	return nil
}

// checkBackend asks the runner for its capabilities and stores them with the result; the error says why it failed.
func checkBackend(name string) (*backend, error) {
	all, err := loadBackends()
	if err != nil {
		return nil, err
	}
	b := all[name]
	if b == nil {
		return nil, crewErr("그 연결을 찾지 못했어요")
	}
	caps, cerr := fetchCaps(context.Background(), b)
	b.CheckedAt = nowZ()
	if cerr != nil {
		b.Status, b.Caps = "failed: "+clipRunes(cerr.Error(), 200), nil
	} else {
		b.Status, b.Caps = "ok", caps
	}
	if err := saveBackends(all); err != nil {
		return nil, err
	}
	return b, cerr
}

// fetchCaps is GET {url}/v1/capabilities; a runner that does not speak arkism.backend.v1 fails the check.
func fetchCaps(ctx context.Context, b *backend) (map[string]bool, error) {
	var page struct {
		Protocol     string          `json:"protocol"`
		Capabilities map[string]bool `json:"capabilities"`
	}
	if _, err := backendCall(ctx, b, http.MethodGet, "/v1/capabilities", nil, "", &page); err != nil {
		return nil, err
	}
	if page.Protocol != backendProtocol {
		return nil, crewErr(fmt.Sprintf("러너가 %s 계약을 말하지 않아요", backendProtocol))
	}
	caps := map[string]bool{}
	for _, k := range backendCaps {
		caps[k] = page.Capabilities[k]
	}
	return caps, nil
}

func runBackendsCmd(name string, a *args, stdinLine string) (any, error) {
	switch name {
	case "backends add":
		b, err := addBackend(a.vals["name"], a.vals["url"], stdinLine, a.flags["local"], a.flags["isolated"], a.flags["own-model"])
		if err != nil {
			return nil, err
		}
		return b.public(), nil
	case "backends remove":
		if err := removeBackend(a.pos[0]); err != nil {
			return nil, err
		}
		return map[string]any{"removed": a.pos[0]}, nil
	case "backends use":
		return useBackend(a.pos[0], a.vals["expert"])
	case "backends check":
		b, err := checkBackend(a.pos[0])
		if b == nil {
			return nil, err
		}
		return b.public(), nil // a failed check is reported in status, not as a command error
	}
	all, err := loadBackends()
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, n := range backendNames(all) {
		out = append(out, all[n].public())
	}
	return out, nil
}

// useBackend points an expert at a connection (local: back to Arkism's own CLIs). It runs as the owner (crew changes
// are a person's), so it is refused inside an agent Arkism started.
func useBackend(name, expert string) (any, error) {
	if insideArkismRun() {
		return nil, crewErr("Arkism이 실행한 에이전트 안에서는 전문가 설정을 바꿀 수 없어요")
	}
	if name != "local" {
		all, err := loadBackends()
		if err != nil {
			return nil, err
		}
		if all[name] == nil {
			return nil, crewErr("그 연결을 찾지 못했어요 (arkism crew backends list)")
		}
	}
	handle := "@" + strings.TrimPrefix(strings.TrimSpace(expert), "@")
	res, err := apiGet("/v1/crew/resolve?"+scopeQuery("handle", handle), 15*time.Second)
	if err != nil {
		return nil, crewErr(handle + " 전문가를 찾지 못했어요")
	}
	item := obj(res["item"])
	c := obj(item["crew"])
	if c["type"] != "agent" {
		return nil, crewErr(handle + "은(는) 전문가가 아니에요")
	}
	if name == "local" {
		delete(c, "backend")
	} else {
		c["backend"] = name
	}
	if _, err := apiPost("/v1/crew/"+pyQuote(sOr(item["id"]), "/"), map[string]any{"scope": scope(), "expected_revision": item["revision"], "crew": c}, 15*time.Second); err != nil {
		return nil, crewErr(handle + " 전문가를 저장하지 못했어요: " + errName(err))
	}
	return map[string]any{"expert": handle, "backend": name}, nil
}
