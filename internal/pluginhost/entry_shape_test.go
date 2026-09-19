package pluginhost_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// listingPlugin answers Info from one root context and List with whatever
// entries the test hands it, so a test can present any entry shape the wire
// permits — including one the node must refuse.
type listingPlugin struct {
	pluginv1.UnimplementedPluginServer
	entries []*pluginv1.Entry
}

func (listingPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "shapes", DisplayName: "shapes", RootContext: "r"}, nil
}

func (p listingPlugin) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Entries: p.entries, Authoritative: true}, nil
}

// listedBy stands the adapter up over a plugin listing entries and reads its
// root grid.
func listedBy(t *testing.T, entries ...*pluginv1.Entry) (*gridwellv1.Grid, []*gridwellv1.Tile, error) {
	t.Helper()
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(listingPlugin{entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a := pluginhost.New(cp, memStore.Namespace("p1"), nil)
	ctx := context.Background()
	info, err := a.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: plugintest.Landing(t, info)})
	if err != nil {
		return nil, nil, err
	}
	return resp.Grid, resp.Tiles, nil
}

// A url entry supplies the address it opens; a page has no address of its own
// and is served at the node's /content/ door. An entry declaring both is
// neither, and it fails in silence, because the client answers UrlString first
// and the page never serves. The node refuses the shape at the door, which is
// the only place that can close it: with the entry gone, no client can be
// handed the combination at all.
func TestAUrlEntryThatAlsoServesAPageIsRefused(t *testing.T) {
	_, _, err := listedBy(t, &pluginv1.Entry{
		Key: "both", Kind: "url", Label: "both", ServesPage: true,
		UrlString: "https://example.invalid/",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("GetGrid = %v, want InvalidArgument: a url entry that also serves a page is not a shape the node can present", err)
	}
	if !strings.Contains(err.Error(), "both") {
		t.Errorf("error = %v, want the offending entry key named so the plugin author can find it", err)
	}
}

// The two halves apart are ordinary and stay so: a url entry with an address,
// and a page entry of any other kind. Refusing the combination must not
// refuse either one.
func TestEitherHalfAloneIsListedNormally(t *testing.T) {
	_, tiles, err := listedBy(t,
		&pluginv1.Entry{Key: "u", Kind: "url", Label: "u", UrlString: "https://example.invalid/"},
		&pluginv1.Entry{Key: "p", Kind: "text", Label: "p", ServesPage: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(tiles) != 2 {
		t.Fatalf("listed %d tiles, want 2", len(tiles))
	}
	for _, tile := range tiles {
		switch tile.AltText {
		case "u":
			if tile.UrlString == "" || tile.ServesPage {
				t.Errorf("url tile = url %q servesPage %v", tile.UrlString, tile.ServesPage)
			}
		case "p":
			if !tile.ServesPage || tile.UrlString != "" {
				t.Errorf("page tile = url %q servesPage %v", tile.UrlString, tile.ServesPage)
			}
		}
	}
}

// "rendered" meant document only, with no way back to the source bytes, which
// the app never allows. The door maps it onto "both" so an old plugin binary
// keeps presenting and the client is left with two values to read.
func TestARetiredRenderedDeclarationArrivesAsBoth(t *testing.T) {
	_, tiles, err := listedBy(t, &pluginv1.Entry{
		Key: "note", Kind: "text", Label: "note", TextPresentation: "rendered",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tiles) != 1 {
		t.Fatalf("listed %d tiles, want 1", len(tiles))
	}
	if got := tiles[0].TextPresentation; got != rpc.TextPresentationBoth {
		t.Errorf("text_presentation = %q, want %q", got, rpc.TextPresentationBoth)
	}
}

// The vocabulary is closed, so a value outside it is a declaration the node
// cannot present. Reading it as undeclared would be the author's declaration
// silently ignored, so the door refuses it the way it refuses every other
// shape it cannot present.
func TestAnUnknownTextPresentationIsRefused(t *testing.T) {
	_, _, err := listedBy(t, &pluginv1.Entry{
		Key: "note", Kind: "text", Label: "note", TextPresentation: "fancy",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("GetGrid = %v, want InvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "note") {
		t.Errorf("error = %v, want the offending entry key named so the plugin author can find it", err)
	}
}

// The live vocabulary and the absent declaration pass through untouched.
func TestTheLiveTextPresentationsAreListedAsDeclared(t *testing.T) {
	_, tiles, err := listedBy(t,
		&pluginv1.Entry{Key: "p", Kind: "text", Label: "p", TextPresentation: rpc.TextPresentationPlain},
		&pluginv1.Entry{Key: "b", Kind: "text", Label: "b", TextPresentation: rpc.TextPresentationBoth},
		&pluginv1.Entry{Key: "u", Kind: "text", Label: "u"},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"p": rpc.TextPresentationPlain, "b": rpc.TextPresentationBoth, "u": ""}
	for _, tile := range tiles {
		if got := tile.TextPresentation; got != want[tile.AltText] {
			t.Errorf("%s: text_presentation = %q, want %q", tile.AltText, got, want[tile.AltText])
		}
	}
}
