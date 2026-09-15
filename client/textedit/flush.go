package textedit

// Flush is one dirty content entry's ordinary-sweep outcome, the sibling of
// DecideUnloadFlush for the sweep that does have a next attempt behind it.
type Flush int

const (
	FlushPost       Flush = iota // the owner row is known and takes the bytes
	FlushFetchRow                // the owner row is not cached; ask for it
	FlushNoRow                   // the server says the row is gone
	FlushUnwritable              // the row is known and will not take the bytes
)

// DecideFlush decides what the sweep does with one dirty entry. Every arm
// leaves the bytes dirty except FlushPost, because the cache entry is the only
// copy of the user's words. The two reporting arms exist because the sweep
// repeats forever: an entry no arm can post is a silent no-op every sweep
// until the tab closes, and the user is told instead.
func DecideFlush(rowKnown, rowEditableText, rowLoadRefused bool) Flush {
	if !rowKnown {
		if rowLoadRefused {
			return FlushNoRow
		}
		return FlushFetchRow
	}
	if !rowEditableText {
		return FlushUnwritable
	}
	return FlushPost
}
