// Package namespace is the node's in-process interface. One namespace, home
// store, plugin adapter or connection, is a Go value the router calls
// directly, and the two codecs here carry the same method set onto the two
// wires that remain. The four gRPC stream shapes become Go control flow,
// decided once so no implementation invents its own:
//
//   - unary:            (ctx, *Request) (*Response, error)
//   - server-streaming: (ctx, *Request, send func(*Chunk) error) error
//   - client-streaming: (ctx, recv func() (*Msg, error)) (*Response, error)
//   - bidirectional:    (ctx, recv func() (*Msg, error), send func(*Msg) error) error
//
// recv ends with io.EOF, as a gRPC server stream's Recv does; a send returning
// an error aborts the call with it; the caller's ctx ends a stream nobody is
// reading.
//
// # Errors
//
// Errors are gRPC status errors, always. The client classifies by code, so the
// code must read the same from a Go call, the Connect codec, or two connection
// hops away.
//
// # Message ownership
//
// A caller and a Namespace share the proto messages they pass, which is why
// there is no copy layer:
//
//   - a Namespace must not retain or mutate a request after it returns; one
//     that rewrites ids clones first, as internal/connection does;
//   - a Namespace must not mutate a response after returning it;
//   - a caller must not mutate a response in place. The qualification layer
//     clones so two subscribers of one event never see each other's prefix.
package namespace

import (
	"context"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Namespace is the gridwell.v1 service's method set in Go.
type Namespace interface {
	// ── identity and capabilities ────────────────────────────────────────
	Info(ctx context.Context, req *pb.InfoRequest) (*pb.InfoResponse, error)
	Probe(ctx context.Context, req *pb.ProbeRequest) (*pb.ProbeResponse, error)
	Handshake(ctx context.Context, req *pb.HandshakeRequest) (*pb.HandshakeResponse, error)

	// ── reads ────────────────────────────────────────────────────────────
	GetGrid(ctx context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error)
	GetTile(ctx context.Context, req *pb.GetTileRequest) (*pb.TileResponse, error)
	GetTilePreview(ctx context.Context, req *pb.GetTilePreviewRequest) (*pb.GetTilePreviewResponse, error)
	Search(ctx context.Context, req *pb.SearchRequest) (*pb.SearchResponse, error)

	// ── writes ───────────────────────────────────────────────────────────
	CreateTile(ctx context.Context, req *pb.CreateTileRequest) (*pb.TileResponse, error)
	SetTile(ctx context.Context, req *pb.SetTileRequest) (*pb.TileResponse, error)
	PlaceTile(ctx context.Context, req *pb.PlaceTileRequest) (*pb.TileResponse, error)
	CloneTile(ctx context.Context, req *pb.CloneTileRequest) (*pb.TileResponse, error)
	DeleteTile(ctx context.Context, req *pb.DeleteTileRequest) (*pb.DeleteTileResponse, error)
	SetFraming(ctx context.Context, req *pb.SetFramingRequest) (*pb.SetFramingResponse, error)

	// ── content ──────────────────────────────────────────────────────────

	// ReadContent's first chunk carries media_type and the row version.
	ReadContent(ctx context.Context, req *pb.ReadContentRequest, send func(*pb.ContentChunk) error) error
	// ServeContent is the /content/ door; the first chunk carries status
	// and media_type.
	ServeContent(ctx context.Context, req *pb.ServeContentRequest, send func(*pb.ServeContentChunk) error) error
	// WriteContent commits once, at a clean io.EOF, so a recv that fails
	// leaves the old value byte-for-byte intact. The first message binds
	// tile_id and claims the version.
	WriteContent(ctx context.Context, recv func() (*pb.WriteContentRequest, error)) (*pb.TileResponse, error)

	// ── live ─────────────────────────────────────────────────────────────

	ShellSessionAlive(ctx context.Context, req *pb.ShellSessionAliveRequest) (*pb.ShellSessionAliveResponse, error)
	// OpenShell's first recv binds the tile id and the initial size.
	OpenShell(ctx context.Context, recv func() (*pb.OpenShellRequest, error), send func(*pb.OpenShellResponse) error) error
	// Subscribe streams this namespace's change events until ctx ends.
	Subscribe(ctx context.Context, req *pb.SubscribeRequest, send func(*pb.Event) error) error
}
