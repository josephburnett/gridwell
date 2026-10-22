package server_test

// Parity: a tile behaves the same wherever its content comes from (CLAUDE.md,
// promises). parityTable is kind × operation × owner, each cell classified
// from the code. Home is the reference every cell is held to, so it has no
// column of its own: a same cell's check runs the operation against home and
// against the owner, and parityOracle compares the two observables. The count,
// the holes plus the same cells with no check, is held at zero by
// test/boundary's TestParityIsZero, which reads it off TestParity/count.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/contentrow"
	"github.com/josephburnett/gridwell/client/deadlink"
	"github.com/josephburnett/gridwell/client/events"
	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellws"
	"github.com/josephburnett/gridwell/client/tilepreview"
	"github.com/josephburnett/gridwell/client/urlview"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

type parityKind string

// The kinds, and the three grids a door can open onto as seen from the node
// holding it: one of its home's, a plugin's it declares, a far home it reaches.
const (
	kText       parityKind = "text"
	kURL        parityKind = "url"
	kShell      parityKind = "shell"
	kWorkspace  parityKind = "workspace"
	kDoor       parityKind = "door"
	kDoorHome   parityKind = "door to a home grid"
	kDoorPlugin parityKind = "door to a plugin grid"
	kDoorFar    parityKind = "door to a far home"
)

var parityKinds = []parityKind{kText, kURL, kShell, kWorkspace, kDoor, kDoorHome, kDoorPlugin, kDoorFar}

type parityOp string

const (
	opCreate     parityOp = "create"
	opMove       parityOp = "move"
	opCopy       parityOp = "copy"
	opLink       parityOp = "link"
	opDelete     parityOp = "delete"
	opRename     parityOp = "rename"
	opDescend    parityOp = "descend"
	opFreeze     parityOp = "freeze"
	opScreenshot parityOp = "keep a screenshot"
	opZoom       parityOp = "save zoom"
	opScroll     parityOp = "save a text scroll"
	opFraming    parityOp = "save framing"
	opRead       parityOp = "read content"
	opWrite      parityOp = "write content"
	opServe      parityOp = "serve a page"
	opFind       parityOp = "find"
	opUpdate     parityOp = "see it update"
	opDead       parityOp = "see it dead"
)

var parityOps = []parityOp{opCreate, opMove, opCopy, opLink, opDelete, opRename, opDescend, opFreeze,
	opScreenshot, opZoom, opScroll, opFraming, opRead, opWrite, opServe, opFind, opUpdate, opDead}

type parityOwner string

const (
	onHome   parityOwner = "home"
	onPlugin parityOwner = "plugin"
	onFar    parityOwner = "far"
)

type parityVerdict int

const (
	same    parityVerdict = iota // check runs it against home and the owner; nil is a cell still owed one
	decided                      // why quotes the CLAUDE.md line that names the difference
	na                           // why says what in the kind leaves nothing to do
	hole                         // why is the file:line doing what no decision names
)

// parityCheck runs one operation on a tile of kind held by owner and answers
// what the user would see.
type parityCheck func(f *parityFix, owner parityOwner, kind parityKind) observable

type parityCell struct {
	kind    parityKind
	op      parityOp
	owner   parityOwner
	verdict parityVerdict
	why     string
	check   parityCheck
}

func (c parityCell) String() string { return fmt.Sprintf("%s/%s/%s", c.kind, c.op, c.owner) }

// What home's kind table leaves nothing to do for, whoever holds the tile.
const (
	naName    = "a text's name is its body's first line, so nothing renames it (store.RenameTile)"
	naLive    = "only a url or a shell has a live surface to freeze or capture (rpc.KindCaps.Freezes)"
	naZoom    = "content zoom belongs to a content descent (contentzoom.Decide)"
	naScroll  = "only a text has a text scroll (textedit.Scroll)"
	naFraming = "framing is a door's (rpc.IsDoorKind)"
	naContent = "the kind holds no content of its own (rpc.KindCaps.Content)"
	naServe   = "only a url entry serves a page (plugin.proto Entry.serves_page)"
)

// What a plugin cannot hold.
const (
	naPluginKind  = "a plugin serves door, text and url entries only (plugin.proto Entry.kind)"
	naPluginDoor  = "a plugin's door to its own grid is the door row, and a door entry has no link variant (plugin.proto Entry.link_target)"
	naPluginGrids = "a plugin entry opens and links only its own grids (plugin.proto Entry.child_grid, Entry.link_target)"
)

// The differences CLAUDE.md decides, quoted.
const (
	dPluginCreates  = "A plugin serves keys and content; the node owns ids and layout."
	dPluginDelete   = "A delete is handed to the plugin, which says what deleting means"
	dPluginDoorCopy = "A plugin's door never copies"
	dPluginPageCopy = "A url entry whose plugin serves its page copies as a link too"
	dPluginName     = "the source's label names it until a rename"
	dPluginMove     = "a plugin that declares no move refuses with the reason" // plugin_move_test.go
	dPluginPage     = "Plugins serve web content through"
	dShellCopy      = "A shell copied across nodes lands as a link"
	dTextName       = "A text tile's name stays its source's, since its presentation reads it."
)

