package desktopcontrol

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MaxFrame = 4096
const StopTimeout = 5 * time.Second

var (
	ErrConfiguration = errors.New("desktop_control_configuration_invalid")
	ErrTransport     = errors.New("desktop_control_unavailable")
	ErrStop          = errors.New("desktop_shutdown_failed")
	ErrTimeout       = errors.New("desktop_shutdown_timeout")
)

type Config struct {
	role       string
	generation uint64
	nonce      string
	pid        int
}

func (Config) String() string   { return "desktopcontrol.Config(redacted)" }
func (Config) GoString() string { return "desktopcontrol.Config(redacted)" }

func ParseEnvironment(get func(string) string, role string) (*Config, error) {
	if get("ARKISM_DESKTOP_CONTROL") == "" {
		return nil, nil
	}
	if get("ARKISM_DESKTOP_CONTROL") != "1" || (role != "gateway" && role != "watcher") || get("ARKISM_DESKTOP_ROLE") != role {
		return nil, ErrConfiguration
	}
	generation, err := strconv.ParseUint(get("ARKISM_DESKTOP_GENERATION"), 10, 64)
	nonce := get("ARKISM_DESKTOP_NONCE")
	if err != nil || generation == 0 || !validNonce(nonce) {
		return nil, ErrConfiguration
	}
	return &Config{role: role, generation: generation, nonce: nonce, pid: os.Getpid()}, nil
}

