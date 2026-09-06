# W4 — the concept audit and the predicate sweep

The design doc for `docs/debt-program.md` W4. Half A enumerates every
user-facing concept and puts the merge candidates to the owner as questions;
it ends in decisions, not code. Half B is the mechanical, behavior-preserving
sweep: every exception check in `client/wasm` routes through a named owner.

Half A is the source for `docs/concepts.md`, which is written after the
owner answers.

## Half A — the enumeration

31 distinctions the user can see or feel. Each line says what it buys that
nothing else does.

### Content primitives (5)

| Concept | What it buys |
|---|---|
| **well** | The only tile with a child grid; the only doorway that pushes a grid frame. |
| **text** | The only tile the user types into, and the only one carrying a version claim on bytes. |
| **url** | The only tile with an address of its own and a Chromium session behind it. |
| **shell** | The only tile whose content is a host process; survives ascent as a tmux session. |
| **pane** | The only tile whose content is an arrangement; descent swaps the window, not the pane. |

### Reference shapes (4)

| Concept | What it buys |
|---|---|
| **exit well** | A doorway onto a grid in another namespace — a mount, a plugin, a file tree. Derived from ids alone (`rpc.IsExitWell`), never stored. |
| **leaf link** | One content tile shown in two places: `link_target_id` and one owning row. Makes "the same document, over there" possible without a copy. |
| **dead link** | A link into a namespace the node no longer declares: grey, inert, costs no RPC, says nothing. Names the difference between "gone from config" and "not answering". |
| **childless reference** (menu swatch, unrooted launcher) | A link that names no namespace yet — the + menu row and the drag ghost, drawn before any grid exists to point at. |

### Places (3)

| Concept | What it buys |
|---|---|
| **grid frame** | One doorway crossing, with the viewport you left it at. The whole of "where a pane is". |
| **content frame** | A frame whose place is the tile, not a grid: text scroll, text mode, content zoom live here. Lets a descent into a document be an ordinary ascent-able level. |
| **level** | A pane-tile descent: the one second axis, session-only. Lets one pane tile hold a whole arrangement without the frame stack having to encode a tree. |

### Lifecycle spaces (2)

| Concept | What it buys |
|---|---|
| **ephemeral scratch visit** | A real row in the scratch grid that no pane owns — a url typed into the + menu, a shell opened from it. Buys "try it without placing it": grey border, gone on ascent, promotable onto a grid. |
| **trash, two-stage** | Delete moves a tile into a per-month subgrid; delete again destroys it. Ids and versions continue, so links keep resolving. Buys "undo a delete" without any undo machinery. Scratch bypasses it — a visit was never placed. |

### Health and staleness (6)

