package localdb

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/source"
	"github.com/josephburnett/gridwell/internal/store"
)

func parseID(s string) int64 {
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

// makeDB builds a source Gridwell DB on disk with a text tile and a well
// containing a nested text tile, then closes it. Returns the file path.
func makeDB(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "source.db")
	st, err := store.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root, err := st.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# hello\nworld\n"),
	}); err != nil {
		t.Fatal(err)
	}
	well, err := st.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 1, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{WellIDs: []int64{well.ID}}, GridID: parseID(well.ChildGridID),
		X: 0, Y: 0, W: 1, H: 1, Data: []byte("# inside\n"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func attach(t *testing.T, s *Source, file string) source.Attachment {
	t.Helper()
	att, err := s.Attach(context.Background(), map[string]string{"db_file": file})
	if err != nil {
		t.Fatal(err)
	}
	return att
}

func TestAttachProjectsRoot(t *testing.T) {
	s := New()
	att := attach(t, s, makeDB(t))
	defer s.Detach(context.Background(), att.RootSourceID)

	if !att.HasSession {
		t.Error("a DB source must advertise a session")
	}
	if att.Network == nil || !att.Network.Direct {
		t.Errorf("local DB should declare direct network, got %+v", att.Network)
	}
	if att.Label != "source" {
		t.Errorf("label = %q, want source", att.Label)
	}
}

func TestListAndDescend(t *testing.T) {
	s := New()
	att := attach(t, s, makeDB(t))
	ctx := context.Background()
	defer s.Detach(ctx, att.RootSourceID)

	lst, err := s.List(ctx, att.RootSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !lst.Authoritative {
		t.Error("DB grid read must be authoritative")
	}

	var well, text *source.Node
	for i := range lst.Nodes {
		switch lst.Nodes[i].Kind {
		case source.KindWell:
			well = &lst.Nodes[i]
		case source.KindText:
			text = &lst.Nodes[i]
		}
	}
	if well == nil || text == nil {
		t.Fatalf("want a well and a text node, got %+v", lst.Nodes)
	}
	if !text.Caps.Write {
		t.Error("a regular text tile should be writable")
	}
	if text.Body == nil {
		t.Fatal("text node has no body ref")
	}

	// Read the text body through the projected blob ref.
	body, err := s.ReadBlob(ctx, att.RootSourceID, text.Body.BlobRef)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "# hello\nworld\n" {
		t.Errorf("body = %q", body)
	}

	// Descend into the well via its child cursor and find the nested text.
	child, err := s.List(ctx, well.Child)
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Nodes) != 1 || child.Nodes[0].Kind != source.KindText {
		t.Fatalf("descend = %+v, want one text node", child.Nodes)
	}
	inner, err := s.ReadBlob(ctx, well.Child, child.Nodes[0].Body.BlobRef)
	if err != nil {
		t.Fatal(err)
	}
	if string(inner) != "# inside\n" {
		t.Errorf("nested body = %q", inner)
	}
}

func TestWriteForwardsToUnderlyingTile(t *testing.T) {
	s := New()
	att := attach(t, s, makeDB(t))
	ctx := context.Background()
	defer s.Detach(ctx, att.RootSourceID)

	lst, _ := s.List(ctx, att.RootSourceID)
	var text source.Node
	for _, n := range lst.Nodes {
		if n.Kind == source.KindText {
			text = n
		}
	}

	updated, err := s.Write(ctx, att.RootSourceID, text.Key, text.Version, []byte("# edited\n"))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version == text.Version {
		t.Error("write should bump the version")
	}
	body, _ := s.ReadBlob(ctx, att.RootSourceID, updated.Body.BlobRef)
	if string(body) != "# edited\n" {
		t.Errorf("after write body = %q", body)
	}

	// A stale version must conflict.
	if _, err := s.Write(ctx, att.RootSourceID, text.Key, text.Version, []byte("x")); err == nil {
		t.Error("stale-version write should conflict")
	}
}

func TestDeleteForwards(t *testing.T) {
	s := New()
	att := attach(t, s, makeDB(t))
	ctx := context.Background()
	defer s.Detach(ctx, att.RootSourceID)

	lst, _ := s.List(ctx, att.RootSourceID)
	var text source.Node
	for _, n := range lst.Nodes {
		if n.Kind == source.KindText {
			text = n
		}
	}
	settled, err := s.Delete(ctx, att.RootSourceID, text.Key, text.Version)
	if err != nil || !settled {
		t.Fatalf("delete: settled=%v err=%v", settled, err)
	}
	after, _ := s.List(ctx, att.RootSourceID)
	for _, n := range after.Nodes {
		if n.Key == text.Key {
			t.Error("deleted tile still projected")
		}
	}
}

// TestSessionRoundTripPersistsInDB is the proof that session is stored in
// the DB file: write a session, detach (closing all handles), re-attach a
// fresh Source, and read it back.
func TestSessionRoundTripPersistsInDB(t *testing.T) {
	file := makeDB(t)
	ctx := context.Background()

	s1 := New()
	att1 := attach(t, s1, file)
	if got, _ := s1.GetSession(ctx, att1.RootSourceID); got != nil {
		t.Errorf("fresh DB session = %q, want empty", got)
	}
	cookies := []byte("cookie-jar-bytes-\x00\x01\x02")
	if err := s1.PutSession(ctx, att1.RootSourceID, cookies); err != nil {
		t.Fatal(err)
	}
	if err := s1.Detach(ctx, att1.RootSourceID); err != nil {
		t.Fatal(err)
	}

	// Fresh Source, same file: the session must survive in the DB.
	s2 := New()
	att2 := attach(t, s2, file)
	defer s2.Detach(ctx, att2.RootSourceID)
	got, err := s2.GetSession(ctx, att2.RootSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(cookies) {
		t.Errorf("session = %q, want %q", got, cookies)
	}
}

func TestUnattachedSourceIDFails(t *testing.T) {
	s := New()
	bogus := encodeCursor(cursor{Root: "/nope", Grid: 1})
	if _, err := s.List(context.Background(), bogus); err == nil {
		t.Error("listing an unattached cursor should fail")
	}
	if _, err := strconv.ParseInt("x", 10, 64); err == nil {
		t.Fatal("sanity")
	}
}
