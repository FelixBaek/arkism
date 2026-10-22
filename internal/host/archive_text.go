package host

// ArchiveTextBlock reports whether a message content block of this type holds archive
// text. Server search, server streaming and capture all join the same blocks, so one
// message hashes the same on both sides; a block whose text is not a string still counts.
func ArchiveTextBlock(kind string) bool {
	return kind == "text" || kind == "input_text" || kind == "output_text"
}

// ArchiveBlockSeparator goes between two text blocks of one message. Codex splits one typed
// prompt into input_text blocks around images and hands its hook their plain concatenation,
// so those join with nothing; every other pair keeps the newline. The first block has none.
func ArchiveBlockSeparator(prev, kind string) string {
	if prev == "" || prev == "input_text" && kind == "input_text" {
		return ""
	}
	return "\n"
}
