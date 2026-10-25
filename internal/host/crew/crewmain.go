package crew

// arkism crew: workflow calls ($이름), AI node runs, 라우팅, stored keys, subscription accounts and 모델 설정.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func init() {
	host.Register("crew", crewMain)
	host.ChildEnv = childEnv
	host.CustomInfo = customInfo
	host.DecideRoute = decideRoute
	host.ReportCall = reportCall
	host.CrewHookContext = func(prompt, callerModel string, timeout time.Duration) (string, error) {
		return hookContext(prompt, callerModel, timeout, nil, nil)
	}
	host.ListCrew = listCrew
	host.StartWorkflow = func(handle, task string, wait time.Duration, cwd string) (map[string]any, error) {
		return startWorkflow(handle, task, wait.Seconds(), cwd, nil)
	}
	host.DecideRun = func(runID string, approve bool, note string, wait time.Duration) (map[string]any, error) {
		return decideRun(runID, approve, note, wait.Seconds())
	}
}

const crewProg = "arkism crew"

const crewUsage = "{resolve,run-node,handoff,start,status,decide,hook-context,list,route,keys,accounts,providers,backends} ..."

// crewCommand is one parsed subcommand: its options and the positionals it needs.
type crewCommand struct {
	valued, bools []string
	positional    []string
	required      []string
	choices       map[string][]string
	floats        []string
}

var crewCommands = map[string]crewCommand{
	"handoff":      {},
	"resolve":      {positional: []string{"handle"}},
	"run-node":     {bools: []string{"json"}},
	"start":        {valued: []string{"task", "wait", "cwd"}, positional: []string{"handle"}, required: []string{"task"}, floats: []string{"wait"}},
	"status":       {positional: []string{"run_id"}},
	"decide":       {valued: []string{"note", "wait"}, bools: []string{"approve", "reject"}, positional: []string{"run_id"}, floats: []string{"wait"}},
	"hook-context": {valued: []string{"caller-model"}},
	"list":         {},
	"route": {valued: []string{"task", "cwd", "access", "effort", "group", "work-id"}, bools: []string{"decide-only"}, required: []string{"task"},
		choices: map[string][]string{"access": {"read", "write", "full"}, "effort": efforts}},
	"keys list":        {},
	"keys add":         {valued: []string{"provider", "name", "base-url", "monthly-budget"}, required: []string{"provider", "name"}, choices: map[string][]string{"provider": providers}, floats: []string{"monthly-budget"}},
	"keys remove":      {positional: []string{"id"}},
	"accounts list":    {},
	"accounts add":     {valued: []string{"cli", "name", "config-dir"}, required: []string{"cli", "name", "config-dir"}, choices: map[string][]string{"cli": {"claude", "codex"}}},
	"accounts remove":  {positional: []string{"id"}},
	"providers show":   {},
	"providers report": {bools: []string{"fresh"}},
	"providers watch":  {valued: []string{"interval", "report-every"}, floats: []string{"interval", "report-every"}},
	"backends list":    {},
	"backends add":     {valued: []string{"name", "url", "kind"}, bools: []string{"local", "isolated", "own-model", "memory"}, required: []string{"name", "url"}},
	"backends check":   {positional: []string{"name"}},
	"backends remove":  {positional: []string{"name"}},
	"backends use":     {valued: []string{"expert"}, positional: []string{"name"}, required: []string{"expert"}},
}

var crewGroups = map[string]string{"keys": "{list,add,remove}", "accounts": "{list,add,remove}", "providers": "{show,report,watch}", "backends": "{list,add,check,remove,use}"}

func quoteChoices(cs []string) string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = "'" + c + "'"
	}
	return strings.Join(out, ", ")
}

