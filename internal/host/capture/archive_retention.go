package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// Owner SSH metadata-only retention checks for generated PC Zcode copies.

const retentionLimit = 64

var (
	retentionHash   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	retentionStates = map[string]bool{"active": true, "expired": true, "unknown": true, "changed": true}
)

func retentionArchiveID(sid string) string {
	return sha256Hex([]byte(strings.Join([]string{"pc", "zcode", sid, "db-session"}, "\x00")))
}

func retentionValidate(items any) error {
	invalid := valueErr("retention_request_invalid")
	list, ok := items.([]any)
	if !ok || len(list) < 1 || len(list) > retentionLimit {
		return invalid
	}
	seen := map[string]bool{}
	for _, r := range list {
		row, ok := retentionRow(r)
		if !ok || len(row) != 2 {
			return invalid
		}
		sid, sok := row["session_id"].(string)
		sha, hok := row["sha256"].(string)
		if !sok || !hok || len(sid) < 1 || len(sid) > 256 || strings.ContainsFunc(sid, func(c rune) bool { return c < 32 }) ||
			seen[sid] || !retentionHash.MatchString(sha) {
			return invalid
		}
		seen[sid] = true
	}
	return nil
}

// retentionRow reads a request row given as a decoded map or a built Obj.
func retentionRow(v any) (map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	case host.Obj:
		m := map[string]any{}
		for _, kv := range x {
			m[kv.Key] = kv.Value
		}
		return m, true
	}
	return nil, false
}

// retentionParse is json.loads that refuses frames over 64 KiB and duplicate keys.
func retentionParse(raw []byte) (any, error) {
	if len(raw) > 65536 {
		return nil, valueErr("retention_frame_limit")
	}
	if !utf8.Valid(raw) {
		return nil, errJSONShape
	}
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))))
	dec.UseNumber()
	v, err := retentionValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, valueErr("extra_data")
	}
	return v, nil
}

func retentionValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}
	if d == '[' {
		list := []any{}
		for dec.More() {
			v, err := retentionValue(dec)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		_, err := dec.Token()
		return list, err
	}
	m := map[string]any{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		v, err := retentionValue(dec)
		if err != nil {
			return nil, err
		}
		if _, dup := m[k.(string)]; dup {
			return nil, valueErr("duplicate_key")
		}
		m[k.(string)] = v
	}
	_, err = dec.Token()
	return m, err
}

// retentionAware parses an aware ISO time; ok false for a naive or invalid one.
func retentionAware(v any) (float64, bool, error) {
	s, ok := v.(string)
	if !ok {
		return 0, false, fmt.Errorf("%w: expires_at", errType)
	}
	ts, aware, ok := fromISO(s)
	if !ok {
		return 0, false, valueErr("Invalid isoformat string")
	}
	return ts, aware, nil
}

// retentionDecide reports, per item, the server's state for its archived copy.
func retentionDecide(client archivePoster, items any, now time.Time) (host.Obj, error) {
	if err := retentionValidate(items); err != nil {
		return nil, err
	}
	invalid := valueErr("retention_response_invalid")
	result := []any{}
	for _, r := range items.([]any) {
		row, _ := retentionRow(r)
		sid := row["session_id"].(string)
		aid := retentionArchiveID(sid)
		decision := host.Obj{{Key: "id", Value: aid}, {Key: "sha256", Value: row["sha256"]}, {Key: "state", Value: "unknown"},
			{Key: "expires_at", Value: nil}, {Key: "revision", Value: 0}}
		view, err := client.post("get", map[string]any{"id": aid})
		var api *APIError
		if err != nil && !(errors.As(err, &api) && api.Status == 404) {
			return nil, err
		}
		if err == nil {
			state, err := retentionViewState(view, aid, sid, row["sha256"], now, invalid)
			if err != nil {
				return nil, err
			}
			decision.Set("state", state)
			decision.Set("expires_at", view["expires_at"])
			decision.Set("revision", view["revision"])
		}
		result = append(result, decision)
	}
	return host.Obj{{Key: "version", Value: 1}, {Key: "items", Value: result}}, nil
}

