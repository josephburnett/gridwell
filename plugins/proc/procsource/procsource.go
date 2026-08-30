// Package procsource reads the Linux /proc filesystem and projects the process
// tree into the abstract entries the proc plugin lists. It is pure Go, with no
// database. Tests run against a temp-dir stub /proc, so they do not depend on
// the host's process table.
package procsource

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DefaultRoot is the production /proc mount point. Tests pass a temp
// directory; the production server passes DefaultRoot.
const DefaultRoot = "/proc"

// Info is the metadata for a single process. Fields beyond the
// identifier triple (PID/PPID/Name) come from /proc/<pid>/status and
// /proc/<pid>/cwd; readers that can't resolve a field leave it at its
// zero value rather than failing the read — a process is allowed to
// hide bits of its state from a non-privileged reader.
type Info struct {
	PID     int64
	PPID    int64
	Name    string
	CmdLine string
	UID     int64
	// State is the single-character /proc kernel state ("R", "S", "D",
	// "Z", etc.) plus the human-readable label the kernel pairs it with
	// ("R (running)"). Empty if status was unreadable.
	State string
	// Threads is the kernel's Threads: count for this thread group.
	Threads int64
	// VmRSSKB is resident set size in kilobytes (the "VmRSS:" line).
	// VmSizeKB is total virtual memory. Both 0 for kernel threads
	// (whose status omits these) and for processes whose status can't
	// be read.
	VmRSSKB  int64
	VmSizeKB int64
	// Cwd is the absolute path of the process's current working
	// directory, resolved through /proc/<pid>/cwd. Empty when the
	// symlink can't be followed (sandboxed, vanished, permission
	// denied).
	Cwd string
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

// Exists reports whether pid currently has an entry in the host process table
// (a /proc/<pid> directory). It is the *presence* signal, deliberately separate
// from Get (which reads metadata and can fail for a process that still exists):
//
//   - present                → (true, nil)
//   - definitively not there  → (false, nil)   [the only "gone" answer]
//   - couldn't determine      → (false, err)   [permission, transient I/O]
//
// Callers must treat a non-nil error as "unknown", never as "gone": a tile is
// removed only on a definitive (false, nil), never on a failure to read. This
// keeps a transiently-unreadable process from losing its tile's position/id.
func Exists(procRoot string, pid int64) (bool, error) {
	_, err := os.Stat(filepath.Join(procRoot, strconv.FormatInt(pid, 10)))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
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

// readInfo reads /proc/<pid>/status and /proc/<pid>/cmdline into an
// Info, then resolves /proc/<pid>/cwd best-effort. Status is the only
// hard dependency: a missing status means the process is gone. cwd
// failures (sandbox / permission) leave Cwd empty without surfacing an
// error.
func readInfo(procRoot string, pid int64) (Info, error) {
	info := Info{PID: pid}
	dir := filepath.Join(procRoot, strconv.FormatInt(pid, 10))
	if err := parseStatus(filepath.Join(dir, "status"), &info); err != nil {
		return Info{}, err
	}
	cmd, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
	info.CmdLine = formatCmdline(cmd)
	if cwd, err := os.Readlink(filepath.Join(dir, "cwd")); err == nil {
		info.Cwd = cwd
	}
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
		case "State":
			// "R (running)" — keep the whole label so callers can
			// surface a short char (split on whitespace) or the full
			// label depending on how much room the renderer has.
			out.State = v
		case "PPid":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				out.PPID = n
			}
		case "Threads":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				out.Threads = n
			}
		case "VmRSS":
			// "VmRSS:\t12345 kB" — kernel always reports in kB. Strip
			// the unit and parse the number.
			if n, err := strconv.ParseInt(firstField(v), 10, 64); err == nil {
				out.VmRSSKB = n
			}
		case "VmSize":
			if n, err := strconv.ParseInt(firstField(v), 10, 64); err == nil {
				out.VmSizeKB = n
			}
		case "Uid":
			// "Uid:\t1000\t1000\t1000\t1000" — the first field is the
			// real UID, which is the one to display.
			if n, err := strconv.ParseInt(firstField(v), 10, 64); err == nil {
				out.UID = n
			}
		}
	}
	return scanner.Err()
}

// firstField returns the first whitespace-separated token of s, or ""
// if s is empty. Used to extract the leading numeric column out of
// "Uid:\t1000\t1000\t1000\t1000" and "VmRSS:\t12345 kB" style fields
// without allocating a full Fields slice.
func firstField(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
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

// MetadataMarkdown returns the descent body for a process info tile. It is
// deterministic, so its blob hash dedupes across reads of an unchanged
// process. The layout is a small markdown list because the same renderer paints
// tile previews and descents, and a bullet list reads as process detail at any
// zoom without the chrome a table would force.
//
// Fields are emitted in stable order, and a zero value for an optional field —
// Cwd, memory, state, threads — is omitted, so a kernel thread's tile does not
// show "vm-size: 0 kB" lines. A field that could not be read is not
// rendered.
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
	if info.State != "" {
		fmt.Fprintf(&b, "- state: %s\n", info.State)
	}
	if info.Threads > 0 {
		fmt.Fprintf(&b, "- threads: %d\n", info.Threads)
	}
	if info.VmRSSKB > 0 {
		fmt.Fprintf(&b, "- rss: %s\n", formatKB(info.VmRSSKB))
	}
	if info.VmSizeKB > 0 {
		fmt.Fprintf(&b, "- vm-size: %s\n", formatKB(info.VmSizeKB))
	}
	if info.Cwd != "" {
		fmt.Fprintf(&b, "- cwd: `%s`\n", info.Cwd)
	}
	if info.CmdLine != "" {
		fmt.Fprintf(&b, "- cmd: `%s`\n", info.CmdLine)
	}
	return b.String()
}

// formatKB turns a kilobyte count into a short human-readable string: "120
// KiB" below a mebibyte, "12.3 MiB" for a typical process, "1.4 GiB" for a
// large one. The units are binary because the kernel reports kB but means KiB,
// in 1024-byte blocks.
func formatKB(kb int64) string {
	const (
		mib = 1024
		gib = 1024 * 1024
	)
	switch {
	case kb >= gib:
		return fmt.Sprintf("%.1f GiB", float64(kb)/float64(gib))
	case kb >= mib:
		return fmt.Sprintf("%.1f MiB", float64(kb)/float64(mib))
	default:
		return fmt.Sprintf("%d KiB", kb)
	}
}
