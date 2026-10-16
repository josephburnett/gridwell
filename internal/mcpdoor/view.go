package mcpdoor

import (
	"errors"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/deadref"
)

// Tile is a row as the door answers it. ID is the qualified address the
// agent names it back by.
type Tile struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Grid      string `json:"grid"`
	X         int64  `json:"x"`
	Y         int64  `json:"y"`
	W         int64  `json:"w"`
	H         int64  `json:"h"`
	Version   int64  `json:"version"`
	Link      string `json:"link,omitempty" jsonschema:"the address this link resolves to, when the tile is a link"`
	Dead      bool   `json:"dead,omitempty" jsonschema:"the link's path ends in nothing: greyed, inert, still deletable"`
	ChildGrid string `json:"child_grid,omitempty" jsonschema:"the grid a well holds"`
	ReadOnly  string `json:"read_only,omitempty" jsonschema:"why this tile's body takes no edits"`
}

// GridOut answers read_grid.
type GridOut struct {
	Grid  Grid   `json:"grid"`
	Tiles []Tile `json:"tiles"`
}

// Grid is a grid's own facts.
type Grid struct {
	ID           string `json:"id"`
	Writable     bool   `json:"writable" jsonschema:"its text bodies take edits"`
	AcceptsTiles bool   `json:"accepts_tiles" jsonschema:"it takes new tiles"`
	Source       string `json:"source,omitempty" jsonschema:"what its source calls what backs it"`
}

// TileOut answers read_tile and every write: the row, and on a read the
// content its kind has.
type TileOut struct {
	Tile         Tile    `json:"tile"`
	Body         *string `json:"body,omitempty"`
	BodyVersion  *int64  `json:"body_version,omitempty" jsonschema:"the version the body was read at, the claim write_text takes"`
	BodyStamp    string  `json:"body_stamp,omitempty" jsonschema:"a plugin's name for the body's bytes, its claim in place of a version"`
	URL          string  `json:"url,omitempty"`
	ShellSession string  `json:"shell_session,omitempty"`
}

// FindOut answers find. Skipped names every namespace that did not answer,
// so a short list never passes for a whole one.
type FindOut struct {
	Hits    []Hit  `json:"hits"`
	Skipped []Skip `json:"skipped,omitempty"`
}

// Hit is one match and the wells it sits inside, outermost first.
type Hit struct {
	Tile    Tile    `json:"tile"`
	Inside  []Crumb `json:"inside,omitempty"`
	Snippet string  `json:"snippet,omitempty"`
}

// Crumb is one well on a hit's path.
type Crumb struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Skip is a namespace a search went without, and why.
type Skip struct {
	Namespace string `json:"namespace"`
	Reason    string `json:"reason"`
}

// DeleteOut answers delete.
type DeleteOut struct {
	Deleted     string `json:"deleted"`
	SessionLeft string `json:"session_left,omitempty" jsonschema:"why a shell session the delete should have ended is still running"`
}

// tileOf is t as the door answers it. With the handshake roster, a link whose
// first hop this node does not declare reads dead, the verdict the client
// draws (deadref.DeadTile); a deeper break is the far node's to answer when
// the link is read.
func tileOf(t *pb.Tile, roster []*pb.PluginInfo, node string) Tile {
	out := Tile{ID: t.GetId(), Kind: t.GetKind(), Name: t.GetAltText(), Grid: t.GetGridId(),
		X: t.GetX(), Y: t.GetY(), W: t.GetW(), H: t.GetH(), Version: t.GetVersion(),
		Link: deadref.TargetID(t), ReadOnly: t.GetReadOnly()}
	if rpc.IsWellKind(t.GetKind()) && !t.GetReference() {
		out.ChildGrid = t.GetChildGridId()
	}
	out.Dead = roster != nil && deadref.DeadTile(t, roster, node, nil)
	return out
}

func gridOf(g *pb.Grid) Grid {
	return Grid{ID: g.GetId(), Writable: g.GetWritable(), AcceptsTiles: g.GetAcceptsTiles(), Source: g.GetSourceLabel()}
}

func dead(err error) bool { return gwerr.IsDeadRef(err) }

// refusal is a router answer as a tool error: what kind of no it is, then the
// router's own reason. The kinds are the verdicts the client tells apart
// (clientsync.Of), read here off the gRPC status the router answers.
func refusal(err error) error {
	st := status.Convert(err)
	why := st.Message()
	switch {
	case gwerr.IsSourceDisabled(err):
		return errors.New("disabled: " + why)
	case gwerr.IsDeadRef(err):
		return errors.New("dead link: " + why)
	case st.Code() == gcodes.FailedPrecondition:
		return errors.New("conflict: " + why + " (read it again)")
	case gwerr.IsTransport(err):
		return errors.New("unreachable: " + why)
	case st.Code() == gcodes.InvalidArgument:
		return errors.New("refused: " + why)
	case st.Code() == gcodes.NotFound:
		return errors.New("not found: " + why)
	case st.Code() == gcodes.Unimplemented:
		return errors.New("not supported here: " + why)
	}
	return errors.New(st.Code().String() + ": " + why)
}
