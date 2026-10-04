package trace

import (
	"errors"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/client/errsurface"
)

func TestDumpNoticeWhole(t *testing.T) {
	sev, msg := DumpNotice("/h/dumps/t.jsonl", nil, []HandOver{
		{Origin: tracewire.OriginClient},
		{Origin: tracewire.OriginElectron},
	})
	if sev != errsurface.Info || msg != "logs dumped to /h/dumps/t.jsonl" {
		t.Errorf("a whole dump = %v %q, want Info naming only the file", sev, msg)
	}
}

// A half whose pending records never reached the node is a gap in the file,
// and a dump that looks whole while missing one is the failure this names.
func TestDumpNoticeNamesAMissingHalf(t *testing.T) {
	sev, msg := DumpNotice("/h/dumps/t.jsonl", nil, []HandOver{
		{Origin: tracewire.OriginClient},
		{Origin: tracewire.OriginElectron, Lost: "connection refused"},
	})
	if sev != errsurface.Error {
		t.Errorf("severity = %v, want Error: the user asked for the whole trace", sev)
	}
	for _, want := range []string{"logs dumped to /h/dumps/t.jsonl ", "electron", "connection refused"} {
		if !strings.Contains(msg, want) {
			t.Errorf("notice %q does not say %q", msg, want)
		}
	}
}

func TestDumpNoticeNoFile(t *testing.T) {
	sev, msg := DumpNotice("", errors.New("503 Service Unavailable"), []HandOver{
		{Origin: tracewire.OriginElectron, Lost: "connection refused"},
	})
	if sev != errsurface.Error || !strings.Contains(msg, "could not be dumped: 503") {
		t.Errorf("a refused dump = %v %q", sev, msg)
	}
}
