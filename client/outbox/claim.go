package outbox

import "sync"

// Claims holds the version each open writer of a row claims: the one the
// user saw when its gesture began, moved only by this client's own writes to
// the row that claimed it and landed. A foreign writer's bump is refused, and
// a retry claims what its gesture saw. pane.Levels keeps a workspace level's
// basis by the same rule.
type Claims struct {
	mu   sync.Mutex
	open map[*Claim]struct{}
}

// Claim is one open writer's claim on a row.
type Claim struct {
	row string
	v   int64
}

func NewClaims() *Claims { return &Claims{open: map[*Claim]struct{}{}} }

// Open starts a writer on row that saw version saw.
func (c *Claims) Open(row string, saw int64) *Claim {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl := &Claim{row: row, v: saw}
	c.open[cl] = struct{}{}
	return cl
}

// Version is what cl claims now.
func (c *Claims) Version(cl *Claim) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cl.v
}

// Landed records that this client's write to row claimed claimed and landed
// at landed: every open writer on row that claimed the same now claims
// landed, and one on any other version keeps it.
func (c *Claims) Landed(row string, claimed, landed int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for cl := range c.open {
		if cl.row == row && cl.v == claimed {
			cl.v = landed
		}
	}
}

// Close ends cl's writer; a closed claim keeps its version but moves no more.
func (c *Claims) Close(cl *Claim) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.open, cl)
}