var parityTable = []parityCell{
	{kText, opCreate, onPlugin, decided, dPluginCreates, nil},
	{kText, opCreate, onFar, same, "", checkCreate},
	{kText, opMove, onPlugin, decided, dPluginMove, nil},
	{kText, opMove, onFar, same, "", checkMove},
	{kText, opCopy, onPlugin, same, "", checkCopy},
	{kText, opCopy, onFar, same, "", checkCopy},
	{kText, opLink, onPlugin, same, "", checkLink},
	{kText, opLink, onFar, same, "", checkLink},
	{kText, opDelete, onPlugin, decided, dPluginDelete, nil},
	{kText, opDelete, onFar, same, "", checkDelete},
	{kText, opRename, onPlugin, na, naName, nil},
	{kText, opRename, onFar, na, naName, nil},
	{kText, opDescend, onPlugin, same, "", checkDescend},
	{kText, opDescend, onFar, same, "", checkDescend},
	{kText, opFreeze, onPlugin, na, naLive, nil},
	{kText, opFreeze, onFar, na, naLive, nil},
	{kText, opScreenshot, onPlugin, na, naLive, nil},
	{kText, opScreenshot, onFar, na, naLive, nil},
	{kText, opZoom, onPlugin, same, "", checkZoom},
	{kText, opZoom, onFar, same, "", checkZoom},
	{kText, opScroll, onPlugin, same, "", checkScroll},
	{kText, opScroll, onFar, same, "", checkScroll},
	{kText, opFraming, onPlugin, na, naFraming, nil},
	{kText, opFraming, onFar, na, naFraming, nil},
	{kText, opRead, onPlugin, same, "", checkRead},
	{kText, opRead, onFar, same, "", checkRead},
	{kText, opWrite, onPlugin, decided, dTextName, nil},
	{kText, opWrite, onFar, same, "", checkWrite},
	{kText, opServe, onPlugin, na, naServe, nil},
	{kText, opServe, onFar, na, naServe, nil},
	{kText, opFind, onPlugin, same, "", checkFind},
	{kText, opFind, onFar, same, "", checkFind},
	{kText, opUpdate, onPlugin, decided, dTextName, nil},
	{kText, opUpdate, onFar, same, "", checkUpdate},
	{kText, opDead, onPlugin, same, "", checkDead},
	{kText, opDead, onFar, same, "", checkDead},
	{kURL, opCreate, onPlugin, decided, dPluginCreates, nil},
	{kURL, opCreate, onFar, same, "", checkCreate},
	{kURL, opMove, onPlugin, decided, dPluginMove, nil},
	{kURL, opMove, onFar, same, "", checkMove},
	{kURL, opCopy, onPlugin, decided, dPluginPageCopy, nil},
	{kURL, opCopy, onFar, same, "", checkCopy},
	{kURL, opLink, onPlugin, same, "", checkLink},
	{kURL, opLink, onFar, same, "", checkLink},
	{kURL, opDelete, onPlugin, decided, dPluginDelete, nil},
	{kURL, opDelete, onFar, same, "", checkDelete},
	{kURL, opRename, onPlugin, same, "", checkRename},
	{kURL, opRename, onFar, same, "", checkRename},
	{kURL, opDescend, onPlugin, same, "", checkDescend},
	{kURL, opDescend, onFar, same, "", checkDescend},
	{kURL, opFreeze, onPlugin, same, "", checkFreeze},
	{kURL, opFreeze, onFar, same, "", checkFreeze},
	{kURL, opScreenshot, onPlugin, decided, dPluginName, nil},
	{kURL, opScreenshot, onFar, same, "", checkScreenshot},
	{kURL, opZoom, onPlugin, same, "", checkZoom},
	{kURL, opZoom, onFar, same, "", checkZoom},
	{kURL, opScroll, onPlugin, na, naScroll, nil},
	{kURL, opScroll, onFar, na, naScroll, nil},
	{kURL, opFraming, onPlugin, na, naFraming, nil},
	{kURL, opFraming, onFar, na, naFraming, nil},
	{kURL, opRead, onPlugin, same, "", checkRead},
	{kURL, opRead, onFar, same, "", checkRead},
	{kURL, opWrite, onPlugin, same, "", checkWrite},
	{kURL, opWrite, onFar, same, "", checkWrite},
	{kURL, opServe, onPlugin, decided, dPluginPage, nil},
	{kURL, opServe, onFar, same, "", checkServe},
	{kURL, opFind, onPlugin, same, "", checkFind},
	{kURL, opFind, onFar, same, "", checkFind},
	{kURL, opUpdate, onPlugin, same, "", checkUpdate},
	{kURL, opUpdate, onFar, same, "", checkUpdate},
	{kURL, opDead, onPlugin, same, "", checkDead},
	{kURL, opDead, onFar, same, "", checkDead},
	{kShell, opCreate, onPlugin, na, naPluginKind, nil},
	{kShell, opCreate, onFar, same, "", checkCreate},
	{kShell, opMove, onPlugin, na, naPluginKind, nil},
	{kShell, opMove, onFar, same, "", checkMove},
	{kShell, opCopy, onPlugin, na, naPluginKind, nil},
	{kShell, opCopy, onFar, decided, dShellCopy, nil},
	{kShell, opLink, onPlugin, na, naPluginKind, nil},
	{kShell, opLink, onFar, same, "", checkLink},
	{kShell, opDelete, onPlugin, na, naPluginKind, nil},
	{kShell, opDelete, onFar, same, "", checkDelete},
	{kShell, opRename, onPlugin, na, naPluginKind, nil},
	{kShell, opRename, onFar, same, "", checkRename},
	{kShell, opDescend, onPlugin, na, naPluginKind, nil},
	{kShell, opDescend, onFar, same, "", checkDescend},
	{kShell, opFreeze, onPlugin, na, naPluginKind, nil},
	{kShell, opFreeze, onFar, same, "", checkFreeze},
	{kShell, opScreenshot, onPlugin, na, naPluginKind, nil},
	{kShell, opScreenshot, onFar, same, "", checkScreenshot},
	{kShell, opZoom, onPlugin, na, naPluginKind, nil},
	{kShell, opZoom, onFar, same, "", checkZoom},
	{kShell, opScroll, onPlugin, na, naScroll, nil},
	{kShell, opScroll, onFar, na, naScroll, nil},
	{kShell, opFraming, onPlugin, na, naFraming, nil},
	{kShell, opFraming, onFar, na, naFraming, nil},
	{kShell, opRead, onPlugin, na, naContent, nil},
	{kShell, opRead, onFar, na, naContent, nil},
	{kShell, opWrite, onPlugin, na, naContent, nil},
	{kShell, opWrite, onFar, na, naContent, nil},
	{kShell, opServe, onPlugin, na, naServe, nil},
	{kShell, opServe, onFar, na, naServe, nil},
	{kShell, opFind, onPlugin, na, naPluginKind, nil},
	{kShell, opFind, onFar, same, "", checkFind},
	{kShell, opUpdate, onPlugin, na, naPluginKind, nil},
	{kShell, opUpdate, onFar, same, "", checkUpdate},
	{kShell, opDead, onPlugin, na, naPluginKind, nil},
	{kShell, opDead, onFar, same, "", checkDead},
	{kWorkspace, opCreate, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opCreate, onFar, same, "", checkCreate},
	{kWorkspace, opMove, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opMove, onFar, same, "", checkMove},
	{kWorkspace, opCopy, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opCopy, onFar, same, "", checkCopy},
	{kWorkspace, opLink, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opLink, onFar, same, "", checkLink},
	{kWorkspace, opDelete, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opDelete, onFar, same, "", checkDelete},
	{kWorkspace, opRename, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opRename, onFar, same, "", checkRename},
	{kWorkspace, opDescend, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opDescend, onFar, same, "", checkDescend},
	{kWorkspace, opFreeze, onPlugin, na, naLive, nil},
	{kWorkspace, opFreeze, onFar, na, naLive, nil},
	{kWorkspace, opScreenshot, onPlugin, na, naLive, nil},
	{kWorkspace, opScreenshot, onFar, na, naLive, nil},
	{kWorkspace, opZoom, onPlugin, na, naZoom, nil},
	{kWorkspace, opZoom, onFar, na, naZoom, nil},
	{kWorkspace, opScroll, onPlugin, na, naScroll, nil},
	{kWorkspace, opScroll, onFar, na, naScroll, nil},
	{kWorkspace, opFraming, onPlugin, na, naFraming, nil},
	{kWorkspace, opFraming, onFar, na, naFraming, nil},
	{kWorkspace, opRead, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opRead, onFar, same, "", checkRead},
	{kWorkspace, opWrite, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opWrite, onFar, same, "", checkWrite},
	{kWorkspace, opServe, onPlugin, na, naServe, nil},
	{kWorkspace, opServe, onFar, na, naServe, nil},
	{kWorkspace, opFind, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opFind, onFar, same, "", checkFind},
	{kWorkspace, opUpdate, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opUpdate, onFar, same, "", checkUpdate},
	{kWorkspace, opDead, onPlugin, na, naPluginKind, nil},
	{kWorkspace, opDead, onFar, same, "", checkDead},
	{kDoor, opCreate, onPlugin, decided, dPluginCreates, nil},
	{kDoor, opCreate, onFar, same, "", checkCreate},
	{kDoor, opMove, onPlugin, decided, dPluginMove, nil},
	{kDoor, opMove, onFar, same, "", checkMove},
	{kDoor, opCopy, onPlugin, decided, dPluginDoorCopy, nil},
	{kDoor, opCopy, onFar, same, "", checkCopy},
	{kDoor, opLink, onPlugin, same, "", checkLink},
	{kDoor, opLink, onFar, same, "", checkLink},
	{kDoor, opDelete, onPlugin, decided, dPluginDelete, nil},
	{kDoor, opDelete, onFar, same, "", checkDelete},
	{kDoor, opRename, onPlugin, same, "", checkRename},
	{kDoor, opRename, onFar, same, "", checkRename},
	{kDoor, opDescend, onPlugin, same, "", checkDescend},
	{kDoor, opDescend, onFar, same, "", checkDescend},
	{kDoor, opFreeze, onPlugin, na, naLive, nil},
	{kDoor, opFreeze, onFar, na, naLive, nil},
	{kDoor, opScreenshot, onPlugin, na, naLive, nil},
	{kDoor, opScreenshot, onFar, na, naLive, nil},
	{kDoor, opZoom, onPlugin, na, naZoom, nil},
	{kDoor, opZoom, onFar, na, naZoom, nil},
	{kDoor, opScroll, onPlugin, na, naScroll, nil},
	{kDoor, opScroll, onFar, na, naScroll, nil},
	{kDoor, opFraming, onPlugin, same, "", checkFraming},
	{kDoor, opFraming, onFar, same, "", checkFraming},
	{kDoor, opRead, onPlugin, na, naContent, nil},
	{kDoor, opRead, onFar, na, naContent, nil},
	{kDoor, opWrite, onPlugin, na, naContent, nil},
	{kDoor, opWrite, onFar, na, naContent, nil},
	{kDoor, opServe, onPlugin, na, naServe, nil},
	{kDoor, opServe, onFar, na, naServe, nil},
	{kDoor, opFind, onPlugin, same, "", checkFind},
	{kDoor, opFind, onFar, same, "", checkFind},
	{kDoor, opUpdate, onPlugin, same, "", checkUpdate},
	{kDoor, opUpdate, onFar, same, "", checkUpdate},
	{kDoor, opDead, onPlugin, same, "", checkDead},
	{kDoor, opDead, onFar, same, "", checkDead},
	{kDoorHome, opCreate, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opCreate, onFar, same, "", checkCreate},
	{kDoorHome, opMove, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opMove, onFar, same, "", checkMove},
	{kDoorHome, opCopy, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opCopy, onFar, same, "", checkCopy},
	{kDoorHome, opLink, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opLink, onFar, same, "", checkLink},
	{kDoorHome, opDelete, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opDelete, onFar, same, "", checkDelete},
	{kDoorHome, opRename, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opRename, onFar, same, "", checkRename},
	{kDoorHome, opDescend, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opDescend, onFar, same, "", checkDescend},
	{kDoorHome, opFreeze, onPlugin, na, naLive, nil},
	{kDoorHome, opFreeze, onFar, na, naLive, nil},
	{kDoorHome, opScreenshot, onPlugin, na, naLive, nil},
	{kDoorHome, opScreenshot, onFar, na, naLive, nil},
	{kDoorHome, opZoom, onPlugin, na, naZoom, nil},
	{kDoorHome, opZoom, onFar, na, naZoom, nil},
	{kDoorHome, opScroll, onPlugin, na, naScroll, nil},
	{kDoorHome, opScroll, onFar, na, naScroll, nil},
	{kDoorHome, opFraming, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opFraming, onFar, same, "", checkFraming},
	{kDoorHome, opRead, onPlugin, na, naContent, nil},
	{kDoorHome, opRead, onFar, na, naContent, nil},
	{kDoorHome, opWrite, onPlugin, na, naContent, nil},
	{kDoorHome, opWrite, onFar, na, naContent, nil},
	{kDoorHome, opServe, onPlugin, na, naServe, nil},
	{kDoorHome, opServe, onFar, na, naServe, nil},
	{kDoorHome, opFind, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opFind, onFar, same, "", checkFind},
	{kDoorHome, opUpdate, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opUpdate, onFar, same, "", checkUpdate},
	{kDoorHome, opDead, onPlugin, na, naPluginGrids, nil},
	{kDoorHome, opDead, onFar, same, "", checkDead},
	{kDoorPlugin, opCreate, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opCreate, onFar, same, "", checkCreate},
	{kDoorPlugin, opMove, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opMove, onFar, same, "", checkMove},
	{kDoorPlugin, opCopy, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opCopy, onFar, same, "", checkCopy},
	{kDoorPlugin, opLink, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opLink, onFar, same, "", checkLink},
	{kDoorPlugin, opDelete, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opDelete, onFar, same, "", checkDelete},
	{kDoorPlugin, opRename, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opRename, onFar, same, "", checkRename},
	{kDoorPlugin, opDescend, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opDescend, onFar, same, "", checkDescend},
	{kDoorPlugin, opFreeze, onPlugin, na, naLive, nil},
	{kDoorPlugin, opFreeze, onFar, na, naLive, nil},
	{kDoorPlugin, opScreenshot, onPlugin, na, naLive, nil},
	{kDoorPlugin, opScreenshot, onFar, na, naLive, nil},
	{kDoorPlugin, opZoom, onPlugin, na, naZoom, nil},
	{kDoorPlugin, opZoom, onFar, na, naZoom, nil},
	{kDoorPlugin, opScroll, onPlugin, na, naScroll, nil},
	{kDoorPlugin, opScroll, onFar, na, naScroll, nil},
	{kDoorPlugin, opFraming, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opFraming, onFar, same, "", checkFraming},
	{kDoorPlugin, opRead, onPlugin, na, naContent, nil},
	{kDoorPlugin, opRead, onFar, na, naContent, nil},
	{kDoorPlugin, opWrite, onPlugin, na, naContent, nil},
	{kDoorPlugin, opWrite, onFar, na, naContent, nil},
	{kDoorPlugin, opServe, onPlugin, na, naServe, nil},
	{kDoorPlugin, opServe, onFar, na, naServe, nil},
	{kDoorPlugin, opFind, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opFind, onFar, same, "", checkFind},
	{kDoorPlugin, opUpdate, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opUpdate, onFar, same, "", checkUpdate},
	{kDoorPlugin, opDead, onPlugin, na, naPluginDoor, nil},
	{kDoorPlugin, opDead, onFar, same, "", checkDead},
	{kDoorFar, opCreate, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opCreate, onFar, same, "", checkCreate},
	{kDoorFar, opMove, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opMove, onFar, same, "", checkMove},
	{kDoorFar, opCopy, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opCopy, onFar, same, "", checkCopy},
	{kDoorFar, opLink, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opLink, onFar, same, "", checkLink},
	{kDoorFar, opDelete, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opDelete, onFar, same, "", checkDelete},
	{kDoorFar, opRename, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opRename, onFar, same, "", checkRename},
	{kDoorFar, opDescend, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opDescend, onFar, same, "", checkDescend},
	{kDoorFar, opFreeze, onPlugin, na, naLive, nil},
	{kDoorFar, opFreeze, onFar, na, naLive, nil},
	{kDoorFar, opScreenshot, onPlugin, na, naLive, nil},
	{kDoorFar, opScreenshot, onFar, na, naLive, nil},
	{kDoorFar, opZoom, onPlugin, na, naZoom, nil},
	{kDoorFar, opZoom, onFar, na, naZoom, nil},
	{kDoorFar, opScroll, onPlugin, na, naScroll, nil},
	{kDoorFar, opScroll, onFar, na, naScroll, nil},
	{kDoorFar, opFraming, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opFraming, onFar, same, "", checkFraming},
	{kDoorFar, opRead, onPlugin, na, naContent, nil},
	{kDoorFar, opRead, onFar, na, naContent, nil},
	{kDoorFar, opWrite, onPlugin, na, naContent, nil},
	{kDoorFar, opWrite, onFar, na, naContent, nil},
	{kDoorFar, opServe, onPlugin, na, naServe, nil},
	{kDoorFar, opServe, onFar, na, naServe, nil},
	{kDoorFar, opFind, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opFind, onFar, same, "", checkFind},
	{kDoorFar, opUpdate, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opUpdate, onFar, same, "", checkUpdate},
	{kDoorFar, opDead, onPlugin, na, naPluginGrids, nil},
	{kDoorFar, opDead, onFar, same, "", checkDead},
}

