package mcpapi

// Sessions: the screens showing this node and where each one is. A session
// answers only where the user is; what a grid holds and has selected is the
// grid's, read with read_grid.

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/josephburnett/gridwell/internal/subscription"
)

// ShellHeader carries the shell an agent runs in, the session key a shell
// tile's tmux session names in tmux.SessionEnv; empty from a plain terminal.
const ShellHeader = "X-Gridwell-Shell"

// Screens is what the node knows of the clients showing it.
type Screens interface {
	Sessions() []subscription.Session
	// ShellHolder is the session holding the home's shell key live now.
	ShellHolder(ctx context.Context, key string) (session string, ok bool, err error)
}

// SessionsOut answers sessions.
type SessionsOut struct {
	Sessions []Session `json:"sessions" jsonschema:"every screen showing this node, the most recently changed first"`
	Note     string    `json:"note,omitempty" jsonschema:"why no screen is marked yours though this agent named its shell"`
}

// Session is one screen showing this node.
type Session struct {
	ID             string   `json:"id"`
	Label          string   `json:"label" jsonschema:"the kind of client: desktop or browser"`
	Changed        string   `json:"changed" jsonschema:"when its view last changed, RFC 3339"`
	AwaySince      string   `json:"away_since,omitempty" jsonschema:"when its stream closed, RFC 3339, as a phone's does in the background; absent while it is open"`
	WorkspaceTiles []string `json:"workspace_tiles,omitempty" jsonschema:"the workspace tiles the window is inside, outermost first"`
	Panes          []Pane   `json:"panes"`
	Yours          bool     `json:"yours,omitempty" jsonschema:"this screen shows the shell this agent runs in, live"`
}

// Pane is one pane of a session's view.
type Pane struct {
	ID      string   `json:"id"`
	Anchor  string   `json:"anchor" jsonschema:"the grid the pane's place is rooted at"`
	Path    []string `json:"path,omitempty" jsonschema:"the door tiles from the anchor down to the pane's grid"`
	Grid    string   `json:"grid,omitempty" jsonschema:"the grid the pane shows, or that its content tile sits in"`
	Content string   `json:"content,omitempty" jsonschema:"the content tile the pane is inside, when it is inside one"`
	Focused bool     `json:"focused,omitempty"`
}

type sessionsIn struct{}

func (d *toolset) sessions(ctx context.Context, req *mcp.CallToolRequest, _ sessionsIn) (*mcp.CallToolResult, SessionsOut, error) {
	out, err := d.listSessions(ctx, shellOf(req))
	return nil, out, err
}

// listSessions is every session showing a screen, open or away
// (subscription.AwayFor), the most recently changed first, the one holding shell
// live marked yours. A view with no panes shows none: it is another node
// telling its share through a connection.
func (d *toolset) listSessions(ctx context.Context, shell string) (SessionsOut, error) {
	all := slices.DeleteFunc(d.screens.Sessions(), func(s subscription.Session) bool { return len(s.View.GetPanes()) == 0 })
	slices.SortStableFunc(all, func(a, b subscription.Session) int { return b.Changed.Compare(a.Changed) })
	out := SessionsOut{Sessions: make([]Session, 0, len(all))}
	for _, s := range all {
		out.Sessions = append(out.Sessions, sessionOf(s))
	}
	if shell == "" {
		return out, nil
	}
	held, ok, err := d.screens.ShellHolder(ctx, shell)
	if i := slices.IndexFunc(out.Sessions, func(s Session) bool { return s.ID == held }); ok && i >= 0 {
		out.Sessions[i].Yours = true
		return out, nil
	}
	if err != nil {
		out.Note = fmt.Sprintf("could not tell which screen holds shell %s: %v", shell, err)
	} else {
		out.Note = fmt.Sprintf("shell %s is live on no screen now", shell)
	}
	return out, nil
}

func sessionOf(s subscription.Session) Session {
	v := s.View
	out := Session{ID: s.ID, Label: v.GetLabel(), Changed: stamp(s.Changed), WorkspaceTiles: v.GetWorkspaceTiles(), Panes: []Pane{}}
	if !s.Open {
		out.AwaySince = stamp(s.Away)
	}
	for _, p := range v.GetPanes() {
		out.Panes = append(out.Panes, Pane{ID: p.GetId(), Anchor: p.GetAnchor(), Path: p.GetPath(),
			Grid: p.GetGridId(), Content: p.GetContentId(), Focused: p.GetFocused()})
	}
	return out
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func shellOf(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil || req.Extra.Header == nil {
		return ""
	}
	return req.Extra.Header.Get(ShellHeader)
}
