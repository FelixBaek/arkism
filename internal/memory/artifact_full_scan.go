package memory

import (
	"io"
)

// Full-content secret inspection is part of the copy itself: blob bytes reach
// the store only through this reader, so no artifact — text or binary, any
// size up to the store limit — can be published without every byte having
// passed the scan. Text artifacts already inspected as a whole are scanned
// again; binary formats get their first complete inspection here. These are
// the ingest secret rules, not a DLP engine; a pattern would have to span a
// chunk seam by more than scanOverlapBytes to slip between two windows.
const (
	scanChunkSize    = 32 << 10
	scanOverlapBytes = 4 << 10
)

type fullScanReader struct {
	src  io.Reader
	prev []byte
	err  error
}

func newFullScanReader(r io.Reader) *fullScanReader {
	return &fullScanReader{src: r}
}

func (f *fullScanReader) Read(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	n, err := f.src.Read(p)
	if n > 0 {
		window := p[:n]
		if containsSecret(scanWindow(f.prev, window)) {
			f.err = ErrSecretDetected
			return 0, f.err
		}
		// Keep the seam window at its full width even when the source trickles
		// bytes in small reads, so a pattern split across reads still matches.
		joined := scanJoined(f.prev, window)
		f.prev = append(f.prev[:0], joined[max(0, len(joined)-scanOverlapBytes):]...)
	}
	if err != nil {
		f.err = err
	}
	return n, err
}

// scanWindow joins the previous tail with the new bytes; the new bytes are not
// copied when no overlap is pending.
func scanWindow(prev, window []byte) string {
	if len(prev) == 0 {
		return string(window)
	}
	return string(scanJoined(prev, window))
}

func scanJoined(prev, window []byte) []byte {
	if len(prev) == 0 {
		return window
	}
	joined := make([]byte, 0, len(prev)+len(window))
	joined = append(joined, prev...)
	joined = append(joined, window...)
	return joined
}
