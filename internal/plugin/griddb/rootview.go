package griddb

// The ROOT grid's persisted viewport (framing audit, 2026-08-13): the fs
// and proc plugins silently swallowed SetRootView, so panning their root
// grids was lost on every re-entry. Both store it here, on the root grid's
// own row — the v2 NULLABLE columns, where NULL means "never set" (the
// legacy view_* columns default to values a fresh row already has, so
// they cannot express unset and stay dead).

import "database/sql"

// RootView reads a grid row's persisted root viewport; ok=false when it
// was never set.
func RootView(db *sql.DB, gridID int64) (cx, cy, zoom float64, ok bool, err error) {
	var ncx, ncy, nzoom sql.NullFloat64
	err = db.QueryRow(`SELECT root_cx, root_cy, root_zoom FROM grids WHERE id = ?`, gridID).
		Scan(&ncx, &ncy, &nzoom)
	if err == sql.ErrNoRows {
		return 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, false, err
	}
	if !nzoom.Valid {
		return 0, 0, 0, false, nil
	}
	return ncx.Float64, ncy.Float64, nzoom.Float64, true, nil
}

// SetRootView persists a grid row's root viewport. Framing-class: nothing
// else on the row changes.
func SetRootView(db *sql.DB, gridID int64, cx, cy, zoom float64) error {
	_, err := db.Exec(`UPDATE grids SET root_cx = ?, root_cy = ?, root_zoom = ? WHERE id = ?`,
		cx, cy, zoom, gridID)
	return err
}
