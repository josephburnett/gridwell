package shellpaste

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

const (
	node   = "n1abcde"
	far    = node + "/geneva/r2abcde"
	origin = "https://rtb.example.ts.net:10010"
)

func file(ns, path string) *pb.Tile { return &pb.Tile{Id: rpc.QualifyID(ns, rpc.KeyTileID(path))} }

func TestText(t *testing.T) {
	for _, c := range []struct {
		name string
		rows []*pb.Tile
		want string
	}{
		{"a file is its path", []*pb.Tile{file(node, "/home/joe/notes.txt")}, "/home/joe/notes.txt"},
		{"a directory is its path", []*pb.Tile{{Id: rpc.QualifyID(node, rpc.KeyTileID("/home/joe")), Kind: rpc.KindDoor,
			ChildGridId: rpc.QualifyID(node, rpc.KeyTileID("/home/joe"))}}, "/home/joe"},
		{"a path with a space is quoted", []*pb.Tile{file(node, "/home/joe/my notes.md")}, "'/home/joe/my notes.md'"},
		{"a quote inside is closed and escaped", []*pb.Tile{file(node, "/tmp/it's")}, `'/tmp/it'\''s'`},
		{"a text is its grid with it the leaf", []*pb.Tile{{Id: node + "/12", GridId: node + "/3", Kind: rpc.KindText}},
			origin + "/" + node + "/3/12"},
		{"a door is its grid", []*pb.Tile{{Id: node + "/12", GridId: node + "/3", Kind: rpc.KindDoor, ChildGridId: node + "/40"}},
			origin + "/" + node + "/40"},
		{"a workspace is the window's place, quoted for its ?", []*pb.Tile{{Id: node + "/12", GridId: node + "/3", Kind: rpc.KindWorkspace}},
			"'" + origin + "/?w=" + "n1abcde%2F12" + "'"},
		{"several are space-separated, no newline", []*pb.Tile{file(node, "/a"), {Id: node + "/7", GridId: node + "/3", Kind: rpc.KindURL}},
			"/a " + origin + "/" + node + "/3/7"},
		{"a far node's tile is spelled under its own ids", []*pb.Tile{{Id: "r2abcde/9", GridId: "r2abcde/1", Kind: rpc.KindText}},
			origin + "/r2abcde/1/9"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Text(c.rows, origin); got != c.want {
				t.Errorf("Text = %q, want %q", got, c.want)
			}
		})
	}
}

// TestReferenceDecodes holds the reference to the client's own grammar: the
// place it decodes to names the tile it was spelled from.
func TestReferenceDecodes(t *testing.T) {
	text := &pb.Tile{Id: node + "/12", GridId: node + "/3", Kind: rpc.KindText}
	st, err := pane.DecodeURL(Reference(origin, text)[len(origin):])
	if err != nil {
		t.Fatal(err)
	}
	if st.Anchor != text.GridId || len(st.TileIDs) != 1 || rpc.QualifyID(rpc.NamespaceOf(st.Anchor), st.TileIDs[0]) != text.Id {
		t.Errorf("decoded %+v, want anchor %s and leaf %s", st, text.GridId, text.Id)
	}
}

func TestRefusal(t *testing.T) {
	shell := &pb.Tile{Id: node + "/5", Kind: rpc.KindShell}
	farShell := &pb.Tile{Id: far + "/5", Kind: rpc.KindShell}
	linkToFar := &pb.Tile{Id: node + "/6", Kind: rpc.KindShell, LinkTargetId: far + "/5"}
	for _, c := range []struct {
		name  string
		rows  []*pb.Tile
		shell *pb.Tile
		want  string
	}{
		{"home's tile onto home's shell", []*pb.Tile{{Id: node + "/12"}}, shell, ""},
		{"this node's file onto home's shell", []*pb.Tile{file(node, "/x")}, shell, ""},
		{"a far file onto home's shell", []*pb.Tile{{Id: far + "/~eA", AltText: "x.txt"}}, shell, "x.txt is not on the shell's node"},
		{"home's file onto a far shell", []*pb.Tile{file(node, "/x")}, farShell, rpc.QualifyID(node, rpc.KeyTileID("/x")) + " is not on the shell's node"},
		{"a far tile onto its own node's shell", []*pb.Tile{{Id: far + "/12"}, file(far, "/y")}, farShell, ""},
		{"a link reads its target's node", []*pb.Tile{{Id: far + "/12"}}, linkToFar, ""},
		{"one row off the node refuses the drop", []*pb.Tile{{Id: node + "/1"}, {Id: far + "/2", AltText: "Two"}}, shell, "Two is not on the shell's node"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Refusal(c.rows, c.shell); got != c.want {
				t.Errorf("Refusal = %q, want %q", got, c.want)
			}
		})
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/plain/path-1_2.txt": "/plain/path-1_2.txt",
		"":                    "''",
		"a b":                 "'a b'",
		"$HOME":               "'$HOME'",
		"x*":                  "'x*'",
		"http://h:1/a?w=1":    "'http://h:1/a?w=1'",
		"it's":                `'it'\''s'`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBindOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:8080":   "http://127.0.0.1:8080",
		":8080":            "http://localhost:8080",
		"0.0.0.0:10010":    "http://localhost:10010",
		"[::]:10010":       "http://localhost:10010",
		"100.64.1.2:10010": "http://100.64.1.2:10010",
		"nonsense":         "",
	} {
		if got := BindOrigin(in); got != want {
			t.Errorf("BindOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}
