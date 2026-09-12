package gesture

// ClickVerdict names what a bare left-click on a tile does. The four arms are
// mutually exclusive and DecideTileClick's order is the decision.
type ClickVerdict int

const (
	// ClickNone: the kind is not a doorway, so the click does nothing. It is
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
	// LeafLink is rpc.LeafLink: the address lives on the target, so a link
	// never prompts for one.
	LeafLink bool
	// DeadLink is client/deadref: it descends nowhere, so it births no pane.
	DeadLink bool
	// SplitNav is ctrl at left-press time; see dragdrop.DropInput.SplitNav.
	SplitNav bool
}

// DecideTileClick reads the prompt arm before the kind partition, because an
// address-less url tile is a content-descent kind and would otherwise descend
// onto nothing.
func DecideTileClick(in ClickInput) ClickVerdict {
	switch {
	case in.URL && in.URLEmpty && !in.LeafLink:
		return ClickConfigureURL
	case !in.Well && !in.ContentDescent && !in.Workspace:
		return ClickNone
	case in.SplitNav && !in.DeadLink:
		return ClickDescendSplit
	}
	return ClickDescend
}
