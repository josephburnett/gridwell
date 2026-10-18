package mcpdoor

// Pointing: how an agent learns what the user means by "here" and "these".
// The node holds each client session's view, open or away (interest.Book); a
// session is chosen, its focused pane's grid read, and that grid row's set is
// what the user has in hand.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/interest"
)

// ShellHeader carries the shell an agent runs in, the session key a shell
// tile's tmux session names in tmux.SessionEnv; empty from a plain terminal.
const ShellHeader = "X-Gridwell-Shell"

// Screens is what the node knows of the clients showing it.
type Screens interface {
	Sessions() []interest.Session
	// ShellHolder is the session holding the home's shell key live now.
	ShellHolder(ctx context.Context, key string) (session string, ok bool, err error)
}

// Session is one screen showing this node.
type Session struct {
	ID        string   `json:"id"`
	Label     string   `json:"label" jsonschema:"the kind of client: desktop or browser"`
	Changed   string   `json:"changed" jsonschema:"when its view last changed, RFC 3339"`
	AwaySince string   `json:"away_since,omitempty" jsonschema:"when its stream closed, RFC 3339, as a phone's does in the background; absent while it is open"`
	PaneTiles []string `json:"pane_tiles,omitempty" jsonschema:"the pane tiles the window is inside, outermost first"`
	Panes     []Pane   `json:"panes"`
}

// Pane is one pane of a session's view.
type Pane struct {
	ID      string   `json:"id"`
	Anchor  string   `json:"anchor" jsonschema:"the grid the pane's place is rooted at"`
	Path    []string `json:"path,omitempty" jsonschema:"the doorway tiles from the anchor down to the pane's grid"`
	Content string   `json:"content,omitempty" jsonschema:"the content tile the pane is inside, when it is inside one"`
	Grid    string   `json:"grid,omitempty" jsonschema:"the grid the pane shows, or that its content tile sits in"`
	Focused bool     `json:"focused,omitempty"`
	// PaneTile is the innermost pane tile of the session's level: every pane
	// of the view is one of its layout's.
	PaneTile string `json:"pane_tile,omitempty" jsonschema:"the pane tile whose layout this pane is part of, when the window is inside one"`
}

// PointedOut answers pointed.
type PointedOut struct {
	Session  string `json:"session"`
	Label    string `json:"label"`
	ChosenBy string `json:"chosen_by" jsonschema:"argument, your shell, or most recent"`
	Changed  string `json:"changed" jsonschema:"when its view last changed, RFC 3339"`
	// AwaySince is as Session.AwaySince.
	AwaySince string `json:"away_since,omitempty" jsonschema:"when its stream closed, RFC 3339; absent while it is open"`
	Note      string `json:"note,omitempty" jsonschema:"why the session was chosen, when it was not the one asked for"`
	// Panes is every pane of the chosen session, the focused one among them.
	Panes     []Pane   `json:"panes"`
	PaneTiles []string `json:"pane_tiles,omitempty" jsonschema:"the pane tiles the window is inside, outermost first"`
	// Others is every other screen showing the node, so the agent can ask
	// which the user meant.
	Others   []Session `json:"others,omitempty" jsonschema:"every other screen showing the node, the most recently changed first"`
	Pane     string    `json:"pane"`
	Content  string    `json:"content,omitempty" jsonschema:"the content tile the focused pane is inside, when it is inside one"`
	PaneTile string    `json:"pane_tile,omitempty" jsonschema:"the pane tile whose layout holds the focused pane, when it is inside one"`
	Grid     Grid      `json:"grid"`
	Points   string    `json:"points_at" jsonschema:"selection when the pane shows one, else grid"`
	Tiles    []Tile    `json:"tiles,omitempty" jsonschema:"the selected tiles, as read_grid answers them"`
}

// Choice spellings.
const (
	byArgument   = "argument"
	byShell      = "your shell"
	byMostRecent = "most recent"
)

// screensNow is every session showing a screen, the most recently changed first.
// A view with no panes shows none: it is another node telling its share
// through a connection.
func (d *door) screensNow() []interest.Session {
	all := slices.DeleteFunc(d.screens.Sessions(), func(s interest.Session) bool { return len(s.View.GetPanes()) == 0 })
	slices.SortStableFunc(all, func(a, b interest.Session) int { return b.Changed.Compare(a.Changed) })
	return all
}

func sessionOf(s interest.Session) Session {
	v := s.View
	out := Session{ID: s.ID, Label: v.GetLabel(), Changed: stamp(s.Changed), PaneTiles: v.GetPaneTiles(), Panes: []Pane{}}
	if !s.Open {
		out.AwaySince = stamp(s.Away)
	}
	for _, p := range v.GetPanes() {
		out.Panes = append(out.Panes, Pane{ID: p.GetId(), Anchor: p.GetAnchor(), Path: p.GetPath(),
			Content: p.GetContentId(), Grid: p.GetGridId(), Focused: p.GetFocused(), PaneTile: innermost(v)})
	}
	return out
}

