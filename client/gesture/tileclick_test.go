package gesture

import "testing"

func TestDecideTileClick(t *testing.T) {
	tests := []struct {
		name string
		in   ClickInput
		want ClickVerdict
	}{
		{
			name: "address-less url tile prompts",
			in:   ClickInput{ContentDescent: true, URL: true, URLEmpty: true},
			want: ClickConfigureURL,
		},
		{
			name: "address-less url link resolves through its target",
			in: ClickInput{ContentDescent: true, URL: true, URLEmpty: true,
				LeafLink: true},
			want: ClickDescend,
		},
		{
			name: "the prompt outranks ctrl, so it stays in place",
			in: ClickInput{ContentDescent: true, URL: true, URLEmpty: true,
				SplitNav: true},
			want: ClickConfigureURL,
		},
		{
			name: "a url tile with an address descends",
			in:   ClickInput{ContentDescent: true, URL: true},
			want: ClickDescend,
		},
		{
			name: "a kind outside the three partitions does nothing",
			in:   ClickInput{},
			want: ClickNone,
		},
		{
			name: "an undescendable kind ignores ctrl",
			in:   ClickInput{SplitNav: true},
			want: ClickNone,
		},
		{name: "well descends", in: ClickInput{Well: true}, want: ClickDescend},
		{
			name: "workspace descends",
			in:   ClickInput{Workspace: true},
			want: ClickDescend,
		},
		{
			name: "ctrl on a well splits below",
			in:   ClickInput{Well: true, SplitNav: true},
			want: ClickDescendSplit,
		},
		{
			name: "ctrl on a content descent splits below",
			in:   ClickInput{ContentDescent: true, SplitNav: true},
			want: ClickDescendSplit,
		},
		{
			name: "a dead link births no pane",
			in:   ClickInput{Well: true, SplitNav: true, DeadLink: true},
			want: ClickDescend,
		},
		{
			name: "a dead link without ctrl still descends in place",
			in:   ClickInput{Well: true, DeadLink: true},
			want: ClickDescend,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DecideTileClick(tt.in); got != tt.want {
				t.Errorf("DecideTileClick(%+v) = %v, want %v",
					tt.in, got, tt.want)
			}
		})
	}
}
