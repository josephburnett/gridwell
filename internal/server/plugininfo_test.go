package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// buildPluginInfo is the pure assembly behind ListPlugins. These tests pin the
// fallback rules — especially the degraded case where a plugin's Info timed out
// or failed (info == nil), which must still produce a listed plugin so one slow
// plugin can't blank or freeze the launcher.

func TestBuildPluginInfo_InfoPresent(t *testing.T) {
	got := buildPluginInfo("uuid-1", "localdb", "Home", &pb.InfoResponse{
		RootGridId:    "7",
		ScratchGridId: "9",
		DisplayName:   "ignored-when-config-label-set",
		Writable:      true, // the handshake declares the capability
	})
	if got.RootGridId != "uuid-1/7" {
		t.Errorf("RootGridId = %q, want qualified uuid-1/7", got.RootGridId)
	}
	if got.ScratchGridId != "uuid-1/9" {
		t.Errorf("ScratchGridId = %q, want qualified uuid-1/9", got.ScratchGridId)
	}
	if got.Label != "Home" {
		t.Errorf("Label = %q, want the configured label Home", got.Label)
	}
	if !got.Writable {
		t.Error("a plugin whose Info declares writable must be writable")
	}
}

// The point of handshake-declared capabilities: a remote localdb reached
// through the ssh proxy has local kind "ssh", but its forwarded Info still
// says writable — it must be presented writable, not stranded read-only by a
// kind check.
func TestBuildPluginInfo_WritableFromHandshakeNotKind(t *testing.T) {
	got := buildPluginInfo("u", "ssh", "Remote", &pb.InfoResponse{
		RootGridId: "1",
		Writable:   true,
	})
	if !got.Writable {
		t.Error("an ssh-kind plugin whose Info declares writable must be writable")
	}
	// And the inverse: kind alone earns nothing.
	got = buildPluginInfo("u", "localdb", "Local", &pb.InfoResponse{RootGridId: "1"})
	if got.Writable {
		t.Error("writable must come from the Info handshake, not the kind string")
	}
}

func TestBuildPluginInfo_LabelFallsBackToDisplayName(t *testing.T) {
	got := buildPluginInfo("u", "fs", "", &pb.InfoResponse{DisplayName: "Files"})
	if got.Label != "Files" {
		t.Errorf("Label = %q, want Info DisplayName Files when no config label", got.Label)
	}
	if got.Writable {
		t.Error("fs (whose Info does not declare writable) must not be writable")
	}
}

// The degraded case: Info failed or timed out → info is nil. The plugin is still
// listed (so the launcher never drops a configured plugin), with no clickable
// root/scratch grid and the configured label.
func TestBuildPluginInfo_NilInfoStillListedWithConfigLabel(t *testing.T) {
	got := buildPluginInfo("u", "proc", "Processes", nil)
	if got.Label != "Processes" {
		t.Errorf("Label = %q, want the configured label even when Info failed", got.Label)
	}
	if got.RootGridId != "" || got.ScratchGridId != "" {
		t.Errorf("a failed Info must leave root/scratch empty, got root=%q scratch=%q",
			got.RootGridId, got.ScratchGridId)
	}
	if got.Kind != "proc" || got.Uuid != "u" {
		t.Errorf("identity must survive a failed Info: kind=%q uuid=%q", got.Kind, got.Uuid)
	}
}

func TestBuildPluginInfo_NilInfoNoLabelFallsBackToKind(t *testing.T) {
	got := buildPluginInfo("u", "ssh", "", nil)
	if got.Label != "ssh" {
		t.Errorf("Label = %q, want the kind when neither config nor Info supplies one", got.Label)
	}
}

func TestBuildPluginInfo_EmptyGridIdsNotQualified(t *testing.T) {
	// A plugin whose Info omits the grids (e.g. no ephemeral support) must not
	// emit a bare "uuid/" — empty stays empty.
	got := buildPluginInfo("u", "fs", "Files", &pb.InfoResponse{RootGridId: "3"})
	if got.RootGridId != "u/3" {
		t.Errorf("RootGridId = %q, want u/3", got.RootGridId)
	}
	if got.ScratchGridId != "" {
		t.Errorf("ScratchGridId = %q, want empty (no scratch grid)", got.ScratchGridId)
	}
}

// countingInfoPlugin is a minimal plugin that counts Info calls and can fail
// the first one — the seam for the Info cache tests.
type countingInfoPlugin struct {
	pb.UnimplementedGridwellServer
	calls     atomic.Int32
	failFirst bool
}

func (p *countingInfoPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	n := p.calls.Add(1)
	if p.failFirst && n == 1 {
		return nil, errors.New("transient info failure")
	}
	return &pb.InfoResponse{Kind: "test", DisplayName: "T", RootGridId: "1"}, nil
}

// TestListPluginsCachesInfo: repeat ListPlugins calls serve the handshake from
// the per-uuid cache — the plugin is asked exactly once. Before the cache,
// every palette open re-handshook every plugin (up to pluginInfoTimeout each
// for a slow remote).
func TestListPluginsCachesInfo(t *testing.T) {
	fake := &countingInfoPlugin{}
	client, closer, err := plugin.ServeInProcess(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("u-1", "test", client, nil)
	srv := New(reg, Config{})
	h := newConnectHandler(srv)

	for i := 0; i < 3; i++ {
		if _, err := h.ListPlugins(context.Background(), connect.NewRequest(&pb.ListPluginsRequest{})); err != nil {
			t.Fatalf("ListPlugins #%d: %v", i+1, err)
		}
	}
	if got := fake.calls.Load(); got != 1 {
		t.Errorf("Info called %d times across 3 ListPlugins, want 1 (cached)", got)
	}
}

// TestListPluginsRetriesFailedInfo: a failed handshake is NOT negatively
// cached — the next call retries and the plugin recovers its listing.
func TestListPluginsRetriesFailedInfo(t *testing.T) {
	fake := &countingInfoPlugin{failFirst: true}
	client, closer, err := plugin.ServeInProcess(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("u-1", "test", client, nil)
	srv := New(reg, Config{})
	h := newConnectHandler(srv)

	r1, err := h.ListPlugins(context.Background(), connect.NewRequest(&pb.ListPluginsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	// Degraded entry: listed, but no clickable root (info was nil).
	if got := r1.Msg.Plugins[0].RootGridId; got != "" {
		t.Errorf("failed Info should list a degraded entry, got root %q", got)
	}
	r2, err := h.ListPlugins(context.Background(), connect.NewRequest(&pb.ListPluginsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.Msg.Plugins[0].RootGridId; got != "u-1/1" {
		t.Errorf("retry after a failed Info should recover, got root %q, want u-1/1", got)
	}
	if got := fake.calls.Load(); got != 2 {
		t.Errorf("Info called %d times, want 2 (fail, then successful retry, then cache)", got)
	}
}