func validNonce(nonce string) bool {
	if len(nonce) != 64 {
		return false
	}
	return strings.IndexFunc(nonce, func(r rune) bool { return !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') }) == -1
}

type request struct {
	Version    uint64 `json:"v"`
	Role       string `json:"role"`
	Generation uint64 `json:"generation"`
	Nonce      string `json:"nonce"`
	PID        int    `json:"pid"`
	ID         uint64 `json:"request_id"`
	Operation  string `json:"op"`
	Lease      uint64 `json:"lease_id"`
}

type response struct {
	Version    uint64 `json:"v"`
	Role       string `json:"role"`
	Generation uint64 `json:"generation"`
	PID        int    `json:"pid"`
	ID         uint64 `json:"request_id"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	Lease      uint64 `json:"lease_id,omitempty"`
}

type Controller struct {
	config     Config
	gate       *Gate
	input      io.Reader
	output     io.Writer
	mu         sync.Mutex
	err        error
	requested  chan struct{}
	done       chan struct{}
	start      sync.Once
	stopBudget time.Duration
	idleCheck  func() bool
}

func FromEnvironment(role string, input io.Reader, output io.Writer) (*Controller, error) {
	config, err := ParseEnvironment(os.Getenv, role)
	if err != nil || config == nil {
		return nil, err
	}
	return New(*config, Default, input, output)
}

// New also permits isolated gates/transports in tests without changing process globals.
func New(config Config, gate *Gate, input io.Reader, output io.Writer) (*Controller, error) {
	if (config.role != "gateway" && config.role != "watcher") || config.generation == 0 || !validNonce(config.nonce) || config.pid != os.Getpid() || gate == nil || input == nil || output == nil {
		return nil, ErrConfiguration
	}
	return newController(config, gate, input, output), nil
}

func newController(config Config, gate *Gate, input io.Reader, output io.Writer) *Controller {
	return &Controller{config: config, gate: gate, input: input, output: output, requested: make(chan struct{}), done: make(chan struct{}), stopBudget: StopTimeout}
}

func (c *Controller) Requested() bool {
	select {
	case <-c.requested:
		return true
	default:
		return false
	}
}

func (c *Controller) Done() <-chan struct{} { return c.done }

func (c *Controller) Admission() *Gate { return c.gate }

// AwaitShutdown keeps the process alive until the final response write is accounted for.
func (c *Controller) AwaitShutdown() error {
	if !c.Requested() {
		return ErrConfiguration
	}
	select {
	case <-c.done:
		return c.Err()
	case <-time.After(StopTimeout + time.Second):
		return ErrTransport
	}
}

func (c *Controller) Err() error { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

// Start is called after role initialization, before it begins accepting work. It writes
// only bounded control metadata; a child PID is always its actual os.Getpid value.
func (c *Controller) Start(stop func(context.Context) error, idleCheck ...func() bool) error {
	if len(idleCheck) > 1 {
		return ErrConfiguration
	}
	started := false
	c.start.Do(func() { started = true })
	if !started {
		return ErrConfiguration
	}
	if len(idleCheck) == 1 {
		c.idleCheck = idleCheck[0]
	}
	if err := c.write(0, "ready", "", 0); err != nil {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
		return err
	}
	go func() {
		defer close(c.done)
		err := c.serve(stop)
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
	}()
	return nil
}

func (c *Controller) write(id uint64, state, code string, lease uint64) error {
	row := response{Version: 1, Role: c.config.role, Generation: c.config.generation, PID: c.config.pid, ID: id, State: state, Error: code, Lease: lease}
	raw, err := json.Marshal(row)
	if err != nil || len(raw)+1 > MaxFrame {
		return ErrTransport
	}
	raw = append(raw, '\n')
	n, err := c.output.Write(raw)
	if err != nil || n != len(raw) {
		return ErrTransport
	}
	return nil
}

func readRequest(reader *bufio.Reader) (request, error) {
	var row request
	raw, err := reader.ReadSlice('\n')
	if err != nil || len(raw) > MaxFrame {
		return row, ErrTransport
	}
	if !strictFields(raw) {
		return row, ErrTransport
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&row) != nil {
		return request{}, ErrTransport
	}
	if decoder.Decode(new(any)) != io.EOF {
		return request{}, ErrTransport
	}
	return row, nil
}

func strictFields(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool, 8)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || !knownField(key) {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	end, err := decoder.Token()
	return err == nil && end == json.Delim('}') && len(seen) == 8 && decoder.Decode(new(any)) == io.EOF
}

func knownField(key string) bool {
	switch key {
	case "v", "role", "generation", "nonce", "pid", "request_id", "op", "lease_id":
		return true
	default:
		return false
	}
}

func (c *Controller) matches(row request) bool {
	return row.Version == 1 && row.Role == c.config.role && row.Generation == c.config.generation && row.PID == c.config.pid &&
		subtle.ConstantTimeCompare([]byte(row.Nonce), []byte(c.config.nonce)) == 1
}

func (c *Controller) serve(stop func(context.Context) error) error {
	reader := bufio.NewReaderSize(c.input, MaxFrame)
	var last uint64
	for {
		row, err := readRequest(reader)
		if err != nil {
			return ErrTransport
		}
		if !c.matches(row) || row.ID == 0 || row.ID <= last {
			if err := c.write(0, "error", "invalid_context_or_replay", 0); err != nil {
				return err
			}
			continue
		}
		last = row.ID
		finished, err := c.dispatch(row, stop)
		if err != nil || finished {
			return err
		}
	}
}

func (c *Controller) dispatch(row request, stop func(context.Context) error) (bool, error) {
	switch row.Operation {
	case "pause":
		return false, c.pause(row)
	case "resume":
		if c.gate.Resume(row.Lease) != nil {
			return false, c.write(row.ID, "error", "invalid_lease", 0)
		}
		return false, c.write(row.ID, "resumed", "", 0)
	case "stop":
		if c.gate.Stop(row.Lease) != nil {
			return false, c.write(row.ID, "error", "invalid_lease", 0)
		}
		close(c.requested)
		err := boundedStop(stop, c.stopBudget)
		if err != nil {
			return true, errors.Join(err, c.write(row.ID, "error", stopCode(err), row.Lease))
		}
		return true, c.write(row.ID, "stopped", "", row.Lease)
	default:
		return false, c.write(row.ID, "error", "unknown_operation", 0)
	}
}

func (c *Controller) pause(row request) error {
	if row.Lease != 0 {
		return c.write(row.ID, "error", "invalid_lease", 0)
	}
	idle, err := c.gate.Pause(row.ID)
	if err != nil {
		return c.write(row.ID, "error", "invalid_lease", 0)
	}
	if idle && c.idleCheck != nil && !c.idleCheck() {
		if c.gate.Resume(row.ID) != nil {
			return c.write(row.ID, "error", "invalid_lease", 0)
		}
		idle = false
	}
	if !idle {
		return c.write(row.ID, "busy", "", 0)
	}
	return c.write(row.ID, "idle", "", row.ID)
}

func boundedStop(stop func(context.Context) error, budget time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if ctx.Err() != nil {
		return ErrTimeout
	}
	finished := make(chan error, 1)
	go func() { finished <- stop(ctx) }()
	select {
	case err := <-finished:
		if ctx.Err() != nil {
			return ErrTimeout
		}
		return err
	case <-ctx.Done():
		return ErrTimeout
	}
}

func stopCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTimeout) {
		return "shutdown_timeout"
	}
	return "shutdown_failed"
}
