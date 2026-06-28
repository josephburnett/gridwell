package server

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
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
		t.Error("localdb must be writable")
	}
}

func TestBuildPluginInfo_LabelFallsBackToDisplayName(t *testing.T) {
	got := buildPluginInfo("u", "fs", "", &pb.InfoResponse{DisplayName: "Files"})
	if got.Label != "Files" {
		t.Errorf("Label = %q, want Info DisplayName Files when no config label", got.Label)
	}
	if got.Writable {
		t.Error("fs must not be writable")
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
