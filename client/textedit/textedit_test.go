package textedit

import "testing"

// TestCanvasHiddenByOverlay pins three invariants:
//   - A preview node (isDescended=false) is never hidden — the textarea
//     covers only the focused descended pane, so suppressing a preview blanks
//     it.
//   - A descended non-focused pane is never hidden — the textarea is over a
//     different pane.
//   - The canvas paints until the textarea actually has content
//     (textareaReady=false keeps it visible during the pane-switch loading
//     race).
func TestCanvasHiddenByOverlay(t *testing.T) {
	cases := []struct {
		name                                string
		isDescended, isFocused, ready, want bool
	}{
		// The one hide case: the focused descended pane whose overlay
		// (textarea or rendered div) holds content.
		{"focused descended with ready overlay hides canvas", true, true, true, true},
		{"preview node never hidden", false, true, true, false},
		{"descended not focused → overlay not here", true, false, true, false},
		// Loading race: overlay cleared on pane switch, blob not yet arrived.
		{"overlay not ready → canvas paints", true, true, false, false},
		{"all false", false, false, false, false},
	}
	for _, c := range cases {
		got := CanvasHiddenByOverlay(c.isDescended, c.isFocused, c.ready)
		if got != c.want {
			t.Errorf("%s: CanvasHiddenByOverlay(%v,%v,%v) = %v, want %v",
				c.name, c.isDescended, c.isFocused, c.ready, got, c.want)
		}
	}
}

func TestDecideTextareaSync(t *testing.T) {
	cases := []struct {
		name string
		in   TextareaSyncInput
		want TextareaSyncDecision
	}{
		{
			// Descend into new tile 7 while the textarea still holds "old
			// content" from tile 4 and the new tile's blob hasn't arrived
			// in the cache yet. The textarea clears so the user doesn't see
			// 4's content as 7's default, and LastTileID advances so the
			// blob fetch's follow-up call seeds rather than re-clears.
			name: "different tile, blob not cached → clear and advance",
			in: TextareaSyncInput{
				FocusedTileID: "7",
				LastTileID:    "4",
				CurrentValue:  "old content",
				BlobCached:    false,
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "",
				NewLastTileID: "7",
			},
		},
		{
			name: "different tile, blob cached → seed with content",
			in: TextareaSyncInput{
				FocusedTileID: "7",
				LastTileID:    "4",
				CurrentValue:  "old content",
				BlobCached:    true,
				BlobContent:   "tile 7 body",
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "tile 7 body",
				NewLastTileID: "7",
			},
		},
		{
			name: `first focus (LastTileID ""), blob cached → seed`,
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "",
				CurrentValue:  "",
				BlobCached:    true,
				BlobContent:   "first focus body",
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "first focus body",
				NewLastTileID: "5",
			},
		},
		{
			name: "same tile, textarea empty (post-toggle), blob cached → seed",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "",
				BlobCached:    true,
				BlobContent:   "tile 5 body",
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "tile 5 body",
				NewLastTileID: "5",
			},
		},
		{
			// The foreign-writer visibility rule: with no pending edit the
			// buffer is a mere view of the cached body and follows it.
			// Another device edited this tile; the event evicted the stale
			// body, the refetch landed the foreign bytes, and the open
			// editor repaints. Real typing always sets PendingEdit, so this
			// input combination is exactly the stale-view case.
			name: "same tile, clean buffer differs from cache → follow the cache",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "stale buffer from before the foreign edit",
				BlobCached:    true,
				BlobContent:   "foreign edit, refetched",
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "foreign edit, refetched",
				NewLastTileID: "5",
			},
		},
		{
			// Clean buffer already matches the cache: no write, no churn (a
			// SetValue would move the caret/scroll for nothing).
			name: "same tile, clean buffer equals cache → leave alone",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "settled body",
				BlobCached:    true,
				BlobContent:   "settled body",
			},
			want: TextareaSyncDecision{
				SetValue:      false,
				NewLastTileID: "5",
			},
		},
		{
			// Deleting everything is an edit like any other: an empty dirty
			// buffer is not reseeded from the cache, which would resurrect
			// the deleted text under the user's caret.
			name: "same tile, pending edit emptied the buffer → preserve",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "",
				BlobCached:    true,
				BlobContent:   "deleted content",
				PendingEdit:   true,
			},
			want: TextareaSyncDecision{
				SetValue:      false,
				NewLastTileID: "5",
			},
		},
		{
			name: "same tile, textarea empty, blob still loading → wait",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "",
				BlobCached:    false,
			},
			want: TextareaSyncDecision{
				SetValue:      false,
				NewLastTileID: "5",
			},
		},
		{
			// The fast-pane-switch case: typing into tile 4 arms the
			// debounced save, and switching to another text descent within
			// the debounce rebinds the textarea. The rebind seeds the new
			// tile; tile 4's typing already lives in its own cache entry,
			// and the dirty sweep posts it regardless of where focus went.
			name: "different tile with pending edit → rebind; the old edit is cache-owned",
			in: TextareaSyncInput{
				FocusedTileID: "7",
				LastTileID:    "4",
				CurrentValue:  "unsaved typing for 4",
				BlobCached:    true,
				BlobContent:   "tile 7 body",
				PendingEdit:   true,
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "tile 7 body",
				NewLastTileID: "7",
			},
		},
		{
			// Same tile: the buffer still belongs to the focused tile; the
			// debounced save owns persistence, not the rebind flush.
			name: "same tile with pending edit → no flush, preserve typing",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "user just typed this",
				BlobCached:    true,
				BlobContent:   "stale cache content",
				PendingEdit:   true,
			},
			want: TextareaSyncDecision{
				SetValue:      false,
				NewLastTileID: "5",
			},
		},
		{
			name: "different tile, blob cached but empty (fresh tile) → clear",
			in: TextareaSyncInput{
				FocusedTileID: "9",
				LastTileID:    "4",
				CurrentValue:  "previous content",
				BlobCached:    true,
				BlobContent:   "",
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "",
				NewLastTileID: "9",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideTextareaSync(tc.in)
			if got != tc.want {
				t.Errorf("DecideTextareaSync(%+v) = %+v, want %+v",
					tc.in, got, tc.want)
			}
		})
	}
}
