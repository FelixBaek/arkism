package memory

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"math/rand"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func pdfOf(objs ...string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	for i, o := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	return []byte(b.String())
}

func pdfFlate(data []byte) string {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	_, _ = w.Write(data)
	_ = w.Close()
	return fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", z.Len(), z.String())
}

// pdfMustFinish fails when parsing panics, hangs or allocates without bound.
func pdfMustFinish(t *testing.T, name string, raw []byte) {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Sprint(p, string(debug.Stack()))
				return
			}
			done <- nil
		}()
		pages, _ := pdfPagesText(raw)
		total := 0
		for _, p := range pages {
			total += len(p)
		}
		if total > pdfMaxText {
			panic(fmt.Sprintf("text %d over cap", total))
		}
	}()
	select {
	case p := <-done:
		if p != nil {
			t.Fatalf("%s: panic %v", name, p)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: did not finish", name)
	}
}

func TestPDFAdversarialInputs(t *testing.T) {
	page := "<< /Type /Catalog /Pages 2 0 R >>"
	cases := map[string][]byte{
		"huge length":     pdfOf("<< /Length 1e300 >>\nstream\nabc\nendstream"),
		"inf length":      pdfOf("<< /Length Inf >>\nstream\nabc\nendstream"),
		"objstm first":    pdfOf("<< /Type /ObjStm /N 1 /First 1e300 /Length 3 >>\nstream\n1 0\nendstream"),
		"objstm offset":   pdfOf("<< /Type /ObjStm /N 1 /First 4 /Length 8 >>\nstream\n1 9e300 abc\nendstream"),
		"cyclic pages":    pdfOf(page, "<< /Type /Pages /Kids [2 0 R 2 0 R] >>"),
		"ref loop":        pdfOf("<< /Type /Catalog /Pages 2 0 R >>", "3 0 R", "2 0 R"),
		"deep nesting":    pdfOf(strings.Repeat("[", 100000) + strings.Repeat("<<", 100000)),
		"unterminated":    pdfOf("<< /A (abc", "[ 1 2"),
		"wide cmap range": pdfOf(page, "<< /Type /Pages /Kids [3 0 R] >>", "<< /Type /Page /Contents 4 0 R /Resources << /Font << /F 5 0 R >> >> >>", pdfFlate([]byte("BT /F 1 Tf (a) Tj ET")), "<< /Subtype /Type0 /ToUnicode 6 0 R >>", pdfFlate([]byte("1 beginbfrange <00000000> <FFFFFFFF> [] endbfrange"))),
		"top cmap range":  pdfOf(page, "<< /Type /Pages /Kids [3 0 R] >>", "<< /Type /Page /Contents 4 0 R /Resources << /Font << /F 5 0 R >> >> >>", pdfFlate([]byte("BT /F 1 Tf (a) Tj ET")), "<< /Subtype /Type0 /ToUnicode 6 0 R >>", pdfFlate([]byte("1 beginbfrange <FFFF0000> <FFFFFFFF> [] endbfrange"))),
		"cmap dst bomb":   pdfOf(page, "<< /Type /Pages /Kids [3 0 R] >>", "<< /Type /Page /Contents 4 0 R /Resources << /Font << /F 5 0 R >> >> >>", pdfFlate([]byte("BT /F 1 Tf ("+strings.Repeat("a", 1<<20)+") Tj ET")), "<< /ToUnicode 6 0 R >>", pdfFlate([]byte("1 beginbfrange <00> <FF> <"+strings.Repeat("0041", 1<<16)+"> endbfrange"))),
	}
	// 1000 pages sharing one 16 MiB stream: every page must not inflate it again.
	kids := strings.TrimSpace(strings.Repeat("3 0 R ", 1000))
	cases["shared stream"] = pdfOf(page, "<< /Type /Pages /Kids ["+kids+"] >>", "<< /Type /Page /Contents 4 0 R >>", pdfFlate(bytes.Repeat([]byte(" "), pdfMaxStream)))
	for name, raw := range cases {
		pdfMustFinish(t, name, raw)
	}
}

func TestPDFRandomInputsNeverPanic(t *testing.T) {
	seed := tinyPDF(t, "")
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		b := append([]byte(nil), seed...)
		for n := rng.Intn(8) + 1; n > 0; n-- {
			b[rng.Intn(len(b))] = "0123456789<>[]()/R \n9e99"[rng.Intn(24)]
		}
		pdfMustFinish(t, fmt.Sprint("mutation ", i), b)
	}
}
