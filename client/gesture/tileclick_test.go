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
			in:   ClickInput{ContentDescent: true, URL: true, URLEmpty: true, AcceptsTiles: true},
			want: ClickConfigureURL,
		},
		{
			name: "a grid that takes no write is never asked for an address",
			in:   ClickInput{ContentDescent: true, URL: true, URLEmpty: true},
			want: ClickNone,
		},
		{
			name: "nor is it with ctrl",
			in:   ClickInput{ContentDescent: true, URL: true, URLEmpty: true, SplitNav: true},
			want: ClickNone,
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
				AcceptsTiles: true, SplitNav: true},
			want: ClickConfigureURL,
		},
		{
			name: "a url tile with an address descends",
			in:   ClickInput{ContentDescent: true, URL: true},
			want: ClickDescend,
		},
		{
			name: "a plugin-served page has no address to ask for",
			in: ClickInput{ContentDescent: true, URL: true, URLEmpty: true,
				Page: true},
			want: ClickDescend,
		},
		{
			name: "ctrl on a plugin-served page splits below",
			in: ClickInput{ContentDescent: true, URL: true, URLEmpty: true,
				Page: true, SplitNav: true},
			want: ClickDescendSplit,
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

func TestSplitNav(t *testing.T) {
	for _, c := range []struct {
		ctrl, meta, want bool
	}{
		{false, false, false},
		{true, false, true},
		{false, true, true}, // cmd, the macOS open-elsewhere modifier
		{true, true, true},
	} {
		if got := SplitNav(c.ctrl, c.meta); got != c.want {
			t.Errorf("SplitNav(ctrl=%v, meta=%v) = %v, want %v", c.ctrl, c.meta, got, c.want)
		}
	}
}

// macOS reports a ctrl + left press as a right press with ctrl held, so a
// right press that asks for a split and never became a drag is that click.
func TestRightClickIsSplitNav(t *testing.T) {
	for _, c := range []struct {
		name              string
		splitNav, dragged bool
		want              bool
	}{
		{"ctrl right click", true, false, true},
		{"ctrl right drag is the link drag", true, true, false},
		{"bare right click stays a no-op", false, false, false},
		{"bare right drag", false, true, false},
	} {
		if got := RightClickIsSplitNav(c.splitNav, c.dragged); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
