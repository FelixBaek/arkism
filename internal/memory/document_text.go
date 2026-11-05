package memory

// HTML pages and saved e-mails (.eml) are read into Markdown-like text with
// the standard library.
import (
	"bytes"
	"encoding/base64"
	"errors"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

// htmlSkipped elements carry no readable text.
var htmlSkipped = map[string]bool{"script": true, "style": true, "head": true, "noscript": true, "template": true, "svg": true}

// htmlBlocks start a new line; headings and list items get Markdown marks.
var htmlBlocks = map[string]string{"p": "", "div": "", "br": "", "tr": "", "section": "", "article": "", "blockquote": "> ", "pre": "", "table": "", "ul": "", "ol": "",
	"h1": "# ", "h2": "## ", "h3": "### ", "h4": "#### ", "h5": "##### ", "h6": "###### ", "li": "- "}

// htmlText strips tags, skips scripts and styles and decodes entities. It is
// a reader for text, not a validating parser: broken markup still yields text.
func htmlText(src string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(src); {
		lt := strings.IndexByte(src[i:], '<')
		if lt < 0 {
			out.WriteString(html.UnescapeString(src[i:]))
			break
		}
		out.WriteString(html.UnescapeString(src[i : i+lt]))
		i += lt
		next, name, closing := htmlTag(src, i)
		switch {
		case htmlSkipped[name] && !closing:
			next = htmlSkipTo(src, next, name)
		case name == "private":
			// Keep the marker as text so the privacy filter masks the span.
			out.WriteString(map[bool]string{false: "<private>", true: "</private>"}[closing])
		case name == "td" || name == "th":
			if !closing {
				out.WriteString(" | ")
			}
		default:
			if mark, ok := htmlBlocks[name]; ok {
				out.WriteString("\n")
				if !closing {
					out.WriteString(mark)
				}
			}
		}
		i = next
	}
	lines := []string{}
	for _, line := range strings.Split(out.String(), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" && strings.Trim(line, "|#->") != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n\n"), nil
}

// htmlTag reads the tag at src[i] ('<') and returns the index after it, its
// lower-case name ("" for comments and declarations) and whether it closes.
func htmlTag(src string, i int) (int, string, bool) {
	if strings.HasPrefix(src[i:], "<!--") {
		end := strings.Index(src[i+4:], "-->")
		if end < 0 {
			return len(src), "", false
		}
		return i + 4 + end + 3, "", false
	}
	end := strings.IndexByte(src[i:], '>')
	if end < 0 {
		return len(src), "", false
	}
	body := src[i+1 : i+end]
	closing := strings.HasPrefix(body, "/")
	body = strings.TrimPrefix(body, "/")
	name := body
	if cut := strings.IndexAny(body, " \t\n/"); cut >= 0 {
		name = body[:cut]
	}
	return i + end + 1, strings.ToLower(name), closing
}

// htmlSkipTo returns the index after the closing tag of name, or the end.
func htmlSkipTo(src string, i int, name string) int {
	end := strings.Index(strings.ToLower(src[i:]), "</"+name)
	if end < 0 {
		return len(src)
	}
	next, _, _ := htmlTag(src, i+end)
	return next
}

// emlText reads a saved e-mail: the main headers, then the text body (an HTML
// body when there is no plain one), then the names of attached files.
func emlText(src string) (string, error) {
	msg, err := mail.ReadMessage(strings.NewReader(src))
	if err != nil {
		return "", ErrInvalid
	}
	dec := new(mime.WordDecoder)
	var out strings.Builder
	for _, key := range []string{"Subject", "From", "To", "Cc", "Date"} {
		if v := msg.Header.Get(key); v != "" {
			if decoded, e := dec.DecodeHeader(v); e == nil {
				v = decoded
			}
			out.WriteString(key + ": " + strings.Join(strings.Fields(v), " ") + "\n")
		}
	}
	body := emlBody{}
	if err = body.read(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), "", msg.Body, 0); err != nil {
		return "", err
	}
	text := body.plain
	if text == "" && body.html != "" {
		text, _ = htmlText(body.html)
	}
	out.WriteString("\n" + strings.TrimSpace(text) + "\n")
	for _, name := range body.files {
		out.WriteString("\nAttachment: " + name)
	}
	return out.String(), nil
}

type emlBody struct {
	plain, html string
	files       []string
}

// read walks one MIME part, up to three multipart levels deep.
func (b *emlBody) read(contentType, encoding, disposition string, r io.Reader, depth int) error {
	media, params, err := mime.ParseMediaType(contentType)
	if contentType == "" || err != nil {
		media, params = "text/plain", map[string]string{"charset": "utf-8"}
	}
	if strings.HasPrefix(media, "multipart/") {
		if depth >= 3 || params["boundary"] == "" {
			return ErrInvalid
		}
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return ErrInvalid
			}
			if err = b.read(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p.Header.Get("Content-Disposition"), p, depth+1); err != nil {
				return err
			}
		}
	}
	name := params["name"]
	if _, d, e := mime.ParseMediaType(disposition); e == nil && d["filename"] != "" {
		name = d["filename"]
	}
	if name != "" {
		b.files = append(b.files, strings.Join(strings.Fields(name), " "))
		return nil
	}
	text, ok := emlDecode(r, encoding, params["charset"])
	if !ok {
		return nil // a part in another charset is left out rather than garbled
	}
	switch media {
	case "text/plain":
		b.plain += text
	case "text/html":
		b.html += text
	}
	return nil
}

// emlDecode undoes the transfer encoding; only UTF-8 and ASCII text is kept.
func emlDecode(r io.Reader, encoding, charset string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, r) // the decoder skips line breaks
	}
	raw, err := io.ReadAll(io.LimitReader(r, documentMaxText))
	if err != nil {
		return "", false
	}
	switch strings.ToLower(charset) {
	case "", "utf-8", "us-ascii", "utf8":
	default:
		return "", false
	}
	return string(bytes.ToValidUTF8(raw, []byte("�"))), true
}
