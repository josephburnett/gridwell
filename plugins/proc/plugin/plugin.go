// Package provider is the v2 proc CONTENT PROVIDER (docs/v2-design.md
// §5): the stateless projection of the process table. A context is a pid
// (its grid lists that process's direct children); tile keys are pid
// strings, plus "info:<pid>" for the @info metadata tile. Listings are
// NON-authoritative — a child unreadable this pass is not gone; the node
// arbitrates absence through Probe (the legacy reconcile's sweep rule,
// now adapter machinery). No database: the process table is the source.
package plugin

import (
	"context"
	"strconv"
	"strings"
	"syscall"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/plugins/proc/procsource"
)

// Killer is the signal interface. Injected so tests never signal real
// processes. Production uses syscall.Kill (sysKiller).
type Killer interface {
	Kill(pid int64, sig syscall.Signal) error
}

// infoLabel is the metadata tile's display label (the legacy key).
const infoLabel = "@info"

// infoKeyPrefix namespaces the metadata tiles' keys: "@info" appears in
// EVERY grid, but provider keys must be globally unique.
const infoKeyPrefix = "info:"

type sysKiller struct{}

func (sysKiller) Kill(pid int64, sig syscall.Signal) error {
	return syscall.Kill(int(pid), sig)
}

// Provider implements pluginv1.PluginServer for the process table.
type Provider struct {
	pluginv1.UnimplementedPluginServer
	procRoot string
	rootPID  int64
	killer   Killer
}

// New builds a provider. Empty procRoot uses /proc; rootPID <= 0 uses
// pid 1; nil killer signals real processes.
func New(procRoot string, rootPID int64, killer Killer) *Provider {
	if procRoot == "" {
		procRoot = procsource.DefaultRoot
	}
	if rootPID <= 0 {
		rootPID = 1
	}
	if killer == nil {
		killer = sysKiller{}
	}
	return &Provider{procRoot: procRoot, rootPID: rootPID, killer: killer}
}

func (p *Provider) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	label := "processes"
	if p.rootPID != 1 {
		label = "pid " + strconv.FormatInt(p.rootPID, 10)
	}
	return &pluginv1.InfoResponse{
		Kind:        "proc",
		DisplayName: label,
		Glyph:       "process",
		RootContext: strconv.FormatInt(p.rootPID, 10),
	}, nil
}

// keyPID resolves any key shape to the pid it denotes.
func keyPID(key string) (int64, error) {
	s := strings.TrimPrefix(key, infoKeyPrefix)
	pid, err := strconv.ParseInt(s, 10, 64)
	if err != nil || pid <= 0 {
		return 0, status.Errorf(codes.InvalidArgument, "proc provider: invalid key %q", key)
	}
	return pid, nil
}

// List enumerates one process's children plus its @info tile — in the
// legacy reconcile's insertion order (@info first), so the node mints
// the same ids the legacy DB did. Never Unavailable: an unreadable
// process table answers what it could read, non-authoritatively, and
// the Probe arbitration does the rest.
func (p *Provider) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	pid, err := keyPID(req.Context)
	if err != nil {
		return nil, err
	}
	resp := &pluginv1.ListResponse{Authoritative: false, SourceLabel: req.Context}
	if _, gerr := procsource.Get(p.procRoot, pid); gerr == nil {
		resp.Entries = append(resp.Entries, &pluginv1.Entry{
			Key:  infoKeyPrefix + req.Context,
			Kind: "text", Label: infoLabel,
		})
	}
	children, cerr := procsource.Children(p.procRoot, pid)
	if cerr == nil {
		for _, c := range children {
			key := strconv.FormatInt(c.PID, 10)
			resp.Entries = append(resp.Entries, &pluginv1.Entry{
				Key: key, Kind: "well", Label: key, ChildContext: key,
			})
		}
	}
	return resp, nil
}

func (p *Provider) ReadContent(req *pluginv1.ReadContentRequest, stream pluginv1.Plugin_ReadContentServer) error {
	if !strings.HasPrefix(req.Key, infoKeyPrefix) {
		// Process wells carry no document body (the legacy rule).
		return stream.Send(&pluginv1.ContentChunk{})
	}
	pid, err := keyPID(req.Key)
	if err != nil {
		return err
	}
	info, gerr := procsource.Get(p.procRoot, pid)
	if gerr != nil {
		return stream.Send(&pluginv1.ContentChunk{})
	}
	return stream.Send(&pluginv1.ContentChunk{
		Data:      []byte(procsource.MetadataMarkdown(info)),
		MediaType: "text/markdown",
	})
}

func (p *Provider) Probe(_ context.Context, req *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	if strings.HasPrefix(req.Key, infoKeyPrefix) {
		// @info is NEVER swept (the legacy reconcile skipped it by
		// name): it describes the grid's own process, and the grid
		// outliving the process is the wells' problem, not @info's.
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_PRESENT}, nil
	}
	pid, err := keyPID(req.Key)
	if err != nil {
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	present, perr := procsource.Exists(p.procRoot, pid)
	switch {
	case perr != nil:
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
	case present:
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_PRESENT}, nil
	default:
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_GONE}, nil
	}
}

// Delete sends SIGTERM — best-effort; the tile sweeps once the process
// is definitively gone (the legacy semantics).
func (p *Provider) Delete(_ context.Context, req *pluginv1.DeleteRequest) (*pluginv1.DeleteResponse, error) {
	pid, err := keyPID(req.Key)
	if err != nil {
		return nil, err
	}
	if kerr := p.killer.Kill(pid, syscall.SIGTERM); kerr != nil {
		return nil, status.Errorf(codes.Internal, "proc provider: kill %d: %v", pid, kerr)
	}
	return &pluginv1.DeleteResponse{}, nil
}
