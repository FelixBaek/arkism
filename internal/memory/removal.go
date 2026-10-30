package memory

import (
	"strings"
	"time"
)

// removalReasons are the quick reasons an owner gives when removing a memory or rejecting a candidate.
var removalReasons = map[string]bool{"stale": true, "changed": true, "unused": true, "wrong": true, "duplicate": true}

// Removal is why a person removed a memory: it stays in history (status rejected) with this note.
type Removal struct {
	Code string    `json:"code" bson:"code"`
	Note string    `json:"note,omitempty" bson:"note,omitempty"`
	By   string    `json:"by" bson:"by"`
	At   time.Time `json:"at" bson:"at"`
}

// validRemoval accepts an empty pair (no reason given) or a known code with a short note.
func validRemoval(code, note string) bool {
	if code == "" {
		return note == ""
	}
	return removalReasons[code] && len(note) <= 2000 && strings.TrimSpace(note) == note
}
