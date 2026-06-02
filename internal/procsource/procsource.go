// Package procsource reads the Linux /proc filesystem and projects the
// process tree into abstract entries the proc-grid reconciler in
// internal/store turns into tile rows. Pure-Go, no DB. Tests run against
// a temp-dir stub /proc so we don't depend on the host's process table.
package procsource

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DefaultRoot is the production /proc mount point. Tests pass a temp
// directory; the production server passes DefaultRoot.
const DefaultRoot = "/proc"

// Info is the metadata for a single process.
type Info struct {
	PID     int64
	PPID    int64
	Name    string
	CmdLine string
	UID     int64
}

// Children returns the direct child processes of parentPID, sorted by
// PID for deterministic auto-grid layout. It walks every numeric
// subdirectory of procRoot, reads the status file, and keeps those
// whose PPid matches.
//
// procRoot is normally DefaultRoot. Tests pass a stub directory.
func Children(procRoot string, parentPID int64) ([]Info, error) {
	pids, err := listPIDs(procRoot)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, pid := range pids {
		info, err := readInfo(procRoot, pid)
		if err != nil {
			// A process can disappear between listPIDs and readInfo.
			// Skip it rather than failing the whole read.
			continue
		}
		if info.PPID == parentPID {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// Get returns Info for a single PID, or an error if the process is gone
// or unreadable.
func Get(procRoot string, pid int64) (Info, error) {
	return readInfo(procRoot, pid)
}

// listPIDs returns the numeric subdirectory names of procRoot as int64
// PIDs.
func listPIDs(procRoot string) ([]int64, error) {
	f, err := os.Open(procRoot)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", procRoot, err)
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("readdirnames %s: %w", procRoot, err)
	}
	out := make([]int64, 0, len(names))
	for _, name := range names {
		pid, err := strconv.ParseInt(name, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, pid)
	}
	return out, nil
}

// readInfo reads /proc/<pid>/status and /proc/<pid>/cmdline into an Info.
func readInfo(procRoot string, pid int64) (Info, error) {
	info := Info{PID: pid}
	dir := filepath.Join(procRoot, strconv.FormatInt(pid, 10))
	if err := parseStatus(filepath.Join(dir, "status"), &info); err != nil {
		return Info{}, err
	}
	cmd, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
	info.CmdLine = formatCmdline(cmd)
	return info, nil
}

// parseStatus reads /proc/<pid>/status (a TEXT key:value list) and
// extracts the fields Info cares about (Name, PPid, Uid).
func parseStatus(path string, out *Info) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		k, v, ok := splitKV(line)
		if !ok {
			continue
		}
		switch k {
		case "Name":
			out.Name = v
		case "PPid":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				out.PPID = n
			}
		case "Uid":
			// "Uid:\t1000\t1000\t1000\t1000" — the first field is the
			// real UID, which is the one to display.
			fields := strings.Fields(v)
			if len(fields) > 0 {
				if n, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
					out.UID = n
				}
			}
		}
	}
	return scanner.Err()
}

// splitKV splits a "Key:\tValue" line.
func splitKV(line string) (key, value string, ok bool) {
	k, v, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	return k, strings.TrimSpace(v), true
}

// formatCmdline replaces the NUL separators in /proc/<pid>/cmdline with
// spaces. Returns "" for kernel threads (which have empty cmdline).
func formatCmdline(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	b = stripTrailingNUL(b)
	for i, c := range b {
		if c == 0 {
			b[i] = ' '
		}
	}
	return string(b)
}

func stripTrailingNUL(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}

// MetadataMarkdown returns the descent body for a process info tile —
// deterministic so its blob hash dedupes across reconciles of an
// unchanged process.
func MetadataMarkdown(info Info) string {
	var b strings.Builder
	b.WriteString("# ")
	if info.Name != "" {
		b.WriteString(info.Name)
	} else {
		fmt.Fprintf(&b, "pid %d", info.PID)
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "- pid: %d\n", info.PID)
	fmt.Fprintf(&b, "- ppid: %d\n", info.PPID)
	fmt.Fprintf(&b, "- uid: %d\n", info.UID)
	if info.CmdLine != "" {
		fmt.Fprintf(&b, "- cmd: `%s`\n", info.CmdLine)
	}
	return b.String()
}
