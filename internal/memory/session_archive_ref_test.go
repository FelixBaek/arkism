package memory

import (
	"strings"
	"testing"
)

func TestValidArchiveMessageRefPositions(t *testing.T) {
	hash := ContentHash("archive-reference")
	tests := []struct {
		name        string
		step        string
		recordKey   string
		messageHash string
		chunk       int
		offset      int
		want        bool
	}{
		{name: "legacy JSONL", want: true},
		{name: "JSONL with message hash", messageHash: hash, want: true},
		{name: "JSONL with record key and message hash", recordKey: hash, messageHash: hash, want: true},
		{name: "record key requires message hash", recordKey: hash},
		{name: "invalid record key", recordKey: "invalid", messageHash: hash},
		{name: "invalid message hash", messageHash: "invalid"},
		{name: "invalid message hash with record key", recordKey: hash, messageHash: "invalid"},
		{name: "last chunk", chunk: sessionArchiveMaxChunks - 1, want: true},
		{name: "negative chunk", chunk: -1},
		{name: "past last chunk", chunk: sessionArchiveMaxChunks},
		{name: "chunk end is valid", offset: SessionArchiveChunkSize, want: true},
		{name: "negative offset", offset: -1},
		{name: "past chunk end", offset: SessionArchiveChunkSize + 1},
		{name: "last chunk end with stable key", recordKey: hash, messageHash: hash, chunk: sessionArchiveMaxChunks - 1, offset: SessionArchiveChunkSize, want: true},
		{name: "SQLite step zero", step: "0", want: true},
		{name: "SQLite positive step", step: "42", want: true},
		{name: "SQLite maximum step", step: "9223372036854775807", want: true},
		{name: "SQLite overflowing step", step: "9223372036854775808"},
		{name: "SQLite negative step", step: "-1"},
		{name: "SQLite leading zero", step: "00"},
		{name: "SQLite explicit plus", step: "+1"},
		{name: "SQLite whitespace", step: " 1"},
		{name: "SQLite nonnumeric step", step: "step"},
		{name: "SQLite with message hash", step: "0", messageHash: hash, want: true},
		{name: "SQLite invalid message hash", step: "0", messageHash: "invalid"},
		{name: "SQLite rejects chunk", step: "0", chunk: 1},
		{name: "SQLite rejects offset", step: "0", offset: 1},
		{name: "SQLite rejects record key", step: "0", recordKey: hash, messageHash: hash},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := ArchiveMessageRef{
				ArchiveID: hash, SHA256: hash,
				SQLiteStep: tt.step, RecordKey: tt.recordKey, MessageSHA256: tt.messageHash,
				Chunk: tt.chunk, Offset: tt.offset,
			}
			if got := validArchiveMessageRef(ref); got != tt.want {
				t.Fatalf("validArchiveMessageRef(%+v) = %v, want %v", ref, got, tt.want)
			}
		})
	}
}

func TestValidArchiveMessageRefHashes(t *testing.T) {
	validHash := strings.Repeat("a", 64)
	for _, hash := range []string{
		"", strings.Repeat("a", 63), strings.Repeat("a", 65),
		strings.Repeat("A", 64), strings.Repeat("g", 64), validHash + "\n",
	} {
		refs := []ArchiveMessageRef{
			{ArchiveID: hash, SHA256: validHash},
			{ArchiveID: validHash, SHA256: hash},
		}
		for _, ref := range refs {
			if validArchiveMessageRef(ref) {
				t.Errorf("accepted invalid archive hash in %+v", ref)
			}
		}
	}
}