// retentionViewState validates the server's view of one archived copy and returns its
// state, "changed" when the copy no longer matches the local sha256.
func retentionViewState(view map[string]any, aid, sid string, want any, now time.Time, invalid error) (string, error) {
	rev, revOK := host.Int(view["revision"])
	state, _ := view["state"].(string)
	if view == nil || view["id"] != aid || view["device"] != "pc" || view["origin"] != "zcode" || view["session_id"] != sid ||
		view["format"] != "reconstructed_zcode" || !revOK || rev < 1 || (state != "active" && state != "expired") {
		return "", invalid
	}
	expires, aware, err := retentionAware(view["expires_at"])
	if err != nil {
		return "", err
	}
	if !aware || state == "expired" && expires > float64(now.UnixNano())/1e9 {
		return "", invalid
	}
	sha, ok := view["sha256"].(string)
	if !ok || !retentionHash.MatchString(sha) {
		return "", invalid
	}
	if sha != want {
		state = "changed"
	}
	return state, nil
}

// retentionRemoteStates asks the server, over the owner SSH path, for each copy's state.
func retentionRemoteStates(items []any) ([]string, error) {
	if err := retentionValidate(items); err != nil {
		return nil, err
	}
	argv, err := winSSHCommand("zcode", "archive-retention")
	if err != nil {
		return nil, err
	}
	payload := host.MustDumps(host.Obj{{Key: "version", Value: 1}, {Key: "items", Value: items}}, host.DumpOpts{})
	code, stdout, err := winRun(argv, []byte(payload), 60*time.Second)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, valueErr("retention_ssh_failed")
	}
	v, err := retentionParse(stdout)
	if err != nil {
		return nil, err
	}
	invalid := valueErr("retention_response_invalid")
	data, ok := v.(map[string]any)
	version, vok := host.Int(data["version"])
	list, lok := data["items"].([]any)
	if !ok || len(data) != 2 || !vok || version != 1 || !lok || len(list) != len(items) {
		return nil, invalid
	}
	states := []string{}
	for i, r := range list {
		request, _ := retentionRow(items[i])
		state, err := retentionRemoteState(request, r, invalid)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

// retentionRemoteState validates the server's answer r for one request row and returns its state.
func retentionRemoteState(request map[string]any, r any, invalid error) (string, error) {
	resp, ok := r.(map[string]any)
	if !ok || len(resp) != 5 {
		return "", invalid
	}
	for _, k := range []string{"id", "sha256", "state", "expires_at", "revision"} {
		if _, has := resp[k]; !has {
			return "", invalid
		}
	}
	state, _ := resp["state"].(string)
	rev, revOK := host.Int(resp["revision"])
	if resp["id"] != retentionArchiveID(request["session_id"].(string)) || resp["sha256"] != request["sha256"] || !retentionStates[state] || !revOK {
		return "", invalid
	}
	if state != "unknown" {
		if _, isStr := resp["expires_at"].(string); rev < 1 || !isStr {
			return "", invalid
		}
		_, aware, err := retentionAware(resp["expires_at"])
		if err != nil {
			return "", err
		}
		if !aware {
			return "", invalid
		}
	}
	return state, nil
}

func retentionMain(_ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	run := func() (host.Obj, error) {
		raw, err := io.ReadAll(io.LimitReader(stdin, 65537))
		if err != nil {
			return nil, err
		}
		v, err := retentionParse(raw)
		if err != nil {
			return nil, err
		}
		request, ok := v.(map[string]any)
		version, vok := host.Int(request["version"])
		_, hasItems := request["items"]
		if !ok || len(request) != 2 || !hasItems || !vok || version != 1 {
			return nil, valueErr("retention_request_invalid")
		}
		return retentionDecide(archiveClient{}, request["items"], time.Now())
	}
	out, err := run()
	if err != nil {
		fmt.Fprintln(stderr, "archive_retention_failed")
		return 1
	}
	fmt.Fprintln(stdout, host.MustDumps(out, host.DumpOpts{}))
	return 0
}

func init() { host.Register("archive-retention", retentionMain) }
