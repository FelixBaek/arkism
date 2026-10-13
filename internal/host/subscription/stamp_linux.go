package subscription

import (
	"os"
	"syscall"
)

func statStamp(fi os.FileInfo) []any {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return []any{fi.ModTime().UnixNano(), 0, fi.Size(), 0}
	}
	return []any{fi.ModTime().UnixNano(), st.Ctim.Nano(), fi.Size(), int64(st.Ino)}
}
