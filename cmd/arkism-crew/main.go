// arkism-crew runs Arkism workflow graphs on the host.
// Configuration comes from the environment; see
// crewrun.FromEnv.
//
//	arkism-crew serve                         daemon: schedules, webhooks, callbacks, waiting runs
//	arkism-crew run CREW_ID [--input JSON]    start a run now and drive it until it ends or waits
//	arkism-crew hook-url WORKFLOW_ID          print the webhook URL and its token (created on first use)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/FelixBaek/arkism/internal/crewrun"
	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/capture"
)

const usage = `usage:
  arkism-crew serve
  arkism-crew run CREW_ID [--input JSON]
  arkism-crew hook-url WORKFLOW_ID`

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := crewrun.FromEnv()
	if os.Getenv("ARKISM_SESSION_PROCESSOR") == "1" { // the bridge's session_processor_sink
		cfg.Engine.Sinks = append(cfg.Engine.Sinks, sessionSink)
	}
	os.Exit(run(ctx, os.Args[1:], os.Stdout, cfg))
}

// sessionSink spools a finished run's receipt for the capture drain's session processor.
func sessionSink(r crewrun.TraceReceipt) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	event, err := host.Loads(string(raw)) // integers stay integers in the spool
	if err != nil {
		return err
	}
	return capture.EnqueueSessionTrace(event.(map[string]any))
}

func run(ctx context.Context, args []string, out io.Writer, cfg crewrun.Config) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		if err := crewrun.Serve(ctx, cfg); err != nil && ctx.Err() == nil {
			slog.Error("arkism-crew", "error", err.Error())
			return 1
		}
		return 0
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		input := fs.String("input", "{}", "run input as JSON")
		if len(args) < 2 || fs.Parse(args[2:]) != nil {
			fmt.Fprintln(os.Stderr, usage)
			return 2
		}
		var in any
		if err := json.Unmarshal([]byte(*input), &in); err != nil {
			fmt.Fprintln(os.Stderr, "--input is not JSON")
			return 2
		}
		e := cfg.Engine
		if err := e.LoadCrew(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "load crew:", err)
			return 1
		}
		r, err := e.StartRun(ctx, args[1], "manual", in)
		if err != nil {
			fmt.Fprintln(os.Stderr, "start:", err)
			return 1
		}
		if err := e.Drive(ctx, r.ID); err != nil {
			fmt.Fprintln(os.Stderr, "run:", err)
		}
		final, err := e.Store.GetRun(context.WithoutCancel(ctx), r.ID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read run:", err)
			return 1
		}
		_ = json.NewEncoder(out).Encode(map[string]any{"run_id": final.ID, "status": final.State.Status,
			"summary": final.State.Summary, "waiting": final.State.Waiting})
		if final.State.Status == "failed" {
			return 1
		}
		return 0
	case "hook-url":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, usage)
			return 2
		}
		tok, err := crewrun.HookToken(cfg.Engine.HooksFile, args[1], true)
		if err != nil {
			fmt.Fprintln(os.Stderr, "hook token:", err)
			return 1
		}
		// For the owner at the terminal only; never logged.
		fmt.Fprintf(out, "POST %s/hooks/%s\nX-Arkism-Hook-Token: %s\n", cfg.Engine.PublicURL, args[1], tok)
		return 0
	}
	fmt.Fprintln(os.Stderr, usage)
	return 2
}
