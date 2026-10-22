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

// Flags is every bool and enum field on Tile, Grid, PluginGrid, InfoResponse,
// ListResponse and Entry, and the strings whose emptiness is the flag; the
// test walks the descriptors against it.
var Flags = []Flag{
	{"gridwell.v1.Tile.link", true, "kind: the doors to a home, a plugin and a far grid are links; link and see it dead make a leaf one", "delete unlinks and copy links"},
	{"gridwell.v1.Tile.url_frozen", true, "operation: freeze sets it and reads it back", "descent keeps the preview instead of going live"},
	{"gridwell.v1.Tile.serves_page", true, "operation: serve a page, and the decided url copy cell on a plugin", "the address is the node's /content/ API, and a copy links"},
	{"gridwell.v1.Tile.named", true, "operation: rename sets it, and the decided url screenshot cell on a plugin", "no capture or listing replaces the name"},
	{"gridwell.v1.Tile.read_only", true, "", "a body write is refused before it is typed"},
	{"gridwell.v1.Grid.writable", true, "", "the grid's text bodies take writes"},
	{"gridwell.v1.Grid.host_content", true, "", "a cross-grid move with a host endpoint is refused (dragdrop.MoveForbidden)"},
	{"gridwell.v1.Grid.accepts_tiles", true, "owner: true on a home, false on a plugin, and the decided create and door copy cells", "create, a swatch drop and a door's copy"},
	{"plugin.v1.InfoResponse.watch", true, "", "an open view is told of a change, or reads on every move (cache.Current)"},
	{"plugin.v1.InfoResponse.writable", true, "", "the plugin's text bodies take writes"},
	{"plugin.v1.InfoResponse.host_content", true, "", "becomes Grid.host_content"},
	{"plugin.v1.InfoResponse.move", true, "the decided move cells, both variants run in plugin_move_test.go", "a move to another grid"},
	{"plugin.v1.ListResponse.authoritative", true, "", "a key absent from the listing is gone, so a link to it is dead"},
	{"plugin.v1.ListResponse.unreachable", true, "", "a cached answer retires nothing, so nothing reads dead"},
	{"plugin.v1.Entry.serves_page", true, "as Tile.serves_page, which it becomes", "becomes Tile.serves_page"},
	{"plugin.v1.Entry.read_only", true, "", "becomes Tile.read_only"},
}
