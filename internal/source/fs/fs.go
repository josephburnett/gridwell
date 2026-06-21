// Package fs implements a Gridwell source over a host directory tree. It
// is the source-plugin form of the old fs-grid reconciler: List projects a
// directory's entries as well (subdirectory) and text (file metadata)
// nodes; Delete trashes the host artifact.
//
// SourceID is the absolute directory path — opaque to Gridwell, but
// globally unique and path-bearing, which is all the reconciler needs.
package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/josephburnett/gridwell/internal/fssource"
	"github.com/josephburnett/gridwell/internal/source"
)

// Host is the destructive side-effect surface, injected so tests never rm
// anything. Production wires a trashing host (recoverable); the default is
// a plain os remove.
type Host interface {
	Remove(path string) error    // a single file
	RemoveAll(path string) error // a directory and its contents
}

type osHost struct{}

func (osHost) Remove(path string) error    { return os.Remove(path) }
func (osHost) RemoveAll(path string) error { return os.RemoveAll(path) }

// Source projects a host directory tree.
type Source struct {
	host Host
}

// New returns an fs source. A nil host uses a plain os remover; production
// should pass a trashing host.
func New(host Host) *Source {
	if host == nil {
		host = osHost{}
	}
	return &Source{host: host}
}

var _ source.Source = (*Source)(nil)

func (s *Source) Info() source.Descriptor {
	return source.Descriptor{Kind: "fs", DisplayName: "files", SchemaVersion: 1}
}

// Attach turns {path} into a root directory node. The path is the
// SourceID; the label is its basename ("/" → "files").
func (s *Source) Attach(_ context.Context, config map[string]string) (source.Attachment, error) {
	path := filepath.Clean(config["path"])
	if path == "" || path == "." {
		return source.Attachment{}, fmt.Errorf("%w: fs requires a path", source.ErrNotFound)
	}
	return source.Attachment{
		RootSourceID: path,
		Label:        dirLabel(path),
		// The root well is just descendable; you don't trash a directory by
		// deleting the tile you dropped — that's a host tile removal.
		Caps:       source.Caps{},
		HasSession: false,
	}, nil
}

func (s *Source) Detach(context.Context, string) error { return nil }

// List reads the directory and projects each entry. Authoritative: a
// successful read is the complete set, so the host sweeps any tile whose
// key is absent. A directory that can't be read yields an empty,
// authoritative listing (the grid is simply empty until it returns) — the
// host never deletes a tile on a *failed* read, but an unreadable
// directory is "no children", matching the old reconciler.
func (s *Source) List(_ context.Context, sourceID string) (source.Listing, error) {
	entries, err := fssource.Read(sourceID)
	if err != nil {
		return source.Listing{Authoritative: true}, nil
	}
	nodes := make([]source.Node, 0, len(entries))
	for _, e := range entries {
		nodes = append(nodes, s.nodeForEntry(e))
	}
	return source.Listing{Nodes: nodes, Authoritative: true}, nil
}

func (s *Source) nodeForEntry(e fssource.Entry) source.Node {
	n := source.Node{
		Key:   e.Name,
		Label: e.Name,
		W:     1, H: 1,
		Caps: source.Caps{Delete: true},
	}
	if e.Kind == fssource.KindDir {
		n.Kind = source.KindWell
		n.Child = e.AbsPath
		return n
	}
	n.Kind = source.KindText
	body := fssource.MetadataMarkdown(e)
	n.Body = &source.ContentRef{
		BlobRef:   e.AbsPath,
		MediaType: "text/markdown",
		Hash:      hashHex([]byte(body)),
		Size:      int64(len(body)),
	}
	return n
}

// Probe stats join(dir, key). Present / definitively-gone / unknown — the
// host sweeps only on PresenceGone.
func (s *Source) Probe(_ context.Context, sourceID, key string) (source.Presence, error) {
	_, err := os.Lstat(filepath.Join(sourceID, key))
	switch {
	case err == nil:
		return source.PresencePresent, nil
	case os.IsNotExist(err):
		return source.PresenceGone, nil
	default:
		return source.PresenceUnknown, nil
	}
}

// ReadBlob regenerates a file tile's metadata body from its path
// (BlobRef). Lazy: List handed back only the hash; the bytes are produced
// here on first read.
func (s *Source) ReadBlob(_ context.Context, _ string, blobRef string) ([]byte, error) {
	e, err := fssource.Stat(blobRef)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", source.ErrNotFound, err)
	}
	return []byte(fssource.MetadataMarkdown(e)), nil
}

func (s *Source) GetSession(context.Context, string) ([]byte, error) {
	return nil, source.ErrUnsupported
}
func (s *Source) PutSession(context.Context, string, []byte) error {
	return source.ErrUnsupported
}

// Delete trashes the host artifact. join(dir, key) is the target; a
// directory is removed recursively. settled=true: the entry is gone now,
// so the host drops the tile row immediately.
func (s *Source) Delete(_ context.Context, sourceID, key string, _ int64) (bool, error) {
	path := filepath.Join(sourceID, key)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil // already gone
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		if err := s.host.RemoveAll(path); err != nil {
			return false, fmt.Errorf("remove %s: %w", path, err)
		}
	} else {
		if err := s.host.Remove(path); err != nil {
			return false, fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return true, nil
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

func dirLabel(path string) string {
	if path == "/" {
		return "files"
	}
	return filepath.Base(path)
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
