package textedit

import "testing"

func TestCanvasHiddenByOverlay(t *testing.T) {
	cases := []struct {
		name                                string
		isDescended, isFocused, ready, want bool
	}{
		{"focused descended with ready overlay hides canvas", true, true, true, true},
		{"preview node never hidden", false, true, true, false},
		{"descended not focused → overlay not here", true, false, true, false},
		// Overlay cleared on pane switch, blob not yet arrived.
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
			// The textarea clears so 4's content is not shown as 7's, and
			// LastTileID advances so the blob fetch's follow-up seeds
			// rather than re-clears.
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
				KeepView:      true,
				NewLastTileID: "5",
			},
		},
		{
			// Real typing always sets PendingEdit, so this combination is
			// exactly a foreign writer's bytes landing under an open
			// editor.
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
				KeepView:      true,
				NewLastTileID: "5",
			},
		},
		{
			// A SetValue here would move the caret and scroll for nothing.
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
			// Reseeding an empty dirty buffer would resurrect the deleted
			// text under the caret.
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
			// A rebind within the debounce seeds the new tile; tile 4's
			// typing lives in its own cache entry and the dirty sweep
			// posts it wherever focus went.
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
