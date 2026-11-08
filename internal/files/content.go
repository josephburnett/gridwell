package files

// A file's bytes: a text file's body, read and written whole under its
// content stamp; a page file's page; an image's picture.

import (
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// sendChunks sends data in chunks, the first carrying first's facts.
func sendChunks(data []byte, first *pb.ContentChunk, send func(*pb.ContentChunk) error) error {
	c := first
	for {
		n := min(len(data), rpc.ContentChunkBytes)
		c.Data = data[:n]
		if err := send(c); err != nil || n == len(data) {
			return err
		}
		data, c = data[n:], &pb.ContentChunk{}
	}
}

// ReadContent answers a text file's body with its stamp, taken before the
// read so bytes written during it read as a stamp they do not match. A page
// file has no address, so its content is the empty address.
func (s *Source) ReadContent(_ context.Context, req *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	t, err := s.tileByPath(req.TileId)
	if err != nil {
		return err
	}
	if rpc.WebContent(t) {
		return send(&pb.ContentChunk{Data: []byte(t.UrlString), MediaType: rpc.AddressMediaType, Version: t.Version})
	}
	path, _ := pathOf(req.TileId)
	o, err := s.open(path)
	switch {
	case gone(err):
		return send(&pb.ContentChunk{})
	case err != nil:
		return readRefusal(path, err)
	}
	defer o.close()
	if !o.fi.Mode().IsRegular() {
		return send(&pb.ContentChunk{})
	}
	data, media := body(o.File, o.fi)
	return sendChunks(data, &pb.ContentChunk{MediaType: media, ContentStamp: contentStamp(o.fi.ModTime(), o.fi.Size())}, send)
}

// WriteContent replaces a text file's bytes whole, by a temp file in its
// directory renamed over it, so a reader sees the old bytes or the new and a
// broken stream writes nothing. It claims the stamp the bytes were read
// under, and a file whose stamp has moved is refused as a conflict. A page
// file has no body (unwritable), and a link is refused, as a link row is at
// home.
func (s *Source) WriteContent(_ context.Context, recv func() (*pb.WriteContentRequest, error)) (*pb.TileResponse, error) {
	first, err := recv()
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "files: write: empty stream")
	}
	t, err := s.tileByPath(first.TileId)
	if err != nil {
		return nil, err
	}
	if t.LinkTargetId != "" {
		return nil, status.Errorf(codes.InvalidArgument, "files: %s is a link, which owns no bytes; its target is written by its own id", t.AltText)
	}
	data, err := namespace.Assembled(first, recv)
	if err != nil {
		return nil, err
	}
	path, _ := pathOf(first.TileId)
	if len(data) > bodyCap {
		return nil, status.Errorf(codes.InvalidArgument, "files: %s: a body is at most %d MiB", path, bodyCap>>20)
	}
	stamp, err := s.write(path, first.ContentStamp, data)
	if err != nil {
		return nil, err
	}
	if t, err = s.tileByPath(first.TileId); err != nil {
		return nil, err
	}
	t.ContentStamp = stamp
	s.fresh(path, stamp)
	s.hub.Publish(&pb.Event{Payload: &pb.Event_TileChanged{
		TileChanged: &pb.TileChanged{Tile: proto.CloneOf(t), ContentChanged: true},
	}})
	return &pb.TileResponse{Tile: t}, nil
}

// write puts data in place of path's file if claimed is its stamp, and
// answers the stamp of the bytes written. The claim holds until the rename
// (lockPath), and the stamp is read again just before it, so neither another
// save nor another process's write is overwritten unseen.
func (s *Source) write(path, claimed string, data []byte) (string, error) {
	o, err := s.open(path)
	var fi os.FileInfo
	if err == nil {
		defer o.close()
		defer s.lockPath(o.Name())()
		fi, err = o.root.Stat(o.rel)
	}
	switch {
	case gone(err):
		return "", gwerr.DeadLink("", "files: %s is gone", path)
	case err != nil:
		return "", diskRefusal("written", path, err)
	case !fi.Mode().IsRegular():
		return "", status.Errorf(codes.InvalidArgument, "files: %s is not a regular file", path)
	}
	if err := unwritable(filepath.Base(o.Name()), fi.Size()); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "files: %s %v", path, err)
	}
	stale := func(fi os.FileInfo) error {
		if contentStamp(fi.ModTime(), fi.Size()) != claimed {
			return status.Errorf(codes.FailedPrecondition, "files: %s changed on disk since it was read", path)
		}
		return nil
	}
	if err := stale(fi); err != nil {
		return "", err
	}
	// The rename would replace a file its mode refuses writes to.
	if err := denied(o.Name()); err != nil {
		return "", diskRefusal("written", path, err)
	}
	return replace(o, fi.Mode().Perm(), data, path, stale)
}

