//go:build !windows

package main

import (
	"os"
	"syscall"
)

// fileOwner is the uid and gid of a file; ok is false where the platform has none.
func fileOwner(fi os.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