// parityCount is the ledger's count: every hole, and every same cell still
// owed its check.
func parityCount(table []parityCell) int {
	n := 0
	for _, c := range table {
		if c.verdict == hole || c.verdict == same && c.check == nil {
			n++
		}
	}
	return n
}

// TestParity holds the table whole and true, runs every check, and logs the
// count test/boundary records.
func TestParity(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		checkParityTable(t, parityTable)
		t.Logf("parity count: %d", parityCount(parityTable))
	})
	for _, c := range parityTable {
		if c.verdict != same || c.check == nil {
			continue
		}
		t.Run(c.String(), func(t *testing.T) {
			t.Parallel()
			f := newParityFix(t)
			want := c.check(f, onHome, c.kind)
			got := c.check(f, c.owner, c.kind)
			parityOracle(t, c, want, got)
		})
	}
}

// checkParityTable holds one cell per kind, operation and owner, every
// verdict but same carrying its why, every decided quote standing in
// CLAUDE.md and every hole naming a file that is there.
func checkParityTable(t *testing.T, table []parityCell) {
	t.Helper()
	claude, err := os.ReadFile("../../CLAUDE.md")
	if err != nil {
		t.Fatal(err)
	}
	space := regexp.MustCompile(`\s+`)
	decisions := space.ReplaceAllString(string(claude), " ")
	seen := map[string]bool{}
	for _, c := range table {
		if seen[c.String()] {
			t.Errorf("%s is listed twice", c)
		}
		seen[c.String()] = true
		switch c.verdict {
		case same:
			if c.why != "" {
				t.Errorf("%s is same and says %q: a same cell's evidence is its check", c, c.why)
			}
		case decided:
			if !strings.Contains(decisions, space.ReplaceAllString(c.why, " ")) {
				t.Errorf("%s is decided by %q, which CLAUDE.md does not say", c, c.why)
			}
		case hole:
			file, _, _ := strings.Cut(c.why, ":")
			if _, err := os.Stat("../../" + file); err != nil {
				t.Errorf("%s is a hole at %q, which is no file: %v", c, file, err)
			}
		}
		if c.verdict != same && c.why == "" {
			t.Errorf("%s says nothing for its verdict", c)
		}
		if c.verdict != same && c.check != nil {
			t.Errorf("%s has a check but is not same", c)
		}
	}
	for _, k := range parityKinds {
		for _, op := range parityOps {
			for _, o := range []parityOwner{onPlugin, onFar} {
				if !seen[parityCell{kind: k, op: op, owner: o}.String()] {
					t.Errorf("%s/%s/%s has no cell", k, op, o)
				}
			}
		}
	}
}

// observable is what the user sees of one operation: the answer, the row read
// back, whether an open view of its grid was told, the client's draw verdict
// on the row, and what a read answered.
type observable struct {
	Outcome string            // clientsync's word for the operation's answer
	Row     map[string]string // the row's facts the operation moved, or a new row's facts
	Told    bool
	Draw    string
	Read    string
}

