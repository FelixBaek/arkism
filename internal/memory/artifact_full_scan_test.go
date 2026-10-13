package memory

import (
	"io"
	"strings"
	"testing"
)

func TestFullScanReaderDetectsSecret(t *testing.T) {
	secret := "\nmongodb://user:hunter2@host.example/db\n"
	payload := strings.Repeat("a", 40_000) + secret + strings.Repeat("b", 40_000)
	r := newFullScanReader(strings.NewReader(payload))
	if _, err := io.Copy(io.Discard, r); err != ErrSecretDetected {
		t.Fatalf("copy = %v, want ErrSecretDetected", err)
	}
	// The reader stays failed: no later read can sneak past the scan.
	if _, err := r.Read(make([]byte, 8)); err != ErrSecretDetected {
		t.Fatalf("post-deny read = %v", err)
	}
}

func TestFullScanReaderCatchesSeamSecret(t *testing.T) {
	payload := strings.Repeat("x", 40_000) + "\napi_key = 'sk-liveABCDEFGHIJ1234'\n" + strings.Repeat("y", 40_000)
	// One-byte reads around the secret put every chunk seam inside the pattern.
	sizes := []int{40_000}
	for range 18 {
		sizes = append(sizes, 1)
	}
	sizes = append(sizes, 80_000)
	r := newFullScanReader(splitReads([]byte(payload), sizes...))
	if _, err := io.Copy(io.Discard, r); err != ErrSecretDetected {
		t.Fatalf("seam copy = %v, want ErrSecretDetected", err)
	}
}

func TestFullScanReaderPassesCleanContent(t *testing.T) {
	payload := strings.Repeat("clean binary \x00\x01 bytes ", 5_000)
	r := newFullScanReader(strings.NewReader(payload))
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if int(n) != len(payload) {
		t.Fatalf("bytes = %d, want %d", n, len(payload))
	}
}

func splitReads(data []byte, sizes ...int) io.Reader {
	var readers []io.Reader
	at := 0
	for _, size := range sizes {
		end := at + size
		if end > len(data) {
			end = len(data)
		}
		readers = append(readers, &byteReader{data: data[at:end]})
		at = end
		if at >= len(data) {
			break
		}
	}
	return io.MultiReader(readers...)
}

type byteReader struct{ data []byte }

func (b *byteReader) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}
