package cli

// The per-directory serve lock: one `gridwell serve` per node directory, since two
// servers over one database would each cache and write independently and
// SQLite's WAL locking would not stop them. An exclusive flock on
// ~/.gridwell/serve.lock dies with its holder, so there is no stale-pidfile
// protocol; its banner is what a second serve reports, so the desktop app
// joins the running server instead of starting one.

// errServeLockHeld carries the holder's banner, empty until the holder has
// written it. Both platform halves name it.
type errServeLockHeld struct {
	banner string
}

func (e *errServeLockHeld) Error() string {
	if e.banner == "" {
		return "another gridwell serve is starting up for this node directory"
	}
	return "another gridwell serve is already running for this node directory: " + e.banner
}
