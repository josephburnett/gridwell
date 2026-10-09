package gesture

// ClickVerdict names what a bare left-click on a tile does. The four arms are
// mutually exclusive and DecideTileClick's order is the decision.
type ClickVerdict int

const (
	// ClickNone: there is nothing to open, a kind that is not a doorway or an
	// address-less url tile its grid cannot be given an address for. It is
	// the zero value, so a fact the caller failed to resolve descends nowhere.
	ClickNone ClickVerdict = iota
	// ClickConfigureURL opens the address prompt instead of descending.
	ClickConfigureURL
	// ClickDescendSplit descends in a new pane split below the clicked one.
	ClickDescendSplit
	// ClickDescend descends in place.
	ClickDescend
)

// ClickInput is the tile's facts at left-click, every one resolved by the
// caller.
type ClickInput struct {
	// Well, ContentDescent and Workspace are rpc.IsWellKind,
	// rpc.IsContentDescentKind and rpc.IsWorkspaceKind of the tile's kind.
	// Together they partition the descendable kinds.
	Well           bool
	ContentDescent bool
	Workspace      bool
	// URL is rpc.KindURL and URLEmpty its typed address being blank.
	URL      bool
	URLEmpty bool
	// Page is rpc.PageContent: the owning plugin serves this url tile's page,
	// so the node derives the address and there is none for the user to type.
	Page bool
	// AcceptsTiles is the tile's Grid.accepts_tiles, unknown reading as
	// false. Only such a grid's url rows hold an address the node writes, so
	// a plugin's is never asked for one.
	AcceptsTiles bool
	// LeafLink is rpc.LeafLink: the address lives on the target, so a link
	// never prompts for one.
	LeafLink bool
	// DeadLink is client/deadref: it descends nowhere, so it births no pane.
	DeadLink bool
	// SplitNav is the press's SplitNav verdict; see dragdrop.DropInput.SplitNav.
	SplitNav bool
}

// DecideTileClick reads the address-less arm before the kind partition,
// because an address-less url tile is a content-descent kind and would
// otherwise descend onto nothing.
func DecideTileClick(in ClickInput) ClickVerdict {
	switch {
	case in.URL && in.URLEmpty && !in.LeafLink && !in.Page:
		if in.AcceptsTiles {
			return ClickConfigureURL
		}
		return ClickNone
	case !in.Well && !in.ContentDescent && !in.Workspace:
		return ClickNone
	case in.SplitNav && !in.DeadLink:
		return ClickDescendSplit
	}
	return ClickDescend
}

// SplitNav reads a press's modifiers: ctrl, or meta (cmd on macOS), asks that
// the click descend in a new pane split below. It is the one owner of which
// keys ask.
func SplitNav(ctrl, meta bool) bool { return ctrl || meta }

// RightClickIsSplitNav is the verdict on a right press over a tile at its
// release. macOS reports a ctrl + left press as a right press with ctrl held,
// so one that asked for a split and never crossed the drag threshold is that
// click; anywhere else such a release had nothing to do.
func RightClickIsSplitNav(splitNav, dragged bool) bool { return splitNav && !dragged }
