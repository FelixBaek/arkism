package capture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// Helpers that keep Python's observable behaviour: error kinds that become metric
// codes, float and datetime text, dict equality and key order.

var errBusy = errors.New("lock held")

// fileStamp is the identity Python read from os.stat: st_dev, st_ino, st_nlink, st_size,
// st_mtime_ns, st_ctime_ns.
type fileStamp struct {
	Dev, Ino, Nlink  uint64
	Size             int64
	MtimeNs, CtimeNs int64
}

// valueErr is a Python ValueError whose text is a code; runtimeErr a RuntimeError.
type valueErr string

func (e valueErr) Error() string { return string(e) }

type runtimeErr string

func (e runtimeErr) Error() string { return string(e) }

// pyType names err the way Python's type(e).__name__ would for the matching failure.
func pyType(err error) string {
	var ve valueErr
	var re runtimeErr
	var api *APIError
	var syn *json.SyntaxError
	var ne net.Error
	var exit *exec.ExitError
	switch {
	case errors.As(err, &ve):
		return "ValueError"
	case errors.As(err, &re):
		return "RuntimeError"
	case errors.As(err, &api):
		return "APIError"
	case host.HTTPCode(err) != 0:
		return "HTTPError"
	case errors.As(err, &syn), errors.Is(err, errJSONShape):
		return "JSONDecodeError"
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, errTimeout):
		return "TimeoutError"
	case errors.As(err, &ne):
		return "URLError"
	case errors.Is(err, fs.ErrNotExist):
		return "FileNotFoundError"
	case errors.Is(err, fs.ErrExist):
		return "FileExistsError"
	case errors.Is(err, fs.ErrPermission):
		return "PermissionError"
	case errors.As(err, &exit):
		return "CalledProcessError"
	case errors.Is(err, errTimeoutExpired):
		return "TimeoutExpired"
	case errors.Is(err, errKey):
		return "KeyError"
	case errors.Is(err, errAttr):
		return "AttributeError"
	case errors.Is(err, errType):
		return "TypeError"
	}
	return "OSError"
}

var (
	errTimeout = errors.New("timed out")
	// errTimeoutExpired is subprocess.TimeoutExpired.
	errTimeoutExpired = errors.New("TimeoutExpired")
	errJSONShape      = errors.New("json: unexpected shape")
	errKey            = errors.New("KeyError")
	errType           = errors.New("TypeError")
	errAttr           = errors.New("AttributeError")
)

// errorCode is Python's `getattr(e, "code", None) or (str(e) if isinstance(e, RuntimeError) else type(e).__name__)`.
func errorCode(err error) string {
	var re runtimeErr
	var api *APIError
	if code := host.HTTPCode(err); code != 0 {
		return strconv.Itoa(code)
	}
	if errors.As(err, &api) && api.Code != "" {
		return api.Code
	}
	if errors.As(err, &re) {
		return string(re)
	}
	return pyType(err)
}

var workCodeRE = regexp.MustCompile(`^work_[a-z_]+$`)

// workCode is a work_* ValueError text, else the Python type name.
func workCode(err error) string {
	var ve valueErr
	if errors.As(err, &ve) && workCodeRE.MatchString(string(ve)) {
		return string(ve)
	}
	return pyType(err)
}

// recovered turns a panic from an unchecked assertion into an error, the way Python's
// broad `except Exception` caught KeyError and TypeError.
func recovered(err *error) {
	if r := recover(); r != nil {
		if e, ok := r.(error); ok {
			*err = fmt.Errorf("%w: %v", errKey, e)
		} else {
			*err = fmt.Errorf("%w: %v", errKey, r)
		}
	}
}