// crewCommandName joins a group and its subcommand ("keys add") and returns the remaining argv.
func crewCommandName(argv []string) (string, []string, error) {
	if len(argv) == 0 {
		return "", nil, fmt.Errorf("the following arguments are required: cmd")
	}
	name, rest := argv[0], argv[1:]
	if group, ok := crewGroups[name]; ok {
		if len(rest) == 0 {
			return "", nil, fmt.Errorf("the following arguments are required: %s_cmd", name)
		}
		if _, ok := crewCommands[name+" "+rest[0]]; !ok {
			return "", nil, fmt.Errorf("argument %s_cmd: invalid choice: '%s' (choose from %s)", name, rest[0], group)
		}
		name, rest = name+" "+rest[0], rest[1:]
	}
	return name, rest, nil
}

// parseCrew is argparse for crew: the command name, its options and positionals, or a usage error.
func parseCrew(argv []string) (string, *args, map[string]float64, error) {
	name, rest, err := crewCommandName(argv)
	if err != nil {
		return "", nil, nil, err
	}
	spec, ok := crewCommands[name]
	if !ok {
		return "", nil, nil, fmt.Errorf("argument cmd: invalid choice: '%s'", name)
	}
	a, err := parseArgs(rest, spec.valued, spec.bools, nil)
	if err != nil {
		return "", nil, nil, err
	}
	missing := append([]string{}, spec.positional[min(len(a.pos), len(spec.positional)):]...)
	for _, r := range spec.required {
		if !a.has(r) {
			missing = append(missing, "--"+r)
		}
	}
	if len(missing) > 0 {
		return "", nil, nil, fmt.Errorf("the following arguments are required: %s", strings.Join(missing, ", "))
	}
	if len(a.pos) > len(spec.positional) {
		return "", nil, nil, fmt.Errorf("unrecognized arguments: %s", strings.Join(a.pos[len(spec.positional):], " "))
	}
	for opt, cs := range spec.choices {
		if v, ok := a.vals[opt]; ok && !in(cs, v) {
			return "", nil, nil, fmt.Errorf("argument --%s: invalid choice: '%s' (choose from %s)", opt, v, quoteChoices(cs))
		}
	}
	floats := map[string]float64{}
	for _, opt := range spec.floats {
		if v, ok := a.vals[opt]; ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return "", nil, nil, fmt.Errorf("argument --%s: invalid float value: '%s'", opt, v)
			}
			floats[opt] = f
		}
	}
	if name == "decide" {
		switch {
		case a.flags["approve"] && a.flags["reject"]:
			return "", nil, nil, fmt.Errorf("argument --reject: not allowed with argument --approve")
		case !a.flags["approve"] && !a.flags["reject"]:
			return "", nil, nil, fmt.Errorf("one of the arguments --approve --reject is required")
		}
	}
	return name, a, floats, nil
}

func (a *args) get(name, def string) string {
	if v, ok := a.vals[name]; ok {
		return v
	}
	return def
}

// field reads k from a map or an ordered object.
func field(v any, k string) any {
	if o, ok := v.(host.Obj); ok {
		x, _ := o.Get(k)
		return x
	}
	return obj(v)[k]
}

// runNodeCLI is run-node --json: the engine's spec on stdin → the result JSON, failures included.
func runNodeCLI(raw string) map[string]any {
	if raw == "" {
		raw = "{}"
	}
	v, err := host.Loads(raw)
	spec, ok := v.(map[string]any)
	if err != nil || !ok {
		return failed("", "표준입력이 JSON 객체가 아니에요")
	}
	out, err := runNodeFn(spec)
	if err != nil {
		// Python lets a non-Crew failure end the process; the engine reads a result either way.
		out = failed("", err.Error())
		if truthy(spec["origin"]) {
			reportFixed(spec, out)
		}
		return out
	}
	if out["backend"] != nil {
		return out // an external runner's usage is what it reported or unknown, never Arkism's estimate
	}
	if out["ok"] == true {
		out["usage"] = usageOf(spec, out)
	}
	// A routed run reported each attempt itself; a fixed one is reported here, when the caller says what it worked for.
	if truthy(spec["origin"]) && out["routed"] == nil {
		reportFixed(spec, out)
	}
	return out
}

