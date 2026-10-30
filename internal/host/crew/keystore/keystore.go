// Package keystore keeps provider key secrets in the operating system's credential store (Windows Credential
// Manager, macOS Keychain, Linux Secret Service) and resolves the "keyring:<service>/<id>" references the keys file
// holds in their place. It has no CGO: wincred is syscall-only, macOS runs /usr/bin/security, Linux speaks D-Bus.
//
// It imports nothing from Arkism, so any reader of the keys file (host, gateway, crewrun) can resolve references.
package keystore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// Service is the credential-store service name every Arkism provider key is filed under.
const Service = "arkism-provider-keys"

const refPrefix = "keyring:"

// Storage values a keys-file row carries.
const (
	StorageKeyring = "keyring"
	StorageFile    = "file"
)

var (
	// ErrUnavailable is returned when no usable credential store answers.
	ErrUnavailable = errors.New("keystore: credential store unavailable")
	// ErrNotFound is returned when a reference names no stored secret.
	ErrNotFound = errors.New("keystore: secret not found")
	// ErrMismatch is returned when a secret does not read back as written.
	ErrMismatch = errors.New("keystore: secret did not read back")
)

// callTimeout bounds every store call: a locked Secret Service can wait on an unlock prompt forever.
var callTimeout = 10 * time.Second

var (
	mu       sync.Mutex
	cached   *bool
	platform = sessionStorePresent
)

// Ref is the reference stored in place of the secret for key id.
func Ref(id string) string { return refPrefix + Service + "/" + id }

// ParseRef splits a "keyring:<service>/<id>" reference.
func ParseRef(ref string) (service, id string, ok bool) {
	rest, ok := strings.CutPrefix(ref, refPrefix)
	if !ok {
		return "", "", false
	}
	service, id, ok = strings.Cut(rest, "/")
	if !ok || service == "" || id == "" {
		return "", "", false
	}
	return service, id, true
}

// Available reports (once per process) whether a credential store answers. ARKISM_KEYRING=off forces the file.
func Available() bool {
	mu.Lock()
	defer mu.Unlock()
	if cached != nil {
		return *cached
	}
	ok := probe()
	cached = &ok
	return ok
}

func probe() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ARKISM_KEYRING"))) {
	case "off", "0", "false", "file":
		return false
	}
	if !platform() {
		return false
	}
	_, err := call(func() (string, error) { return keyring.Get(Service, "arkism-probe") })
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}

// sessionStorePresent skips D-Bus entirely on a headless Linux/BSD host (no session bus): godbus would otherwise
// try to autolaunch one. Windows and macOS always have their store.
func sessionStorePresent() bool {
	switch runtime.GOOS {
	case "windows", "darwin":
		return true
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "bus")); err == nil {
			return true
		}
	}
	return false
}

func call(fn func() (string, error)) (string, error) {
	type res struct {
		v   string
		err error
	}
	ch := make(chan res, 1)
	go func() { v, err := fn(); ch <- res{v, err} }()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(callTimeout):
		return "", ErrUnavailable
	}
}

// Put stores secret for id and reads it back; it returns the reference only when the read-back matches.
func Put(id, secret string) (string, error) { return PutIn(Service, id, secret) }

// PutIn is Put under another service name (e.g. "arkism-discord" for bot tokens).
func PutIn(service, id, secret string) (string, error) {
	if !Available() {
		return "", ErrUnavailable
	}
	if _, err := call(func() (string, error) { return "", keyring.Set(service, id, secret) }); err != nil {
		return "", err
	}
	got, err := call(func() (string, error) { return keyring.Get(service, id) })
	if err != nil || got != secret {
		_, _ = call(func() (string, error) { return "", keyring.Delete(service, id) })
		if err == nil {
			err = ErrMismatch
		}
		return "", err
	}
	return refPrefix + service + "/" + id, nil
}

// Get resolves a reference to its secret.
func Get(ref string) (string, error) {
	service, id, ok := ParseRef(ref)
	if !ok {
		return "", ErrNotFound
	}
	if !Available() {
		return "", ErrUnavailable
	}
	v, err := call(func() (string, error) { return keyring.Get(service, id) })
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

// Delete removes a reference's secret; a secret already gone is not an error.
func Delete(ref string) error {
	service, id, ok := ParseRef(ref)
	if !ok {
		return nil
	}
	if !Available() {
		return ErrUnavailable
	}
	_, err := call(func() (string, error) { return "", keyring.Delete(service, id) })
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Resolve fills "key" in place for every row that holds a keyring reference (rows it cannot resolve keep no key),
// and sets "storage" on every row; a "headers_ref" (a custom endpoint's extra headers) fills "headers" the same way.
// Readers of the keys file call this after parsing it.
func Resolve(rows []map[string]any) {
	for _, k := range rows {
		if ref, _ := k["headers_ref"].(string); ref != "" {
			if v, err := Get(ref); err == nil {
				k["headers"] = v
			}
		}
		ref, _ := k["secret_ref"].(string)
		if ref == "" {
			k["storage"] = StorageFile
			continue
		}
		if v, err := Get(ref); err == nil {
			k["key"], k["storage"] = v, StorageKeyring
		} else if plain, _ := k["key"].(string); plain != "" {
			delete(k, "secret_ref") // unresolvable, but the file still has the secret: treat it as a file row
			k["storage"] = StorageFile
		} else {
			k["storage"] = StorageKeyring // keep the reference; a later run with the store may resolve it
		}
	}
}

// UseMock swaps in go-keyring's in-memory store (err != nil: a store that fails every call) and forgets the
// cached probe. For tests only.
func UseMock(err error) {
	if err != nil {
		keyring.MockInitWithError(err)
	} else {
		keyring.MockInit()
	}
	mu.Lock()
	platform = func() bool { return true }
	cached = nil
	mu.Unlock()
}