| Concept | What it buys |
|---|---|
| **dead** (`client/deadref`) | The namespace is not declared. Nothing is asked, nothing is said. Never fetched, so an undeclared plugin cannot storm the strip. |
| **dark** (`internal/sourcecache`, health events) | Declared, not answering right now. Fetched, reported, recovers on its own. |
| **stale** (`Grid.Meta.Stale`) | This grid is a remembering, not an answer. One bar chip; never moves or restyles a tile. |
| **broken** (`pluginhealth.Broken`) | `Info` failed or timed out. Alarm tint; the click reports at `Error` severity. |
| **rootless** (`pluginhealth.Rootless`) | `Info` answered and declared no root grid. Softer tint; the click reports at `Info` severity. Carries a **third** meaning today: a connection row that has not been answered yet (`ClickNotice`'s `PluginKindConnection` arm). |
| **unknown** (uncached) | Not yet known, which is neither yes nor no. `scratch.For` returns it as a third value; `a.gridWritable` and `a.gridKnownReadOnly` are two functions expressing the two safe defaults for the same tri-state. |

### Write classes (4 names, 2 enforced)

| Concept | What it buys |
|---|---|
| **content** | The user's bytes: a text body, a typed url, a typed name. The only class with a version claim and a 409. |
| **framing** | Where you left a view: `SetFraming`, `SetTextView`, `SetContentZoom`. No claim, no bump. |
| **capture** | What the machine observed: a preview JPEG, a page title, a url trail. No claim, no bump. |
| **layout** | Where things sit: place, clone, delete. No claim, no bump; last-writer-wins. |

The store enforces two: `claimContentVersion` + `finishContentEdit` for
content, `loadForWrite` + `emitTileChanged` for the other three. The three
names differ only in who initiates the write.

### Presentation exceptions (7)

| Concept | What it buys |
|---|---|
| **read-only** (`a.tileReadOnly`) | A text tile in a non-writable grid: no textarea, no save, no checkbox flip, rendered face only. Prevents typing into a derived body and silently re-posting it. |
| **host_content** (`Grid.Meta.HostContent`) | Every row in this grid projects host state: the red "outside Gridwell" treatment, declared by the plugin, so the client never learns plugin kinds. |
| **serves_page** | This tile's face and descent are web content served at the `/content/` door — a file whose presentation is a page. Buys a plugin-served page without the plugin owning an address. |
| **text_presentation: plain** | Verbatim preformatted text; no rendered/raw toggle. |
| **text_presentation: rendered** | Document render only; no toggle. |
| **text_presentation: both** | Toggle stays, whether or not the tile is writable. |
| **url_frozen** | A standing user intent not to go live. Distinct from "has a preview": the preview is what a frozen tile looks like, the intent is why it stays that way. |

**Count: 31.**

## The merge questions

Eight. Each says what a merge would gain, what decided behavior it would
cost, and a recommendation. Three recommend a merge; five recommend keeping
the distinction.

### Q1 — broken vs rootless: merge into one non-enterable status?

**Gain.** `pluginhealth.Status` becomes two-valued; one tint, one message
template, one branch in `drawPluginHealthTint`.

**Cost.** The severity split is real: broken is a failure the user expected
to work (`Error`), rootless is a configuration gap (`Info`). Merged, either
a missing `config.root` cries error on every click, or a crashed plugin
whispers.

**Recommendation: do not merge — split further.** `Rootless` carries two
meanings: "declared no root" and "a connection that has not answered yet".
The second is a waiting state, and it is distinguished inside `ClickNotice`
by reading `pl.Kind` — a second classification below the first. Give it its
own `Status` (`Waiting`), so `Classify` stays the one owner.

### Q2 — serves_page vs the url kind: can a page tile just be a url tile?

**Gain.** `WebContent()` collapses to `Kind == KindURL`; six checks in the
shim disappear; one descent path instead of two.

**Cost — decided behavior.** A page tile is a **file**: its grid face is the
plugin's thumbnail in the text family's border (`render.go:943`), its
address is derived at use time because the desktop origin is an ephemeral
port (`webAddress`), and it carries no persisted url state, content zoom,
freeze intent, or history — those are the plugin's, and a plugin holds no
node fact. Making it a url tile would hand a plugin an address to own and a
url row to write, and would require the node to mint a url kind for a text
entry: a switch on what a plugin is.

**Recommendation: do not merge.** Merge the *derivation* instead — the
url-vs-page question is spelled three ways in the shim today. Half B names
two predicates and routes all of them through those.

### Q3 — text_presentation vs tileReadOnly: one axis for a text tile's faces?

**Gain.** One question — "what faces does this text tile have, and can it be
edited" — instead of a declared axis and a derived one with a precedence
rule.

**Cost.** They answer different questions. `text_presentation` is about
**faces** (rendered, raw, both); `tileReadOnly` is about **writing** (the
textarea, the save path, the checkbox). A read-only tile declaring `both`
toggles between rendered and raw source and stays uneditable — a real
combination a single axis cannot express without inventing cross-product
values.

**Recommendation: do not merge the facts. Merge the derivation.** The
precedence rule is stated once (`textToggleVisible`), but it and
`presentationHTML` sit in `client/wasm`, where `make check` executes
neither. Move both into `client/textedit` beside `DescentMode`, with a table
test over (presentation, read-only, name). In scope for W4's code half.

### Q4 — the ephemeral grey border: promote it to a fourth write class?

**Gain.** "A write about a row that is about to die" becomes a named class
with the grey border as its face, instead of a guard each durable-write site
must remember (`possiblyEphemeral`, read at 6 sites).

**Cost.** It is not a write class. Ephemerality is a property of the
**target row**, not of the write, and the four classes are about what a
write claims. A fifth row in the version table would say nothing about
versions.

**Recommendation: do not add a class. Close the gap the audit found.**
`possiblyEphemeral` is already the one owner and is read by url state
(`url_stream_client.go:200`), the freeze intent (`:340`), rename
(`rename_overlay.go:46`), text scroll (`urlsync.go:238`), workspace capture
(`workspace.go:106`), and re-anchoring (`input.go:1176`). **It is not read
by `applyContentZoom` (`content_zoom.go:110–156`)**: ctrl+scroll inside an
ephemeral url visit posts `SetContentZoom` against a row ascent then
deletes. That is a behavior bug, not a sweep item — it wants a failing test
first, and it is the argument for making the guard a listed rule rather than
a habit.

### Q5 — dead and dark: one "unreachable" face?

**Gain.** One grey face, one code path, and the user stops having to tell
two greys apart.

**Cost — decided behavior, twice over.** CLAUDE.md: "nothing is asked for it
and nothing is said about it. Dead is not dark." A merged face must pick one
behavior: either dead links start being fetched (every undeclared plugin
becomes a notice storm, and avoiding that fetch is what dead is for), or
dark sources stop being reported (a real outage goes silent and never
announces its recovery). The comes-back distinction is asymmetric too — a
retired name never returns, an undeclared namespace returns the moment it is
declared — and only the roster can say which.

**Recommendation: do not merge.** The two greys are the visible shadow of
"nothing is asked" vs "asked and not answered". W3's document is where a
user-facing explanation of the two belongs.

### Q6 — framing, capture, layout: three names for one enforced class?

**Gain.** A reader stops looking for a third store pair. The invariant table
already says two pairs; the prose says three words.

**Cost.** Nothing in code. The three words carry origin — user gesture,
machine observation, arrangement — which is useful when deciding whether a
new write may be made without asking.

**Recommendation: keep the words, state the shape.** One sentence in
`ARCHITECTURE.md`'s version section: framing, captures, and layout are three
origins of one enforced class, and the store has exactly two pairs. No code
change.

### Q7 — one tri-state for "not cached yet"?

**Gain.** `a.gridWritable` and `a.gridKnownReadOnly` are two functions
naming the two safe defaults for one uncached grid. `scratch.For` already
proves the better shape: return `(value, known)` and let each caller pick
its own safe default at the call site, where the reason is visible.

**Cost.** Call sites get one line longer. The current pair is documented and
correct; this is tidiness, not a bug.

**Recommendation: merge.** Adopt `(writable, known bool)` on one function
after Half B lands, so the client has one convention for "not known yet"
rather than two. Low risk, and it removes the trap where a future caller
picks `!gridWritable` when it meant `gridKnownReadOnly`.

### Q8 — trash and scratch: two system grids, two rules?

**Gain.** One system grid, one delete rule.

**Cost.** They are opposite by design: the trash is a net for things the
user placed in space and asked to remove; scratch holds system-made
ephemerals the user never placed. A scratch tile that landed in the trash on
ascent would fill the trash with every url the user glanced at.

**Recommendation: do not merge.** The bypass is pinned by a test and by
`deleteBypassesTrash`'s comment; both grids are system-keyed singletons
surfaced as declared menu entries, so nothing special-cases them at the
client. Closed.

**Not asked (decided, examined, closed).** Frame stack vs level stack —
CLAUDE.md: "The pane-tile level is the one second axis." Exit well vs leaf
link — they differ in what they reference (a grid vs a content row) and the
store keys delete and clone on that difference.

## Half B — the predicate sweep

**Done (2026-09-05).** Mechanical and behavior-preserving. 19 of the 20
sites re-routed; the twentieth (`content_zoom.go`) is a question, below.
Three new `rpc.Tile` methods, two new `cache.Grid` methods, one new
`pluginhealth` helper, each with a unit test in a package `make check`
executes. The greps are now a gate: `scripts/check-exception-owners.sh`,
on `make check`.

### New owners

| Owner | Where | Rule |
|---|---|---|
| `(*rpc.Tile).TextDocument()` | `api/rpc/types.go` | `Kind == KindText && !ServesPage` — a text tile whose content is its own document body: the one that fetches a blob, carries text framing, and shows the markdown face. |
| `(*rpc.Tile).PageContent()` | `api/rpc/types.go` | `ServesPage && Kind != KindURL` — presents at the `/content/` door: no persisted url state, no content zoom, no freeze intent. The complement of the url arm inside `WebContent()`. |
| `(*rpc.Tile).LeafLink()` | `api/rpc/types.go` | `LinkTargetID != ""` — the leaf half of `Reference`. `ContentID()` already resolves it; this names the question "is this row a link" where the answer is not an id. |
| `(*cache.Grid).HostContent()` | `client/cache` | nil-safe `g.Meta.HostContent`. |
| `(*cache.Grid).Stale()` | `client/cache` | nil-safe `g.Meta.Stale`. |
| `pluginhealth.UnrootedLink(*rpc.Tile)` | `client/pluginhealth` | `Reference && IsWellKind(Kind) && ChildGridID == ""` — a link with no target: the health tint's own precondition, which belongs beside `Classify`. |

### The sweep list

**`.ServesPage` — 9 sites**

| Site | Becomes |
|---|---|
| `client/wasm/render.go:708` | `case file.TextDocument():` |
| `client/wasm/render.go:943` | `if !n.TextDocument() {` (inside the `KindText` arm) |
| `client/wasm/nav.go:198` | `if file.TextDocument() {` |
| `client/wasm/input.go:1262` | `if !file.TextDocument() { return }` |
| `client/wasm/urlsync.go:237` | `if !ok || !file.TextDocument() || …` |
| `client/wasm/url_stream_client.go:97` | `if t.PageContent() {` — the `KindURL` arm returned already, so this is identity |
| `client/wasm/url_stream_client.go:187` | `page := t.PageContent()` |
| `client/wasm/content_zoom.go:121` | **not swept** — it would change behavior; see the question below |
| `client/wasm/input.go:1096` | `textedit.ModeInput` takes the answer, not the fields: its `Kind` and `ServesPage` became one `TextDocument bool`, filled by `file.TextDocument()`. A whole `rpc.Tile` was the design's first shape; the bool is smaller and, unlike the row, carries no second copy of `TextMode`, which the struct already holds as `Stored`. |

Already owners, no change: `api/rpc/types.go:267` (`WebContent`),
`client/wasm/url_preview.go:52` (`previewBlobKey` — it keys on `ServesPage`
alone deliberately, since a preview key is wanted for any page-serving row).
`DescentMode` was one too, and stopped reading the field at all: it now
takes `TextDocument` as an input.

**`.LinkTargetID` — 6 bare sites**

| Site | Becomes |
|---|---|
| `client/wasm/input.go:878–880` | `target := src.ContentID()` |
| `client/wasm/input.go:1052` | `hit.Kind == rpc.KindURL && hit.URLString == "" && !hit.LeafLink()` |
| `client/wasm/url_stream_client.go:136` | `if !t.LeafLink() { … }` |
| `client/wasm/url_stream_client.go:147` | `a.cl.GetTile(ctx, t.ContentID())` |
| `client/wasm/workspace.go:287,292` | `if id := fresh.ContentID(); id != tileID { tileID = id; refetch }` |
| `client/wasm/workspace.go:354,356` | same shape |

Already owners: `api/rpc/types.go:293` (`ContentID`),
`client/deadref/deadref.go:40` (`TargetID`).

**`Meta.HostContent` — 3 bare reads**

| Site | Becomes |
|---|---|
| `client/wasm/render.go:728` | `inHost := g.HostContent()` |
| `client/wasm/render.go:1275` | `childInHost := child.HostContent()` |
| `client/wasm/render.go:1426` | `if gridOK && g.HostContent() {` |

Already an owner: `client/wasm/drop_target.go:197` (`gridHostContent`),
which becomes a lookup plus the new method.

**`Meta.Stale` — 1 bare read**

`client/wasm/bottombar.go:140` → `if !ok || !g.Stale() { return }`.

**`.Reference` — 1 bare read**

`client/wasm/palette_draw.go:204` → `if pluginhealth.UnrootedLink(n) {`.
Already owners: `client/wasm/render.go:1084` (`isLinkTile`),
`client/deadref/deadref.go:37` (`TargetID`).

**`.URLFrozen` — judged, not a sweep item**

Two reads. `client/wasm/input.go:1234` feeds `shellconn.DecideAutoLive`, the
owner. `client/wasm/url_stream_client.go:119` is the unfreeze-on-open guard
inside the one function that clears the intent — a predicate around a single
field read here would add a name and own nothing.

**`.TextPresentation` — no bare reads, one misplaced owner**

Both reads are inside owners: `text_overlay.go:630` (`textToggleVisible`)
and `rendered_overlay.go:222` (`presentationHTML`). Both owners live in
`client/wasm`, so `make check` executes neither; the gate allow-lists both
files, with the reason, until Q3's move to `client/textedit`.

**`tileReadOnly` — no bare reads**

All eleven call sites (`rendered_overlay.go:120,176`; `input.go:1096,1283`;
`nav.go:205`; `text_flush.go:104,139`; `text_overlay.go:410,461,596`;
`urlsync.go:238`) go through `a.tileReadOnly` (`render.go:1041`), which is
the owner. It needs an `App` for the grid lookup, so it stays in the shim;
its rule is one line and does not warrant extraction on its own.

**`Meta.Writable` — no bare reads**

Both are inside `a.gridWritable` / `a.gridKnownReadOnly`
(`plugin_id.go:56,65`). Q7 proposes folding them into one tri-state after
the sweep.

### The one site left as a question

`client/wasm/content_zoom.go:121` reads bare `t.ServesPage`; the owner
`PageContent()` also requires `Kind != KindURL`. A url tile with
`serves_page` set — which nothing mints today, but the wire permits, since
both fields come from a plugin `Entry` — would keep its content zoom under
the predicate and lose it under the bare read. That is a behavior
difference, not a rename, so the sweep left the line alone and the gate
allow-lists it with the reason.

**The question for the owner:** should a url row that also declares
`serves_page` keep its content zoom (take `PageContent()` here, and the two
"is this a page" readers agree), or is the row itself the thing to refuse —
the node rejecting a `serves_page` url entry at the plugin door, so the
shape never reaches a client? The second closes the class; the first closes
this line. Either way the allow-list entry comes out.

**Answered (2026-09-05): refuse the row.** Nothing wants the combined shape
— a url entry must supply `url_string` and a page has no address — and it
failed silently, since `webAddress` answers `UrlString` first and the page
never served. `checkEntries` in `internal/pluginhost` refuses an entry
declaring both, at the one door a plugin's entries enter. The line then
takes `PageContent()` as identity, and the allow-list entry is out.

Not to be confused with Q4's separate gap in the same function: the
`possiblyEphemeral` guard `applyContentZoom` was missing has since landed,
and the sweep did not disturb it.

`client/wasm/input.go:1052` is the second one to read twice: `LeafLink()` is
exactly `LinkTargetID != ""` there, so it is identity — but note that
`isLinkTile` (which reads `Reference`) is **not** a substitute, because
`Reference` is also true for a well with a qualified child grid. Use
`LeafLink()`, not `isLinkTile`.

### The gate

The verifying greps are `scripts/check-exception-owners.sh`, on `make
check` beside `check-vocabulary` and `check-docpaths`. Each of the seven
fields may be read only in the paths `scripts/exception-owners.txt` lists,
which carries the reason for every entry. Scope is `client/` and
`api/rpc/`; `_test.go` and `client/wasm/testhook.go` are exempt by path
(the testhook reports fields to the e2e harness and decides nothing), and a
whole-line comment is not a read, so an owner can still name the field in
prose.

Two allow-list entries are open questions rather than owners, and say so in
the file:

- `client/wasm/content_zoom.go` — the behavior question above.
- `client/wasm/text_overlay.go` — `textToggleVisible` is a real owner, but
  it sits in `client/wasm`, where `make check` executes nothing. Q3's move
  to `client/textedit` clears it, along with `rendered_overlay.go`'s
  `presentationHTML`.

### What landed

1. The six owners, each with a unit test, paired in one commit with the
   sites that use them — the deadcode gate refuses an owner with no caller,
   which is the right shape for the history anyway: one commit per field
   family, each saying why the rewrite is identity.
2. 19 sites re-routed; `content_zoom.go` left as the question above.
3. The greps as the gate.

Gates run: `make check` green on each commit, plus a targeted `check-e2e`
chunk (fs page and content, text edit, tile links, plugin health, dead
link, workspace rebind and round trip, freeze intent, content zoom,
ephemeral url, swatch click) — 20 specs, all green, since every changed
line is in `client/wasm`, which `make check` compiles and never runs.

Still open, in order: Q3's `client/textedit` move and Q7's tri-state, if
the owner agrees, then the `content_zoom.go` question above.
