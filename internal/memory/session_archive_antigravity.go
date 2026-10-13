package memory

import (
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

// Field numbers were checked against the installed producer's descriptors;
// see docs/reference/antigravity-sqlite-schema.md. Never recursively extract
// strings: response siblings contain thinking, tool calls and internal state.
type archiveProtoField struct {
	wire   uint64
	number uint64
	bytes  []byte
}

func archiveProtoFields(raw []byte, wanted ...uint64) (map[uint64]archiveProtoField, bool) {
	out := map[uint64]archiveProtoField{}
	for len(raw) > 0 {
		tag, n := binary.Uvarint(raw)
		if n <= 0 || tag>>3 == 0 || tag>>3 > 1<<29-1 {
			return nil, false
		}
		raw = raw[n:]
		f := archiveProtoField{wire: tag & 7}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(raw)
			if n <= 0 {
				return nil, false
			}
			f.number = v
			raw = raw[n:]
		case 1, 5:
			n := 8
			if f.wire == 5 {
				n = 4
			}
			if len(raw) < n {
				return nil, false
			}
			raw = raw[n:]
		case 2:
			size, n := binary.Uvarint(raw)
			if n <= 0 || size > uint64(len(raw)-n) {
				return nil, false
			}
			raw = raw[n:]
			f.bytes = raw[:int(size)]
			raw = raw[int(size):]
		default:
			return nil, false
		}
		for _, key := range wanted {
			if tag>>3 == key {
				if _, duplicate := out[key]; duplicate {
					return nil, false
				}
				out[key] = f
			}
		}
	}
	return out, true
}
func archiveAntigravityText(kind, status, format int64, payload []byte) (text, role string, valid bool) {
	if kind != 14 && kind != 15 || status != 3 || format != 0 {
		return "", "", true
	}
	fields, ok := archiveProtoFields(payload, 1, 4, 19, 20)
	if !ok || fields[1].wire != 0 || fields[1].number != uint64(kind) || fields[4].wire != 0 || fields[4].number != uint64(status) {
		return "", "", false
	}
	outer, inner, role := uint64(19), uint64(2), "user"
	other := uint64(20)
	if kind == 15 {
		outer, inner, role, other = 20, 1, "assistant", 19
	}
	part, present := fields[outer]
	_, ambiguous := fields[other]
	if !present || part.wire != 2 || ambiguous {
		return "", "", false
	}
	nested, ok := archiveProtoFields(part.bytes, inner)
	if !ok {
		return "", "", false
	}
	field, present := nested[inner]
	if !present {
		return "", role, true
	}
	if field.wire != 2 || !utf8.Valid(field.bytes) {
		return "", "", false
	}
	return string(field.bytes), role, true
}
func archiveSQLiteMessageHash(idx, kind, status, format int64, role string, payload []byte) string {
	raw := append([]byte(fmt.Sprintf("arkism-sqlite-step-v1\n%d\n%d\n%d\n%d\n", idx, kind, status, format)), payload...)
	return ContentHash("arkism-sqlite-message-v1\n" + role + "\n" + ContentHash(string(raw)))
}
