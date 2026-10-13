//go:build !linux

package subscription

import "os"

// statStamp has no change time or inode here, so a same-size rewrite within one
// mtime tick goes unnoticed.
func statStamp(fi os.FileInfo) []any { return []any{fi.ModTime().UnixNano(), 0, fi.Size(), 0} }
