package trace

import (
	"strings"

	"github.com/josephburnett/gridwell/client/errsurface"
)

// HandOver is one origin's answer before a dump: Lost is why its pending
// records did not reach the node, empty when they did.
type HandOver struct {
	Origin string
	Lost   string
}

// DumpNotice is what a dump tells the user. The node writes only its own ring,
// so a half that could not hand over is missing from the file, and the notice
// says which and why rather than reading as a whole dump.
func DumpNotice(path string, err error, halves []HandOver) (errsurface.Severity, string) {
	if err != nil {
		return errsurface.Error, "logs could not be dumped: " + err.Error()
	}
	var lost []string
	for _, h := range halves {
		if h.Lost != "" {
			lost = append(lost, h.Origin+": "+h.Lost)
		}
	}
	msg := "logs dumped to " + path
	if len(lost) == 0 {
		return errsurface.Info, msg
	}
	return errsurface.Error, msg + " without the records still pending in " + strings.Join(lost, "; ")
}
