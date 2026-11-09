package fileinfo

import (
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

func TestSize(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0", 1023: "1023", 1024: "1.0K", 1025: "1.1K", 1536: "1.5K", 10189: "10K",
		10240: "10K", 1048575: "1.0M", 1258291: "1.2M", 3565158: "3.4M", 3565159: "3.5M",
		6012954214: "5.6G", 524288000: "500M",
	} {
		if got := Size(n); got != want {
			t.Errorf("Size(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestPreview(t *testing.T) {
	for _, c := range []struct {
		name, stamp, want string
	}{
		{"archive.tar.gz", rpc.ContentStamp(1, 1258291), "1.2M  gzip"},
		{"firmware.bin", rpc.ContentStamp(1, 500), "500  binary"},
		{"thing", "", "file"},
	} {
		if got := Preview(&gridwellv1.Tile{AltText: c.name, ContentStamp: c.stamp}); got != c.want {
			t.Errorf("Preview(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRows(t *testing.T) {
	birth := int64(1_700_000_000_000_000_000)
	info := &gridwellv1.FileInfo{Path: "/r/a.bin", Size: 2048, Mode: "-rw-r--r--", Perm: 0o644, Uid: 1000,
		User: "joe", Gid: 7, Nlink: 1, Inode: 42, Device: 3, MtimeNs: birth, AtimeNs: birth, CtimeNs: birth,
		BirthNs: &birth, SniffedType: "gzip", ExtensionType: "binary"}
	rows := Rows(info, time.UTC)
	want := map[string]string{
		"path": "/r/a.bin", "size": "2.0K (2048 bytes)", "type": "gzip", "by its name": "binary",
		"mode": "-rw-r--r-- (0644)", "owner": "joe (1000)", "group": "7", "links": "1", "inode": "42",
		"modified": "2023-11-14 22:13:20.000 UTC", "born": "2023-11-14 22:13:20.000 UTC",
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Label] = r.Value
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	info.BirthNs = nil
	if n := len(Rows(info, time.UTC)); n != len(rows)-1 {
		t.Errorf("a host with no birth time shows %d rows, want %d", n, len(rows)-1)
	}
}
