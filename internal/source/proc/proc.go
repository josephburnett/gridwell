// Package proc implements a Gridwell source over the host process table.
// It is the source-plugin form of the old proc-grid reconciler: List
// projects a process's direct children as well nodes plus a synthetic
// "@info" text node carrying the process's own metadata; Delete signals
// the process.
//
// SourceID is the PID as a decimal string. Listings are NOT authoritative:
// a process unreadable this pass is skipped, so the host must Probe a
// missing key and sweep only on a definitive PresenceGone — never on a
// failed read.
package proc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/josephburnett/gridwell/internal/procsource"
	"github.com/josephburnett/gridwell/internal/source"
)

// infoKey is the reserved key of the synthetic node that shows a process's
// own metadata, mirroring the old reconciler's "@info" source_key.
const infoKey = "@info"

// Killer is the signal surface, injected so tests never signal real
// processes. Production wires syscall.Kill.
type Killer interface {
	Kill(pid int64, sig syscall.Signal) error
}

type sysKiller struct{}

func (sysKiller) Kill(pid int64, sig syscall.Signal) error {
	return syscall.Kill(int(pid), sig)
}

// Source projects the host process table rooted at a PID.
type Source struct {
	procRoot string
	killer   Killer
}

// New returns a proc source. An empty procRoot uses /proc; a nil killer
// uses syscall.Kill. Tests pass a stub /proc and a recording killer.
func New(procRoot string, killer Killer) *Source {
	if procRoot == "" {
		procRoot = procsource.DefaultRoot
	}
	if killer == nil {
		killer = sysKiller{}
	}
	return &Source{procRoot: procRoot, killer: killer}
}

var _ source.Source = (*Source)(nil)

func (s *Source) Info() source.Descriptor {
	return source.Descriptor{Kind: "proc", DisplayName: "processes", SchemaVersion: 1}
}

// Attach turns {pid} into a root process node (default PID 1, init).
func (s *Source) Attach(_ context.Context, config map[string]string) (source.Attachment, error) {
	pidStr := config["pid"]
	if pidStr == "" {
		pidStr = "1"
	}
	pid, err := strconv.ParseInt(pidStr, 10, 64)
	if err != nil || pid <= 0 {
		return source.Attachment{}, fmt.Errorf("%w: bad pid %q", source.ErrNotFound, pidStr)
	}
	return source.Attachment{
		RootSourceID: strconv.FormatInt(pid, 10),
		Label:        rootLabel(pid),
		Caps:         source.Caps{},
		HasSession:   false,
	}, nil
}

func (s *Source) Detach(context.Context, string) error { return nil }

// List projects the @info node (the well's own metadata) plus one well per
// direct child process. Normally non-authoritative (Children may skip
// processes it couldn't read this pass, so absence is not proof of exit).
// Exception: when the well's own process is definitively gone (directory
// absent from /proc), the listing is authoritative-and-empty so the host
// sweeps all tiles — including reparented children that are still running
// but no longer belong to this well's subtree.
func (s *Source) List(_ context.Context, sourceID string) (source.Listing, error) {
	pid, err := strconv.ParseInt(sourceID, 10, 64)
	if err != nil {
		return source.Listing{}, fmt.Errorf("%w: bad pid %q", source.ErrNotFound, sourceID)
	}

	nodes := make([]source.Node, 0, 8)
	// @info — the well's own metadata. Built from one status read; the
	// same read feeds the dedup hash so an unchanged process re-dedups.
	if info, err := procsource.Get(s.procRoot, pid); err == nil {
		body := procsource.MetadataMarkdown(info)
		nodes = append(nodes, source.Node{
			Key:   infoKey,
			Kind:  source.KindText,
			Label: "info",
			W:     1, H: 1,
			Caps: source.Caps{Delete: true}, // deleting @info signals the well's own pid
			Body: &source.ContentRef{
				BlobRef:   sourceID,
				MediaType: "text/markdown",
				Hash:      hashHex([]byte(body)),
				Size:      int64(len(body)),
			},
		})
	} else {
		// Get failed. If the process is definitively gone return an
		// authoritative empty listing so the host sweeps everything,
		// including reparented children. A transient read failure
		// (dir exists, status unreadable) falls through to a normal
		// non-authoritative listing so tiles are preserved.
		present, existsErr := procsource.Exists(s.procRoot, pid)
		if existsErr == nil && !present {
			return source.Listing{Authoritative: true}, nil
		}
	}

	children, err := procsource.Children(s.procRoot, pid)
	if err != nil {
		return source.Listing{}, fmt.Errorf("children of %d: %w", pid, err)
	}
	for _, c := range children {
		nodes = append(nodes, source.Node{
			Key:   strconv.FormatInt(c.PID, 10),
			Kind:  source.KindWell,
			Label: displayName(c),
			W:     1, H: 1,
			Caps:  source.Caps{Delete: true},
			Child: strconv.FormatInt(c.PID, 10),
		})
	}
	return source.Listing{Nodes: nodes, Authoritative: false}, nil
}

