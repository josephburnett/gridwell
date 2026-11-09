// Package fileinfo says what a file is: the preview of a file that renders as
// nothing, from its row alone; its file information as rows of a table, from
// the node's FileInfo; and its bytes as the hex view lays them out.
package fileinfo

import (
	"fmt"
	"math"
	"strconv"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Size is n bytes as ls -h spells it: bytes below 1024, then K, M, G, T, P
// and E rounded up, with one decimal below 10 and none above.
func Size(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10)
	}
	v := float64(n)
	for _, unit := range "KMGTPE" {
		v /= 1024
		if tenths := math.Ceil(v * 10); tenths < 100 {
			return strconv.FormatFloat(tenths/10, 'f', 1, 64) + string(unit)
		}
		if whole := math.Ceil(v); whole < 1024 {
			return strconv.FormatFloat(whole, 'f', 0, 64) + string(unit)
		}
	}
	return strconv.FormatFloat(math.Ceil(v), 'f', 0, 64) + "E"
}

// Preview is what a file's tile shows under its name with nothing read: its
// size from the stamp on its row and the type its extension says.
func Preview(t *gridwellv1.Tile) string {
	preview := rpc.ExtensionType(t.GetAltText())
	if n, ok := rpc.StampSize(t.GetContentStamp()); ok {
		preview = Size(n) + "  " + preview
	}
	return preview
}

// Row is one line of the file information table.
type Row struct{ Label, Value string }

// Rows is the file information table for info, its times in loc.
func Rows(info *gridwellv1.FileInfo, loc *time.Location) []Row {
	at := func(ns int64) string { return time.Unix(0, ns).In(loc).Format("2006-01-02 15:04:05.000 MST") }
	id := func(name string, n uint32) string {
		if name == "" {
			return strconv.FormatUint(uint64(n), 10)
		}
		return name + " (" + strconv.FormatUint(uint64(n), 10) + ")"
	}
	rows := []Row{
		{"path", info.GetPath()},
		{"size", Size(info.GetSize()) + " (" + strconv.FormatInt(info.GetSize(), 10) + " bytes)"},
		{"type", info.GetSniffedType()},
		{"by its name", info.GetExtensionType()},
		{"mode", fmt.Sprintf("%s (%04o)", info.GetMode(), info.GetPerm())},
		{"owner", id(info.GetUser(), info.GetUid())},
		{"group", id(info.GetGroup(), info.GetGid())},
		{"links", strconv.FormatUint(info.GetNlink(), 10)},
		{"inode", strconv.FormatUint(info.GetInode(), 10)},
		{"device", strconv.FormatUint(info.GetDevice(), 10)},
		{"modified", at(info.GetMtimeNs())},
		{"accessed", at(info.GetAtimeNs())},
		{"changed", at(info.GetCtimeNs())},
	}
	if info.BirthNs != nil {
		rows = append(rows, Row{"born", at(info.GetBirthNs())})
	}
	return rows
}
