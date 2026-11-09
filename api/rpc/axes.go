package rpc

// Flag is one wire flag a site branches on, classed (CLAUDE.md, The
// principles 3). An axis changes which operation is allowed or what it does,
// and Carrier names the parity dimension that varies on it
// (internal/server/parity_test.go), "" when no cell does; a presentation flag
// changes only how a row is drawn or told. Field is the proto full name, and
// Why says what the flag changes.
type Flag struct {
	Field        string
	Axis         bool
	Carrier, Why string
}

// Flags is every bool and enum field on Tile, Grid, SourceGrid, InfoResponse,
// EventSourceHealth and CreateTileRequest, and the strings whose emptiness
// is the flag; the test walks the descriptors against it.
var Flags = []Flag{
	{"gridwell.v1.Tile.link", true, "kind: the doors to a home grid, a directory and a far home are links, a directory's symlink among them; link and see it dead make a leaf one", "delete unlinks and copy links"},
	{"gridwell.v1.Tile.url_frozen", true, "operation: freeze sets it and reads it back", "descent keeps the preview instead of going live"},
	{"gridwell.v1.Tile.serves_page", true, "operation: serve a page, and the decided url copy cell on the files", "the address is the node's /content/ API, and a copy links"},
	{"gridwell.v1.Tile.named", true, "operation: rename sets it, and the decided url screenshot cell on the files", "no capture or listing replaces the name"},
	{"gridwell.v1.Tile.read_only", true, "variant a read-only file: the decided text write cells, here and through the connection; variant a disabled source: the decided far text write cell", "a body write is refused before it is typed"},
	{"gridwell.v1.Grid.writable", true, "variant a disabled source: the decided far text write cell reads it false, and the files' cell true", "the grid's text bodies take writes"},
	{"gridwell.v1.Grid.accepts_tiles", true, "owner: true on a home, a directory and a Gridwell tile on one, and the decided door copy cells", "create, a swatch drop and a door's copy"},
	{"gridwell.v1.CreateTileRequest.file", true, "owner: the decided create cells on the files make a file or a directory, and the create cells on a directory make home's row", "which side of a directory's grid a new tile is on: the disk or Gridwell"},
	{"gridwell.v1.EventSourceHealth.healthy", true, "variant a directory that will not read: the decided see it dead cells, here and through the connection", "a dark source's grids serve from cache and are never current (cache.Current)"},
	{"gridwell.v1.EventSourceHealth.told", true, "variant a far node refusing the share: the decided far url update cell", "a source's held grids are current, or read on every move (cache.Current)"},
	{"gridwell.v1.EventSourceHealth.disabled", false, "", "a dark source says nothing more: no notice stands (events.ReactHealth)"},
	{"gridwell.v1.EventSourceHealth.live_updates_off", false, "", "a notice says why the source failed to tell its changes (events.ReactHealth)"},
	{"gridwell.v1.Grid.moves", true, "variant a disabled source: the decided far text write cell reads it false; parity_move_test.go reads it true on home, the files and a far home", "a left-drag to another grid is a move, or refused (dragdrop.MoveForbidden)"},
}
