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

// DecideFlush decides what the sweep does with one dirty entry. Every arm but
// FlushPost leaves the bytes dirty, the cache entry being the only copy of the
// user's words; the reporting arms tell the user instead of looping silently.
func DecideFlush(rowKnown, rowTakesBytes, rowLoadRefused bool) Flush {
	if !rowKnown {
		if rowLoadRefused {
			return FlushNoRow
		}
		return FlushFetchRow
	}
	if !rowTakesBytes {
		return FlushUnwritable
	}
	return FlushPost
}
