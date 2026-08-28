package dbformat

import (
	"context"
	"database/sql"
	"fmt"
)

// setPragmaInt writes an integer-valued PRAGMA outside a transaction — used
// only by tests seeding foreign/corrupt headers; production stamps ride the
// transactional setPragmaIntTx.
func setPragmaInt(ctx context.Context, db *sql.DB, name string, v int64) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA %s = %d", name, v))
	return err
}
