package skills

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Memory signals (ST-813): the daily timer and the manual recompute share this
// entrypoint; dry-run unless --execute. Output is counts only.

func init() { host.Register("memory-signals", memorySignalsMain) }

// recomputeSignals pages POST /v1/memory-signals/recompute until no cursor is left or maxBatches is spent.
func recomputeSignals(endpoint, token string, scope host.Obj, maxBatches int) (host.Obj, error) {
	if !validExpiryEndpoint(endpoint, token) || maxBatches < 1 || maxBatches > 1000 {
		return nil, errors.New("invalid endpoint, credentials or budget")
	}
	var computed, changed int64
	cursor := ""
	for batch := 1; batch <= maxBatches; batch++ {
		c, d, next, err := recomputeSignalsBatch(endpoint, token, scope, cursor)
		if err != nil {
			return nil, err
		}
		computed, changed, cursor = computed+c, changed+d, next
		if cursor == "" {
			return host.Obj{{Key: "batches", Value: batch}, {Key: "computed", Value: computed}, {Key: "changed", Value: changed}, {Key: "more_possible", Value: false}}, nil
		}
	}
	return host.Obj{{Key: "batches", Value: maxBatches}, {Key: "computed", Value: computed}, {Key: "changed", Value: changed}, {Key: "more_possible", Value: true}}, nil
}

// recomputeSignalsBatch posts one recompute page and validates its counts and next cursor.
func recomputeSignalsBatch(endpoint, token string, scope host.Obj, cursor string) (int64, int64, string, error) {
	payload := host.Obj{{Key: "scope", Value: scope}}
	if cursor != "" {
		payload = append(payload, host.KV{Key: "cursor", Value: cursor})
	}
	body, err := host.Dumps(payload, host.DumpOpts{})
	if err != nil {
		return 0, 0, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/memory-signals/recompute", strings.NewReader(body))
	if err != nil {
		cancel()
		return 0, 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := noRedirectClient.Do(req)
	if err != nil {
		cancel()
		return 0, 0, "", err
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	res.Body.Close()
	cancel()
	if err != nil || res.StatusCode != 200 || len(raw) > 8192 {
		return 0, 0, "", errors.New("recompute request failed")
	}
	v, err := loadsStrict(string(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))))
	if err != nil {
		return 0, 0, "", err
	}
	data, ok := v.(map[string]any)
	next, isStr := data["next_cursor"].(string)
	c, cOK := host.Int(data["computed"])
	d, dOK := host.Int(data["changed"])
	if !ok || !keysAre(data, "computed", "changed", "next_cursor") || !isStr || len(next) > 2048 || !cOK || !dOK || c < 0 || c > 100 || d < 0 || d > c {
		return 0, 0, "", errors.New("invalid response")
	}
	return c, d, next, nil
}

func memorySignalsMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	p := newArgs("memory-signals", stderr)
	workspace, project := p.str("--workspace-id", ""), p.str("--project-id", "")
	user, agent := p.str("--user-id", ""), p.str("--agent-id", "")
	maxBatches := p.int("--max-batches", 100)
	endpoint := p.str("--url", host.DefaultURL)
	execute := p.flag("--execute")
	if !p.parse(args, 0, 0) || !p.need("--workspace-id", "--project-id") {
		return 2
	}
	scope := host.Obj{}
	for _, kv := range [][2]string{{"workspace_id", *workspace}, {"project_id", *project}, {"user_id", *user}, {"agent_id", *agent}} {
		if len(kv[1]) >= 256 {
			_, _ = io.WriteString(stderr, "invalid scope\n")
			return 2
		}
		scope = append(scope, host.KV{Key: kv[0], Value: kv[1]})
	}
	if !*execute {
		_, _ = io.WriteString(stdout, host.MustDumps(host.Obj{{Key: "dry_run", Value: true}, {Key: "max_batches", Value: *maxBatches}}, host.DumpOpts{})+"\n")
		return 0
	}
	result, err := recomputeSignals(*endpoint, os.Getenv("MEMORY_API_TOKEN"), scope, *maxBatches)
	if err != nil {
		_, _ = io.WriteString(stderr, "Signal recompute failed; earlier batches may have committed. Inputs, credentials and response bodies suppressed.\n")
		return 2
	}
	_, _ = io.WriteString(stdout, host.MustDumps(result, host.DumpOpts{})+"\n")
	return 0
}
