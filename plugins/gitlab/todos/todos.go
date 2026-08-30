// Package todos is the pure half of the gitlab todos plugin: the todo record,
// the week calendar, the memory of every todo seen, and the derivations —
// entries, labels, placement hints — the plugin answers with. There is no
// network and no gRPC here, so everything is unit-tested against fakes, and
// the plugin package only wires it to the wire.
package todos

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Todo is the subset of GitLab's todo object the plugin uses. Only the fields
// that survive every target_type are read from the nested target; a Commit or
// Project target carries no iid, for instance.
type Todo struct {
	ID         int64  `json:"id"`
	ActionName string `json:"action_name"`
	TargetType string `json:"target_type"`
	TargetURL  string `json:"target_url"`
	Body       string `json:"body"`
	// State is "pending" or "done", GitLab's word, and the one fact the
	// plugin re-derives locally; see Memory.Sync. A todo absent from the
	// pending set is done, whatever GitLab says about it.
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Project   struct {
		ID                int64  `json:"id"`
		Name              string `json:"name"`
		PathWithNamespace string `json:"path_with_namespace"`
		WebURL            string `json:"web_url"`
	} `json:"project"`
	Author struct {
		Name     string `json:"name"`
		Username string `json:"username"`
		WebURL   string `json:"web_url"`
	} `json:"author"`
	Target struct {
		IID   int64  `json:"iid"`
		Title string `json:"title"`
		State string `json:"state"`
	} `json:"target"`
}

const (
	StatePending = "pending"
	StateDone    = "done"
)

// Done reports the derived completion state.
func (t *Todo) Done() bool { return t.State == StateDone }

// Ref is the GitLab short reference of the target, such as "!42" or "#7", and
// "" when the target type has none.
func (t *Todo) Ref() string {
	if t.Target.IID == 0 {
		return ""
	}
	switch t.TargetType {
	case "MergeRequest":
		return "!" + strconv.FormatInt(t.Target.IID, 10)
	case "Issue":
		return "#" + strconv.FormatInt(t.Target.IID, 10)
	case "Epic":
		return "&" + strconv.FormatInt(t.Target.IID, 10)
	}
	return ""
}

// Title is the todo's headline: the target's title, else the body's
// first line, else the action on the target type.
func (t *Todo) Title() string {
	if s := strings.TrimSpace(t.Target.Title); s != "" {
		return s
	}
	if line, _, _ := strings.Cut(strings.TrimSpace(t.Body), "\n"); line != "" {
		return line
	}
	return t.Action() + " " + t.TargetType
}

// actionPhrases humanizes GitLab's action_name vocabulary.
var actionPhrases = map[string]string{
	"assigned":                "assigned to you",
	"mentioned":               "mentioned you",
	"build_failed":            "build failed",
	"marked":                  "marked",
	"approval_required":       "approval required",
	"unmergeable":             "unmergeable",
	"directly_addressed":      "addressed you",
	"merge_train_removed":     "removed from merge train",
	"review_requested":        "review requested",
	"member_access_requested": "access requested",
	"review_submitted":        "review submitted",
}

// Action is the human phrase for the todo's action_name. An unknown action
// rides verbatim with its underscores opened.
func (t *Todo) Action() string {
	if p, ok := actionPhrases[t.ActionName]; ok {
		return p
	}
	return strings.ReplaceAll(t.ActionName, "_", " ")
}

// Label is the tile's banner: a done mark, who it is from, the short
// ref, and the title.
func (t *Todo) Label() string {
	var b strings.Builder
	if t.Done() {
		b.WriteString("✓ ")
	}
	if name := strings.TrimSpace(t.Author.Name); name != "" {
		b.WriteString(name)
		b.WriteString(": ")
	}
	if r := t.Ref(); r != "" {
		b.WriteString(r)
		b.WriteByte(' ')
	}
	b.WriteString(t.Title())
	return b.String()
}

// Key is the todo's plugin key: GitLab's own id, stable forever.
func (t *Todo) Key() string { return KeyPrefix + strconv.FormatInt(t.ID, 10) }

// KeyPrefix namespaces todo keys; WeekPrefix namespaces week contexts;
// RootContext is the plugin's landing grid.
const (
	KeyPrefix   = "todo:"
	WeekPrefix  = "week:"
	RootContext = "todos"
)

// ParseKey resolves a todo key to its id.
func ParseKey(key string) (int64, bool) {
	s, ok := strings.CutPrefix(key, KeyPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

// ── weeks ──────────────────────────────────────────────────────────────

// WeekStart is the Monday 00:00 UTC that begins the week containing t. It is
// UTC because GitLab timestamps are UTC and a week key must never shift with
// the host's zone: a key names the same thing forever.
func WeekStart(t time.Time) time.Time {
	u := t.UTC()
	wd := (int(u.Weekday()) + 6) % 7 // Monday = 0
	d := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return d.AddDate(0, 0, -wd)
}

// WeekKey is the context key of the week starting at start.
func WeekKey(start time.Time) string { return WeekPrefix + start.UTC().Format("2006-01-02") }

// ParseWeekKey resolves a week context key to its Monday.
func ParseWeekKey(key string) (time.Time, bool) {
	s, ok := strings.CutPrefix(key, WeekPrefix)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil || t.Weekday() != time.Monday {
		return time.Time{}, false
	}
	return t, true
}

// HintEpoch anchors the root calendar: the month containing it is row y=0,
// later months climb into negative y, and earlier months descend. It is a
// fixed date, so a week's hint is the same on every host and every restart and
// two nodes never disagree about where a week first lands.
var HintEpoch = time.Date(2026, time.August, 24, 0, 0, 0, 0, time.UTC)

// WeekCell is the root hint for the week starting at start: one row per month,
// the month the Monday falls in, with the month's weeks left to right by their
// Monday's position in the month, x from 0 to 4. It reads as a calendar page,
// newest at the top.
func WeekCell(start time.Time) (x, y int64) {
	u := start.UTC()
	months := (u.Year()-HintEpoch.Year())*12 + int(u.Month()-HintEpoch.Month())
	return int64((u.Day() - 1) / 7), -int64(months)
}

// WeekLabel names a week well by its Monday and its counts.
func WeekLabel(start time.Time, open, done int) string {
	return fmt.Sprintf("%s · %d open · %d done", start.UTC().Format("2006-01-02"), open, done)
}
