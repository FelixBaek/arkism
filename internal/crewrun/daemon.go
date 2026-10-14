package crewrun

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config comes from the environment (see FromEnv).
type Config struct {
	Listen string
	Engine *Engine
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// FromEnv builds the engine like `arkism capture` does its client:
//
//	ARKISM_URL (http://127.0.0.1:8091), ARKISM_TOKEN_FILE (/srv/arkism/config/crew.token),
//	ARKISM_WORKSPACE, ARKISM_PROJECT, ARKISM_USER, ARKISM_AGENT (scope),
//	ARKISM_CREW_LISTEN (127.0.0.1:8095), ARKISM_CREW_PUBLIC_URL (http://LISTEN),
//	ARKISM_CREW_RUNNER ("/srv/arkism/bin/arkism crew"), ARKISM_CREW_SANDBOX,
//	ARKISM_CREW_MODEL ("opus 5.5", AI nodes without a model), ARKISM_CREW_GH (gh), TZ (schedule time zone),
//	ARKISM_CREW_KEYS, ARKISM_CREW_NOTIFY, ARKISM_CREW_HOOKS (files under ~/.config/arkism).
func FromEnv() Config {
	home, _ := os.UserHomeDir()
	state := filepath.Join(home, ".config", "arkism")
	listen := envOr("ARKISM_CREW_LISTEN", "127.0.0.1:8095")
	loc := time.Local
	if tz := os.Getenv("TZ"); tz != "" {
		if l, err := time.LoadLocation(strings.TrimPrefix(tz, ":")); err == nil {
			loc = l
		}
	}
	client := &Client{
		BaseURL:   envOr("ARKISM_URL", "http://127.0.0.1:8091"),
		TokenFile: envOr("ARKISM_TOKEN_FILE", "/srv/arkism/config/crew.token"),
		Scope: Scope{WorkspaceID: envOr("ARKISM_WORKSPACE", "default"),
			ProjectID: envOr("ARKISM_PROJECT", "default"), UserID: envOr("ARKISM_USER", "management:owner"),
			AgentID: os.Getenv("ARKISM_AGENT")},
		// The local Arkism is reached directly, never through a proxy.
		HTTP: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: nil}},
	}
	return Config{Listen: listen, Engine: &Engine{
		Store:        client,
		Meetings:     client,
		Loc:          loc,
		DefaultModel: envOr("ARKISM_CREW_MODEL", "opus 5.5"),
		Runner:       strings.Fields(envOr("ARKISM_CREW_RUNNER", "/srv/arkism/bin/arkism crew")),
		Sandbox:      strings.Fields(os.Getenv("ARKISM_CREW_SANDBOX")),
		GH:           envOr("ARKISM_CREW_GH", "gh"),
		KeysFile:     envOr("ARKISM_CREW_KEYS", filepath.Join(state, "provider-keys.json")),
		NotifyFile:   envOr("ARKISM_CREW_NOTIFY", filepath.Join(state, "notify.json")),
		HooksFile:    envOr("ARKISM_CREW_HOOKS", filepath.Join(state, "crew-hooks.json")),
		PublicURL:    envOr("ARKISM_CREW_PUBLIC_URL", "http://"+listen),
	}}
}

// FireSchedules starts enabled schedule workflows whose cron matches the
// current minute, at most once per workflow per minute.
func (e *Engine) FireSchedules(ctx context.Context, now time.Time) []string {
	loc := e.Loc
	if loc == nil {
		loc = time.Local
	}
	minute := now.In(loc).Truncate(time.Minute)
	var due []string
	e.mu.Lock()
	if e.fired == nil {
		e.fired, e.crons = map[string]time.Time{}, map[string]*Cron{}
	}
	for id, it := range e.crew {
		t := it.Crew.Trigger
		if it.Crew.Type != "workflow" || !it.Crew.Enabled || t == nil || t.Kind != "schedule" || e.fired[id].Equal(minute) {
			continue
		}
		c, ok := e.crons[t.Cron]
		if !ok {
			var err error
			if c, err = ParseCron(t.Cron); err != nil {
				e.log().Warn("crew schedule", "workflow_id", id, "error", err.Error())
				e.crons[t.Cron] = nil
				continue
			}
			e.crons[t.Cron] = c
		}
		if c != nil && c.Match(minute) {
			e.fired[id] = minute
			due = append(due, id)
		}
	}
	e.mu.Unlock()
	var started []string
	for _, id := range due {
		run, err := e.StartRun(ctx, id, "schedule", map[string]any{"scheduled_at": minute.Format(time.RFC3339)})
		if err != nil {
			e.log().Warn("crew schedule start", "workflow_id", id, "error", err.Error())
			continue
		}
		started = append(started, run.ID)
		e.Go(ctx, run.ID)
	}
	return started
}

