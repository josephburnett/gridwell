package dial

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/internal/server"
)

// Whether the far node spoke crosses the connection API, on a unary call and
// a stream alike: an error it answered is heard whatever its code, and a far
// node gone is unheard.
func TestTheDialHearsWhatTheFarNodeAnswered(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "federation.sock")
	ln, err := server.ListenConnectionAPI(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.ConnectionAPIServer(connectionAPIHandler(t))
	go srv.Serve(ln)
	client, closer, err := Dial(Config{Addr: sock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	unary := func() error {
		_, err := client.GetTile(ctx, &pb.GetTileRequest{TileId: "ur1/999"})
		return err
	}
	stream := func() error {
		return client.ReadContent(ctx, &pb.ReadContentRequest{TileId: "ur1/999"}, func(*pb.ContentChunk) error { return nil })
	}
	for name, call := range map[string]func() error{"unary": unary, "stream": stream} {
		if err := call(); status.Code(err) != codes.NotFound || gwerr.IsUnheard(err) {
			t.Errorf("%s answered NotFound: err = %v, unheard = %v, want heard", name, err, gwerr.IsUnheard(err))
		}
	}
	_ = srv.Close()
	for name, call := range map[string]func() error{"unary": unary, "stream": stream} {
		if err := call(); !gwerr.IsUnheard(err) {
			t.Errorf("%s with the far node gone: err = %v, want unheard", name, err)
		}
	}
}
