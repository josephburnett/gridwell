package traceevent

import (
	"testing"

	"github.com/josephburnett/gridwell/client/errsurface"
)

// Every notice the strip shows is in the trace, under the source that raised
// it, so a dump answers "what did the user see" without a second list.
func TestNoticeCarriesItsSourceAndSeverity(t *testing.T) {
	e := Notice(errsurface.Error, "grid:g7abcde", "grid unavailable")
	if e.Src != "grid:g7abcde" || e.Kind != "notice" || e.KV["sev"] != "error" {
		t.Errorf("error notice is %+v", e)
	}
	if got := Notice(errsurface.Info, "textsave", "kept").KV["sev"]; got != "info" {
		t.Errorf("info notice severity is %q", got)
	}
}

// The console tags are written with brackets, which are punctuation for a
// console line and noise in a record's src field.
func TestLogSrcIsTheTagWithoutItsBrackets(t *testing.T) {
	if got := Log("[shellstream]", "opened").Src; got != "shellstream" {
		t.Errorf("log src is %q, want the bare tag", got)
	}
}

// A key with nothing in it is absent, so a reader grepping for an id never
// matches a record that names none.
func TestAnEmptyValueIsNoKey(t *testing.T) {
	if got := kv("pane", "p1", "tile", ""); len(got) != 1 || got["pane"] != "p1" {
		t.Errorf("kv is %v, want only the pane", got)
	}
	if got := kv("tile", ""); got != nil {
		t.Errorf("kv with nothing to say is %v, want nil", got)
	}
}
