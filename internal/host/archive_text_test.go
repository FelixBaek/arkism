package host

import "testing"

func TestArchiveTextJoin(t *testing.T) {
	for _, kind := range []string{"text", "input_text", "output_text"} {
		if !ArchiveTextBlock(kind) {
			t.Fatalf("%s is archive text", kind)
		}
	}
	if ArchiveTextBlock("input_image") {
		t.Fatal("input_image is not archive text")
	}
	for _, c := range []struct{ prev, kind, want string }{
		{"", "text", ""}, {"input_text", "input_text", ""}, {"text", "text", "\n"}, {"input_text", "text", "\n"}, {"output_text", "input_text", "\n"},
	} {
		if got := ArchiveBlockSeparator(c.prev, c.kind); got != c.want {
			t.Fatalf("separator(%q,%q) = %q, want %q", c.prev, c.kind, got, c.want)
		}
	}
}
