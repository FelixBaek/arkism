package memory

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"unicode/utf8"
)

// Local defence for unchanged textual file bytes, not permission to store them.
// The caller must still authorize/inspect the complete payload and raise PII
// sensitivity. Binary formats need their own inspection before public ingestion.
func inspectArtifactText(content []byte, mime string) (Inspection, error) {
	if len(content) == 0 || len(content) > 200<<20 || (mime != "text/plain" && mime != "application/json") || !utf8.Valid(content) || bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf}) {
		return Inspection{}, ErrInvalid
	}
	if http.DetectContentType(content) != "text/plain; charset=utf-8" {
		return Inspection{}, ErrInvalid
	}
	report := Inspection{RulesVersion: inspectionVersion}
	check := func(s string) error {
		for _, b := range s {
			if b < 32 && b != '\t' && b != '\r' && b != '\n' || b == 127 {
				return ErrInvalid
			}
		}
		if containsSecret(s) {
			return ErrSecretDetected
		}
		report.PersonalData = report.PersonalData || personalPattern.MatchString(s)
		return nil
	}
	if err := check(string(content)); err != nil {
		return Inspection{}, err
	}
	isJSON := json.Valid(content)
	if mime == "application/json" && !isJSON {
		return Inspection{}, ErrInvalid
	}
	if !isJSON {
		return report, nil
	}
	// Tokenize instead of unmarshalling a map: duplicate keys must not hide an
	// earlier secret. Decoded strings expose JSON unicode/escape sequences.
	d := json.NewDecoder(bytes.NewReader(content))
	d.UseNumber()
	type frame struct {
		object, nextKey bool
		key             string
	}
	var stack []frame
	for {
		token, err := d.Token()
		if err == io.EOF {
			return report, nil // json.Valid already requires exactly one document.
		}
		if err != nil {
			return Inspection{}, ErrInvalid
		}
		if delim, ok := token.(json.Delim); ok && (delim == '}' || delim == ']') {
			stack = stack[:len(stack)-1]
			continue
		}
		if s, ok := token.(string); ok {
			if err := check(s); err != nil {
				return Inspection{}, err
			}
		}
		if len(stack) > 0 && stack[len(stack)-1].object {
			f := &stack[len(stack)-1]
			if f.nextKey {
				f.key = token.(string) // JSON grammar guarantees an object key.
			} else {
				// Inspect decoded field names together with their value, including
				// non-string values and containers, using the existing assignment rules.
				value := structuredText(token)
				if delim, ok := token.(json.Delim); ok {
					value = string(delim)
				}
				if err := check(f.key + ":" + value); err != nil {
					return Inspection{}, err
				}
			}
			f.nextKey = !f.nextKey
		}
		if delim, ok := token.(json.Delim); ok {
			stack = append(stack, frame{object: delim == '{', nextKey: true})
		}
	}
}
