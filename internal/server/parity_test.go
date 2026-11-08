package server_test

// Parity: a tile behaves the same wherever its content comes from (CLAUDE.md,
// promises). parityTable is kind × operation × owner, each cell classified
// from the code. Home is the reference every cell is held to, so it has no
// column of its own: a same cell's check runs the operation against home and
// against the owner, and parityOracle compares the two observables. The count,
// the holes plus the same cells with no check, is held at zero by
// test/boundary's TestParityIsZero, which reads it off TestParity/count.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/contentrow"
	"github.com/josephburnett/gridwell/client/deadlink"
	"github.com/josephburnett/gridwell/client/events"
	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellws"
	"github.com/josephburnett/gridwell/client/tilepreview"
	"github.com/josephburnett/gridwell/client/urlview"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/files"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/server"
)

type parityKind string

// The kinds, and the three grids a door can open onto as seen from the node
// holding it: one of its home's, one of its directories, a far home it
// reaches.
const (
	kText      parityKind = "text"
	kURL       parityKind = "url"
	kShell     parityKind = "shell"
	kWorkspace parityKind = "workspace"
	kDoor      parityKind = "door"
	kDoorHome  parityKind = "door to a home grid"
	kDoorDir   parityKind = "door to a directory"
	kDoorFar   parityKind = "door to a far home"
)

var parityKinds = []parityKind{kText, kURL, kShell, kWorkspace, kDoor, kDoorHome, kDoorDir, kDoorFar}

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
	onHome  parityOwner = "home"
	onFiles parityOwner = "files"
	onFar   parityOwner = "far"
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
	naServe   = "only a url tile serving its page has a page to serve (rpc.PageContent)"
)

// What the files cannot hold.
const (
	naFilesKind     = "the files hold directories, text files, page files and symlinks only, so no shell and no workspace (files.entryOf)"
	naFilesTextName = "a text file's name is its file name, which its presentation reads, so nothing renames it (store.Namespace.Rename)"
	naFilesGrids    = "a directory's doors are its subdirectories and its symlinks, which land under a root only, so none opens a home's or a far node's grid (files.linkFacts)"
)

// The differences CLAUDE.md decides, quoted.
const (
	dFilesCreates  = "A directory's grid takes no new tile but a text, which is made a markdown file on disk"
	dFilesMakes    = "A text tile copied into a directory, by a right-drag or `copy`, or created there, as `write_text` does, is a new markdown file holding its body"
	dFilesDelete   = "Delete moves the file to the OS trash, by a rename"
	dFilesDoorCopy = "A directory's door never copies"
	dFilesPageCopy = "a page file, whose page is the node's, copies as a link"
	dFilesPath     = "a renamed or moved file retires its old id and a link to it reads dead"
	dFilesPage     = "The node serves a page file's page through its content API"
	dPageAddress   = "A page the node serves has no address of its own"
	dPageCapture   = "a capture of it keeps the screenshot alone, in place of an image's own picture: its title is the file's name and it has no history"
	dFilesName     = "A file's name is its file name until a rename"
	dFilesFind     = "a text query answers empty: no body is read for it."
	dShellCopy     = "A shell copied across nodes lands as a link"
	dTextName      = "A text tile's name stays the file's, since its presentation reads it"
	dLinkDead      = "Either is dashed, opens the target, and is dead when the target is gone or outside every root"
)

