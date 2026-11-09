package files

import (
	"os"
	"syscall"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// inode fills what fi's stat says beyond its size, mode and mtime.
func inode(_ *os.File, fi os.FileInfo, info *pb.FileInfo) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	info.Perm, info.Uid, info.Gid = uint32(st.Mode)&0o7777, st.Uid, st.Gid
	info.Nlink, info.Inode, info.Device = uint64(st.Nlink), st.Ino, uint64(st.Dev)
	info.AtimeNs, info.CtimeNs = st.Atimespec.Nano(), st.Ctimespec.Nano()
	b := st.Birthtimespec.Nano()
	info.BirthNs = &b
}
