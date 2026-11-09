//go:build !linux && !darwin

package files

import (
	"os"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// inode says nothing beyond fi's size, mode and mtime on a host whose stat
// this package does not read.
func inode(*os.File, os.FileInfo, *pb.FileInfo) {}
