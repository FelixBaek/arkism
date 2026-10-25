package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Candidate expiry: the bounded scheduler entrypoint; dry-run unless --execute.

func init() { host.Register("expire-candidates", expireMain) }

var scopeFields = []string{"workspace_id", "project_id", "user_id", "agent_id"}

func expiryPlan(scope map[string]string, days, batchSize, maxBatches int, now time.Time) (host.Obj, error) {
	if days < 1 || days > 3650 || batchSize < 1 || batchSize > 100 || maxBatches < 1 || maxBatches > 100 {
		return nil, errors.New("invalid expiry budget")
	}
	if len(scope) != len(scopeFields) {
		return nil, errors.New("invalid scope")
	}
	scoped := host.Obj{}
	for _, k := range scopeFields {
		v, ok := scope[k]
		if !ok || len(v) >= 256 {
			return nil, errors.New("invalid scope")
		}
		scoped = append(scoped, host.KV{Key: k, Value: v})
	}
	if scope["workspace_id"] == "" || scope["project_id"] == "" {
		return nil, errors.New("missing scope")
	}
	before := now.UTC().Add(-time.Duration(days) * 24 * time.Hour)
	return host.Obj{{Key: "scope", Value: scoped}, {Key: "before", Value: isoMillis(before)}, {Key: "limit", Value: batchSize}}, nil
}

// positiveInt is `type(v) is int and v >= 1` for a decoded JSON value, at any size.
func positiveInt(v any) bool {
	n, ok := v.(json.Number)
	s := string(n)
	return ok && !strings.ContainsAny(s, ".eE-") && s != "0"
}

func validExpiryEndpoint(raw, token string) bool {
	u, err := url.Parse(raw)
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n") {
		return false
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 0 || n > 65535 {
			return false
		}
	}
	host := strings.ToLower(u.Hostname())
	scheme := strings.ToLower(u.Scheme)
	if host == "" || u.User.String() != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return scheme == "https" || scheme == "http" && containsStr([]string{"localhost", "127.0.0.1", "::1"}, host)
}

// noRedirectClient never follows redirects and never uses a proxy from the environment.
var noRedirectClient = &http.Client{
	Transport:     &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func executeExpiry(endpoint, token string, payload host.Obj, maxBatches int) (host.Obj, error) {
	if !validExpiryEndpoint(endpoint, token) {
		return nil, errors.New("invalid endpoint or credentials")
	}
	limitAny, _ := payload.Get("limit")
	limit, ok := limitAny.(int)
	if maxBatches < 1 || maxBatches > 100 || !ok || limit < 1 || limit > 100 {
		return nil, errors.New("invalid batch budget")
	}
	body, err := host.Dumps(payload, host.DumpOpts{})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for batch := 1; batch <= maxBatches; batch++ {
		raw, err := executeExpiryPost(endpoint, token, body)
		if err != nil {
			return nil, err
		}
		v, err := loadsStrict(string(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))))
		if err != nil {
			return nil, err
		}
		data, ok := v.(map[string]any)
		expired, isList := data["expired"].([]any)
		if !ok || len(data) != 1 || !isList || len(expired) > limit {
			return nil, errors.New("invalid response")
		}
		if err := executeExpiryRefs(expired, seen); err != nil {
			return nil, err
		}
		if len(expired) < limit {
			return host.Obj{{Key: "batches", Value: batch}, {Key: "expired", Value: len(seen)}, {Key: "more_possible", Value: false}}, nil
		}
	}
	return host.Obj{{Key: "batches", Value: maxBatches}, {Key: "expired", Value: len(seen)}, {Key: "more_possible", Value: true}}, nil
}

// executeExpiryPost sends one expiry batch and returns the response body.
func executeExpiryPost(endpoint, token, body string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/memory_candidates/expire", strings.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := noRedirectClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	res.Body.Close()
	cancel()
	if res.StatusCode != 200 {
		return nil, errors.New("expiry request failed")
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, errors.New("oversized response")
	}
	return raw, nil
}

// executeExpiryRefs checks one batch's expired references and adds their ids to seen.
func executeExpiryRefs(expired []any, seen map[string]bool) error {
	for _, r := range expired {
		ref, ok := r.(map[string]any)
		id, isStr := ref["id"].(string)
		if !ok || !keysAre(ref, "collection", "id", "revision") || ref["collection"] != "memory_candidates" ||
			!isStr || len(id) < 1 || len(id) > 256 || !positiveInt(ref["revision"]) || seen[id] {
			return errors.New("invalid expired reference")
		}
		seen[id] = true
	}
	return nil
}

func expireMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	p := newArgs("expire-candidates", stderr)
	workspace, project := p.str("--workspace-id", ""), p.str("--project-id", "")
	user, agent := p.str("--user-id", ""), p.str("--agent-id", "")
	days, batchSize, maxBatches := p.int("--retention-days", 0), p.int("--batch-size", 50), p.int("--max-batches", 1)
	endpoint := p.str("--url", host.DefaultURL)
	execute := p.flag("--execute")
	if !p.parse(args, 0, 0) || !p.need("--workspace-id", "--project-id", "--retention-days") {
		return 2
	}
	result, err := func() (host.Obj, error) {
		payload, err := expiryPlan(map[string]string{"workspace_id": *workspace, "project_id": *project, "user_id": *user, "agent_id": *agent},
			*days, *batchSize, *maxBatches, time.Now())
		if err != nil {
			return nil, err
		}
		if !*execute {
			before, _ := payload.Get("before")
			return host.Obj{{Key: "dry_run", Value: true}, {Key: "before", Value: before}, {Key: "batch_size", Value: *batchSize}, {Key: "max_batches", Value: *maxBatches}}, nil
		}
		return executeExpiry(*endpoint, os.Getenv("MEMORY_API_TOKEN"), payload, *maxBatches)
	}()
	if err != nil {
		_, _ = io.WriteString(stderr, "Expiry failed; earlier batches may have committed. Inputs, credentials and response bodies suppressed.\n")
		return 2
	}
	_, _ = io.WriteString(stdout, host.MustDumps(result, host.DumpOpts{})+"\n")
	return 0
}