// innermost is the pane tile whose layout the view's panes are, "" outside
// every pane tile.
func innermost(v *pb.SetViewRequest) string {
	if t := v.GetPaneTiles(); len(t) > 0 {
		return t[len(t)-1]
	}
	return ""
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

type pointedIn struct {
	Session string `json:"session,omitempty" jsonschema:"a session id from an earlier answer's others; empty chooses by the shell this agent runs in, then the most recent"`
}

func (d *door) pointed(ctx context.Context, req *mcp.CallToolRequest, in pointedIn) (*mcp.CallToolResult, PointedOut, error) {
	all := d.screensNow()
	if len(all) == 0 {
		return nil, PointedOut{}, errors.New("no session: no screen is showing this node now or was in the last " + interest.AwayFor.String() + ", so nothing is pointed at; open Gridwell and ask again")
	}
	s, by, note, err := d.choose(ctx, all, in.Session, shellOf(req))
	if err != nil {
		return nil, PointedOut{}, err
	}
	chosen := sessionOf(s)
	out := PointedOut{Session: s.ID, Label: chosen.Label, ChosenBy: by, Changed: chosen.Changed, AwaySince: chosen.AwaySince,
		Note: note, Panes: chosen.Panes, PaneTiles: chosen.PaneTiles}
	for _, o := range all {
		if o.ID != s.ID {
			out.Others = append(out.Others, sessionOf(o))
		}
	}
	i := slices.IndexFunc(s.View.GetPanes(), (*pb.ViewPane).GetFocused)
	if i < 0 {
		return nil, PointedOut{}, fmt.Errorf("not loaded: session %s has no focused pane yet; ask again", s.ID)
	}
	p := s.View.GetPanes()[i]
	out.Pane, out.Content = p.GetId(), p.GetContentId()
	if p.GetGridId() == "" {
		return nil, PointedOut{}, fmt.Errorf("not loaded: session %s's focused pane has not loaded its grid yet; ask again", s.ID)
	}
	g, err := d.grid(ctx, p.GetGridId())
	if err != nil {
		return nil, PointedOut{}, err
	}
	out.PaneTile = innermost(s.View)
	// A pane inside a content tile shows no selection.
	var sel []string
	if p.GetContentId() == "" {
		sel = g.Grid.Selection
	}
	out.Grid, out.Points = g.Grid, "grid"
	if len(sel) > 0 {
		out.Points = "selection"
		for _, t := range g.Tiles {
			if slices.Contains(sel, t.ID) {
				out.Tiles = append(out.Tiles, t)
			}
		}
	}
	return nil, out, nil
}

// choose is the pointing rule: the session named, open or away; else, among
// the open ones, the one holding the agent's shell live, else the one whose
// view changed last; else, none being open, the one that went away last.
func (d *door) choose(ctx context.Context, all []interest.Session, named, shell string) (interest.Session, string, string, error) {
	if named != "" {
		if i := slices.IndexFunc(all, func(s interest.Session) bool { return s.ID == named }); i >= 0 {
			return all[i], byArgument, "", nil
		}
		return interest.Session{}, "", "", fmt.Errorf("not found: no session %s; pointed with no session names them all", named)
	}
	open := slices.DeleteFunc(slices.Clone(all), func(s interest.Session) bool { return !s.Open })
	if len(open) == 0 {
		last := slices.MaxFunc(all, func(a, b interest.Session) int { return a.Away.Compare(b.Away) })
		return last, byMostRecent, fmt.Sprintf("no screen is open; chose the one that went away last, away since %s", stamp(last.Away)), nil
	}
	all = open
	if shell == "" {
		return all[0], byMostRecent, "", nil
	}
	held, ok, err := d.screens.ShellHolder(ctx, shell)
	if err != nil {
		return all[0], byMostRecent, fmt.Sprintf("could not tell which screen holds shell %s (%v); chose the session whose view changed last", shell, err), nil
	}
	if ok {
		if i := slices.IndexFunc(all, func(s interest.Session) bool { return s.ID == held }); i >= 0 {
			return all[i], byShell, "", nil
		}
	}
	return all[0], byMostRecent, fmt.Sprintf("shell %s is live on no screen now; chose the session whose view changed last", shell), nil
}

func shellOf(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil || req.Extra.Header == nil {
		return ""
	}
	return req.Extra.Header.Get(ShellHeader)
}
