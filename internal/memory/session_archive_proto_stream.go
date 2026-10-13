package memory

import (
	"bufio"
	"io"
	"unicode/utf8"
)

type archiveProtoSpan struct {
	wire, number uint64
	start, size  int64
}

// Only selected field positions are retained. Unknown length-delimited values
// are opaque bytes; parsing never recursively treats their contents as text.
func archiveProtoStreamFields(r io.ReaderAt, start, size int64, wanted ...uint64) (map[uint64]archiveProtoSpan, error) {
	section := io.NewSectionReader(r, start, size)
	input := bufio.NewReaderSize(section, 32<<10)
	at := int64(0)
	readNumber := func() (uint64, error) {
		var value uint64
		for i := 0; i < 10; i++ {
			b, err := input.ReadByte()
			if err != nil {
				return 0, err
			}
			at++
			if i == 9 && b > 1 {
				return 0, ErrInvalid
			}
			value |= uint64(b&0x7f) << uint(7*i)
			if b < 0x80 {
				return value, nil
			}
		}
		return 0, ErrInvalid
	}
	skip := func(n int64) error {
		if n < 0 || n > size-at {
			return ErrInvalid
		}
		at += n
		if n <= int64(input.Buffered()) {
			_, err := input.Discard(int(n))
			return err
		}
		if _, err := section.Seek(at, io.SeekStart); err != nil {
			return err
		}
		input.Reset(section)
		return nil
	}
	fields := map[uint64]archiveProtoSpan{}
	for at < size {
		tag, err := readNumber()
		if err != nil {
			return nil, err
		}
		if tag>>3 == 0 || tag>>3 > 1<<29-1 {
			return nil, ErrInvalid
		}
		field := archiveProtoSpan{wire: tag & 7}
		switch field.wire {
		case 0:
			field.number, err = readNumber()
		case 1:
			err = skip(8)
		case 5:
			err = skip(4)
		case 2:
			var n uint64
			n, err = readNumber()
			if err == nil {
				if n > uint64(size-at) {
					return nil, ErrInvalid
				}
				field.start, field.size = start+at, int64(n)
				err = skip(int64(n))
			}
		default:
			return nil, ErrInvalid
		}
		if err != nil {
			return nil, err
		}
		for _, key := range wanted {
			if tag>>3 == key {
				if _, exists := fields[key]; exists {
					return nil, ErrInvalid
				}
				fields[key] = field
			}
		}
	}
	return fields, nil
}
func archiveAntigravityStream(r io.ReaderAt, size, kind, status, format int64) (archiveProtoSpan, string, error) {
	if kind != 14 && kind != 15 || status != 3 || format != 0 {
		return archiveProtoSpan{}, "", nil
	}
	fields, err := archiveProtoStreamFields(r, 0, size, 1, 4, 19, 20)
	if err != nil {
		return archiveProtoSpan{}, "", err
	}
	if fields[1].wire != 0 || fields[1].number != uint64(kind) || fields[4].wire != 0 || fields[4].number != uint64(status) {
		return archiveProtoSpan{}, "", ErrInvalid
	}
	outer, inner, other, role := uint64(19), uint64(2), uint64(20), "user"
	if kind == 15 {
		outer, inner, other, role = 20, 1, 19, "assistant"
	}
	part, found := fields[outer]
	_, ambiguous := fields[other]
	if !found || part.wire != 2 || ambiguous {
		return archiveProtoSpan{}, "", ErrInvalid
	}
	nested, err := archiveProtoStreamFields(r, part.start, part.size, inner)
	if err != nil {
		return archiveProtoSpan{}, "", err
	}
	text, found := nested[inner]
	if !found {
		return archiveProtoSpan{}, role, nil
	}
	if text.wire != 2 {
		return archiveProtoSpan{}, "", ErrInvalid
	}
	return text, role, nil
}

// Validate raw protobuf text while streaming, retaining at most one unfinished
// UTF-8 rune across reads. JSON streams already validate their decoded text.
type archiveUTF8Reader struct {
	source io.Reader
	tail   []byte
}

func (r *archiveUTF8Reader) Read(dst []byte) (int, error) {
	n, err := r.source.Read(dst)
	data := append(append([]byte(nil), r.tail...), dst[:n]...)
	r.tail = nil
	if !utf8.Valid(data) {
		valid := false
		for cut := 1; cut <= 3 && cut <= len(data); cut++ {
			end := len(data) - cut
			if utf8.Valid(data[:end]) && !utf8.FullRune(data[end:]) {
				r.tail = append(r.tail, data[end:]...)
				valid = true
				break
			}
		}
		if !valid {
			return n, ErrInvalid
		}
	}
	if err == io.EOF && len(r.tail) > 0 {
		return n, ErrInvalid
	}
	return n, err
}
