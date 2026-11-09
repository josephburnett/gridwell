package files

import (
	"os"

	"golang.org/x/sys/unix"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// inode fills what f's inode says beyond fi; statx alone knows a birth time,
// and a filesystem that keeps none leaves it absent.
func inode(f *os.File, _ os.FileInfo, info *pb.FileInfo) {
	var st unix.Statx_t
	c, err := f.SyscallConn()
	if err == nil {
		_ = c.Control(func(fd uintptr) {
			err = unix.Statx(int(fd), "", unix.AT_EMPTY_PATH, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &st)
		})
	}
	if err != nil {
		return
	}
	ns := func(t unix.StatxTimestamp) int64 { return t.Sec*1e9 + int64(t.Nsec) }
	info.Perm, info.Uid, info.Gid = uint32(st.Mode)&0o7777, st.Uid, st.Gid
	info.Nlink, info.Inode = uint64(st.Nlink), st.Ino
	info.Device = unix.Mkdev(st.Dev_major, st.Dev_minor)
	info.AtimeNs, info.CtimeNs = ns(st.Atime), ns(st.Ctime)
	if st.Mask&unix.STATX_BTIME != 0 {
		b := ns(st.Btime)
		info.BirthNs = &b
	}
}
