package server

import (
	"cmp"
	"context"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/shellpaste"
)

// PasteShell routes a drop onto a shell to the node owning its tmux, which
// spells what it types (shellpaste.Text) and hands the text to its home. A
// far node is sent no origin, so it spells under its own web address, the one
// a shell on its machine reaches.
func (rt *router) PasteShell(ctx context.Context, req *pb.PasteShellRequest) (*pb.PasteShellResponse, error) {
	switch {
	case req.Text != "":
		return nil, status.Error(gcodes.InvalidArgument, "a paste names its tiles, and the node owning the shell spells the text")
	case len(req.TileIds) == 0:
		return nil, status.Error(gcodes.InvalidArgument, "a paste names at least one tile")
	case rt.srv.cfg.DisableShells:
		return nil, status.Error(gcodes.PermissionDenied, "shell tiles are disabled on this node (server.yaml disable_shells)")
	}
	c, local, _, transit, err := rt.route(req.TileId)
	if err != nil {
		return nil, err
	}
	if err := rt.refuseDisabled(req.TileId); err != nil {
		return nil, err
	}
	if transit {
		far := proto.CloneOf(req)
		far.Origin = ""
		return c.PasteShell(ctx, rpc.PeelRequest(rt.hop(req.TileId, true), far))
	}
	text, err := rt.pasteText(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.PasteShell(ctx, &pb.PasteShellRequest{TileId: local, Text: text})
}

// pasteText reads the rows a paste names and spells them, refusing a row of
// another node before reading any.
func (rt *router) pasteText(ctx context.Context, req *pb.PasteShellRequest) (string, error) {
	rows := make([]*pb.Tile, len(req.TileIds))
	for i, id := range req.TileIds {
		rows[i] = &pb.Tile{Id: id}
	}
	if why := shellpaste.Refusal(rows, &pb.Tile{Id: req.TileId}); why != "" {
		return "", status.Error(gcodes.InvalidArgument, why)
	}
	origin := cmp.Or(req.Origin, shellpaste.BindOrigin(rt.srv.cfg.WebAddr))
	for i, id := range req.TileIds {
		resp, err := rt.GetTile(ctx, &pb.GetTileRequest{TileId: id})
		if err != nil {
			return "", err
		}
		rows[i] = resp.GetTile()
		if _, file := rpc.FilePath(id); !file && origin == "" {
			return "", status.Error(gcodes.InvalidArgument, "this node knows no web address to spell a reference under")
		}
	}
	return shellpaste.Text(rows, origin), nil
}