var parityTable = []parityCell{
	{kText, opCreate, onFiles, decided, dFilesMakes, nil},
	{kText, opCreate, onFar, same, "", checkCreate},
	{kText, opMove, onFiles, decided, dFilesPath, nil},
	{kText, opMove, onFar, same, "", checkMove},
	{kText, opCopy, onFiles, same, "", checkCopy},
	{kText, opCopy, onFar, same, "", checkCopy},
	{kText, opLink, onFiles, same, "", checkLink},
	{kText, opLink, onFar, same, "", checkLink},
	{kText, opDelete, onFiles, decided, dFilesDelete, nil},
	{kText, opDelete, onFar, same, "", checkDelete},
	{kText, opRename, onFiles, na, naFilesTextName, nil},
	{kText, opRename, onFar, na, naName, nil},
	{kText, opDescend, onFiles, same, "", checkDescend},
	{kText, opDescend, onFar, same, "", checkDescend},
	{kText, opFreeze, onFiles, na, naLive, nil},
	{kText, opFreeze, onFar, na, naLive, nil},
	{kText, opScreenshot, onFiles, na, naLive, nil},
	{kText, opScreenshot, onFar, na, naLive, nil},
	{kText, opZoom, onFiles, same, "", checkZoom},
	{kText, opZoom, onFar, same, "", checkZoom},
	{kText, opScroll, onFiles, same, "", checkScroll},
	{kText, opScroll, onFar, same, "", checkScroll},
	{kText, opFraming, onFiles, na, naFraming, nil},
	{kText, opFraming, onFar, na, naFraming, nil},
	{kText, opRead, onFiles, same, "", checkRead},
	{kText, opRead, onFar, same, "", checkRead},
	{kText, opWrite, onFiles, decided, dTextName, nil},
	{kText, opWrite, onFar, same, "", checkWrite},
	{kText, opServe, onFiles, na, naServe, nil},
	{kText, opServe, onFar, na, naServe, nil},
	{kText, opFind, onFiles, decided, dFilesFind, nil},
	{kText, opFind, onFar, same, "", checkFind},
	{kText, opUpdate, onFiles, decided, dTextName, nil},
	{kText, opUpdate, onFar, same, "", checkUpdate},
	{kText, opDead, onFiles, same, "", checkDead},
	{kText, opDead, onFar, same, "", checkDead},
	{kURL, opCreate, onFiles, decided, dFilesCreates, nil},
	{kURL, opCreate, onFar, same, "", checkCreate},
	{kURL, opMove, onFiles, decided, dFilesPath, nil},
	{kURL, opMove, onFar, same, "", checkMove},
	{kURL, opCopy, onFiles, decided, dFilesPageCopy, nil},
	{kURL, opCopy, onFar, same, "", checkCopy},
	{kURL, opLink, onFiles, decided, dFilesPage, nil},
	{kURL, opLink, onFar, same, "", checkLink},
	{kURL, opDelete, onFiles, decided, dFilesDelete, nil},
	{kURL, opDelete, onFar, same, "", checkDelete},
	{kURL, opRename, onFiles, same, "", checkRename},
	{kURL, opRename, onFar, same, "", checkRename},
	{kURL, opDescend, onFiles, decided, dFilesPage, nil},
	{kURL, opDescend, onFar, same, "", checkDescend},
	{kURL, opFreeze, onFiles, same, "", checkFreeze},
	{kURL, opFreeze, onFar, same, "", checkFreeze},
	{kURL, opScreenshot, onFiles, decided, dPageCapture, nil},
	{kURL, opScreenshot, onFar, same, "", checkScreenshot},
	{kURL, opZoom, onFiles, same, "", checkZoom},
	{kURL, opZoom, onFar, same, "", checkZoom},
	{kURL, opScroll, onFiles, na, naScroll, nil},
	{kURL, opScroll, onFar, na, naScroll, nil},
	{kURL, opFraming, onFiles, na, naFraming, nil},
	{kURL, opFraming, onFar, na, naFraming, nil},
	{kURL, opRead, onFiles, decided, dPageAddress, nil},
	{kURL, opRead, onFar, same, "", checkRead},
	{kURL, opWrite, onFiles, decided, dPageAddress, nil},
	{kURL, opWrite, onFar, same, "", checkWrite},
	{kURL, opServe, onFiles, decided, dFilesPage, nil},
	{kURL, opServe, onFar, same, "", checkServe},
	{kURL, opFind, onFiles, decided, dFilesFind, nil},
	{kURL, opFind, onFar, same, "", checkFind},
	{kURL, opUpdate, onFiles, decided, dPageAddress, nil},
	{kURL, opUpdate, onFar, same, "", checkUpdate},
	{kURL, opDead, onFiles, same, "", checkDead},
	{kURL, opDead, onFar, same, "", checkDead},
	{kShell, opCreate, onFiles, na, naFilesKind, nil},
	{kShell, opCreate, onFar, same, "", checkCreate},
	{kShell, opMove, onFiles, na, naFilesKind, nil},
	{kShell, opMove, onFar, same, "", checkMove},
	{kShell, opCopy, onFiles, na, naFilesKind, nil},
	{kShell, opCopy, onFar, decided, dShellCopy, nil},
	{kShell, opLink, onFiles, na, naFilesKind, nil},
	{kShell, opLink, onFar, same, "", checkLink},
	{kShell, opDelete, onFiles, na, naFilesKind, nil},
	{kShell, opDelete, onFar, same, "", checkDelete},
	{kShell, opRename, onFiles, na, naFilesKind, nil},
	{kShell, opRename, onFar, same, "", checkRename},
	{kShell, opDescend, onFiles, na, naFilesKind, nil},
	{kShell, opDescend, onFar, same, "", checkDescend},
	{kShell, opFreeze, onFiles, na, naFilesKind, nil},
	{kShell, opFreeze, onFar, same, "", checkFreeze},
	{kShell, opScreenshot, onFiles, na, naFilesKind, nil},
	{kShell, opScreenshot, onFar, same, "", checkScreenshot},
	{kShell, opZoom, onFiles, na, naFilesKind, nil},
	{kShell, opZoom, onFar, same, "", checkZoom},
	{kShell, opScroll, onFiles, na, naScroll, nil},
	{kShell, opScroll, onFar, na, naScroll, nil},
	{kShell, opFraming, onFiles, na, naFraming, nil},
	{kShell, opFraming, onFar, na, naFraming, nil},
	{kShell, opRead, onFiles, na, naContent, nil},
	{kShell, opRead, onFar, na, naContent, nil},
	{kShell, opWrite, onFiles, na, naContent, nil},
	{kShell, opWrite, onFar, na, naContent, nil},
	{kShell, opServe, onFiles, na, naServe, nil},
	{kShell, opServe, onFar, na, naServe, nil},
	{kShell, opFind, onFiles, na, naFilesKind, nil},
	{kShell, opFind, onFar, same, "", checkFind},
	{kShell, opUpdate, onFiles, na, naFilesKind, nil},
	{kShell, opUpdate, onFar, same, "", checkUpdate},
	{kShell, opDead, onFiles, na, naFilesKind, nil},
	{kShell, opDead, onFar, same, "", checkDead},
	{kWorkspace, opCreate, onFiles, na, naFilesKind, nil},
	{kWorkspace, opCreate, onFar, same, "", checkCreate},
	{kWorkspace, opMove, onFiles, na, naFilesKind, nil},
	{kWorkspace, opMove, onFar, same, "", checkMove},
	{kWorkspace, opCopy, onFiles, na, naFilesKind, nil},
	{kWorkspace, opCopy, onFar, same, "", checkCopy},
	{kWorkspace, opLink, onFiles, na, naFilesKind, nil},
	{kWorkspace, opLink, onFar, same, "", checkLink},
	{kWorkspace, opDelete, onFiles, na, naFilesKind, nil},
	{kWorkspace, opDelete, onFar, same, "", checkDelete},
	{kWorkspace, opRename, onFiles, na, naFilesKind, nil},
	{kWorkspace, opRename, onFar, same, "", checkRename},
	{kWorkspace, opDescend, onFiles, na, naFilesKind, nil},
	{kWorkspace, opDescend, onFar, same, "", checkDescend},
	{kWorkspace, opFreeze, onFiles, na, naLive, nil},
	{kWorkspace, opFreeze, onFar, na, naLive, nil},
	{kWorkspace, opScreenshot, onFiles, na, naLive, nil},
	{kWorkspace, opScreenshot, onFar, na, naLive, nil},
	{kWorkspace, opZoom, onFiles, na, naZoom, nil},
	{kWorkspace, opZoom, onFar, na, naZoom, nil},
	{kWorkspace, opScroll, onFiles, na, naScroll, nil},
	{kWorkspace, opScroll, onFar, na, naScroll, nil},
	{kWorkspace, opFraming, onFiles, na, naFraming, nil},
	{kWorkspace, opFraming, onFar, na, naFraming, nil},
	{kWorkspace, opRead, onFiles, na, naFilesKind, nil},
	{kWorkspace, opRead, onFar, same, "", checkRead},
	{kWorkspace, opWrite, onFiles, na, naFilesKind, nil},
	{kWorkspace, opWrite, onFar, same, "", checkWrite},
	{kWorkspace, opServe, onFiles, na, naServe, nil},
	{kWorkspace, opServe, onFar, na, naServe, nil},
	{kWorkspace, opFind, onFiles, na, naFilesKind, nil},
	{kWorkspace, opFind, onFar, same, "", checkFind},
	{kWorkspace, opUpdate, onFiles, na, naFilesKind, nil},
	{kWorkspace, opUpdate, onFar, same, "", checkUpdate},
	{kWorkspace, opDead, onFiles, na, naFilesKind, nil},
	{kWorkspace, opDead, onFar, same, "", checkDead},
	{kDoor, opCreate, onFiles, decided, dFilesCreates, nil},
	{kDoor, opCreate, onFar, same, "", checkCreate},
	{kDoor, opMove, onFiles, decided, dFilesPath, nil},
	{kDoor, opMove, onFar, same, "", checkMove},
	{kDoor, opCopy, onFiles, decided, dFilesDoorCopy, nil},
	{kDoor, opCopy, onFar, same, "", checkCopy},
	{kDoor, opLink, onFiles, same, "", checkLink},
	{kDoor, opLink, onFar, same, "", checkLink},
	{kDoor, opDelete, onFiles, decided, dFilesDelete, nil},
	{kDoor, opDelete, onFar, same, "", checkDelete},
	{kDoor, opRename, onFiles, same, "", checkRename},
	{kDoor, opRename, onFar, same, "", checkRename},
	{kDoor, opDescend, onFiles, same, "", checkDescend},
	{kDoor, opDescend, onFar, same, "", checkDescend},
	{kDoor, opFreeze, onFiles, na, naLive, nil},
	{kDoor, opFreeze, onFar, na, naLive, nil},
	{kDoor, opScreenshot, onFiles, na, naLive, nil},
	{kDoor, opScreenshot, onFar, na, naLive, nil},
	{kDoor, opZoom, onFiles, na, naZoom, nil},
	{kDoor, opZoom, onFar, na, naZoom, nil},
	{kDoor, opScroll, onFiles, na, naScroll, nil},
	{kDoor, opScroll, onFar, na, naScroll, nil},
	{kDoor, opFraming, onFiles, same, "", checkFraming},
	{kDoor, opFraming, onFar, same, "", checkFraming},
	{kDoor, opRead, onFiles, na, naContent, nil},
	{kDoor, opRead, onFar, na, naContent, nil},
	{kDoor, opWrite, onFiles, na, naContent, nil},
	{kDoor, opWrite, onFar, na, naContent, nil},
	{kDoor, opServe, onFiles, na, naServe, nil},
	{kDoor, opServe, onFar, na, naServe, nil},
	{kDoor, opFind, onFiles, decided, dFilesFind, nil},
	{kDoor, opFind, onFar, same, "", checkFind},
	{kDoor, opUpdate, onFiles, same, "", checkUpdate},
	{kDoor, opUpdate, onFar, same, "", checkUpdate},
	{kDoor, opDead, onFiles, same, "", checkDead},
	{kDoor, opDead, onFar, same, "", checkDead},
	{kDoorHome, opCreate, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opCreate, onFar, same, "", checkCreate},
	{kDoorHome, opMove, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opMove, onFar, same, "", checkMove},
	{kDoorHome, opCopy, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opCopy, onFar, same, "", checkCopy},
	{kDoorHome, opLink, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opLink, onFar, same, "", checkLink},
	{kDoorHome, opDelete, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opDelete, onFar, same, "", checkDelete},
	{kDoorHome, opRename, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opRename, onFar, same, "", checkRename},
	{kDoorHome, opDescend, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opDescend, onFar, same, "", checkDescend},
	{kDoorHome, opFreeze, onFiles, na, naLive, nil},
	{kDoorHome, opFreeze, onFar, na, naLive, nil},
	{kDoorHome, opScreenshot, onFiles, na, naLive, nil},
	{kDoorHome, opScreenshot, onFar, na, naLive, nil},
	{kDoorHome, opZoom, onFiles, na, naZoom, nil},
	{kDoorHome, opZoom, onFar, na, naZoom, nil},
	{kDoorHome, opScroll, onFiles, na, naScroll, nil},
	{kDoorHome, opScroll, onFar, na, naScroll, nil},
	{kDoorHome, opFraming, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opFraming, onFar, same, "", checkFraming},
	{kDoorHome, opRead, onFiles, na, naContent, nil},
	{kDoorHome, opRead, onFar, na, naContent, nil},
	{kDoorHome, opWrite, onFiles, na, naContent, nil},
	{kDoorHome, opWrite, onFar, na, naContent, nil},
	{kDoorHome, opServe, onFiles, na, naServe, nil},
	{kDoorHome, opServe, onFar, na, naServe, nil},
	{kDoorHome, opFind, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opFind, onFar, same, "", checkFind},
	{kDoorHome, opUpdate, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opUpdate, onFar, same, "", checkUpdate},
	{kDoorHome, opDead, onFiles, na, naFilesGrids, nil},
	{kDoorHome, opDead, onFar, same, "", checkDead},
	{kDoorDir, opCreate, onFiles, decided, dFilesCreates, nil},
	{kDoorDir, opCreate, onFar, same, "", checkCreate},
	{kDoorDir, opMove, onFiles, decided, dFilesPath, nil},
	{kDoorDir, opMove, onFar, same, "", checkMove},
	{kDoorDir, opCopy, onFiles, same, "", checkCopy},
	{kDoorDir, opCopy, onFar, same, "", checkCopy},
	{kDoorDir, opLink, onFiles, same, "", checkLink},
	{kDoorDir, opLink, onFar, same, "", checkLink},
	{kDoorDir, opDelete, onFiles, decided, dFilesDelete, nil},
	{kDoorDir, opDelete, onFar, same, "", checkDelete},
	{kDoorDir, opRename, onFiles, same, "", checkRename},
	{kDoorDir, opRename, onFar, same, "", checkRename},
	{kDoorDir, opDescend, onFiles, same, "", checkDescend},
	{kDoorDir, opDescend, onFar, same, "", checkDescend},
	{kDoorDir, opFreeze, onFiles, na, naLive, nil},
	{kDoorDir, opFreeze, onFar, na, naLive, nil},
	{kDoorDir, opScreenshot, onFiles, na, naLive, nil},
	{kDoorDir, opScreenshot, onFar, na, naLive, nil},
	{kDoorDir, opZoom, onFiles, na, naZoom, nil},
	{kDoorDir, opZoom, onFar, na, naZoom, nil},
	{kDoorDir, opScroll, onFiles, na, naScroll, nil},
	{kDoorDir, opScroll, onFar, na, naScroll, nil},
	{kDoorDir, opFraming, onFiles, same, "", checkFraming},
	{kDoorDir, opFraming, onFar, same, "", checkFraming},
	{kDoorDir, opRead, onFiles, na, naContent, nil},
	{kDoorDir, opRead, onFar, na, naContent, nil},
	{kDoorDir, opWrite, onFiles, na, naContent, nil},
	{kDoorDir, opWrite, onFar, na, naContent, nil},
	{kDoorDir, opServe, onFiles, na, naServe, nil},
	{kDoorDir, opServe, onFar, na, naServe, nil},
	{kDoorDir, opFind, onFiles, decided, dFilesFind, nil},
	{kDoorDir, opFind, onFar, same, "", checkFind},
	{kDoorDir, opUpdate, onFiles, same, "", checkUpdate},
	{kDoorDir, opUpdate, onFar, same, "", checkUpdate},
	{kDoorDir, opDead, onFiles, decided, dLinkDead, nil},
	{kDoorDir, opDead, onFar, same, "", checkDead},
	{kDoorFar, opCreate, onFiles, na, naFilesGrids, nil},
	{kDoorFar, opCreate, onFar, same, "", checkCreate},
	{kDoorFar, opMove, onFiles, na, naFilesGrids, nil},
	{kDoorFar, opMove, onFar, same, "", checkMove},
	{kDoorFar, opCopy, onFiles, na, naFilesGrids, nil},
	{kDoorFar, opCopy, onFar, same, "", checkCopy},
	{kDoorFar, opLink, onFiles, na, naFilesGrids, nil},
	{kDoorFar, opLink, onFar, same, "", checkLink},
	{kDoorFar, opDelete, onFiles, na, naFilesGrids, nil},
	{kDoorFar, opDelete, onFar, same, "", checkDelete},
	{kDoorFar, opRename, onFiles, na, naFilesGrids, nil},
	{kDoorFar, opRename, onFar, same, "", checkRename},
	{kDoorFar, opDescend, onFiles, na, naFilesGrids, nil},
	{kDoorFar, opDescend, onFar, same, "", checkDescend},
	{kDoorFar, opFreeze, onFiles, na, naLive, nil},
	{kDoorFar, opFreeze, onFar, na, naLive, nil},
	{kDoorFar, opScreenshot, onFiles, na, naLive, nil},
	{kDoorFar, opScreenshot, onFar, na, naLive, nil},
	{kDoorFar, opZoom, onFiles, na, naZoom, nil},
	{kDoorFar, opZoom, onFar, na, naZoom, nil},
	{kDoorFar, opScroll, onFiles, na, naScroll, nil},
	{kDoorFar, opScroll, onFar, na, naScroll, nil},
	{kDoorFar, opFraming, onFiles, na, naFilesGrids, nil},
	{kDoorFar, opFraming, onFar, same, "", checkFraming},
	{kDoorFar, opRead, onFiles, na, naContent, nil},
	{kDoorFar, opRead, onFar, na, naContent, nil},
	{kDoorFar, opWrite, onFiles, na, naContent, nil},
	{kDoorFar, opWrite, onFar, na, naContent, nil},
	{kDoorFar, opServe, onFiles, na, naServe, nil},
	{kDoorFar, opServe, onFar, na, naServe, nil},
	{kDoorFar, opFind, onFiles, na, naFilesGrids, nil},
	{kDoorFar, opFind, onFar, same, "", checkFind},
	{kDoorFar, opUpdate, onFiles, na, naFilesGrids, nil},
	{kDoorFar, opUpdate, onFar, same, "", checkUpdate},
	{kDoorFar, opDead, onFiles, na, naFilesGrids, nil},
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
		checkParityTable(t, append(slices.Clone(parityTable), parityVariantRows()...))
		t.Logf("parity count: %d", parityCount(parityTable)+parityVariantOwed())
	})
	runParityVariants(t)
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
			for _, o := range []parityOwner{onFiles, onFar} {
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

// parityFix is three nodes in a chain, the local one reaching the far one
// through a connection and the far one reaching a third the same way, the
// first two each with its home and its files over a parityDisk, and a client
// subscribed at the local web API showing every grid the cells use. The
// third node is what a far home's door opens when the far node holds it.
type parityFix struct {
	t       *testing.T
	ctx     context.Context
	cl      *rpc.Client
	raw     gridwellv1connect.GridwellClient
	h       *transportHarness
	third   *farNode
	disk    map[string]parityDisk  // each node's files by the prefix it is reached through, "" the local one
	root    map[parityOwner]string // the grid each owner's tiles sit on
	away    map[parityOwner]string // another grid of the same owner
	farHome map[parityOwner]string // the far home a door on the owner's grid opens
	// undeclare takes out the connections of the node holding the owner's
	// tiles, answering what declares them again.
	undeclare map[parityOwner]func() func()
	nodes     map[string]parityNode // each node by the prefix it is reached through, "" the local one
	roster    []*gridwellv1.SourceInfo
	token     string
	events    chan *gridwellv1.Event
	breaks    map[string]func() // per tile, what breaks the path to it
	mends     map[string]func() // per tile, what mends a break the next run needs whole
	marks     int
	healthMu  sync.Mutex
	health    map[string]*gridwellv1.EventSourceHealth // the last health each source told
	heard     *cache.Cache                             // every health told, folded as a client folds it
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

// parityNodeIDs is each node's id by the prefix it is reached through.
var parityNodeIDs = map[string]string{"": localNodeID, parityConn: "rnode1"}

// parityDisk is one node's files: three roots. main holds a text file, a
// page file, a directory and a symlink to lib's hall; away is empty; lib
// holds hall and inner, the directory a home door opens. Each directory
// holds one file.
type parityDisk struct {
	main, away, lib string
}

// parityFileNames is main's tiles by name; fileTile places each where home's
// twin sits.
var parityFileNames = map[parityKind]string{kText: "Body.md", kURL: "Site.png", kDoor: "Door", kDoorDir: "Hall"}

func (d parityDisk) hall() string  { return filepath.Join(d.lib, "hall") }
func (d parityDisk) inner() string { return filepath.Join(d.lib, "inner") }

func newParityDisk(t *testing.T) parityDisk {
	d := parityDisk{main: t.TempDir(), away: t.TempDir(), lib: t.TempDir()}
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d.main, "Body.md"), "Body.md")
	writeFile(t, filepath.Join(d.main, "Site.png"), img.String())
	writeFile(t, filepath.Join(d.main, "Door", "Inner"), "Inner")
	writeFile(t, filepath.Join(d.hall(), "Inner"), "Inner")
	writeFile(t, filepath.Join(d.inner(), "Inner"), "Inner")
	if err := os.Symlink(d.hall(), filepath.Join(d.main, "Hall")); err != nil {
		t.Fatal(err)
	}
	return d
}

