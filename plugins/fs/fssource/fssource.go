// Package fssource reads a host directory and projects its contents into the
// abstract entries an fs grid is reconciled against. It is pure Go, with no
// database and no Gridwell types; the fs plugin turns these entries into
// listing entries and the node mints the tile rows.
package fssource

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EntryKind classifies a directory entry: a subdirectory, which the fs plugin
// projects as a well, or a file, projected as a text tile. The string values
// are internal markers; callers compare by constant.
type EntryKind string

const (
	KindDir  EntryKind = "dir"
	KindFile EntryKind = "file"
)

// Entry is one synthesized item from a directory listing. Name is the
// basename within the parent directory; AbsPath is the resolved absolute
// path (parent + "/" + Name). Size and ModTime come straight from
// os.FileInfo. Symlinks are followed when they resolve; broken links
// surface as Kind=KindFile with IsBrokenSymlink=true and Size=0.
type Entry struct {
	Name            string
	AbsPath         string
	Kind            EntryKind
	Size            int64
	ModTime         time.Time
	IsSymlink       bool
	IsBrokenSymlink bool
}

// Read lists dir, sorts results alphabetically (so the auto-grid layout
// is deterministic), and returns Entry values. Hidden files (dotfiles)
// are included. Returns the underlying error if dir cannot be read.
func Read(dir string) ([]Entry, error) {
	dir = filepath.Clean(dir)
	f, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	defer f.Close()
	dirents, err := f.Readdir(-1)
	if err != nil {
		return nil, fmt.Errorf("readdir %s: %w", dir, err)
	}
	out := make([]Entry, 0, len(dirents))
	for _, info := range dirents {
		e := entryFromFileInfo(dir, info)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// entryFromFileInfo projects one os.FileInfo into an Entry. Symlinks
// are dereferenced via os.Stat (vs Lstat) — directory or file kind
// follows the target, and the IsSymlink flag is set either way.
func entryFromFileInfo(dir string, info os.FileInfo) Entry {
	name := info.Name()
	abs := filepath.Join(dir, name)
	mode := info.Mode()
	e := Entry{
		Name:    name,
		AbsPath: abs,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if mode&os.ModeSymlink != 0 {
		e.IsSymlink = true
		target, err := os.Stat(abs)
		if err != nil {
			e.IsBrokenSymlink = true
			e.Kind = KindFile
			e.Size = 0
			return e
		}
		if target.IsDir() {
			e.Kind = KindDir
		} else {
			e.Kind = KindFile
			e.Size = target.Size()
		}
		return e
	}
	if mode.IsDir() {
		e.Kind = KindDir
		return e
	}
	e.Kind = KindFile
	return e
}

// Stat builds an Entry for a single path: the single-item counterpart to Read,
// used to regenerate a file tile's metadata body lazily. Symlink and kind
// handling match Read exactly, so a path listed by Read and stat'd here
// project identically.
func Stat(path string) (Entry, error) {
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		return Entry{}, fmt.Errorf("lstat %s: %w", path, err)
	}
	return entryFromFileInfo(filepath.Dir(path), info), nil
}

// MetadataMarkdown returns a small markdown blob describing one Entry: the
// descent body for a file tile whose own bytes are not rendered. The output is
// deterministic, so the blob hash dedupes for unchanged files.
func MetadataMarkdown(e Entry) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(e.Name)
	b.WriteString("\n\n")
	if e.IsSymlink {
		if e.IsBrokenSymlink {
			b.WriteString("_broken symlink_\n\n")
		} else {
			b.WriteString("_symlink_\n\n")
		}
	}
	if e.Kind == KindDir {
		b.WriteString("directory\n\n")
	}
	fmt.Fprintf(&b, "- path: `%s`\n", e.AbsPath)
	fmt.Fprintf(&b, "- size: %d bytes\n", e.Size)
	fmt.Fprintf(&b, "- modified: %s\n", e.ModTime.UTC().Format(time.RFC3339))
	return b.String()
}
