package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"
)

var errInput = errors.New("invalid maintenance input")

const usage = `memory-maintenance status
memory-maintenance begin-privacy --request FILE --allow-global-downtime [--token-file FILE]
memory-maintenance begin-supersede --memory-id ID --request FILE --allow-global-downtime [--token-file FILE]
memory-maintenance advance|hold|release|cancel --expected-revision N

MEMORY_DATABASE_URL must be an administrative connection. Begin also requires
MEMORY_MAINTENANCE_TOKEN or a private token file for an authorized human.
Beginning maintenance fences normal database operations globally. Advance commits
one bounded step. Release only resumes held work; it does not reopen the service.
Only successful completion reopens service after mutation. Cancel is restricted
to privacy discovery before any quarantine or mutation; it cannot cancel stale
maintenance. There is no force-unlock.
Optional --timeout (default 30s, maximum 15m) bounds each invocation.
`

type options struct {
	command, request, memoryID, tokenFile string
	revision                              int64
	downtime                              bool
	timeout                               time.Duration
}

func parseOptions(args []string) (options, bool, error) {
	var o options
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return o, true, nil
	}
	if len(args) == 0 {
		return o, false, errInput
	}
	o.command = args[0]
	switch o.command {
	case "status", "begin-privacy", "begin-supersede", "advance", "hold", "release", "cancel":
	default:
		return o, false, errInput
	}
	fs := flag.NewFlagSet("memory-maintenance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.request, "request", "", "request JSON file")
	fs.StringVar(&o.memoryID, "memory-id", "", "memory ID")
	fs.StringVar(&o.tokenFile, "token-file", "", "private token file")
	fs.Int64Var(&o.revision, "expected-revision", 0, "current maintenance revision")
	fs.BoolVar(&o.downtime, "allow-global-downtime", false, "explicit global service fence acknowledgement")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Second, "bounded operation timeout")
	// Repeated options are mistakes, not last-value-wins administrative requests.
	seen := map[string]bool{}
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "-") {
			name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
			if seen[name] {
				return o, false, errInput
			}
			seen[name] = true
		}
	}
	if e := fs.Parse(args[1:]); e != nil {
		if e == flag.ErrHelp {
			return o, true, nil
		}
		return o, false, errInput
	}
	if fs.NArg() != 0 || o.timeout < time.Second || o.timeout > 15*time.Minute {
		return o, false, errInput
	}
	begin := o.command == "begin-privacy" || o.command == "begin-supersede"
	if begin {
		if !o.downtime || o.request == "" || seen["expected-revision"] {
			return o, false, errInput
		}
		if (o.command == "begin-supersede") != (o.memoryID != "") {
			return o, false, errInput
		}
	} else {
		if seen["request"] || seen["memory-id"] || seen["token-file"] || seen["allow-global-downtime"] {
			return o, false, errInput
		}
		if o.command == "status" {
			if seen["expected-revision"] {
				return o, false, errInput
			}
		} else if o.revision < 1 {
			return o, false, errInput
		}
	}
	return o, false, nil
}

// Check the opened inode, not just a path that might have changed meanwhile.
func readRegular(path string, private bool, limit int64) ([]byte, error) {
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() {
		return nil, errInput
	}
	if private && before.Mode().Perm()&0077 != 0 {
		return nil, errInput
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, errInput
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || private && opened.Mode().Perm()&0077 != 0 {
		return nil, errInput
	}
	data, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(data)) > limit {
		return nil, errInput
	}
	return data, nil
}
func loadToken(o options, getenv func(string) string) (string, error) {
	token := getenv("MEMORY_MAINTENANCE_TOKEN")
	if o.tokenFile != "" {
		if token != "" {
			return "", errInput
		}
		data, e := readRegular(o.tokenFile, true, 8192)
		if e != nil {
			return "", e
		}
		token = string(data)
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 8192 || strings.ContainsAny(token, "\r\n\x00") {
		return "", errInput
	}
	return token, nil
}
func decodeRequest(path string, out any) error {
	raw, e := readRegular(path, false, 1<<20)
	if e != nil {
		return e
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if e = d.Decode(out); e != nil {
		return errInput
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return errInput
	}
	return nil
}