// Probe reports a key's definitive presence. @info maps to the well's own
// pid (sourceID); a child key is the child pid. A failed read is
// PresenceUnknown so the host keeps the tile.
func (s *Source) Probe(_ context.Context, sourceID, key string) (source.Presence, error) {
	pidStr := key
	if key == infoKey {
		pidStr = sourceID
	}
	pid, err := strconv.ParseInt(pidStr, 10, 64)
	if err != nil {
		return source.PresenceUnknown, fmt.Errorf("bad pid %q", pidStr)
	}
	present, err := procsource.Exists(s.procRoot, pid)
	switch {
	case err != nil:
		return source.PresenceUnknown, nil
	case present:
		return source.PresencePresent, nil
	default:
		return source.PresenceGone, nil
	}
}

// ReadBlob regenerates the @info metadata body. BlobRef is the pid whose
// info to render.
func (s *Source) ReadBlob(_ context.Context, _ string, blobRef string) ([]byte, error) {
	pid, err := strconv.ParseInt(blobRef, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: bad pid %q", source.ErrNotFound, blobRef)
	}
	info, err := procsource.Get(s.procRoot, pid)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", source.ErrNotFound, err)
	}
	return []byte(procsource.MetadataMarkdown(info)), nil
}

func (s *Source) GetSession(context.Context, string) ([]byte, error) {
	return nil, source.ErrUnsupported
}
func (s *Source) PutSession(context.Context, string, []byte) error {
	return source.ErrUnsupported
}

// Delete signals a process. A child key targets the child pid; @info
// targets the well's own pid (sourceID). settled=false: SIGTERM is
// best-effort, so the host keeps the tile and lets the next reconcile
// sweep it once the process is definitively gone.
func (s *Source) Delete(_ context.Context, sourceID, key string, _ int64) (bool, error) {
	pidStr := key
	if key == infoKey {
		pidStr = sourceID
	}
	pid, err := strconv.ParseInt(pidStr, 10, 64)
	if err != nil || pid <= 0 {
		return false, fmt.Errorf("%w: bad pid %q", source.ErrNotFound, pidStr)
	}
	if err := s.killer.Kill(pid, syscall.SIGTERM); err != nil {
		return false, fmt.Errorf("kill %d: %w", pid, err)
	}
	return false, nil
}

func (s *Source) Move(context.Context, source.MoveRequest) (source.Node, error) {
	return source.Node{}, source.ErrUnsupported
}
func (s *Source) Clone(context.Context, source.CloneRequest) (source.Node, error) {
	return source.Node{}, source.ErrUnsupported
}
func (s *Source) Write(context.Context, string, string, int64, []byte) (source.Node, error) {
	return source.Node{}, source.ErrUnsupported
}
func (s *Source) SetView(context.Context, source.SetViewRequest) (source.Node, error) {
	return source.Node{}, source.ErrUnsupported
}

func rootLabel(pid int64) string {
	if pid == 1 {
		return "processes"
	}
	return fmt.Sprintf("pid %d", pid)
}

// displayName picks a short label for a process: kernel Name, else the
// basename of argv[0], else "pid N". Mirrors the old reconciler's
// procDisplayName so projected labels read identically.
func displayName(info procsource.Info) string {
	if info.Name != "" {
		return info.Name
	}
	if fields := strings.Fields(info.CmdLine); len(fields) > 0 {
		if base := filepath.Base(fields[0]); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return fmt.Sprintf("pid %d", info.PID)
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
