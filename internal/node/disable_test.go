package node

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/trace"
)

// A connection the user disables stays off until the node restarts, and only
// in memory: the whole seam is the test, from the web door's verb through the
// registry's switch to the transport's dialer and back out the event stream,
// because the switch is registered where the node assembles itself.
func TestADisabledConnectionIsNeverDialedAgainAndReadsDark(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "server.yaml")
	cfg, err := BuildConfig(home, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	// A socket nobody listens on: the dial fails at once, every time.
	sock := filepath.Join(t.TempDir(), "nobody.sock")
	cfg.Connections = []config.ConnectionConfig{{Name: "away", Label: "away", Addr: sock}}
	yamlBefore, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	n.ServeBackground()
	t.Cleanup(func() { _ = n.Close() })

	ns := rpc.QualifyID(cfg.ID, "away")
	cl := webClient(t, "http://"+n.Ln.Addr().String(), cfg.WebPassword)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	health := healthOf(ctx, t, cl, ns)
	if h := <-health; h.Healthy || h.Disabled {
		t.Fatalf("first health of %s = %+v, want down and not disabled", ns, h)
	}

	mark := lastSeq()
	if err := cl.DisableSource(ctx, ns); err != nil {
		t.Fatal(err)
	}
	if h := <-health; !h.Disabled || h.Healthy || h.Detail != rpc.DisabledDetail {
		t.Fatalf("health after the verb = %+v, want disabled", h)
	}

	// Everything that used to dial: the menu (its rows learn the landing) and
	// a read through the name, a few times over.
	far := ns + "/rnode1/1"
	for range 3 {
		if _, err := cl.Handshake(ctx); err != nil {
			t.Fatal(err)
		}
		_, err := cl.GetGrid(ctx, far)
		if err == nil {
			t.Fatal("a disabled connection answered a read")
		}
		if connect.CodeOf(err) != connect.CodeUnavailable || gwerr.IsDeadRef(err) {
			t.Fatalf("read through a disabled connection = %v, want unavailable (dark), never dead", err)
		}
	}
	for _, rec := range trace.Default().Snapshot() {
		if rec.Seq > mark && rec.Src == "connection" && rec.Kind == "dial" && rec.KV["conn"] == "away" {
			t.Errorf("dialed after the disable: %s", rec.Msg)
		}
		if rec.Seq > mark && rec.Src == "store" && rec.Kind == "write" {
			t.Errorf("the disable wrote the store: %s %v", rec.Msg, rec.KV)
		}
	}

	// The far end comes back, and nothing knocks: no redial, no fan-in retry,
	// through two of the fan-in's backoff steps.
	ln, err := server.ListenConnectionDoor(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	knocked := make(chan struct{}, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
			knocked <- struct{}{}
		}
	}()
	select {
	case <-knocked:
		t.Fatal("the node dialed a disabled connection once its far end was back")
	case <-time.After(3500 * time.Millisecond):
	}
	if _, err := cl.GetGrid(ctx, far); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("read after the far end came back = %v, want still unavailable", err)
	}

	// A client that opens after the disable is told at once.
	late := healthOf(ctx, t, webClient(t, "http://"+n.Ln.Addr().String(), cfg.WebPassword), ns)
	if h := <-late; !h.Disabled {
		t.Fatalf("a late subscriber was told %+v, want disabled", h)
	}

	yamlAfter, err := os.ReadFile(cfgPath)
	if err != nil || !bytes.Equal(yamlBefore, yamlAfter) {
		t.Fatalf("server.yaml changed under a disable (%v)", err)
	}
}

// Home is not a source the user can switch off, and neither is a name this
// node does not declare.
func TestOnlyADeclaredSourceCanBeDisabled(t *testing.T) {
	home := t.TempDir()
	cfg, err := BuildConfig(home, filepath.Join(home, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Bind = "127.0.0.1:0"
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	n.ServeBackground()
	t.Cleanup(func() { _ = n.Close() })
	cl := webClient(t, "http://"+n.Ln.Addr().String(), cfg.WebPassword)
	for _, ns := range []string{cfg.ID, rpc.QualifyID(cfg.ID, "nobody"), "nosuchplugin"} {
		err := cl.DisableSource(context.Background(), ns)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("disable %q = %v, want refused with the reason", ns, err)
		}
	}
}

func webClient(t *testing.T, origin, password string) *rpc.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(origin)
	jar.SetCookies(u, []*http.Cookie{{Name: server.AuthCookieName, Value: server.AuthToken(password)}})
	return rpc.NewClient(&http.Client{Jar: jar}, origin, connect.WithProtoJSON())
}

// healthOf streams every health event about ns until ctx ends.
func healthOf(ctx context.Context, t *testing.T, cl *rpc.Client, ns string) <-chan *gridwellv1.EventPluginHealth {
	t.Helper()
	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan *gridwellv1.EventPluginHealth, 64)
	go func() {
		defer stream.Close()
		for {
			ev, ok, err := stream.Recv()
			if !ok || err != nil {
				return
			}
			if h := ev.GetPluginHealth(); h != nil && h.PluginUuid == ns {
				out <- h
			}
		}
	}()
	return out
}

func lastSeq() uint64 {
	var seq uint64
	for _, rec := range trace.Default().Snapshot() {
		seq = max(seq, rec.Seq)
	}
	return seq
}
