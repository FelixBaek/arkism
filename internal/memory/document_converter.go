package memory

// An operator may name one external converter for formats the built-in
// readers do not cover (MEMORY_DOCUMENT_CONVERTER, an absolute path, and
// MEMORY_DOCUMENT_CONVERTER_EXTENSIONS, e.g. ".doc,.ppt,.xls,.epub"). It runs
// without a shell, with an empty environment, in a private temporary folder,
// under a time limit and an output limit, and must print the text (Markdown or
// plain) to standard output. Without it those formats are unsupported.
import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/procgroup"
)

const converterMaxOutput = 4 << 20

// converterTimeout is a variable so a test can shorten it.
var converterTimeout = 60 * time.Second

var errOutputLimit = errors.New("converter output limit")

// converterHandles reports whether the configured converter takes ext.
func converterHandles(ext string) bool {
	if ext == "" || os.Getenv("MEMORY_DOCUMENT_CONVERTER") == "" {
		return false
	}
	return slices.ContainsFunc(strings.Split(os.Getenv("MEMORY_DOCUMENT_CONVERTER_EXTENSIONS"), ","), func(e string) bool {
		e = strings.ToLower(strings.TrimSpace(e))
		return e == ext && strings.HasPrefix(e, ".") && !strings.ContainsAny(e, `/\ `)
	})
}

// convertDocument runs the converter on the file and returns its text. The
// converter runs in its own process group, so a timeout also stops what it started.
func convertDocument(ext string, raw []byte) (string, error) {
	bin := os.Getenv("MEMORY_DOCUMENT_CONVERTER")
	if !filepath.IsAbs(bin) {
		return "", ErrUnavailable
	}
	dir, err := os.MkdirTemp("", "arkism-convert-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "input"+ext)
	if err = os.WriteFile(input, raw, 0o600); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), converterTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, input)
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, []string{"HOME=" + dir, "TMPDIR=" + dir, "LANG=C.UTF-8"}, 5*time.Second
	procgroup.Set(cmd)
	out := &cappedBuffer{limit: converterMaxOutput}
	cmd.Stdout, cmd.Stderr = out, io.Discard
	err = cmd.Run()
	switch {
	case out.over:
		return "", errBlobTooLarge
	case err != nil:
		return "", ErrUnavailable
	}
	text := out.buf.String()
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || strings.TrimSpace(text) == "" {
		return "", ErrInvalid
	}
	return strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), nil
}

// cappedBuffer keeps at most limit bytes and fails the write past it.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		c.over = true
		return 0, errOutputLimit
	}
	return c.buf.Write(p)
}
