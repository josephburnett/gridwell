package cli

// The per-home serve lock: one `gridwell serve` per Gridwell home, since two
// servers over one database would each cache and write independently and
// SQLite's WAL locking would not stop them. An exclusive flock on
// <home>/serve.lock dies with its holder, so there is no stale-pidfile
// protocol; its banner is what a second serve reports, so the desktop app
// joins the running server instead of starting one.

// errServeLockHeld carries the holder's banner, empty until the holder has
// written it. Both platform halves name it.
type errServeLockHeld struct {
	banner string
}

func (e *errServeLockHeld) Error() string {
	if e.banner == "" {
		return "another gridwell serve is starting up for this home"
	}
	return "another gridwell serve is already running for this home: " + e.banner
}