// timeNow is time.time(); tests replace it.
var timeNow = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// pyRepr is Python's repr(float): shortest digits, fixed between 1e-4 and 1e16.
func pyRepr(x float64) string {
	if math.IsInf(x, 0) || math.IsNaN(x) {
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	e := strconv.FormatFloat(x, 'e', -1, 64) // d.ddde±XX, the same spelling as Python
	_, exps, _ := strings.Cut(e, "e")
	if exp, _ := strconv.Atoi(exps); exp < -4 || exp >= 16 {
		return e
	}
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

// num reads a JSON number (or Go number) as float64; ok is false for bools and non-numbers.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// pyNorm re-spells decoded numbers as Python would after json.loads (1e5 -> 100000.0),
// so a re-dumped value hashes the same as in Python.
func pyNorm(v any) any {
	switch x := v.(type) {
	case json.Number:
		if _, ok := host.Int(x); ok {
			return json.Number(strings.TrimLeft(string(x), "+"))
		}
		f, err := x.Float64()
		if err != nil {
			return x
		}
		return f
	case float64:
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = pyNorm(e)
		}
		return out
	case host.Obj:
		out := make(host.Obj, len(x))
		for i, kv := range x {
			out[i] = host.KV{Key: kv.Key, Value: pyNorm(kv.Value)}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = pyNorm(e)
		}
		return out
	}
	return v
}

// eq is Python == for JSON values (numbers compare by value, dict order is ignored).
func eq(a, b any) bool {
	return canon(a) == canon(b)
}

func canon(v any) string {
	s, err := host.Dumps(canonNums(v), host.DumpOpts{Compact: true, NoASCII: true, SortKeys: true})
	if err != nil {
		return fmt.Sprintf("\x00%v", v)
	}
	return s
}

// canonNums spells every number as a float so 1 == 1.0, like Python.
func canonNums(v any) any {
	if f, ok := num(v); ok {
		return json.Number(strconv.FormatFloat(f, 'g', -1, 64))
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = canonNums(e)
		}
		return out
	case host.Obj:
		out := make(map[string]any, len(x))
		for _, kv := range x {
			out[kv.Key] = canonNums(kv.Value)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = canonNums(e)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	}
	return v
}

// loadsOrdered decodes JSON keeping object key order (host.Obj), as Python dicts do.
func loadsOrdered(raw []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	v, err := orderedValue(dec)
	if err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("json: trailing data")
	}
	return v, nil
}

func orderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '[' {
			list := []any{}
			for dec.More() {
				v, err := orderedValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token()
			return list, err
		}
		obj := host.Obj{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := orderedValue(dec)
			if err != nil {
				return nil, err
			}
			obj.Set(kt.(string), v) // a repeated key keeps its first position, last value (Python dict)
		}
		_, err := dec.Token()
		return obj, err
	}
	return tok, nil
}

// loads decodes JSON like json.loads (numbers as json.Number, objects as map[string]any).
func loads(raw []byte) (any, error) { return host.Loads(string(raw)) }

// loadObj decodes a JSON object; any other value is an error.
func loadObj(raw []byte) (map[string]any, error) {
	v, err := loads(raw)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errJSONShape
	}
	return m, nil
}

// loadsBytes is json.loads(bytes): invalid UTF-8 is an error, a UTF-8 BOM is skipped.
func loadsBytes(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errJSONShape
	}
	return loads(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))
}

// loadObjBytes is loadsBytes that must give a JSON object.
func loadObjBytes(raw []byte) (map[string]any, error) {
	v, err := loadsBytes(raw)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errJSONShape
	}
	return m, nil
}

// dumps is json.dumps(v, ensure_ascii=False).
func dumps(v any) string { return host.MustDumps(v, host.DumpOpts{NoASCII: true}) }

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// utcFromTimestamp is datetime.fromtimestamp(at, timezone.utc) (microseconds rounded half-even).
func utcFromTimestamp(at float64) time.Time {
	sec, frac := math.Modf(at)
	us := math.RoundToEven(frac * 1e6)
	if us >= 1e6 {
		sec++
		us -= 1e6
	} else if us < 0 {
		sec--
		us += 1e6
	}
	return time.Unix(int64(sec), int64(us)*1000).UTC()
}

// isoformat is datetime.isoformat() of a UTC time: microseconds only when non-zero, "+00:00".
func isoformat(t time.Time) string {
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05") + "+00:00"
	}
	return t.Format("2006-01-02T15:04:05.000000") + "+00:00"
}

var isoRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2})(?::?(\d{2})(?::?(\d{2})(?:[.,](\d{1,9}))?)?)?(Z|[+-]\d{2}(?::?\d{2}(?::?\d{2}(?:\.\d{1,6})?)?)?)?)?$`)

// fromISO is datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp() for aware values;
// aware is false for a naive value, ok false when Python would raise.
func fromISO(s string) (ts float64, aware, ok bool) {
	s = strings.ReplaceAll(s, "Z", "+00:00")
	m := isoRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false, false
	}
	atoi := func(x string) int { n, _ := strconv.Atoi(x); return n }
	y, mo, d := atoi(m[1]), atoi(m[2]), atoi(m[3])
	h, mi, se := atoi(m[4]), atoi(m[5]), atoi(m[6])
	if mo < 1 || mo > 12 || d < 1 || d > daysIn(y, mo) || h > 23 || mi > 59 || se > 59 || y < 1 {
		return 0, false, false
	}
	var ns int
	if f := m[7]; f != "" {
		ns = atoi((head(f, 6) + "000000")[:6]) * 1000
	}
	loc := time.Local // a naive value's .timestamp() is local time
	if tz := m[8]; tz != "" {
		aware = true
		sign := 1
		if tz[0] == '-' {
			sign = -1
		}
		digits, _, _ := strings.Cut(strings.ReplaceAll(tz[1:], ":", ""), ".")
		off := atoi(digits[0:2]) * 3600
		if len(digits) >= 4 {
			off += atoi(digits[2:4]) * 60
		}
		if len(digits) >= 6 {
			off += atoi(digits[4:6])
		}
		if off >= 86400 {
			return 0, false, false
		}
		loc = time.FixedZone("", sign*off)
	}
	t := time.Date(y, time.Month(mo), d, h, mi, se, ns, loc)
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9, aware, true
}

func daysIn(y, m int) int {
	return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// absPath is Path(p).absolute(): joined to the working directory, not cleaned of "..".
func absPath(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	wd, err := os.Getwd()
	if err != nil {
		return p
	}
	return filepath.Join(wd, p)
}

// resolved reports Path(p).resolve() == p: no symlink anywhere on the absolute path.
// A missing tail resolves to itself, like Python's non-strict resolve.
func resolved(p string) bool {
	r, err := evalCanonicalPath(p)
	if err != nil {
		if _, statErr := os.Lstat(p); !os.IsNotExist(statErr) {
			return false
		}
		// Resolve the existing prefix; the rest stays as written.
		dir, base := filepath.Dir(p), filepath.Base(p)
		if dir == p {
			return false
		}
		return resolved(dir) && base != ".."
	}
	return resolvedPathEqual(filepath.Clean(p), r)
}

// isSymlink is Path.is_symlink().
func isSymlink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// isFile is Path.is_file() (follows symlinks).
func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// exists is Path.exists() (follows symlinks).
// isDir is os.path.isdir.
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// lockFile opens path (created 0666) and takes the exclusive lock; wait false means LOCK_NB.
// The caller closes the returned file to release it.
func lockFile(path string, wait bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		return nil, err
	}
	lock := tryLock
	if wait {
		lock = waitLock
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// str is Python str(v) for JSON scalars (None -> "None").
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		return string(pyNorm(x).(json.Number))
	case float64:
		return pyRepr(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return fmt.Sprint(v)
}

// truthy is Python bool(v) for JSON values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	case host.Obj:
		return len(x) > 0
	case []string:
		return len(x) > 0
	}
	return true
}

// or returns the first truthy value (Python `a or b`), else the last.
func or(vals ...any) any {
	for _, v := range vals {
		if truthy(v) {
			return v
		}
	}
	if len(vals) == 0 {
		return nil
	}
	return vals[len(vals)-1]
}

// obj returns v as a dict, or an empty one (Python `x.get(k) or {}`).
func obj(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// sstr returns v when it is a string, else "".
func sstr(v any) string { s, _ := v.(string); return s }

// runes slices s by code points like Python s[i:j] (j < 0 means to the end).
func runes(s string, i, j int) string {
	r := []rune(s)
	if i < 0 {
		i = max(0, len(r)+i)
	}
	if j < 0 || j > len(r) {
		j = len(r)
	}
	if i > j {
		return ""
	}
	return string(r[i:j])
}

// isPySpace is str.isspace for one code point (adds the \x1c-\x1f separators Go omits).
func isPySpace(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f }

// pyStrip is str.strip().
func pyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// pyRstrip is str.rstrip().
func pyRstrip(s string) string { return strings.TrimRightFunc(s, isPySpace) }

// pyFields is str.split() with no separator.
func pyFields(s string) []string { return strings.FieldsFunc(s, isPySpace) }

// head is s[:n] by code points.
func head(s string, n int) string { return runes(s, 0, n) }

// tailRunes is s[-n:] by code points.
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