// PollWaiting resumes waiting runs whose decision arrived or whose time came.
func (e *Engine) PollWaiting(ctx context.Context) error {
	runs, err := e.Store.ListRuns(ctx, "waiting", maxListedRuns)
	if err != nil {
		return err
	}
	now := e.now()
	for _, r := range runs {
		if readyToResume(r, now) {
			e.Go(ctx, r.ID)
		}
	}
	return e.StartRequested(ctx)
}

// StartRequested drives runs a person created (the desktop, $handle calls)
// that nothing has touched yet: every node pending, or done/skipped when the
// run restarts from where a failed one stopped. Runs another engine process
// created are never taken.
func (e *Engine) StartRequested(ctx context.Context) error {
	runs, err := e.Store.ListRuns(ctx, "running", maxListedRuns)
	if err != nil {
		return err
	}
	var todo []string
	for _, r := range runs {
		if r.CreatedByType == "human" && untouched(r) {
			todo = append(todo, r.ID)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	// A run asked for right after a save must meet that save, not definitions cached up to a minute ago.
	if err := e.LoadCrew(ctx); err != nil {
		return err
	}
	for _, id := range todo {
		e.Go(ctx, id)
	}
	return nil
}

func untouched(r Run) bool {
	for _, n := range r.State.Nodes {
		if n.Status != "pending" && n.Status != "done" && n.Status != "skipped" {
			return false
		}
	}
	return true
}

// shortcut: one page of open runs per status; page through them if a scope
// ever holds more than this many waiting or running runs.
const maxListedRuns = 1000

// ResumeRunning continues runs left running by an earlier engine; used once
// at start-up (a run another process drives would be settled as interrupted).
func (e *Engine) ResumeRunning(ctx context.Context) error {
	runs, err := e.Store.ListRuns(ctx, "running", maxListedRuns)
	if err != nil {
		return err
	}
	for _, r := range runs {
		e.Go(ctx, r.ID)
	}
	return nil
}

// Serve is the daemon: crew reload every minute, schedules every 15 s,
// waiting runs every 10 s, and the webhook/callback listener.
func Serve(ctx context.Context, cfg Config) error {
	// The crew API is plain HTTP with callbacks, so it stays on loopback; put a TLS proxy in front for remote use.
	if !loopbackAddr(cfg.Listen) {
		return errors.New("ARKISM_CREW_LISTEN must be a loopback IP address, got " + cfg.Listen)
	}
	e := cfg.Engine
	log := e.log()
	for {
		if err := e.LoadCrew(ctx); err == nil {
			break
		} else {
			log.Warn("crew load", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	if err := e.ResumeRunning(ctx); err != nil {
		log.Warn("crew resume", "error", err.Error())
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: e.Handler(ctx), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("crew listener", "error", err.Error())
		}
	}()
	log.Info("arkism-crew serving", "listen", cfg.Listen)
	reload, sched, poll := time.NewTicker(time.Minute), time.NewTicker(15*time.Second), time.NewTicker(10*time.Second)
	defer reload.Stop()
	defer sched.Stop()
	defer poll.Stop()
	e.FireSchedules(ctx, e.now())
	for {
		select {
		case <-ctx.Done():
			shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(shut)
			cancel()
			e.Wait()
			return nil
		case <-reload.C:
			if err := e.LoadCrew(ctx); err != nil {
				log.Warn("crew load", "error", err.Error())
			}
		case <-sched.C:
			e.FireSchedules(ctx, e.now())
		case <-poll.C:
			if err := e.PollWaiting(ctx); err != nil {
				log.Warn("crew poll", "error", err.Error())
			}
			if err := e.PollMeetings(ctx); err != nil {
				log.Warn("crew meetings", "error", err.Error())
			}
		}
	}
}

// loopbackAddr accepts loopback IP literals only; a name like "localhost" could resolve elsewhere.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