// replace writes data beside o's file, through its root, and renames it over
// the file, unless stale refuses what the file is just before.
func replace(o *opened, perm os.FileMode, data []byte, path string, stale func(os.FileInfo) error) (string, error) {
	dir, base := filepath.Split(o.rel)
	name := filepath.Join(dir, "."+base+".gridwell-"+strconv.FormatUint(rand.Uint64(), 36))
	tmp, err := o.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", diskRefusal("written", path, err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = o.root.Remove(name)
		}
	}()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = o.root.Chmod(name, perm)
	}
	var fi, now os.FileInfo
	if err == nil {
		fi, err = o.root.Stat(name)
	}
	if err == nil {
		now, err = o.root.Stat(o.rel)
	}
	if err == nil {
		if err := stale(now); err != nil {
			return "", err
		}
		err = o.root.Rename(name, o.rel)
	}
	if err != nil {
		return "", diskRefusal("written", path, err)
	}
	keep = true
	return contentStamp(fi.ModTime(), fi.Size()), nil
}

// readRefusal answers a read the disk refused on path: one gone, or landing
// outside every root, is dead, as a link out of the tree is.
func readRefusal(path string, err error) error {
	switch {
	case gone(err):
		return gwerr.DeadLink("", "files: %s is gone", path)
	case errors.Is(err, errOutside):
		return gwerr.DeadLink("", "files: %s %v", path, err)
	}
	return diskRefusal("read", path, err)
}

// diskRefusal answers a content verb the disk refused on path: a path
// outside every root or a permission refused is a verdict with its reason,
// anything else a disk that cannot answer right now.
func diskRefusal(verb, path string, err error) error {
	switch {
	case errors.Is(err, errOutside):
		return status.Errorf(codes.PermissionDenied, "files: %s %v", path, err)
	case errors.Is(err, iofs.ErrPermission) || errors.Is(err, syscall.EROFS):
		return status.Errorf(codes.PermissionDenied, "files: %s cannot be %s: %v", path, verb, pathErr(err))
	}
	return status.Errorf(codes.Unavailable, "files: %s cannot be %s right now: %v", path, verb, pathErr(err))
}

// ServeContent is a page file's bytes, the range asked (rpc.PageRange), with
// its media type and size, read and sent a chunk at a time through one
// buffer, so a video plays and seeks as it arrives and none is held whole. A
// range past the end sends the first chunk alone. Only a page file has a page: any other file's bytes
// could be a page that runs a script, in the content API's sandbox, which
// allows scripts. A link serves where it lands, so it is a page file by its
// target's name.
func (s *Source) ServeContent(_ context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	path, _, err := s.tilePath(req.TileId)
	if err != nil {
		return err
	}
	f, err := s.open(path)
	if err != nil {
		return readRefusal(path, err)
	}
	defer f.close()
	if !f.fi.Mode().IsRegular() || !isPage(f.Name()) {
		return status.Errorf(codes.NotFound, "files: %s is not an image, video, audio or PDF file, which alone serve a page", path)
	}
	size := f.fi.Size()
	from, left, _ := rpc.PageRange(req.Offset, req.Length, size)
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return diskRefusal("read", path, err)
	}
	r := io.LimitReader(f, left)
	buf := make([]byte, rpc.ContentChunkBytes)
	for first := true; ; first = false {
		n, err := io.ReadFull(r, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return diskRefusal("read", path, err)
		}
		if c := (&pb.ServeContentChunk{Data: buf[:n]}); n > 0 || first {
			if first {
				c.MediaType, c.Size = mediaType(f.Name()), &size
			}
			if err := send(c); err != nil {
				return err
			}
		}
		if err != nil {
			return nil
		}
	}
}

// GetTilePreview answers the picture previewKey named: the row's screenshot
// once there is one, else an image's own.
func (s *Source) GetTilePreview(_ context.Context, req *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error) {
	path, dir, err := s.tilePath(req.TileId)
	if err != nil {
		return nil, err
	}
	id, ok, err := s.row(dir, path)
	if err != nil {
		return nil, err
	}
	if ok {
		jpeg, err := s.mem.Preview(id)
		if err != nil {
			return nil, gwerr.ToStatus(err)
		}
		if len(jpeg) > 0 {
			return &pb.GetTilePreviewResponse{Jpeg: jpeg}, nil
		}
	}
	f, err := s.open(path)
	if err != nil {
		return nil, readRefusal(path, err)
	}
	defer f.close()
	return &pb.GetTilePreviewResponse{Jpeg: previewJPEG(f.File, f.fi)}, nil
}