func crewMain(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	name, a, floats, err := parseCrew(argv)
	if err != nil {
		usage := crewUsage
		if len(argv) > 0 && crewGroups[argv[0]] != "" {
			usage = argv[0] + " " + crewGroups[argv[0]] + " ..."
		}
		return usageError(stderr, crewProg, usage, err)
	}
	code, err := runCrew(name, a, floats, stdin, stdout)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		if isCrewErr(err) {
			return 2
		}
		return 1
	}
	return code
}

func runCrew(name string, a *args, floats map[string]float64, stdin io.Reader, stdout io.Writer) (int, error) {
	readAll := func() string { raw, _ := io.ReadAll(stdin); return string(raw) }
	switch name {
	case "handoff":
		return runHandoffCmd(stdin, stdout)
	case "resolve":
		res, err := resolve(a.pos[0], 8*time.Second)
		if err != nil {
			return 0, err
		}
		if len(res) == 0 {
			return 0, crewErr("그 이름의 워크플로우가 없어요")
		}
		fmt.Fprintln(stdout, dumpIndent(res))
	case "run-node":
		stopOnTerm.Store(true)
		out := runNodeCLI(readAll())
		fmt.Fprintln(stdout, dumpLine(ordered(out, resultOrder...)))
		if out["ok"] != true {
			return 1, nil
		}
	case "start", "status", "decide":
		return runWorkflowCmd(name, a, floats, stdout)
	case "hook-context":
		text, err := hookContext(readAll(), a.get("caller-model", os.Getenv("ARKISM_CALLER_MODEL")), 3*time.Second, nil, nil)
		if err != nil {
			return 0, err
		}
		if text != "" {
			fmt.Fprintln(stdout, text)
		}
	case "list":
		rows, err := listCrew(8 * time.Second)
		if err != nil {
			return 0, err
		}
		out := []any{}
		for _, r := range rows {
			out = append(out, ordered(r, crewOrder...))
		}
		fmt.Fprintln(stdout, dumpIndent(out))
	case "route":
		return runRouteCmd(a, stdout)
	case "providers show", "providers report", "providers watch":
		return runProvidersCmd(name, a, floats, stdin, stdout)
	case "accounts add", "accounts remove", "accounts list", "backends list", "backends add", "backends check", "backends remove", "backends use":
		if strings.HasPrefix(name, "accounts ") {
			return runAccountsCmd(name, a, stdout)
		}
		return runBackendsCLI(name, a, stdin, stdout)
	case "keys list", "keys add", "keys remove":
		return runKeysCmd(name, a, floats, stdin, stdout)
	}
	return 0, nil
}

func runHandoffCmd(stdin io.Reader, stdout io.Writer) (int, error) {
	stopOnTerm.Store(true)
	out, err := runHandoff(stdin, handoffCallerRequest, runNodeFn)
	if err != nil {
		return 0, err
	}
	fmt.Fprintln(stdout, dumpIndent(out))
	if out["ok"] != true {
		return 1, nil
	}
	return 0, nil
}

func runWorkflowCmd(name string, a *args, floats map[string]float64, stdout io.Writer) (int, error) {
	var view map[string]any
	var err error
	switch name {
	case "start":
		view, err = startWorkflow(a.pos[0], a.vals["task"], floats["wait"], a.get("cwd", ""), nil)
	case "status":
		var rec map[string]any
		if rec, err = getRun(a.pos[0]); err == nil {
			view = runView(rec)
		}
	default:
		view, err = decideRun(a.pos[0], a.flags["approve"], a.get("note", ""), floats["wait"])
	}
	if err != nil {
		return 0, err
	}
	fmt.Fprintln(stdout, dumpIndent(ordered(view, viewOrder...)))
	if in([]string{"failed", "stopped"}, pyStr(view["status"])) {
		return 1, nil
	}
	return 0, nil
}

