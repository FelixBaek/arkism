package capture

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateInspectionFileLimit(t *testing.T) {
	const limit = 64
	for _, tc := range []struct {
		name, text string
		marked     bool
		oversized  bool
	}{
		{name: "empty"},
		{name: "normal_boundary", text: strings.Repeat("x", limit)},
		{name: "normal_oversized", text: strings.Repeat("x", limit+1), oversized: true},
		{name: "marker_boundary", text: "<private>" + strings.Repeat("x", limit-9), marked: true},
		{name: "marker_oversized", text: "<private>" + strings.Repeat("x", limit-8), oversized: true},
		{name: "unfinished_prefix", text: "<private", marked: true},
		{name: "escaped_marker", text: `\u003cprivate\u003e`, marked: true},
		{name: "normal_route", text: "<PrivateRoute>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.jsonl")
			if err := os.WriteFile(path, []byte(tc.text), 0o600); err != nil {
				t.Fatal(err)
			}
			marked, err := privateMarkedLimit(path, limit)
			if tc.oversized {
				var typed valueErr
				if marked || !errors.As(err, &typed) || typed != "too_large" {
					t.Fatal("oversized source became an opt-out", marked, err)
				}
			} else if err != nil || marked != tc.marked {
				t.Fatal("bounded inspection changed marker semantics", marked, err)
			}
		})
	}
}

func TestPrivateInspectionFileTypeAndMissing(t *testing.T) {
	dir := t.TempDir()
	marked, err := privateMarkedLimit(dir, 64)
	var typed valueErr
	if marked || !errors.As(err, &typed) || typed != "archive_source_not_regular" {
		t.Fatal("nonregular source became an opt-out", marked, err)
	}
	marked, err = privateMarkedLimit(filepath.Join(dir, "missing.jsonl"), 64)
	if marked || !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing-source compatibility lost its typed error", marked, err)
	}
}

type privateInspectionReadFunc func([]byte) (int, error)

func (f privateInspectionReadFunc) Read(p []byte) (int, error) { return f(p) }

func TestPrivateInspectionObservedIOFailure(t *testing.T) {
	synthetic := errors.New("synthetic inspection read failure")
	for _, text := range []string{"", "normal", "<private>"} {
		t.Run(text, func(t *testing.T) {
			reader := privateInspectionReadFunc(func(p []byte) (int, error) {
				return copy(p, text), synthetic
			})
			marked, err := scanPrivateLimit(reader, 64)
			if marked || !errors.Is(err, synthetic) {
				t.Fatal("observed I/O error became an opt-out", marked, err)
			}
		})
	}
	reader := privateInspectionReadFunc(func(p []byte) (int, error) {
		return copy(p, "<private>"), io.EOF
	})
	if marked, err := scanPrivateLimit(reader, 64); err != nil || !marked {
		t.Fatal("a complete final marker was lost", marked, err)
	}
}

func TestPrivateInspectionGrowingFileBoundsAndEarlyMarker(t *testing.T) {
	const limit = 16
	for _, marker := range []bool{false, true} {
		name, initial := "plain", "123456789"
		if marker {
			name, initial = "marker", "<private>"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "growing.jsonl")
			if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil || info.Size() > limit {
				t.Fatal("growth fixture must start within budget", err)
			}
			calls, observed := 0, 0
			reader := privateInspectionReadFunc(func(p []byte) (int, error) {
				calls++
				if len(p) > len(initial) {
					p = p[:len(initial)]
				}
				n, readErr := file.Read(p)
				observed += n
				if calls == 1 {
					appendFile, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
					if err != nil {
						t.Fatal(err)
					}
					_, err = appendFile.WriteString(strings.Repeat("x", 64))
					closeErr := appendFile.Close()
					if err != nil || closeErr != nil {
						t.Fatal(err, closeErr)
					}
				}
				return n, readErr
			})
			marked, err := scanPrivateLimit(reader, limit)
			if marker {
				if !marked || err != nil || calls != 1 || observed != len(initial) {
					t.Fatal("positive opt-out read beyond its marker", marked, err, calls, observed)
				}
			} else {
				var typed valueErr
				if marked || !errors.As(err, &typed) || typed != "too_large" || observed != limit+1 {
					t.Fatal("growing source escaped its byte budget", marked, err, observed)
				}
			}
		})
	}
}

func TestPrivateInspectionObservedOverflowPrecedesMarker(t *testing.T) {
	const limit = 16
	observed := 0
	reader := privateInspectionReadFunc(func(p []byte) (int, error) {
		n := copy(p, "<private>"+strings.Repeat("x", 64))
		observed += n
		return n, nil
	})
	marked, err := scanPrivateLimit(reader, limit)
	var typed valueErr
	if marked || !errors.As(err, &typed) || typed != "too_large" || observed != limit+1 {
		t.Fatal("observed overflow became a successful opt-out", marked, err, observed)
	}
}