// registerParityFiles registers d's roots as the node's files, as the node
// does, a delete removing the path rather than moving it to the OS trash.
func registerParityFiles(t *testing.T, reg *server.Sources, st *store.Store, node string, d parityDisk) {
	src, err := files.New(st.Files(), []files.Root{{Path: d.main}, {Path: d.away}, {Path: d.lib}}, byPath(os.RemoveAll), "")
	if err != nil {
		t.Fatal(err)
	}
	reg.Files = src
}

// fileID is path's id on the node reached through prefix, as the local
// node spells it.
func (f *parityFix) fileID(prefix, path string) string {
	return prefix + rpc.QualifyID(parityNodeIDs[prefix], rpc.KeyTileID(path))
}

// pathOf is the path a files id names on the node holding it, ok false for
// any other id.
func pathOf(id string) (path string, ok bool) {
	if !rpc.IsFilesID(id) {
		return "", false
	}
	return rpc.TileKey(rpc.LocalOf(id))
}

// newParityFix builds the fixture, each variant then applied.
func newParityFix(t *testing.T, variants ...parityVariant) *parityFix {
	disks := map[string]parityDisk{"": newParityDisk(t), parityConn: newParityDisk(t)}
	third := newFarNode(t, "cnode1")
	undeclare := map[parityOwner]func() func(){}
	at := func(o parityOwner) func(*server.Sources, *store.Store) {
		return func(reg *server.Sources, st *store.Store) {
			undeclare[o] = func() func() { return undeclareConnections(t, reg, st) }
		}
	}
	h := buildTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil, nil,
		func(reg *server.Sources, st *store.Store) {
			registerParityFiles(t, reg, st, localNodeID, disks[""])
			at(onHome)(reg, st)
		},
		func(reg *server.Sources, st *store.Store) {
			registerParityFiles(t, reg, st, "rnode1", disks[parityConn])
		},
		connectTo(t, "zurich", third, at(onFar)))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	f := &parityFix{t: t, ctx: ctx, cl: h.localCl, h: h, third: third, undeclare: undeclare, disk: disks,
		raw:    gridwellv1connect.NewGridwellClient(h.localHTTP.Client(), h.localURL, connect.WithProtoJSON()),
		events: make(chan *gridwellv1.Event, 1024), breaks: map[string]func(){}, mends: map[string]func(){},
		health: map[string]*gridwellv1.EventSourceHealth{}, heard: cache.New(),
		root: map[parityOwner]string{}, away: map[parityOwner]string{}, farHome: map[parityOwner]string{},
		nodes: map[string]parityNode{"": {h.localCl, h.localHTTP, h.localShell},
			parityConn: {h.remoteCl, h.remoteWeb, h.remoteShell}, parityThirdConn: {third.cl, third.web, third.shell}}}
	lp, err := f.cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.roster, f.token = lp.Sources, lp.ContentToken
	f.root[onHome], f.root[onFar] = lp.HomeGridId, connectionRows(lp)[0].RootGridId
	f.root[onFiles], f.away[onFiles] = f.fileID("", disks[""].main), f.fileID("", disks[""].away)
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
	for _, o := range []parityOwner{onHome, onFiles, onFar} {
		shown = append(shown, f.root[o], f.away[o])
	}
	for prefix, d := range disks {
		for _, dir := range []string{d.main, d.away, filepath.Join(d.main, "Door"), d.hall(), d.inner()} {
			shown = append(shown, f.fileID(prefix, dir))
		}
	}
	for _, v := range variants {
		v.apply(f)
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
			if h := ev.GetSourceHealth(); h != nil {
				f.healthMu.Lock()
				f.health[h.Uuid] = h
				f.heard.NoteHealth(h.Uuid, h.Healthy)
				f.heard.NoteTold(h)
				f.healthMu.Unlock()
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
	kDoor: {5, 1}, kDoorHome: {6, 1}, kDoorDir: {7, 1}, kDoorFar: {8, 1},
}

var parityLayout = []byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`)

// tile answers owner's tile of kind: the files' from disk, home's and a far
// home's made as the user makes them, the same on both.
func (f *parityFix) tile(o parityOwner, k parityKind) *gridwellv1.Tile {
	f.t.Helper()
	if o == onFiles {
		return f.fileTile(k)
	}
	at := parityCells[k]
	cell := func(tl *gridwellv1.Tile) *gridwellv1.Tile {
		tl.X, tl.Y, tl.W, tl.H = at[0], at[1], 1, 1
		return tl
	}
	var made *gridwellv1.Tile
	switch k {
	case kText:
		made = f.create(o, cell(&gridwellv1.Tile{Kind: rpc.KindText}), []byte("Body.md"))
	case kURL:
		made = f.create(o, cell(&gridwellv1.Tile{Kind: rpc.KindURL, UrlString: "https://example.com/u", AltText: "Site.png"}), nil)
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
	case kDoorDir:
		prefix := ""
		if o == onFar {
			prefix = parityConn
		}
		dir := f.disk[prefix].inner()
		made = f.create(o, cell(&gridwellv1.Tile{Kind: rpc.KindDoor, AltText: "Hall", ChildGridId: f.fileID(prefix, dir)}), nil)
		f.breaks[made.Id] = func() { f.remove(dir) }
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

// fileTile is the files' tile of kind on main, placed where home's twin
// sits as the user places it.
func (f *parityFix) fileTile(k parityKind) *gridwellv1.Tile {
	f.t.Helper()
	d := f.disk[""]
	path := filepath.Join(d.main, parityFileNames[k])
	t, err := f.cl.GetTile(f.ctx, f.fileID("", path))
	if err != nil {
		f.t.Fatalf("the files hold no %s at %s: %v", k, path, err)
	}
	if at := parityCells[k]; t.X != at[0] || t.Y != at[1] {
		if t, err = f.cl.MoveTile(f.ctx, &gridwellv1.MoveTileRequest{TileId: t.Id, X: at[0], Y: at[1], W: 1, H: 1,
			Claim: rpc.PlacementOf(t)}); err != nil {
			f.t.Fatalf("place %s: %v", k, err)
		}
	}
	gone := path
	if k == kDoorDir {
		gone = d.hall()
	}
	f.breaks[t.Id] = func() { f.remove(gone) }
	return t
}

// remove takes path off disk, as a user's rm -r.
func (f *parityFix) remove(path string) {
	f.t.Helper()
	if err := os.RemoveAll(path); err != nil {
		f.t.Fatal(err)
	}
}

// door makes a door holding one text, as a directory holds one file.
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
// the row opens and whether it sits in a directory, the differences the
// promise and the directory tint allow a draw. The hue is the row's own, the
// tint a directory's rows wear aside.
func (f *parityFix) draw(t *gridwellv1.Tile) string {
	inDir := rpc.IsFilesID(t.GridId)
	dead := deadlink.DeadTile(t, f.roster, localNodeID, func(id string) bool {
		var err error
		if t.LinkTargetId != "" {
			_, err = f.cl.GetTile(f.ctx, id)
		} else {
			_, err = f.cl.GetGrid(f.ctx, id)
		}
		return clientsync.Of(err) == clientsync.OutcomeDead
	})
	elsewhere := rpc.IsDoorKind(t.Kind) && t.ChildGridId != "" && rpc.SourceOf(t.ChildGridId) != rpc.SourceOf(t.GridId)
	return fmt.Sprintf("dead=%v outside-as-its-place=%v hue=%d banner=%q", dead, tilepreview.Outside(t, inDir) == (elsewhere || inDir),
		tilepreview.BannerHue(t, tilepreview.Outside(t, false)), t.GetAltText())
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
		// A served page is under the tile's own, whose id is the owner's.
		page := rpc.PageURL("", f.token, t.Id)
		return strings.Replace(urlview.Address(t, page), page, "<page>/", 1), nil
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
	return 0, map[parityOwner]int64{onHome: 4, onFiles: 5, onFar: 6}[o]
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

// parityFileEdits is the bytes a file's own change puts: a text's body, a
// picture of another size.
var parityFileEdits = map[parityKind]string{kText: "Body edited", kURL: "\x89PNG another picture"}

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
// writer, or a file written on disk, each in its own words. A door's change
// is a tile more on the grid it opens.
func (f *parityFix) change(t *gridwellv1.Tile) {
	f.t.Helper()
	if rpc.IsDoorKind(t.Kind) {
		if dir, ok := pathOf(t.ChildGridId); ok {
			writeFile(f.t, filepath.Join(dir, "More"), "More")
			return
		}
		n, grid := f.holder(t.ChildGridId)
		if _, err := n.cl.CreateWithContent(f.ctx, &gridwellv1.CreateTileRequest{GridId: grid,
			Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 3, Y: 3, W: 1, H: 1}}, []byte("More")); err != nil {
			f.t.Fatal(err)
		}
		return
	}
	if path, ok := pathOf(t.Id); ok {
		writeFile(f.t, path, parityFileEdits[parityKind(t.Kind)])
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
	// The break reaches the files by their watch, so the read is asked
	// until it answers something else.
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
// reference is the local files' page; the far run asks the far node's.
func checkServe(f *parityFix, o parityOwner, k parityKind) observable {
	prefix := ""
	if o == onFar {
		prefix = parityConn
	}
	g, err := f.cl.GetGrid(f.ctx, f.fileID(prefix, f.disk[prefix].main))
	if err != nil {
		return observable{Outcome: clientsync.Of(err).String()}
	}
	for _, t := range g.Tiles {
		if !rpc.PageContent(t) {
			continue
		}
		resp, err := f.h.localHTTP.Client().Get(urlview.Address(t, rpc.PageURL(f.h.localURL, f.token, t.Id)))
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
	f.t.Fatalf("%q's files list no page", prefix)
	return observable{}
}
