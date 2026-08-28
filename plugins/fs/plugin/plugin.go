// Package provider is the v2 fs CONTENT PROVIDER (docs/v2-design.md §5):
// the stateless projection of a directory tree. Keys are slash-relative
// paths under the configured root ("." is the root context); every
// derivation and byte-level answer comes from plugins/fs/fsfile, SHARED
// with the legacy plugin so the two stacks answer identically by
// construction. No database, no ids, no layout — the node owns those.
package plugin

import (
	"context"
	"errors"
	iofs "io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/plugins/fs/fsfile"
	"github.com/josephburnett/gridwell/plugins/fs/fssource"
	"github.com/josephburnett/gridwell/plugins/fs/trash"
)

// MenuEntrySearch is the (+) menu entry id fs declares (#258); the
// search well itself waits on the userdocs store (#271).
const MenuEntrySearch = "search"

// SearchParamSchema is the form the client prompts with on first
// descent into a search well.
const SearchParamSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "title": "name contains"}
  },
  "required": ["query"]
}`

// Host is the destructive side-effect surface, injected so tests never
// touch real files (the legacy plugin's discipline).
type Host interface {
	Remove(path string) error
	RemoveAll(path string) error
}

type trashHost struct{}

func (trashHost) Remove(p string) error    { return trash.Trash(p) }
func (trashHost) RemoveAll(p string) error { return trash.Trash(p) }

// Plugin implements pluginv1.PluginServer for one directory root.
type Plugin struct {
	pluginv1.UnimplementedPluginServer
	root    string
	host    Host
	readDir func(dir string) ([]fssource.Entry, error)
}

// New builds a provider over root. nil host trashes (production); tests
// inject a recorder.
func New(root string, host Host) *Plugin {
	if host == nil {
		host = trashHost{}
	}
	return &Plugin{root: filepath.Clean(root), host: host, readDir: fssource.Read}
}

// SetReadDir overrides the directory reader (the legacy test seam:
// simulate EACCES without root). nil restores the default.
func (p *Plugin) SetReadDir(f func(dir string) ([]fssource.Entry, error)) {
	if f == nil {
		f = fssource.Read
	}
	p.readDir = f
}

// abs resolves a relative key under the root, refusing escapes. Keys are
// node-supplied (from this provider's own earlier answers), so an escape
// is a bug or an attack either way — refuse loudly.
func (p *Plugin) abs(key string) (string, error) {
	clean := path.Clean("/" + key) // "/" + forces the cleanup to anchor
	full := filepath.Join(p.root, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	if !fsfile.UnderRoot(p.root, full) {
		return "", status.Errorf(codes.InvalidArgument, "fs provider: key %q escapes the root", key)
	}
	return full, nil
}

// keyDirName splits a file key into its directory's absolute path and
// the file's name.
func (p *Plugin) keyDirName(key string) (dir, name string, err error) {
	full, err := p.abs(key)
	if err != nil {
		return "", "", err
	}
	return filepath.Dir(full), filepath.Base(full), nil
}

func (p *Plugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	resp := &pluginv1.InfoResponse{
		Kind:        "fs",
		DisplayName: "files",
		Glyph:       "folder",
		// The (+) tool fs declares (#258). The adapter strips creation
		// entries until the userdocs store exists (#271), so this is a
		// forward declaration of the search well.
		MenuEntries: []*pluginv1.MenuEntry{{
			Id:          MenuEntrySearch,
			Label:       "search",
			Kind:        "well",
			ParamSchema: SearchParamSchema,
		}},
	}
	// No configured root → ROOTLESS (the legacy rule): the plugin is
	// listed but not enterable; no context exists to descend into.
	if p.root == "" || p.root == "." {
		return resp, nil
	}
	resp.RootContext = "."
	if label := filepath.Base(p.root); label != "/" && label != "." {
		resp.DisplayName = label
	}
	return resp, nil
}

// List enumerates one directory context. A definitively-missing
// directory is an AUTHORITATIVE empty listing (its entries are gone); a
// directory that exists but cannot be read answers Unavailable — "not
// right now", which the node's read-through cache degrades to the
// remembered answer (I12, now node machinery).
func (p *Plugin) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	dir, err := p.abs(req.Context)
	if err != nil {
		return nil, err
	}
	entries, readErr := p.readDir(dir)
	if readErr != nil {
		if errors.Is(readErr, iofs.ErrNotExist) {
			return &pluginv1.ListResponse{Authoritative: true, SourceLabel: dir}, nil
		}
		return nil, status.Errorf(codes.Unavailable, "fs provider: read %s: %v", dir, readErr)
	}
	resp := &pluginv1.ListResponse{Authoritative: true, SourceLabel: dir}
	for _, e := range entries {
		key := e.Name
		if req.Context != "." && req.Context != "" {
			key = req.Context + "/" + e.Name
		}
		out := &pluginv1.Entry{Key: key, Label: e.Name}
		if e.Kind == fssource.KindDir {
			out.Kind = "well"
			out.ChildContext = key
		} else {
			out.Kind = "text"
			out.ServesPage = fsfile.ServesPage(e.Name)
			out.TextPresentation = fsfile.TextPresentation(e.Name)
			out.PreviewStamp = fsfile.PreviewStamp(dir, e.Name)
		}
		resp.Entries = append(resp.Entries, out)
	}
	return resp, nil
}

func (p *Plugin) ReadContent(req *pluginv1.ReadContentRequest, stream pluginv1.Plugin_ReadContentServer) error {
	dir, name, err := p.keyDirName(req.Key)
	if err != nil {
		return err
	}
	if fi, statErr := os.Lstat(filepath.Join(dir, name)); statErr != nil || fi.IsDir() {
		// Directories and vanished files have no document body — an
		// empty chunk, never an error (the legacy ContentBody rule).
		return stream.Send(&pluginv1.ContentChunk{})
	}
	data, mediaType := fsfile.Body(dir, name)
	return stream.Send(&pluginv1.ContentChunk{Data: data, MediaType: mediaType})
}

// serveStream adapts the provider chunk stream to fsfile's sender (the
// two services' chunk shapes match field-for-field).
type serveStream struct {
	s pluginv1.Plugin_ServeContentServer
}

func (w serveStream) Send(c *gridwellv1.ServeContentChunk) error {
	return w.s.Send(&pluginv1.ServeContentChunk{Status: c.Status, MediaType: c.MediaType, Data: c.Data})
}

func (p *Plugin) ServeContent(req *pluginv1.ServeContentRequest, stream pluginv1.Plugin_ServeContentServer) error {
	dir, name, err := p.keyDirName(req.Key)
	if err != nil {
		return err
	}
	if fi, statErr := os.Lstat(filepath.Join(dir, name)); statErr == nil && fi.IsDir() {
		return status.Error(codes.NotFound, "fs provider: directories serve no page")
	}
	return fsfile.ServeFile(serveStream{stream}, dir, name, req.Subpath)
}

func (p *Plugin) GetPreview(_ context.Context, req *pluginv1.GetPreviewRequest) (*pluginv1.GetPreviewResponse, error) {
	dir, name, err := p.keyDirName(req.Key)
	if err != nil {
		return nil, err
	}
	return &pluginv1.GetPreviewResponse{Jpeg: fsfile.PreviewJPEG(dir, name)}, nil
}

func (p *Plugin) Probe(_ context.Context, req *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	full, err := p.abs(req.Key)
	if err != nil {
		return nil, err
	}
	_, statErr := os.Lstat(full)
	switch {
	case statErr == nil:
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_PRESENT}, nil
	case os.IsNotExist(statErr):
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_GONE}, nil
	default:
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
	}
}

// Delete moves the source path to the trash (via Host). An already-gone
// path succeeds — the delete gesture is idempotent.
func (p *Plugin) Delete(_ context.Context, req *pluginv1.DeleteRequest) (*pluginv1.DeleteResponse, error) {
	full, err := p.abs(req.Key)
	if err != nil {
		return nil, err
	}
	info, statErr := os.Lstat(full)
	if statErr != nil {
		return &pluginv1.DeleteResponse{}, nil
	}
	if info.IsDir() {
		if err := p.host.RemoveAll(full); err != nil {
			return nil, status.Errorf(codes.Internal, "fs provider: remove %s: %v", full, err)
		}
	} else {
		if err := p.host.Remove(full); err != nil {
			return nil, status.Errorf(codes.Internal, "fs provider: remove %s: %v", full, err)
		}
	}
	return &pluginv1.DeleteResponse{}, nil
}
