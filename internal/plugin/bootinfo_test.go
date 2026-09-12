package plugin

// The launch gate's bound, from both sides, over the real plugin.v1 wire: a
// plugin whose Info never answers fails the gate when bootInfoWait elapses and
// not before, and one that answers passes it. LoadInto's only gate on a live
// plugin is this call, so waiting it out here is waiting out the boot.

import (
	"context"
	"testing"
	"time"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// hangingInfoPlugin is a plugin that came up and then never answers: a binary
// blocked on a config file, a mount, or a network its keys point at.
type hangingInfoPlugin struct {
	pluginv1.UnimplementedPluginServer
}

func (hangingInfoPlugin) Info(ctx context.Context, _ *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type answeringPlugin struct {
	pluginv1.UnimplementedPluginServer
}

func (answeringPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "fs", DisplayName: "files"}, nil
}

// loopbackPlugin is the shape the loader gates: the pluginhost adapter over a
// plugin.v1 client, here on an in-memory gRPC connection, so the deadline is
// enforced across the same wire a subprocess answers on.
func loopbackPlugin(t *testing.T, impl pluginv1.PluginServer) namespace.Namespace {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cp, closer, err := plugintest.Loopback(impl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	return pluginhost.New(cp, st.Namespace("p1abcde"), nil)
}

// shortBootInfoWait shrinks the launch gate for one test and restores it.
func shortBootInfoWait(t *testing.T) time.Duration {
	t.Helper()
	was := bootInfoWait
	t.Cleanup(func() { bootInfoWait = was })
	bootInfoWait = 200 * time.Millisecond
	return bootInfoWait
}

// A plugin whose Info hangs fails the launch at bootInfoWait: the boot neither
// waits on it forever nor gives up on a plugin that is merely slow.
func TestBootInfoFailsAtItsBound(t *testing.T) {
	wait := shortBootInfoWait(t)
	ns := loopbackPlugin(t, hangingInfoPlugin{})

	type outcome struct {
		took time.Duration
		err  error
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		err := bootInfo(ns)
		done <- outcome{time.Since(start), err}
	}()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(3 * wait / 2):
		t.Fatalf("the launch gate was still waiting after %v — Info must be bounded by bootInfoWait (%v)", 3*wait/2, wait)
	}
	if got.err == nil {
		t.Fatal("the gate passed a plugin that never answered Info")
	}
	if got.took < wait {
		t.Fatalf("the gate failed after %v, before bootInfoWait (%v) — a slow plugin gets its wait", got.took, wait)
	}
}

// The other side of the same bound: a plugin that answers passes the gate, so
// the wait is a ceiling on a hung binary and never a delay on a healthy one.
func TestBootInfoPassesAPluginThatAnswers(t *testing.T) {
	shortBootInfoWait(t)
	start := time.Now()
	if err := bootInfo(loopbackPlugin(t, answeringPlugin{})); err != nil {
		t.Fatalf("bootInfo: %v", err)
	}
	if took := time.Since(start); took >= bootInfoWait {
		t.Fatalf("the gate took %v against a plugin that answered at once", took)
	}
}
