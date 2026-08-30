package markdown

import (
	"strings"
	"testing"
)

// TaskCount is the tests' parity oracle: how many toggleable checkboxes the
// toggle machinery sees. Test-only — production counts nothing, it only
// toggles (the deadcode gate keeps it that way).
func TaskCount(src []byte) int {
	return len(taskMarkerOffsets(src))
}

func TestToggleTaskBasic(t *testing.T) {
	src := []byte("# Todo\n\n- [ ] alpha\n- [x] beta\n- [X] gamma\n")

	// Unchecked → checked.
	out, ok := ToggleTask(src, 0)
	if !ok {
		t.Fatal("toggle 0 refused")
	}
	if !strings.Contains(string(out), "- [x] alpha") {
		t.Errorf("toggle 0: %q", out)
	}

	// Checked → unchecked (lowercase and uppercase X both).
	out, ok = ToggleTask(src, 1)
	if !ok || !strings.Contains(string(out), "- [ ] beta") {
		t.Errorf("toggle 1: ok=%v %q", ok, out)
	}
	out, ok = ToggleTask(src, 2)
	if !ok || !strings.Contains(string(out), "- [ ] gamma") {
		t.Errorf("toggle 2 (uppercase X): ok=%v %q", ok, out)
	}

	// Round trip: toggling twice restores the source byte-for-byte —
	// except an [X], which normalizes to [x] (unchecked then rechecked).
	once, _ := ToggleTask(src, 0)
	twice, _ := ToggleTask(once, 0)
	if string(twice) != string(src) {
		t.Errorf("double toggle must restore the source:\n%q\n%q", src, twice)
	}
}

func TestToggleTaskEditsExactlyOneByte(t *testing.T) {
	src := []byte("intro text\n\n- [ ] a\n  - [x] nested\n\ntail\n")
	out, ok := ToggleTask(src, 1)
	if !ok {
		t.Fatal("toggle refused")
	}
	if len(out) != len(src) {
		t.Fatalf("length changed: %d → %d", len(src), len(out))
	}
	diff := 0
	for i := range src {
		if src[i] != out[i] {
			diff++
		}
	}
	if diff != 1 {
		t.Errorf("changed %d bytes, want exactly 1 — everything the user didn't touch stays byte-identical", diff)
	}
	if !strings.Contains(string(out), "- [ ] nested") {
		t.Errorf("nested toggle missed: %q", out)
	}
}

func TestToggleTaskDocumentOrderAcrossShapes(t *testing.T) {
	// Ordered lists, nesting, and a blockquote — the DOM renders these
	// checkboxes in document order and the toggle must count the same way.
	src := []byte(`1. [ ] first
2. [x] second

- outer
  1. [ ] third

> - [ ] fourth
`)
	for i, want := range []string{"1. [x] first", "2. [ ] second", "1. [x] third", "> - [x] fourth"} {
		out, ok := ToggleTask(src, i)
		if !ok {
			t.Fatalf("toggle %d refused", i)
		}
		if !strings.Contains(string(out), want) {
			t.Errorf("toggle %d: want %q in\n%s", i, want, out)
		}
	}
}

func TestToggleTaskIgnoresNonTasks(t *testing.T) {
	src := []byte("- [ ] real\n\n```\n- [ ] code, not a task\n```\n\n" +
		"a paragraph with [ ] brackets\n\n    - [ ] indented code block\n")
	if n := TaskCount(src); n != 1 {
		t.Fatalf("TaskCount = %d, want 1 (code/prose brackets are not tasks)", n)
	}
	out, ok := ToggleTask(src, 0)
	if !ok || !strings.Contains(string(out), "- [x] real") {
		t.Fatalf("toggle 0: ok=%v %q", ok, out)
	}
	if !strings.Contains(string(out), "- [ ] code, not a task") {
		t.Errorf("code-fence bytes must stay untouched: %q", out)
	}
	if _, ok := ToggleTask(src, 1); ok {
		t.Errorf("index 1 addresses no task and must refuse")
	}
}

func TestToggleTaskOutOfRange(t *testing.T) {
	src := []byte("- [ ] only\n")
	if _, ok := ToggleTask(src, -1); ok {
		t.Error("negative index must refuse")
	}
	if _, ok := ToggleTask(src, 1); ok {
		t.Error("past-the-end index must refuse")
	}
	if _, ok := ToggleTask([]byte("no tasks here"), 0); ok {
		t.Error("task-free doc must refuse")
	}
	if _, ok := ToggleTask(nil, 0); ok {
		t.Error("empty doc must refuse")
	}
}

// TestToggleTaskRenderParity pins THE mapping invariant: the number of
// checkbox inputs RenderHTML emits equals TaskCount, for shapes chosen to
// tempt them apart (code fences, prose brackets, html-ish text, nesting,
// blockquotes, loose/tight lists). The DOM index → source index mapping is
// sound exactly when these two counts can never disagree — they share one
// parser, and this test is the tripwire if that ever stops being true.
func TestToggleTaskRenderParity(t *testing.T) {
	docs := []string{
		"- [ ] a\n- [x] b\n",
		"# h\n\ntext [ ] not a task\n\n- [ ] real\n",
		"```\n- [ ] fenced\n```\n\n- [X] real\n",
		"    - [ ] indented code\n\n1. [ ] ordered\n",
		"> quoted\n>\n> - [ ] task in quote\n",
		"- plain item\n- [ ] task\n- another plain\n",
		"- [ ] outer\n  - [x] inner\n    - [ ] innermost\n",
		"para\n\n1. [ ] a\n\n2. [x] loose list\n",
		"no tasks at all\n",
		"- [ ]\n", // marker with no label
	}
	for _, d := range docs {
		html := RenderHTML([]byte(d), false)
		rendered := strings.Count(html, `type="checkbox"`)
		if got := TaskCount([]byte(d)); got != rendered {
			t.Errorf("parity broken for %q: TaskCount=%d, rendered inputs=%d\nhtml: %s", d, got, rendered, html)
		}
	}
}

// TestRenderHTMLCheckboxesInteractive pins that the rendered view's
// checkboxes are not disabled (a disabled input swallows clicks — it could
// never be a control), that checked state survives sanitization, and that
// the sanitizer still strips active content around them.
func TestRenderHTMLCheckboxesInteractive(t *testing.T) {
	html := RenderHTML([]byte("- [ ] open\n- [x] done <script>alert(1)</script>\n"), false)
	if strings.Contains(html, "disabled") {
		t.Errorf("checkboxes must not be disabled: %s", html)
	}
	if strings.Count(html, `type="checkbox"`) != 2 {
		t.Errorf("want 2 checkboxes: %s", html)
	}
	if !strings.Contains(html, `checked=""`) {
		t.Errorf("checked state must survive sanitization: %s", html)
	}
	if strings.Contains(html, "<script") {
		t.Errorf("sanitizer must still strip scripts: %s", html)
	}
}
