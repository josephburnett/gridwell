// Package provider is the v2 fs CONTENT PROVIDER (docs/v2-design.md §5):
// the stateless projection of a directory tree. Keys are slash-relative
// paths under the configured root ("." is the root context); every
// derivation and byte-level answer comes from plugins/fs/fsfile, SHARED
// with the legacy plugin so the two stacks answer identically by
// construction. No database, no ids, no layout — the node owns those.
package provider

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

	cpv1 "github.com/josephburnett/gridwell/api/gen/contentprovider/v1"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	fslegacy "github.com/josephburnett/gridwell/plugins/fs"
	"github.com/josephburnett/gridwell/plugins/fs/fsfile"
	"github.com/josephburnett/gridwell/plugins/fs/fssource"
	"github.com/josephburnett/gridwell/plugins/fs/trash"
)

// Host is the destructive side-effect surface, injected so tests never
// touch real files (the legacy plugin's discipline).
type Host interface {
	Remove(path string) error
	RemoveAll(path string) error
}

type trashHost struct{}

func (trashHost) Remove(p string) error    { return trash.Trash(p) }
func (trashHost) RemoveAll(p string) error { return trash.Trash(p) }

// Provider implements cpv1.ContentProviderServer for one directory root.
type Provider struct {
	cpv1.UnimplementedContentProviderServer
	root    string
	host    Host
	readDir func(dir string) ([]fssource.Entry, error)
}

// New builds a provider over root. nil host trashes (production); tests
// inject a recorder.
func New(root string, host Host) *Provider {
	if host == nil {
		host = trashHost{}
	}
	return &Provider{root: filepath.Clean(root), host: host, readDir: fssource.Read}
}

// SetReadDir overrides the directory reader (the legacy test seam:
// simulate EACCES without root). nil restores the default.
func (p *Provider) SetReadDir(f func(dir string) ([]fssource.Entry, error)) {
	if f == nil {
		f = fssource.Read
	}
	p.readDir = f
}

// abs resolves a relative key under the root, refusing escapes. Keys are
// node-supplied (from this provider's own earlier answers), so an escape
// is a bug or an attack either way — refuse loudly.
func (p *Provider) abs(key string) (string, error) {
	clean := path.Clean("/" + key) // "/" + forces the cleanup to anchor
	full := filepath.Join(p.root, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	if full != p.root && !strings.HasPrefix(full, p.root+string(filepath.Separator)) {
		return "", status.Errorf(codes.InvalidArgument, "fs provider: key %q escapes the root", key)
	}
	return full, nil
}

// keyDirName splits a file key into its directory's absolute path and
// the file's name.
func (p *Provider) keyDirName(key string) (dir, name string, err error) {
	full, err := p.abs(key)
	if err != nil {
		return "", "", err
	}
	return filepath.Dir(full), filepath.Base(full), nil
}

func (p *Provider) Info(context.Context, *cpv1.InfoRequest) (*cpv1.InfoResponse, error) {
	resp := &cpv1.InfoResponse{
		Kind:        "fs",
		DisplayName: "files",
		Glyph:       "folder",
		// The same (+) tool the legacy plugin declares (#258) — one
		// schema, one id, two declarers until the legacy plugin dies.
		MenuEntries: []*cpv1.MenuEntry{{
			Id:          fslegacy.MenuEntrySearch,
			Label:       "search",
			Kind:        "well",
			ParamSchema: fslegacy.SearchParamSchema,
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
func (p *Provider) List(_ context.Context, req *cpv1.ListRequest) (*cpv1.ListResponse, error) {
	dir, err := p.abs(req.Context)
	if err != nil {
		return nil, err
	}
	entries, readErr := p.readDir(dir)
	if readErr != nil {
		if errors.Is(readErr, iofs.ErrNotExist) {
			return &cpv1.ListResponse{Authoritative: true, SourceLabel: dir}, nil
		}
		return nil, status.Errorf(codes.Unavailable, "fs provider: read %s: %v", dir, readErr)
	}
	resp := &cpv1.ListResponse{Authoritative: true, SourceLabel: dir}
	for _, e := range entries {
		key := e.Name
		if req.Context != "." && req.Context != "" {
			key = req.Context + "/" + e.Name
		}
		out := &cpv1.Entry{Key: key, Label: e.Name}
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

func (p *Provider) ReadContent(req *cpv1.ReadContentRequest, stream cpv1.ContentProvider_ReadContentServer) error {
	dir, name, err := p.keyDirName(req.Key)
	if err != nil {
		return err
	}
	if fi, statErr := os.Lstat(filepath.Join(dir, name)); statErr != nil || fi.IsDir() {
		// Directories and vanished files have no document body — an
		// empty chunk, never an error (the legacy ContentBody rule).
		return stream.Send(&cpv1.ContentChunk{})
	}
	data, mediaType := fsfile.Body(dir, name)
	return stream.Send(&cpv1.ContentChunk{Data: data, MediaType: mediaType})
}

// serveStream adapts the provider chunk stream to fsfile's sender (the
// two services' chunk shapes match field-for-field).
type serveStream struct {
	s cpv1.ContentProvider_ServeContentServer
}

func (w serveStream) Send(c *gridwellv1.ServeContentChunk) error {
	return w.s.Send(&cpv1.ServeContentChunk{Status: c.Status, MediaType: c.MediaType, Data: c.Data})
}

func (p *Provider) ServeContent(req *cpv1.ServeContentRequest, stream cpv1.ContentProvider_ServeContentServer) error {
	dir, name, err := p.keyDirName(req.Key)
	if err != nil {
		return err
	}
	if fi, statErr := os.Lstat(filepath.Join(dir, name)); statErr == nil && fi.IsDir() {
		return status.Error(codes.NotFound, "fs provider: directories serve no page")
	}
	return fsfile.ServeFile(serveStream{stream}, dir, name, req.Subpath)
}

func (p *Provider) GetPreview(_ context.Context, req *cpv1.GetPreviewRequest) (*cpv1.GetPreviewResponse, error) {
	dir, name, err := p.keyDirName(req.Key)
	if err != nil {
		return nil, err
	}
	return &cpv1.GetPreviewResponse{Jpeg: fsfile.PreviewJPEG(dir, name)}, nil
}

func (p *Provider) Probe(_ context.Context, req *cpv1.ProbeRequest) (*cpv1.ProbeResponse, error) {
	full, err := p.abs(req.Key)
	if err != nil {
		return nil, err
	}
	_, statErr := os.Lstat(full)
	switch {
	case statErr == nil:
		return &cpv1.ProbeResponse{Presence: cpv1.ProbeResponse_PRESENCE_PRESENT}, nil
	case os.IsNotExist(statErr):
		return &cpv1.ProbeResponse{Presence: cpv1.ProbeResponse_PRESENCE_GONE}, nil
	default:
		return &cpv1.ProbeResponse{Presence: cpv1.ProbeResponse_PRESENCE_UNSPECIFIED}, nil
	}
}

// Delete moves the source path to the trash (via Host). An already-gone
// path succeeds — the delete gesture is idempotent.
func (p *Provider) Delete(_ context.Context, req *cpv1.DeleteRequest) (*cpv1.DeleteResponse, error) {
	full, err := p.abs(req.Key)
	if err != nil {
		return nil, err
	}
	info, statErr := os.Lstat(full)
	if statErr != nil {
		return &cpv1.DeleteResponse{}, nil
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
	return &cpv1.DeleteResponse{}, nil
}
