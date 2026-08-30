// Package plugin is the fs content plugin: a stateless projection of a
// directory tree. Keys are slash-relative paths under the configured root, and
// "." is the root context. Every derivation and byte-level answer comes from
// plugins/fs/fsfile. There is no database, no ids, and no layout; the node
// owns those.
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

// Host is the destructive side-effect surface, injected so tests never touch
// real files.
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

// FromConfig builds the production plugin from the shared config vocabulary.
// It is the one owner of the config-to-plugin derivation, so the subprocess
// main and a bundled binary compose exactly the same plugin. The config key is
// root, the projected directory. No root makes a rootless plugin — listed but
// not enterable, a fixable gap the client reports as a notice — rather than a
// refusal.
func FromConfig(cfg map[string]string) (pluginv1.PluginServer, error) {
	return New(strings.TrimSpace(cfg["root"]), nil), nil
}

// New builds a plugin over root. A nil host trashes, as production does; tests
// inject a recorder.
func New(root string, host Host) *Plugin {
	if host == nil {
		host = trashHost{}
	}
	return &Plugin{root: filepath.Clean(root), host: host, readDir: fssource.Read}
}

// SetReadDir overrides the directory reader, so a test can simulate EACCES
// without root. nil restores the default.
func (p *Plugin) SetReadDir(f func(dir string) ([]fssource.Entry, error)) {
	if f == nil {
		f = fssource.Read
	}
	p.readDir = f
}

// abs resolves a relative key under the root, refusing escapes. Keys are
// node-supplied (from this plugin's own earlier answers), so an escape
// is a bug or an attack either way — refuse loudly.
func (p *Plugin) abs(key string) (string, error) {
	clean := path.Clean("/" + key) // the leading "/" anchors the cleanup
	full := filepath.Join(p.root, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	if !fsfile.UnderRoot(p.root, full) {
		return "", status.Errorf(codes.InvalidArgument, "fs plugin: key %q escapes the root", key)
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
	}
	// No configured root makes the plugin rootless: it is listed but not
	// enterable, because no context exists to descend into.
	if p.root == "" || p.root == "." {
		return resp, nil
	}
	resp.RootContext = "."
	if label := filepath.Base(p.root); label != "/" && label != "." {
		resp.DisplayName = label
	}
	return resp, nil
}

// List enumerates one directory context. A definitively missing directory is
// an authoritative empty listing, because its entries are gone; a directory
// that exists but cannot be read answers Unavailable, meaning "not right now",
// which the node's read-through cache degrades to the remembered answer.
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
		return nil, status.Errorf(codes.Unavailable, "fs plugin: read %s: %v", dir, readErr)
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
		// A directory or a vanished file has no document body: an empty
		// chunk, never an error.
		return stream.Send(&pluginv1.ContentChunk{})
	}
	data, mediaType := fsfile.Body(dir, name)
	return stream.Send(&pluginv1.ContentChunk{Data: data, MediaType: mediaType})
}

// serveStream adapts the plugin chunk stream to fsfile's sender; the two chunk
// shapes match field for field.
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
		return status.Error(codes.NotFound, "fs plugin: directories serve no page")
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

// Delete moves the source path to the trash, through Host. An already-gone
// path succeeds: the delete gesture is idempotent.
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
			return nil, status.Errorf(codes.Internal, "fs plugin: remove %s: %v", full, err)
		}
	} else {
		if err := p.host.Remove(full); err != nil {
			return nil, status.Errorf(codes.Internal, "fs plugin: remove %s: %v", full, err)
		}
	}
	return &pluginv1.DeleteResponse{}, nil
}
