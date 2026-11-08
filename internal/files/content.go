package files

// A file's bytes: a text file's body, read and written whole under its
// content stamp; a page file's page; an image's picture.

import (
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
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

// contentRefusal answers a content verb for a path it cannot read: one
// outside every root is a verdict, anything else a disk that cannot answer.
func contentRefusal(path string, err error) error {
	if errors.Is(err, errOutside) {
		return status.Errorf(codes.PermissionDenied, "files: %s %v", path, err)
	}
	return status.Errorf(codes.Unavailable, "files: read %s: %v", path, pathErr(err))
}

// ReadContent answers a text file's body with its stamp, taken before the
// read so bytes written during it read as a stamp they do not match. A url
// tile's content is its row's address, as home's is.
func (s *Source) ReadContent(_ context.Context, req *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error {
	t, err := s.tileByPath(req.TileId)
	if err != nil {
		return err
	}
	if rpc.WebContent(t) {
		return send(&pb.ContentChunk{Data: []byte(t.UrlString), MediaType: rpc.AddressMediaType, Version: t.Version})
	}
	path, _ := pathOf(req.TileId)
	real, err := s.content(path)
	var fi os.FileInfo
	if err == nil {
		fi, err = os.Stat(real)
	}
	if err != nil && !gone(err) {
		return contentRefusal(path, err)
	}
	if err != nil || fi.IsDir() {
		return send(&pb.ContentChunk{})
	}
	stamp := contentStamp(fi.ModTime(), fi.Size())
	data, media := body(real)
	return sendChunks(data, &pb.ContentChunk{MediaType: media, ContentStamp: stamp}, send)
}

// WriteContent replaces a text file's bytes whole, by a temp file in its
// directory renamed over it, so a reader sees the old bytes or the new and a
// broken stream writes nothing. It claims the stamp the bytes were read
// under, and a file whose stamp has moved is refused as a conflict. A url
// tile's address is its row's, which a page file refuses, and a link is
// refused, as a link row is at home.
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
	if rpc.WebContent(t) {
		id, err := s.mint(first.TileId)
		if err != nil {
			return nil, err
		}
		changed, err := s.mem.SetAddress(id, first.Version, string(data))
		return s.changedTile(path, changed, err, false)
	}
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
// answers the stamp of the bytes written.
func (s *Source) write(path, claimed string, data []byte) (string, error) {
	real, err := s.content(path)
	var fi os.FileInfo
	if err == nil {
		fi, err = os.Stat(real)
	}
	switch {
	case errors.Is(err, errOutside):
		return "", status.Errorf(codes.PermissionDenied, "files: %s %v, and nothing there is written", path, err)
	case gone(err):
		return "", gwerr.DeadLink("", "files: %s is gone", path)
	case err != nil:
		return "", status.Errorf(codes.Unavailable, "files: write %s: %v", path, pathErr(err))
	case fi.IsDir():
		return "", status.Errorf(codes.InvalidArgument, "files: %s is a directory", path)
	}
	if err := unwritable(filepath.Base(real), fi.Size()); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "files: %s %v", path, err)
	}
	if now := contentStamp(fi.ModTime(), fi.Size()); claimed != now {
		return "", status.Errorf(codes.FailedPrecondition, "files: %s changed on disk since it was read", path)
	}
	// The rename would replace a file its mode refuses writes to.
	if err := denied(real); err != nil {
		return "", writeRefusal(path, err)
	}
	return replace(real, fi.Mode().Perm(), data, path)
}

// replace writes data beside real and renames it over real.
func replace(real string, perm os.FileMode, data []byte, path string) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(real), "."+filepath.Base(real)+".gridwell-*")
	if err != nil {
		return "", writeRefusal(path, err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmp.Name())
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
		err = os.Chmod(tmp.Name(), perm)
	}
	var fi os.FileInfo
	if err == nil {
		fi, err = os.Stat(tmp.Name())
	}
	if err == nil {
		err = os.Rename(tmp.Name(), real)
	}
	if err != nil {
		return "", writeRefusal(path, err)
	}
	keep = true
	return contentStamp(fi.ModTime(), fi.Size()), nil
}

// writeRefusal answers a failed write: a permission refused is a verdict
// with its reason, anything else a disk that cannot answer right now.
func writeRefusal(path string, err error) error {
	if errors.Is(err, iofs.ErrPermission) || errors.Is(err, syscall.EROFS) {
		return status.Errorf(codes.PermissionDenied, "files: %s cannot be written: %v", path, pathErr(err))
	}
	return status.Errorf(codes.Unavailable, "files: write %s: %v", path, pathErr(err))
}

// ServeContent is a page file's whole bytes and media type, read and sent a
// chunk at a time through one buffer, so a video plays as it arrives and
// none is held whole. Only a page file has a page: any other file's bytes
// could be a page that runs a script, in the content API's sandbox, which
// allows scripts. A link serves where it lands, so it is a page file by its
// target's name.
func (s *Source) ServeContent(_ context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error {
	path, _, err := s.tilePath(req.TileId)
	if err != nil {
		return err
	}
	real, err := s.content(path)
	var f *os.File
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(real); err == nil && (fi.IsDir() || !isPage(real)) {
			return status.Errorf(codes.NotFound, "files: %s is not an image, video, audio or PDF file, which alone serve a page", path)
		}
	}
	if err == nil {
		f, err = os.Open(real)
	}
	switch {
	case gone(err):
		return gwerr.DeadLink("", "files: %s is gone", path)
	case err != nil:
		return contentRefusal(path, err)
	}
	defer f.Close()
	buf := make([]byte, rpc.ContentChunkBytes)
	for first := true; ; first = false {
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return status.Errorf(codes.Unavailable, "files: read %s: %v", path, pathErr(err))
		}
		if c := (&pb.ServeContentChunk{Data: buf[:n]}); n > 0 || first {
			if first {
				c.MediaType = mediaType(real)
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
	real, err := s.content(path)
	switch {
	case gone(err):
		return &pb.GetTilePreviewResponse{}, nil
	case err != nil:
		return nil, contentRefusal(path, err)
	}
	return &pb.GetTilePreviewResponse{Jpeg: previewJPEG(real)}, nil
}