// parityOracle is the one comparison: an owner's observable is home's.
func parityOracle(t *testing.T, c parityCell, home, got observable) {
	t.Helper()
	if home.Outcome != clientsync.OutcomeOK.String() {
		t.Fatalf("%s: home's own run answered %s (%+v), so there is nothing to hold the owner to", c, home.Outcome, home)
	}
	if !reflect.DeepEqual(home, got) {
		t.Errorf("%s differs from home:\nhome %+v\n%-4s %+v", c, home, c.owner, got)
	}
}

// parityFacts is the row as the client draws and reads it, ids aside.
func parityFacts(t *gridwellv1.Tile) map[string]string {
	out := map[string]string{
		"kind":    t.Kind,
		"link":    fmt.Sprint(t.Link),
		"name":    t.AltText,
		"named":   fmt.Sprint(t.Named),
		"place":   fmt.Sprint(t.X, t.Y, t.W, t.H),
		"grid":    t.GridId,
		"url":     t.UrlString,
		"history": t.UrlHistory,
		"frozen":  fmt.Sprint(t.UrlFrozen),
		"preview": fmt.Sprint(t.PreviewBlobId != 0),
		"framing": fmt.Sprint(t.ViewCx, t.ViewCy, t.ViewZoom),
		"scroll":  fmt.Sprint(t.TextX, t.TextY, t.TextMode),
		"zoom":    fmt.Sprint(t.ContentZoom),
	}
	if rpc.IsBodyKind(t.Kind) {
		// The basis a save of the body claims; a url's content is its address.
		out["gen"] = fmt.Sprint(t.Version, t.ContentStamp)
	}
	return out
}

// parityIdentity are the facts whose value is a name or a count the owner
// spells its own way: a write says only that it moved them.
var parityIdentity = map[string]bool{"grid": true, "gen": true}

// parityDelta is what a write moved, or with before nil every fact of a new
// row but its identity.
func parityDelta(before, after *gridwellv1.Tile) map[string]string {
	out := map[string]string{}
	b, a := map[string]string{}, parityFacts(after)
	if before != nil {
		b = parityFacts(before)
	}
	for k, v := range a {
		switch {
		case before == nil && parityIdentity[k]:
		case before == nil:
			out[k] = v
		case b[k] == v:
		case parityIdentity[k]:
			out[k] = "moved"
		default:
			out[k] = v
		}
	}
	return out
}

const (
	parityPluginUUID    = "pparity"
	parityFarPluginUUID = "pparfar"
)

// parityFix is three nodes in a chain, the local one reaching the far one
// through a connection and the far one reaching a third the same way, the
// first two each with its home and a paritySource, and a client subscribed at
// the local web API showing every grid the cells use. The third node is what
// a far home's door opens when the far node holds it.
type parityFix struct {
	t       *testing.T
	ctx     context.Context
	cl      *rpc.Client
	raw     gridwellv1connect.GridwellClient
	h       *transportHarness
	third   *farNode
	src     map[string]*paritySource // by the qualified namespace of its grids
	root    map[parityOwner]string   // the grid each owner's tiles sit on
	away    map[parityOwner]string   // another grid of the same owner
	farHome map[parityOwner]string   // the far home a door on the owner's grid opens
	// undeclare takes out the connections of the node holding the owner's
	// tiles, answering what declares them again.
	undeclare map[parityOwner]func() func()
	nodes     map[string]parityNode // each node by the prefix it is reached through, "" the local one
	plugins   []*gridwellv1.PluginInfo
	token     string
	events    chan *gridwellv1.Event
	breaks    map[string]func() // per tile, what breaks the path to it
	mends     map[string]func() // per tile, what mends a break the next run needs whole
	marks     int
}

// parityNode is one node of the chain as its own user reaches it.
type parityNode struct {
	cl    *rpc.Client
	web   *httptest.Server
	shell *shellsvctest.FakeStreamer
}

// The connection the local node reaches the far one by, and the far one
// reaches the third by.
const (
	parityConn      = localNodeID + "/geneva/"
	parityThirdConn = parityConn + "rnode1/zurich/"
)

func newParityFix(t *testing.T) *parityFix {
	local, far := newParitySource(), newParitySource()
	third := newFarNode(t, "cnode1")
	undeclare := map[parityOwner]func() func(){}
	at := func(o parityOwner) func(*plugin.Registry, *store.Store) {
		return func(reg *plugin.Registry, st *store.Store) {
			undeclare[o] = func() func() { return undeclareConnections(t, reg, st) }
		}
	}
	h := buildTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil, nil,
		func(reg *plugin.Registry, st *store.Store) {
			registerParitySource(t, reg, st, parityPluginUUID, local)
			at(onHome)(reg, st)
		},
		func(reg *plugin.Registry, st *store.Store) {
			registerParitySource(t, reg, st, parityFarPluginUUID, far)
		},
		connectTo(t, "zurich", third, at(onFar)))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	f := &parityFix{t: t, ctx: ctx, cl: h.localCl, h: h, third: third, undeclare: undeclare,
		raw:    gridwellv1connect.NewGridwellClient(h.localHTTP.Client(), h.localURL, connect.WithProtoJSON()),
		events: make(chan *gridwellv1.Event, 1024), breaks: map[string]func(){}, mends: map[string]func(){},
		root: map[parityOwner]string{}, away: map[parityOwner]string{}, farHome: map[parityOwner]string{},
		nodes: map[string]parityNode{"": {h.localCl, h.localHTTP, h.localShell},
			parityConn: {h.remoteCl, h.remoteWeb, h.remoteShell}, parityThirdConn: {third.cl, third.web, third.shell}}}
	lp, err := f.cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.src = map[string]*paritySource{parityPluginUUID: local, parityConn + parityFarPluginUUID: far}
	f.plugins, f.token = lp.Plugins, lp.ContentToken
	f.root[onHome], f.root[onFar] = lp.HomeGridId, connectionRows(lp)[0].RootGridId
	f.root[onPlugin] = rpc.QualifyID(parityPluginUUID, rpc.EntryGridID("all"))
	f.away[onPlugin] = rpc.QualifyID(parityPluginUUID, rpc.EntryGridID("other"))
	// The far node's + menu, read through the connection, lands its own
	// connection on the third node's home.
	farMenu, err := f.cl.HandshakeNS(ctx, strings.TrimSuffix(parityConn, "/"))
	if err != nil {
		t.Fatal(err)
	}
	if rows := connectionRows(farMenu); len(rows) != 1 || rows[0].RootGridId == "" {
		t.Fatalf("the far node's connection rows = %+v, want zurich landed", rows)
	} else {
		f.farHome[onHome], f.farHome[onFar] = f.root[onFar], rows[0].RootGridId
	}
	for _, o := range []parityOwner{onHome, onFar} {
		f.away[o] = f.create(o, &gridwellv1.Tile{Kind: rpc.KindDoor, X: 9, Y: 9, W: 1, H: 1, AltText: "Away"}, nil).ChildGridId
	}
	// The third home holds what the far one does, so a door onto either
	// opens the same listing.
	if _, err := third.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: third.root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 9, Y: 9, W: 1, H: 1, AltText: "Away"}}); err != nil {
		t.Fatal(err)
	}
	shown := []string{f.farHome[onFar]}
	for _, o := range []parityOwner{onHome, onPlugin, onFar} {
		shown = append(shown, f.root[o], f.away[o])
	}
	for ns := range f.src {
		for _, g := range []string{"all", "inner", "other"} {
			shown = append(shown, rpc.QualifyID(ns, rpc.EntryGridID(g)))
		}
	}
	es, err := f.cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			select {
			case f.events <- ev:
			default:
			}
		}
	}()
	if err := f.cl.SetView(ctx, &gridwellv1.SetViewRequest{ShownGridIds: shown}); err != nil {
		t.Fatal(err)
	}
	return f
}

// registerParitySource registers src as the loader does, through
// pluginhost.Start.
func registerParitySource(t *testing.T, reg *plugin.Registry, st *store.Store, uuid string, src *paritySource) {
	cp, closer, err := plugintest.Loopback(src)
	if err != nil {
		t.Fatal(err)
	}
	a, stop := pluginhost.Start(cp, st.Namespace(uuid), nil, "plugin "+uuid+" watch")
	t.Cleanup(func() { stop(); closer() })
	reg.Register(uuid, "parity", a, nil)
	reg.SetLabel(uuid, "parity")
}

func (f *parityFix) create(o parityOwner, tl *gridwellv1.Tile, body []byte) *gridwellv1.Tile {
	f.t.Helper()
	created, err := f.cl.CreateWithContent(f.ctx, &gridwellv1.CreateTileRequest{GridId: f.root[o], Tile: tl}, body)
	if err != nil {
		f.t.Fatalf("create %s on %s: %v", tl.Kind, o, err)
	}
	return created
}

