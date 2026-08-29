package remote

// Connections are server CONFIG (v2 #269, reversing #199 — owner
// decision 2026-08-22): server.yaml's `connections:` list is the one
// owner of the connection SET and each connection's params; the
// transport's DB is the node-side MATERIALIZATION — namespaces, framing,
// the picker's rows — reconciled here at every boot. A `connections:`
// key present (even empty) puts the transport in CONFIG MODE: rows
// absent from the list tombstone, and every mutating verb that used to
// edit connections (the picker's create, a params commit, delete,
// rename) refuses with a pointer at the file.
//
// Name immutability: a config name IS the namespace segment inside
// stored references. A retired name (retired_names, or a tombstoned row)
// never returns — reusing one is refused at boot, loudly, before the
// node serves anything.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/idshape"
)

// ConnSpec is one declared connection, in the transport's own vocabulary
// (mirrors config.ConnectionConfig without importing the host's config
// package).
type ConnSpec struct {
	Name       string
	Label      string
	Host       string
	User       string
	Port       int64
	Addr       string
	Key        string
	KnownHosts string
}

// paramsDoc renders the spec as the SAME params document the picker
// committed (#198 form fields), so a materialized row canonicalizes
// equal to the picker-created row it replaces.
func (c ConnSpec) paramsDoc() (string, error) {
	m := map[string]any{}
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("host", c.Host)
	set("user", c.User)
	set("addr", c.Addr)
	set("key", c.Key)
	set("known_hosts", c.KnownHosts)
	if c.Port != 0 {
		m["port"] = c.Port
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if _, err := ParseParams(b); err != nil {
		return "", err
	}
	return string(b), nil
}

// SyncConfig reconciles the connection store against the declared set.
// Returns the materialized live NS list (config order).
func SyncConfig(ctx context.Context, db *DB, conns []ConnSpec, retired []string) ([]string, error) {
	seen := map[string]bool{}
	retiredSet := map[string]bool{}
	for _, r := range retired {
		retiredSet[r] = true
	}
	for _, c := range conns {
		if c.Name == "" {
			return nil, fmt.Errorf("connection with empty name")
		}
		if err := idshape.ValidateSegment("connection name", c.Name); err != nil {
			return nil, fmt.Errorf("connection %q: invalid name (it is a namespace segment): %w", c.Name, err)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("connection %q declared twice", c.Name)
		}
		if retiredSet[c.Name] {
			return nil, fmt.Errorf("connection %q: this name is RETIRED — a retired name never returns; mint a new one", c.Name)
		}
		seen[c.Name] = true
	}

	rows, err := db.List(ctx)
	if err != nil {
		return nil, err
	}
	byNS := map[string]*Conn{}
	for _, r := range rows {
		byNS[r.NS] = r
	}

	var live []string
	for _, c := range conns {
		doc, err := c.paramsDoc()
		if err != nil {
			return nil, fmt.Errorf("connection %q: %w", c.Name, err)
		}
		// A declared label is the USER speaking (yaml is theirs): apply
		// it through the rename latch, or connTile's auto-label override
		// displays joe@host instead. No label = the auto-label rules.
		label := c.Label
		latch := label != ""
		if label == "" {
			label = autoLabel(doc)
		}
		row := byNS[c.Name]
		if row == nil {
			// A name may exist only as a TOMBSTONED row (deleted through
			// the old picker): that name never returns.
			if t, terr := db.GetByNS(ctx, c.Name); terr == nil && t.Deleted {
				return nil, fmt.Errorf("connection %q: this name is RETIRED in the connection store — a retired name never returns; mint a new one", c.Name)
			}
			created, cerr := db.CreateWithNS(ctx, c.Name, label)
			if cerr != nil {
				return nil, fmt.Errorf("connection %q: %w", c.Name, cerr)
			}
			row = created
		}
		wantCanon, err := CanonicalParams([]byte(doc))
		if err != nil {
			return nil, err
		}
		haveCanon := ""
		if row.Params != "" {
			if haveCanon, err = CanonicalParams([]byte(row.Params)); err != nil {
				haveCanon = "" // an unparsable stored doc is replaced
			}
		}
		if haveCanon != wantCanon {
			if row, err = db.SetParams(ctx, row.ID, row.Version, doc); err != nil {
				return nil, fmt.Errorf("connection %q: params: %w", c.Name, err)
			}
		}
		if row.AltText != label || (latch && !row.AltUser) {
			if latch {
				if _, err = db.Rename(ctx, row.ID, row.Version, label); err != nil {
					return nil, fmt.Errorf("connection %q: label: %w", c.Name, err)
				}
			} else if row.AltText != label {
				// The auto-label path never latches: SetAlt keeps
				// alt_user as it is (0 here — the row is config-made).
				if _, err = db.SetAlt(ctx, row.ID, label); err != nil {
					return nil, fmt.Errorf("connection %q: label: %w", c.Name, err)
				}
			}
		}
		live = append(live, c.Name)
	}

	// Rows the config no longer declares tombstone (their namespaces stay
	// reserved forever — the row is the reservation).
	for _, r := range rows {
		if r.Deleted || seen[r.NS] {
			continue
		}
		if err := db.Tombstone(ctx, r.ID, r.Version); err != nil {
			return nil, fmt.Errorf("retire connection %q: %w", r.NS, err)
		}
	}
	// retired_names without rows become reservations so the name can
	// never mint (a fresh materialization on an empty DB still honors an
	// old home's graveyard).
	for _, name := range retired {
		if _, err := db.GetByNS(ctx, name); err == nil {
			continue // a row (live is refused above; tombstoned is already the reservation)
		}
		c, err := db.CreateWithNS(ctx, name, "")
		if err != nil {
			return nil, fmt.Errorf("reserve retired name %q: %w", name, err)
		}
		if err := db.Tombstone(ctx, c.ID, c.Version); err != nil {
			return nil, fmt.Errorf("reserve retired name %q: %w", name, err)
		}
	}
	return live, nil
}

// SetConfigMode marks the server's connection set as CONFIG-OWNED: the
// picker's create, params commits, deletes, and renames refuse.
func (s *Server) SetConfigMode(on bool) { s.configMode = on }

// errConfigMode is the one refusal every connection mutation answers
// with in config mode.
var errConfigMode = fmt.Errorf("connections are server config (v2): edit server.yaml's connections: list and restart — the picker no longer edits them")

// bootDialWait bounds how long ConnectAll waits per connection: a dead
// remote delays boot by this much at most, never bricks it. A var so
// the bound itself is testable.
var bootDialWait = 20 * time.Second

// ConnectAll dials every declared connection NOW, synchronously, and
// logs each outcome — the boot doesn't serve mysteries (Joe,
// 2026-08-23): by the time the node is up, every connection is either
// LIVE (root learned) or its error is in the server log verbatim (and
// on the wire as the row's status). Bounded per connection so a dead
// remote delays boot, never bricks it; the lazy re-kick on reads still
// retries afterward.
func (s *Server) ConnectAll(ctx context.Context) {
	conns, err := s.db.List(ctx)
	if err != nil {
		log.Printf("gridwell: connections: list: %v", err)
		return
	}
	var wg sync.WaitGroup
	for _, c := range conns {
		if c.Params == "" {
			continue
		}
		wg.Add(1)
		go func(c *Conn) {
			defer wg.Done()
			label := c.AltText
			if label == "" {
				label = c.NS
			}
			done := make(chan struct{})
			var root string
			var lerr error
			go func() {
				defer close(done)
				root, lerr = s.learnRoot(c)
			}()
			select {
			case <-done:
				if lerr != nil {
					log.Printf("gridwell: connection %q (%s): %v", label, c.NS, lerr)
				} else {
					log.Printf("gridwell: connection %q (%s): connected — root %s", label, c.NS, root)
				}
			case <-time.After(bootDialWait):
				log.Printf("gridwell: connection %q (%s): no answer after %s — still trying in the background", label, c.NS, bootDialWait)
			}
		}(c)
	}
	wg.Wait()
}

// learnRoot is THE connect-and-learn body — the boot path (ConnectAll)
// calls it synchronously, the lazy kick (kickRootFetch) wraps it in a
// goroutine. ONE implementation so the two paths cannot disagree about
// what "learn" means (they were separate copies and had drifted).
//
// Dial the transport; a stored root is final. A learned root persists,
// bumps the connection grid, and publishes the row so open clients see
// the well gain its room.
func (s *Server) learnRoot(c *Conn) (string, error) {
	lc, err := s.ensureLive(c)
	if err != nil {
		return "", err // ensureLive recorded the detail already
	}
	if c.RemoteRoot != "" {
		return c.RemoteRoot, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root, err := s.remoteHome(ctx, lc)
	if err != nil {
		s.setRootErr(c.NS, status.Convert(err).Message())
		return "", err
	}
	if root == "" {
		return "", fmt.Errorf("the remote declared no home")
	}
	s.setRootErr(c.NS, "")
	row, err := s.db.SetRemoteRoot(ctx, c.ID, root)
	if err != nil {
		// A learned root that cannot be STORED is a real failure — record
		// it (the well's status_detail says why it stays childless) rather
		// than silently re-learning on every read forever.
		s.setRootErr(c.NS, "learned root could not be stored: "+err.Error())
		return "", err
	}
	_ = s.db.BumpGridVersion(ctx)
	s.hub.Publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: tileFromConn(row)},
	}})
	return root, nil
}