func runRouteCmd(a *args, stdout io.Writer) (int, error) {
	if a.flags["decide-only"] {
		d, err := decideRoute(a.vals["task"], a.get("group", ""))
		if err != nil {
			return 0, err
		}
		fmt.Fprintln(stdout, dumpIndent(d))
		return 0, nil
	}
	stopOnTerm.Store(true)
	out, err := runRoutedFn(map[string]any{"task": a.vals["task"], "cwd": a.get("cwd", ""), "access": a.get("access", "read"),
		"effort": a.get("effort", "medium"), "group": a.get("group", ""), "work_id": a.get("work-id", "")}, nil, nil, nil)
	if err != nil {
		return 0, err
	}
	fmt.Fprintln(stdout, dumpLine(ordered(out, resultOrder...)))
	if out["ok"] != true {
		return 1, nil
	}
	return 0, nil
}

func runProvidersCmd(name string, a *args, floats map[string]float64, stdin io.Reader, stdout io.Writer) (int, error) {
	switch name {
	case "providers show":
		inv, err := inventory("", nil, false)
		if err != nil {
			return 0, err
		}
		fmt.Fprintln(stdout, dumpIndent(inv))
	case "providers report":
		n, err := providersReport(a.flags["fresh"])
		if err != nil {
			return 0, err
		}
		fmt.Fprintf(stdout, "처리한 요청 %d개\n", n)
	case "providers watch":
		interval, every := 3.0, 60.0
		if v, ok := floats["interval"]; ok {
			interval = v
		}
		if v, ok := floats["report-every"]; ok {
			every = v
		}
		if err := desktopProvidersWatch(time.Duration(interval*float64(time.Second)), time.Duration(every*float64(time.Second)), stdin, stdout); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

func runBackendsCLI(name string, a *args, stdin io.Reader, stdout io.Writer) (int, error) {
	line := ""
	if name == "backends add" { // the token comes on stdin, never as an argument
		line, _ = bufio.NewReader(stdin).ReadString('\n')
	}
	out, err := runBackendsCmd(name, a, strings.TrimSpace(line))
	if err != nil {
		return 0, err
	}
	fmt.Fprintln(stdout, dumpIndent(out))
	return 0, nil
}

func runAccountsCmd(name string, a *args, stdout io.Writer) (int, error) {
	var result any
	var err error
	switch name {
	case "accounts add":
		result, err = host.AccountsRegister(a.vals["cli"], a.vals["name"], a.vals["config-dir"], host.Home, host.AgentsState)
	case "accounts remove":
		if err = host.AccountsUnregister(a.pos[0], host.AgentsState); err == nil {
			result = host.Obj{{Key: "removed", Value: a.pos[0]}}
		}
	default:
		var inv []any
		if inv, err = inventory("", nil, false); err == nil {
			rows := []any{}
			for _, p := range inv {
				for _, acc := range list(field(p, "accounts")) {
					if c := field(acc, "cli"); c == "claude" || c == "codex" {
						rows = append(rows, acc)
					}
				}
			}
			result = rows
		}
	}
	if err != nil {
		if isCrewErr(err) {
			return 0, err
		}
		return 0, crewErr(err.Error())
	}
	fmt.Fprintln(stdout, dumpIndent(result))
	return 0, nil
}

func runKeysCmd(name string, a *args, floats map[string]float64, stdin io.Reader, stdout io.Writer) (int, error) {
	switch name {
	case "keys list":
		keys, err := loadKeys()
		if err != nil {
			return 0, err
		}
		out := []any{}
		for _, k := range keys {
			out = append(out, publicKey(k))
		}
		fmt.Fprintln(stdout, dumpIndent(out))
	case "keys add":
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		var budget any
		if f, ok := floats["monthly-budget"]; ok {
			budget = pyFloat(f)
		}
		row, err := addKey(a.vals["provider"], a.vals["name"], line, a.get("base-url", ""), budget, "")
		if err != nil {
			return 0, err
		}
		fmt.Fprintln(stdout, dumpLine(row))
	case "keys remove":
		if err := removeKey(a.pos[0]); err != nil {
			return 0, err
		}
		fmt.Fprintln(stdout, "지웠어요")
	}
	return 0, nil
}