// parityCells is where each kind sits, on every owner alike.
var parityCells = map[parityKind][2]int64{
	kText: {1, 1}, kURL: {2, 1}, kShell: {3, 1}, kWorkspace: {4, 1},
	kDoor: {5, 1}, kDoorHome: {6, 1}, kDoorPlugin: {7, 1}, kDoorFar: {8, 1},
}

var parityLayout = []byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`)

// tile answers owner's tile of kind: a plugin's from its listing, home's and
// a far home's made as the user makes them, the same on both.
func (f *parityFix) tile(o parityOwner, k parityKind) *gridwellv1.Tile {
	f.t.Helper()
	if o == onPlugin {
		name := map[parityKind]string{kText: "Body", kURL: "Site", kDoor: "Door"}[k]
		g, err := f.cl.GetGrid(f.ctx, f.root[o])
		if err != nil {
			f.t.Fatal(err)
		}
		for _, tl := range g.Tiles {
			if tl.AltText == name {
				f.breaks[tl.Id] = func() { f.src[parityPluginUUID].remove(rpc.LocalOf(tl.Id)) }
				return tl
			}
		}
		f.t.Fatalf("the plugin lists no %s named %q", k, name)
	}
	at := parityCells[k]
	cell := func(tl *gridwellv1.Tile) *gridwellv1.Tile {
		tl.X, tl.Y, tl.W, tl.H = at[0], at[1], 1, 1
		return tl
	}
	var made *gridwellv1.Tile
	switch k {
	case kText:
		made = f.create(o, cell(&gridwellv1.Tile{Kind: rpc.KindText}), []byte("Body"))
	case kURL:
		made = f.create(o, cell(&gridwellv1.Tile{Kind: rpc.KindURL, UrlString: "https://example.com/u", AltText: "Site"}), nil)
	case kShell:
		made = f.create(o, cell(&gridwellv1.Tile{Kind: rpc.KindShell}), nil)
	case kWorkspace:
		made = f.create(o, cell(&gridwellv1.Tile{Kind: rpc.KindWorkspace, AltText: "Desk", Named: true}), parityLayout)
	case kDoor:
		made = f.door(o, cell(&gridwellv1.Tile{Kind: rpc.KindDoor, AltText: "Door"}))
		f.breaks[made.Id] = f.destroyer(made.Id)
		return made
	case kDoorHome:
		target := f.door(o, &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 8, W: 1, H: 1, AltText: "Target"})
		made = f.create(o, cell(&gridwellv1.Tile{Kind: rpc.KindDoor, AltText: "Hall", ChildGridId: target.ChildGridId}), nil)
		f.breaks[made.Id] = f.destroyer(target.Id)
		return made
	case kDoorPlugin:
		ns := parityPluginUUID
		if o == onFar {
			ns = parityConn + parityFarPluginUUID
		}
		made = f.create(o, cell(&gridwellv1.Tile{Kind: rpc.KindDoor, AltText: "Hall", ChildGridId: rpc.QualifyID(ns, rpc.EntryGridID("inner"))}), nil)
		f.breaks[made.Id] = func() { f.src[ns].dropGrid("inner") }
		return made
	case kDoorFar:
		made = f.create(o, cell(&gridwellv1.Tile{Kind: rpc.KindDoor, AltText: "Hall", ChildGridId: f.farHome[o]}), nil)
		// Undeclaring the holding node's connection breaks the first hop
		// past it; the next run needs that hop whole.
		f.breaks[made.Id] = func() { f.mends[made.Id] = f.undeclare[o]() }
		return made
	}
	f.breaks[made.Id] = f.destroyer(made.Id)
	return made
}

// door makes a door holding one text, as the plugin's door holds its inner
// grid's.
func (f *parityFix) door(o parityOwner, tl *gridwellv1.Tile) *gridwellv1.Tile {
	f.t.Helper()
	made := f.create(o, tl, nil)
	if _, err := f.cl.CreateWithContent(f.ctx, &gridwellv1.CreateTileRequest{GridId: made.ChildGridId,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("Inner")); err != nil {
		f.t.Fatal(err)
	}
	return made
}

// destroyer deletes id twice, to the trash and out of it, so nothing holds
// it any more.
func (f *parityFix) destroyer(id string) func() {
	return func() {
		for range 2 {
			row, err := f.cl.GetTile(f.ctx, id)
			if err != nil {
				f.t.Fatalf("read %s to destroy it: %v", id, err)
			}
			if _, err := f.cl.DeleteTile(f.ctx, &gridwellv1.DeleteTileRequest{TileId: id,
				ClaimVersion: row.Version, Claim: rpc.PlacementOf(row)}); err != nil {
				f.t.Fatalf("delete %s: %v", id, err)
			}
		}
	}
}

// settle waits until every event owner's earlier writes told has arrived,
// by a root framing written after them on the same stream, and drops them.
func (f *parityFix) settle(o parityOwner) {
	f.t.Helper()
	f.marks++
	zoom := 1 + float64(f.marks)/64
	if _, err := f.cl.SetFraming(f.ctx, &gridwellv1.SetFramingRequest{RootGridId: f.root[o], Cx: 0.5, Cy: 0.5, Zoom: zoom}); err != nil {
		f.t.Fatalf("mark %s: %v", o, err)
	}
	for {
		select {
		case ev := <-f.events:
			if fr := ev.GetGridFramingChanged(); fr != nil && fr.GetGridId() == f.root[o] && fr.GetViewZoom() == zoom {
				return
			}
		case <-f.ctx.Done():
			f.t.Fatalf("the mark on %s never arrived", o)
		}
	}
}

// told reports whether an open view of one of grids, or one holding a tile of
// ids, learned a change before the wait ran out, as client/events routes it.
func (f *parityFix) told(ids, grids []string) bool {
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-f.events:
			if tc := ev.GetTileChanged().GetTile(); tc != nil && (slices.Contains(ids, tc.Id) || slices.Contains(grids, tc.GridId)) {
				return true
			}
			if g := events.Route(ev).Fetch; g != "" && slices.Contains(grids, g) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// draw is the client's verdict on the row: dead or not, the banner's hue and
// words, and whether the outside treatment agrees with who owns the address
// the row opens, which is the one difference the promise allows a draw.
func (f *parityFix) draw(t *gridwellv1.Tile) string {
	host := false
	if g, err := f.cl.GetGrid(f.ctx, t.GridId); err == nil {
		host = g.Grid.GetHostContent()
	}
	dead := deadlink.DeadTile(t, f.plugins, localNodeID, func(id string) bool {
		var err error
		if t.LinkTargetId != "" {
			_, err = f.cl.GetTile(f.ctx, id)
		} else {
			_, err = f.cl.GetGrid(f.ctx, id)
		}
		return clientsync.Of(err) == clientsync.OutcomeDead
	})
	outside := tilepreview.Outside(t, host)
	elsewhere := rpc.IsDoorKind(t.Kind) && t.ChildGridId != "" && rpc.NamespaceOf(t.ChildGridId) != rpc.NamespaceOf(t.GridId)
	return fmt.Sprintf("dead=%v outside-as-its-address=%v hue=%d banner=%q",
		dead, outside == elsewhere, tilepreview.BannerHue(t, outside), tilepreview.BannerText(t))
}

// observe is a write's observable: its answer, and once it landed the row
// read back against before (nil for a new row), its draw, and, when the
// write told, whether a view of its grid heard.
func (f *parityFix) observe(before *gridwellv1.Tile, id string, err error, tells bool) observable {
	obs := observable{Outcome: clientsync.Of(err).String()}
	if err != nil {
		return obs
	}
	after, err := f.cl.GetTile(f.ctx, id)
	if err != nil {
		obs.Row = map[string]string{"read back": clientsync.Of(err).String()}
		return obs
	}
	obs.Row, obs.Draw = parityDelta(before, after), f.draw(after)
	if tells {
		grids := []string{after.GridId}
		if before != nil {
			grids = append(grids, before.GridId)
		}
		obs.Told = f.told([]string{id}, grids)
	}
	return obs
}

// paritySource is a plugin over one small source: a text, a url, a page and a
// door onto a grid holding one text, plus an empty grid, every one placed
// where home's twin is.
type paritySource struct {
	pluginv1.UnimplementedPluginServer
	mu      sync.Mutex
	grids   map[string][]*pluginv1.Entry
	bodies  map[string]string
	stamp   int
	changes chan *pluginv1.Change
	moves   bool // declares and answers Move (plugin_move_test.go)
}

func newParitySource() *paritySource {
	at := func(x, y int64) *pluginv1.PlacementHint { return &pluginv1.PlacementHint{X: x, Y: y, W: 1, H: 1} }
	return &paritySource{
		grids: map[string][]*pluginv1.Entry{
			"all": {
				{Key: "t", Kind: rpc.KindText, Label: "Body", ContentStamp: "s0", PlacementHint: at(1, 1)},
				{Key: "u", Kind: rpc.KindURL, Label: "Site", UrlString: "https://example.com/u", PlacementHint: at(2, 1)},
				{Key: "p", Kind: rpc.KindURL, Label: "Page", ServesPage: true, PlacementHint: at(2, 2)},
				{Key: "d", Kind: rpc.KindDoor, Label: "Door", ChildGrid: "inner", PlacementHint: at(5, 1)},
			},
			"inner": {{Key: "it", Kind: rpc.KindText, Label: "Inner", ContentStamp: "s0", PlacementHint: at(0, 0)}},
			"other": {},
		},
		bodies:  map[string]string{"t": "Body", "it": "Inner"},
		changes: make(chan *pluginv1.Change, 64),
	}
}

func (s *paritySource) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &pluginv1.InfoResponse{Kind: "parity", DisplayName: "parity", Watch: true, Writable: true, Move: s.moves,
		Grids: []*pluginv1.PluginGrid{{Id: "all", Label: "All", Grid: "all"}, {Id: "other", Label: "Other", Grid: "other"}}}, nil
}

func (s *paritySource) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &pluginv1.ListResponse{Authoritative: true}
	for _, e := range s.grids[req.Grid] {
		resp.Entries = append(resp.Entries, proto.CloneOf(e))
	}
	return resp, nil
}

// entry is key's entry and the grid listing it; the caller holds mu.
func (s *paritySource) entry(key string) (*pluginv1.Entry, string) {
	for g, es := range s.grids {
		for _, e := range es {
			if e.Key == key {
				return e, g
			}
		}
	}
	return nil, ""
}

func (s *paritySource) ReadContent(req *pluginv1.ReadContentRequest, stream grpc.ServerStreamingServer[pluginv1.ContentChunk]) error {
	s.mu.Lock()
	body, ok := s.bodies[req.Key]
	e, _ := s.entry(req.Key)
	s.mu.Unlock()
	if !ok || e == nil {
		return status.Errorf(codes.NotFound, "no body %q", req.Key)
	}
	return stream.Send(&pluginv1.ContentChunk{Data: []byte(body), MediaType: "text/markdown", ContentStamp: e.ContentStamp})
}

func (s *paritySource) WriteContent(stream grpc.ClientStreamingServer[pluginv1.WriteContentRequest, pluginv1.WriteContentResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	data := slices.Clone(first.Data)
	for {
		next, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		data = append(data, next.Data...)
	}
	s.mu.Lock()
	e, _ := s.entry(first.Key)
	if e == nil || e.ContentStamp != first.ContentStamp {
		s.mu.Unlock()
		return status.Errorf(codes.FailedPrecondition, "%q moved past stamp %q", first.Key, first.ContentStamp)
	}
	s.stamp++
	e.ContentStamp, s.bodies[first.Key] = fmt.Sprintf("s%d", s.stamp), string(data)
	stamp := e.ContentStamp
	s.mu.Unlock()
	return stream.SendAndClose(&pluginv1.WriteContentResponse{ContentStamp: stamp})
}

func (s *paritySource) ServeContent(req *pluginv1.ServeContentRequest, stream grpc.ServerStreamingServer[pluginv1.ServeContentChunk]) error {
	return stream.Send(&pluginv1.ServeContentChunk{Status: 200, MediaType: "text/html", Data: []byte("<p>" + req.Key + "</p>")})
}

func (s *paritySource) Probe(_ context.Context, req *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, g := s.entry(req.Key); e != nil && (req.Grid == "" || req.Grid == g) {
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_PRESENT}, nil
	}
	return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_GONE}, nil
}

func (s *paritySource) Find(_ context.Context, req *pluginv1.FindRequest) (*pluginv1.FindResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := map[string][]string{"all": {"all"}, "inner": {"all", "inner"}, "other": {"other"}}
	resp := &pluginv1.FindResponse{}
	for g, es := range s.grids {
		for _, e := range es {
			if strings.Contains(e.Label, req.Query) || strings.Contains(s.bodies[e.Key], req.Query) {
				resp.Results = append(resp.Results, &pluginv1.FindResult{Entry: proto.CloneOf(e), GridPath: path[g]})
			}
		}
	}
	return resp, nil
}

func (s *paritySource) Watch(_ *pluginv1.WatchRequest, stream grpc.ServerStreamingServer[pluginv1.Change]) error {
	for {
		select {
		case ch := <-s.changes:
			if err := stream.Send(ch); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// change edits the source under mu and tells the node what moved.
func (s *paritySource) change(edit func() *pluginv1.Change) {
	s.mu.Lock()
	ch := edit()
	s.mu.Unlock()
	s.changes <- ch
}

// remove takes the entry an id names out of the source, as a file deleted.
func (s *paritySource) remove(id string) {
	_, key, _, _ := rpc.SplitEntryID(id)
	s.change(func() *pluginv1.Change {
		_, g := s.entry(key)
		s.grids[g] = slices.DeleteFunc(s.grids[g], func(e *pluginv1.Entry) bool { return e.Key == key })
		return &pluginv1.Change{Payload: &pluginv1.Change_GridChanged{GridChanged: &pluginv1.GridChanged{Grid: g}}}
	})
}

// dropGrid takes a grid and the door opening it out of the source.
func (s *paritySource) dropGrid(grid string) {
	s.change(func() *pluginv1.Change {
		delete(s.grids, grid)
		s.grids["all"] = slices.DeleteFunc(s.grids["all"], func(e *pluginv1.Entry) bool { return e.ChildGrid == grid })
		return &pluginv1.Change{Payload: &pluginv1.Change_GridChanged{GridChanged: &pluginv1.GridChanged{Grid: "all"}}}
	})
}

// listing is a grid as a view lists it: the names of its tiles, sorted.
func (f *parityFix) listing(gridID string) (string, error) {
	g, err := f.cl.GetGrid(f.ctx, gridID)
	if err != nil {
		return "", err
	}
	var names []string
	for _, tl := range g.Tiles {
		names = append(names, tl.AltText)
	}
	slices.Sort(names)
	return strings.Join(names, ","), nil
}

// content is what a descent into t reads: the grid a door opens, the address
// a url goes live at (urlview.Address), the bytes a content tile holds, the
// row itself for a shell; a leaf link's are its target row's (contentrow.Of).
func (f *parityFix) content(t *gridwellv1.Tile) (string, error) {
	if rpc.LeafLink(t) {
		target, err := f.cl.GetTile(f.ctx, t.LinkTargetId)
		if err != nil {
			return "", err
		}
		row, _ := contentrow.Of(t, func(string) *gridwellv1.Tile { return target }, false)
		if rpc.KindOf(row.Kind).Content != rpc.ContentAddress {
			data, _, _, err := f.cl.ReadContent(f.ctx, t.Id)
			return string(data), err
		}
		t = row
	}
	switch k := rpc.KindOf(t.Kind); {
	case k.Descends == rpc.DescendGrid:
		return f.listing(t.ChildGridId)
	case k.Content == rpc.ContentAddress:
		return urlview.Address(rpc.PageContent(t), rpc.PageURL("", f.token, t.Id), t.UrlString), nil
	case k.Content != rpc.ContentNone:
		data, _, _, err := f.cl.ReadContent(f.ctx, t.Id)
		return string(data), err
	}
	_, err := f.cl.GetTile(f.ctx, t.Id)
	return "", err
}

// checkDescend reads the row and what it opens onto.
func checkDescend(f *parityFix, o parityOwner, k parityKind) observable {
	t := f.tile(o, k)
	row, err := f.cl.GetTile(f.ctx, t.Id)
	if err != nil {
		return observable{Outcome: clientsync.Of(err).String()}
	}
	read, err := f.content(row)
	return observable{Outcome: clientsync.Of(err).String(), Row: parityDelta(nil, row), Draw: f.draw(row), Read: read}
}

// checkRead reads the content a tile holds.
func checkRead(f *parityFix, o parityOwner, k parityKind) observable {
	t := f.tile(o, k)
	data, _, _, err := f.cl.ReadContent(f.ctx, t.Id)
	return observable{Outcome: clientsync.Of(err).String(), Read: string(data)}
}

// checkFind finds the tile on its grid by its kind, which the node filters,
// and by its name, which its namespace answers.
func checkFind(f *parityFix, o parityOwner, k parityKind) observable {
	t := f.tile(o, k)
	hit := func(req *gridwellv1.FindRequest) (string, error) {
		resp, err := f.raw.Find(f.ctx, connect.NewRequest(req))
		if err != nil {
			return "", err
		}
		for _, r := range resp.Msg.Results {
			if r.Tile.GetId() == t.Id {
				return "hit", nil
			}
		}
		return "miss", nil
	}
	byKind, err := hit(&gridwellv1.FindRequest{GridId: f.root[o], Kind: t.Kind})
	if err != nil {
		return observable{Outcome: clientsync.Of(err).String()}
	}
	byName := "unnamed"
	if t.AltText != "" {
		if byName, err = hit(&gridwellv1.FindRequest{Query: t.AltText, GridId: f.root[o]}); err != nil {
			return observable{Outcome: clientsync.Of(err).String()}
		}
	}
	return observable{Outcome: clientsync.OutcomeOK.String(), Read: "kind " + byKind + ", name " + byName}
}

// checkLink links the tile onto home, as a ctrl + right-drag does, and reads
// through the link.
func checkLink(f *parityFix, o parityOwner, k parityKind) observable {
	t := f.tile(o, k)
	x, y := f.drop(o)
	link := &gridwellv1.Tile{Kind: t.Kind, X: x, Y: y, W: 1, H: 1, AltText: t.AltText, Named: t.Named}
	if rpc.IsDoorKind(t.Kind) {
		link.ChildGridId = t.ChildGridId
	} else {
		link.LinkTargetId = t.Id
	}
	f.settle(onHome)
	made, err := f.cl.CreateTile(f.ctx, &gridwellv1.CreateTileRequest{GridId: f.root[onHome], Tile: link})
	if err != nil {
		return observable{Outcome: clientsync.Of(err).String()}
	}
	obs := f.dropped(f.observe(nil, made.Id, nil, true), x, y)
	read, err := f.content(made)
	obs.Read = read + " / " + clientsync.Of(err).String()
	return obs
}

// drop is the cell on home a gesture from owner's tile lands on, one per
// owner, since home's run and the owner's share home's root.
func (f *parityFix) drop(o parityOwner) (x, y int64) {
	return 0, map[parityOwner]int64{onHome: 4, onPlugin: 5, onFar: 6}[o]
}

// dropped says a new row sits where its gesture dropped it.
func (f *parityFix) dropped(obs observable, x, y int64) observable {
	if obs.Row["place"] == fmt.Sprint(x, y, 1, 1) {
		obs.Row["place"] = "the dropped cell"
	}
	return obs
}

// write runs one write on owner's tile of kind and observes it, answering
// the tile too.
func (f *parityFix) write(o parityOwner, k parityKind, w func(t *gridwellv1.Tile) error) (observable, *gridwellv1.Tile) {
	t := f.tile(o, k)
	before, err := f.cl.GetTile(f.ctx, t.Id)
	if err != nil {
		f.t.Fatal(err)
	}
	f.settle(o)
	return f.observe(before, t.Id, w(before), true), t
}

// checkCreate makes the tile as the user does.
func checkCreate(f *parityFix, o parityOwner, k parityKind) observable {
	f.settle(o)
	t := f.tile(o, k)
	return f.observe(nil, t.Id, nil, true)
}

// checkMove moves the tile to another grid of its owner.
func checkMove(f *parityFix, o parityOwner, k parityKind) observable {
	obs, _ := f.write(o, k, func(t *gridwellv1.Tile) error {
		_, err := f.cl.MoveTile(f.ctx, &gridwellv1.MoveTileRequest{TileId: t.Id, GridId: f.away[o], X: 2, Y: 2, W: 1, H: 1, Claim: rpc.PlacementOf(t)})
		return err
	})
	return obs
}

func checkRename(f *parityFix, o parityOwner, k parityKind) observable {
	obs, _ := f.write(o, k, func(t *gridwellv1.Tile) error {
		_, err := f.cl.RenameTile(f.ctx, t.Id, t.Version, "Renamed")
		return err
	})
	return obs
}

func checkFreeze(f *parityFix, o parityOwner, k parityKind) observable {
	obs, _ := f.write(o, k, func(t *gridwellv1.Tile) error {
		_, err := f.cl.SetFrozen(f.ctx, t.Id, true, false)
		return err
	})
	return obs
}

// checkScreenshot keeps a capture as a closing view writes it
// (urlview.Writeback), or as a shell's capture.
func checkScreenshot(f *parityFix, o parityOwner, k parityKind) observable {
	obs, _ := f.write(o, k, func(t *gridwellv1.Tile) error {
		capture := urlview.Capture{JPEG: []byte("\xff\xd8 picture"), Title: "Page title", History: "https://example.com/u"}
		if rpc.KindOf(t.Kind).Live == rpc.DescentURL {
			g, err := f.cl.GetGrid(f.ctx, t.GridId)
			if err != nil {
				return err
			}
			capture, _ = urlview.Writeback(true, urlview.Owns(rpc.PageContent(t), g.Grid.GetAcceptsTiles(), true), capture)
		}
		_, err := f.cl.SetTile(f.ctx, &gridwellv1.SetTileRequest{TileId: t.Id, Preview: capture.JPEG,
			Tile: &gridwellv1.Tile{Kind: t.Kind, AltText: capture.Title, UrlHistory: capture.History}})
		return err
	})
	return obs
}

func checkZoom(f *parityFix, o parityOwner, k parityKind) observable {
	obs, _ := f.write(o, k, func(t *gridwellv1.Tile) error {
		_, err := f.cl.SetContentZoom(f.ctx, t.Id, 1.5)
		return err
	})
	return obs
}

func checkScroll(f *parityFix, o parityOwner, k parityKind) observable {
	obs, _ := f.write(o, k, func(t *gridwellv1.Tile) error {
		_, err := f.cl.SetTile(f.ctx, &gridwellv1.SetTileRequest{TileId: t.Id,
			Tile: &gridwellv1.Tile{Kind: t.Kind, TextY: 40, TextMode: rpc.TextModeText}})
		return err
	})
	return obs
}

func checkFraming(f *parityFix, o parityOwner, k parityKind) observable {
	obs, _ := f.write(o, k, func(t *gridwellv1.Tile) error {
		_, err := f.cl.SetFraming(f.ctx, &gridwellv1.SetFramingRequest{TileId: t.Id, Cx: 3, Cy: 4, Zoom: 2})
		return err
	})
	return obs
}

// parityEdits is the content each kind's write puts, the edit a user makes.
var parityEdits = map[parityKind]string{
	kText:      "Body edited",
	kURL:       "https://example.com/v",
	kWorkspace: `{"v":1,"root":{"pane":{"id":"p2","zoom":1}},"focus":"p2"}`,
}

// checkWrite saves an edit claiming the basis it read, then reads it back.
func checkWrite(f *parityFix, o parityOwner, k parityKind) observable {
	obs, t := f.write(o, k, func(t *gridwellv1.Tile) error {
		_, _, basis, err := f.cl.ReadContent(f.ctx, t.Id)
		if err != nil {
			return err
		}
		_, err = f.cl.WriteContent(f.ctx, t.Id, basis, []byte(parityEdits[k]))
		return err
	})
	if obs.Outcome == clientsync.OutcomeOK.String() {
		data, _, _, err := f.cl.ReadContent(f.ctx, t.Id)
		obs.Read = string(data) + " / " + clientsync.Of(err).String()
	}
	return obs
}

// add lists one more entry in a grid.
func (s *paritySource) add(grid string, e *pluginv1.Entry) {
	s.change(func() *pluginv1.Change {
		s.grids[grid] = append(s.grids[grid], e)
		return &pluginv1.Change{Payload: &pluginv1.Change_GridChanged{GridChanged: &pluginv1.GridChanged{Grid: grid}}}
	})
}

// edit changes an entry in place, a text's body or a url's address, as a
// file written.
func (s *paritySource) edit(key, value string) {
	s.change(func() *pluginv1.Change {
		e, g := s.entry(key)
		if e.Kind == rpc.KindURL {
			e.UrlString = value
		} else {
			s.stamp++
			e.ContentStamp, s.bodies[key] = fmt.Sprintf("s%d", s.stamp), value
		}
		return &pluginv1.Change{Payload: &pluginv1.Change_EntryChanged{EntryChanged: &pluginv1.EntryChanged{Grid: g, Entry: proto.CloneOf(e)}}}
	})
}

// checkDelete deletes the tile as the trashcan drop does and reads it back.
func checkDelete(f *parityFix, o parityOwner, k parityKind) observable {
	obs, _ := f.write(o, k, func(t *gridwellv1.Tile) error {
		_, err := f.cl.DeleteTile(f.ctx, &gridwellv1.DeleteTileRequest{TileId: t.Id, ClaimVersion: t.Version, Claim: rpc.PlacementOf(t)})
		return err
	})
	return obs
}

// checkCopy copies the tile onto home, as a right-drag does, after leaving
// on it every fact a copy could carry, and reads the copy.
func checkCopy(f *parityFix, o parityOwner, k parityKind) observable {
	t := f.tile(o, k)
	caps := rpc.KindOf(t.Kind)
	if caps.Descends == rpc.DescendContent {
		if _, err := f.cl.SetContentZoom(f.ctx, t.Id, 1.5); err != nil {
			f.t.Fatal(err)
		}
	}
	if caps.Content == rpc.ContentBody {
		if _, err := f.cl.SetTile(f.ctx, &gridwellv1.SetTileRequest{TileId: t.Id,
			Tile: &gridwellv1.Tile{Kind: t.Kind, TextY: 40, TextMode: rpc.TextModeText}}); err != nil {
			f.t.Fatal(err)
		}
	}
	if caps.Freezes() {
		if _, err := f.cl.SetFrozen(f.ctx, t.Id, true, false); err != nil {
			f.t.Fatal(err)
		}
	}
	if caps.Descends == rpc.DescendGrid && !t.Link {
		if _, err := f.cl.SetFraming(f.ctx, &gridwellv1.SetFramingRequest{TileId: t.Id, Cx: 3, Cy: 4, Zoom: 2}); err != nil {
			f.t.Fatal(err)
		}
	}
	x, y := f.drop(o)
	f.settle(onHome)
	made, err := f.cl.CopyTile(f.ctx, &gridwellv1.CopyTileRequest{TileId: t.Id, DestGridId: f.root[onHome], X: x, Y: y})
	if err != nil {
		return observable{Outcome: clientsync.Of(err).String()}
	}
	obs := f.dropped(f.observe(nil, made.Id, nil, true), x, y)
	read, err := f.content(made)
	obs.Read = read + " / " + clientsync.Of(err).String()
	return obs
}

// change is the tile's source changing it: home's or the far node's own
// writer, or the plugin's source, each in its own words. A door's change is
// a tile more on the grid it opens.
func (f *parityFix) change(t *gridwellv1.Tile) {
	f.t.Helper()
	if rpc.IsDoorKind(t.Kind) {
		if src := f.src[rpc.NamespaceOf(t.ChildGridId)]; src != nil {
			gkey, _, _, _ := rpc.SplitEntryID(rpc.LocalOf(t.ChildGridId))
			src.add(gkey, &pluginv1.Entry{Key: "more", Kind: rpc.KindText, Label: "More", PlacementHint: &pluginv1.PlacementHint{X: 3, Y: 3, W: 1, H: 1}})
			return
		}
		n, grid := f.holder(t.ChildGridId)
		if _, err := n.cl.CreateWithContent(f.ctx, &gridwellv1.CreateTileRequest{GridId: grid,
			Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 3, Y: 3, W: 1, H: 1}}, []byte("More")); err != nil {
			f.t.Fatal(err)
		}
		return
	}
	if src := f.src[rpc.NamespaceOf(t.Id)]; src != nil {
		_, key, _, _ := rpc.SplitEntryID(rpc.LocalOf(t.Id))
		src.edit(key, parityEdits[parityKind(t.Kind)])
		return
	}
	if t.Kind == rpc.KindShell {
		f.detach(t.Id, "vim")
		return
	}
	n, id := f.holder(t.Id)
	_, _, basis, err := n.cl.ReadContent(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := n.cl.WriteContent(f.ctx, id, basis, []byte(parityEdits[parityKind(t.Kind)])); err != nil {
		f.t.Fatal(err)
	}
}

// holder is the node holding id, and id in its frame.
func (f *parityFix) holder(id string) (parityNode, string) {
	n, at := f.nodes[""], id
	for prefix, w := range f.nodes {
		if bare, ok := strings.CutPrefix(id, prefix); ok && len(bare) < len(at) {
			n, at = w, bare
		}
	}
	return n, at
}

// detach attaches to a shell on the node holding it and detaches, which is
// when the node names the shell by its foreground command.
func (f *parityFix) detach(id, command string) {
	f.t.Helper()
	n, at := f.holder(id)
	n.shell.PaneCmd = command
	out := make(chan []byte, 8)
	reg := shellstream.New(shellws.Dialer(shellws.Options{Origin: n.web.URL, HTTPClient: n.web.Client()}),
		func(_ string, b []byte) { out <- b }, func(shellstream.Exit) {})
	reg.Open(at, at, 80, 24)
	reg.Write(at, []byte("x"))
	select {
	case <-out:
	case <-f.ctx.Done():
		f.t.Fatalf("the shell %s never attached", id)
	}
	reg.Close(at)
}

// checkUpdate opens a view of the tile, has the source change it, and sees
// whether the view learns, and what it then reads.
func checkUpdate(f *parityFix, o parityOwner, k parityKind) observable {
	t := f.tile(o, k)
	f.settle(o)
	before, err := f.cl.GetTile(f.ctx, t.Id)
	if err != nil {
		f.t.Fatal(err)
	}
	// The view is open: it has read what the tile shows.
	if _, err := f.content(before); err != nil {
		f.t.Fatal(err)
	}
	f.change(before)
	grids := []string{before.GridId}
	if before.ChildGridId != "" {
		grids = append(grids, before.ChildGridId)
	}
	told := f.told([]string{t.Id}, grids)
	obs := f.observe(before, t.Id, nil, false)
	obs.Told = told
	after, err := f.cl.GetTile(f.ctx, t.Id)
	if err != nil {
		f.t.Fatal(err)
	}
	read, err := f.content(after)
	obs.Read = read + " / " + clientsync.Of(err).String()
	return obs
}

// checkDead breaks the path to the tile and draws a link to it: the tile
// itself when it is a link, else one made onto home.
func checkDead(f *parityFix, o parityOwner, k parityKind) observable {
	t := f.tile(o, k)
	link := t
	if !t.Link {
		x, y := f.drop(o)
		l := &gridwellv1.Tile{Kind: t.Kind, X: x, Y: y, W: 1, H: 1, AltText: t.AltText, Named: t.Named}
		if rpc.IsDoorKind(t.Kind) {
			l.ChildGridId = t.ChildGridId
		} else {
			l.LinkTargetId = t.Id
		}
		var err error
		if link, err = f.cl.CreateTile(f.ctx, &gridwellv1.CreateTileRequest{GridId: f.root[onHome], Tile: l}); err != nil {
			f.t.Fatal(err)
		}
	}
	unbroken, _ := f.content(link)
	f.breaks[t.Id]()
	if mend := f.mends[t.Id]; mend != nil {
		defer mend()
	}
	// The break reaches a plugin's listing by its Watch, so the read is
	// asked until it answers something else.
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err := f.cl.GetTile(f.ctx, link.Id)
		if err != nil {
			return observable{Outcome: clientsync.Of(err).String()}
		}
		read, rerr := f.content(row)
		obs := observable{Outcome: clientsync.OutcomeOK.String(), Draw: f.draw(row), Read: clientsync.Of(rerr).String()}
		if read != unbroken || rerr != nil || time.Now().After(deadline) {
			return obs
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// checkServe asks the content API for a page. Home serves none, so its
// reference is the local plugin's page; the far run asks the far node's.
func checkServe(f *parityFix, o parityOwner, k parityKind) observable {
	ns := parityPluginUUID
	if o == onFar {
		ns = localNodeID + "/geneva/" + parityFarPluginUUID
	}
	g, err := f.cl.GetGrid(f.ctx, rpc.QualifyID(ns, rpc.EntryGridID("all")))
	if err != nil {
		return observable{Outcome: clientsync.Of(err).String()}
	}
	for _, t := range g.Tiles {
		if !rpc.PageContent(t) {
			continue
		}
		resp, err := f.h.localHTTP.Client().Get(urlview.Address(true, rpc.PageURL(f.h.localURL, f.token, t.Id), t.UrlString))
		if err != nil {
			return observable{Outcome: clientsync.Of(err).String()}
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			f.t.Fatal(err)
		}
		return observable{Outcome: clientsync.OutcomeOK.String(), Draw: f.draw(t), Read: fmt.Sprintf("%d %s", resp.StatusCode, body)}
	}
	f.t.Fatalf("%s lists no page", ns)
	return observable{}
}
