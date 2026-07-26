# Gridwell

Gridwell is a single-tenant personal operating environment. Tiles live
on a 2D grid; drop one at a coordinate and it stays there. A tile's
preview is what you see when you descend into it; ascend after editing
and the preview shows what you were just looking at.

For the *machine* — the layers, the seams, the invariant inventory, and where
the bugs come from — read **`ARCHITECTURE.md`**. This file is the *philosophy*
(what must always be true) and the *engineering charter* (how to change the code
without breaking it). The charter is not optional; it is the answer to "why does
nothing stay fixed."

## The guiding rule: things stay as you left them

**This is the deciding factor.** When a technical decision is unclear,
the option that preserves this principle wins — over performance, over
elegance, over implementation convenience. If a design lets something
change that the user didn't change, the design is wrong.

Gridwell is a physical space. You rearrange it constantly — it is
write-heavy and mutates freely (drop a tile, pan, capture a page,
type) — but **nothing changes except by your explicit action.** Step
out of a room and look back (ascent): you see it exactly as you left
it. Step back in (descent): it is exactly the same. The round trip is
idempotent, and that holds for *everything* — content, view framing,
and layout alike. Reading never mutates.

Four faces of one rule:

1. **Placement is persistent.** A tile at (x, y, w, h) stays until
   something explicitly moves it. No auto-relayout, no resort.
2. **Identity is persistent and stable.** A tile's row id is a
   permanent handle: editing a tile never moves it (mutation is
   in-place; the id never changes), so a reference always returns
   *that* tile. Copies are made only by the explicit **clone** gesture.
3. **Preview = descent target = ascent return.** A well's stored
   framing *is* its preview; descending restores it; ascending writes
   it back. One value, read the same way every time.
4. **Mutation is local and reflected.** Every change goes through the
   store, which fans an event to every open view.

When you make a judgement call about correct behavior, the guiding rule decides.
Exceptions are deliberate and few: the menu appears only on the focused pane (to
show focus), and processes/files outside Gridwell may come and go outside our
control (though their *placement* stays stable while they exist).

**Session-ephemeral by decision (issue #13, owner call 2026-07-08; pane
tiles added 2026-07-09):** the SESSION split-pane layout (splits, ratios,
pane zoom — the `pane.Tree` at the root of a session), the rendered-mode
caret, and the selection live only in the client session and reset on
reload. These are the *documented* exceptions to charter §7 ("no client-only
state") — transient workspace scaffolding, not content; nothing they lose is
a user's data. Only the focused pane's descent path + viewport rides the URL.
**The opt-in durable home for a layout is the `pane` tile** (owner decision
2026-07-09): make a workspace a *thing* and its whole arrangement — splits,
ratios, per-pane places and viewports, focus, zoom — becomes a server fact
(the layout blob, written by the snapshot-diff persister; layout writes are
framing-class and never bump version). The session tree at the root stays
ephemeral; the workspace *stack* (which pane tile you are inside, and the
outer tree each descent replaced) is session-only like portal frames — a
reload restores the innermost workspace from `?w=`, and its bar ascent falls
back to the pane tile's containing grid. Do not persist the remaining
session-ephemeral items without a new owner decision, and do not re-litigate
the choice.

---

## The engineering charter (READ THIS — it is why fixes haven't stuck)

The instability in this project has one root cause, and it is not bad luck. An
independent analysis of the full git history, the conversation record, the
architecture, and the test suite all converged on the same finding:

> **The same fact is stored in several parallel copies with no single owner, and
> written from many code paths. A fix corrects one copy; another path keeps
> writing the rest; the symptom returns later under slightly different
> conditions.** ("You're drawing it different in two places.")

Every recurring bug — menus disappearing, previews going wonky, content
vanishing, controls on the wrong pane — lives at a **seam between two
representations of the same state.** `ARCHITECTURE.md §8` catalogs the seams.
The charter below exists to stop creating new ones.

### 1. One fact, one owner. Derive once, read everywhere.

This is the prime directive. **Before adding any field, map, or piece of state,
ask: does this fact already live somewhere?** If yes, read it from there — do
not copy it. If a fact genuinely must be available in two layers, **one place
*derives* it and everything else *reads* it**; there is never a second writer.

The codebase already contains the cure five times. **Copy these shapes; do not
invent a parallel one:**
- `Tile.reference` — "is this well a link" derived once in `qualifyTiles`, read
  by render/delete/clone.
- `emitTileChanged` / `finishContentEdit` — "does this mutation bump version"
  decided in one place in `store/tiles.go`.
- `classifyStoreError` — one error→status mapping.
- `zoomtrans.LiveFromIntrinsic` / `IntrinsicFromLive` — the viewport transform,
  one pure pair.
- `client/menu` — "is the menu open, and on which pane": one state machine
  where 14 scattered boolean writes used to live. The worked proof that
  applying this cure to a chronic symptom makes it stop recurring.
- `cache` content entries — "the text bytes this client has seen, which
  server version they derive from, and whether they carry an unsaved edit":
  one `{bytes, base, dirty}` fact. Saves claim `SaveBasis` (advanced only by
  fetches and save responses), so a version can never be claimed apart from
  its bytes — the foreign-writer stomp became unrepresentable instead of
  patched. And every save READS the entry by tile id through one door
  (`client/wasm/text_flush.go`) — no flush touches the DOM — so bytes can
  never be posted under another tile's id (the 2026-07-18 cross-tile stomp,
  same cure extended one level).

Each makes a bug class *unrepresentable*. That is the goal of every change:
prefer the design where the bug **cannot be written**, over the design where the
bug is merely fixed this once.

**Concretely, when you touch the fragile areas** (viewport/framing, the menu,
focused-pane controls, native-view bounds — see `ARCHITECTURE.md §5–8`): do not
add another write to one of the existing copies. **Reduce the number of copies.**
A change that leaves the count the same has not fixed the class; it has added a
patch that will regress.

### 2. Root-cause before you touch anything. No papering over.

The single sharpest lesson from this project's history:

> "You implemented a 'fix' that reloads the page… You didn't even try to figure
> out *why* the tab crashed. That level of hacking and patching is what got us
> into this problem in the first place."

**Find the mechanism before you change a line.** State the root cause in one
sentence ("the menu stays open because the right-drag end path at
`right_button.go:276` is the only one that clears `menuOpen`, and this gesture
ends through `input.go:419` instead"). If you cannot name the mechanism, you are
guessing — keep digging. A reload, a retry, a `setTimeout`, a re-fetch, a
defensive `if` that hides a `nil` — these are smells, not fixes. **Evidence, not
medium-confidence guesswork.** Add logging and reproduce before you conclude.

### 3. Every bug fix is a test first.

No fix lands without a test that **fails before it and passes after.** This is
not negotiable, and it has three parts (the owner asks all three every time):
1. **A reproducing test.** Write it first; watch it fail for the real reason.
2. **"Why was this not caught?"** Answer it in the commit message. The gap that
   let this bug exist is itself a bug — close it.
3. **Fix the class, not the instance.** "Can we solve this *class* of bugs?" If
   text tiles vanish on drop, the fix is not "handle text tiles" — it is "why
   did the create→redraw seam drop one kind, and what guarantees no kind is
   dropped?"

If the code is not testable as written, **make it testable** — extract the logic
into a pure package (see §5). "It can only be tested by running the app" is a
structural defect to fix, not an excuse.

### 4. Test the seam, not just each side.

A unit test on each side of a contract **will not** catch a contract mismatch —
and the contract mismatch is the bug. The framing-writeback story is the
cautionary tale: the store side was tested (`framing_version_test.go`), the
client geometry was tested (`client/preview`), and the round trip — descend →
reframe → ascend — was tested *nowhere*, which is exactly where it kept
breaking. It now has its crossing test (`framing-roundtrip.spec.ts`); that spec
exists because the gap was found, not because the seam was designed with one.
When you fix a seam bug, the test must **cross the seam**. The bugs that have
stayed fixed (the live-view context menu, the menu owner) are the ones with
both a unit test *and* a real-stack e2e — that is the standard.

### 5. The wasm orchestration and the native layer are where bugs hide. Treat them accordingly.

- **`client/wasm` is ~10,700 LOC with zero unit tests**, and `make check`
  compiles it but runs none of it. Do not add more untested orchestration there.
  When you change behavior in `input.go`/`render.go`/`right_button.go`, **extract
  the decision logic into a `js`-free `client/*` package** (as `client/pane`,
  `client/gesture`, `client/zoomtrans` already are) and unit-test it. The wasm
  file becomes a thin call into tested code.
- **The Electron native layer (`apps/desktop/src/main`) is invisible to
  `make check`** — `WebContentsView`s are separate webContents off the main page.
  Logic there (bounds, clip, focus, teardown, IPC) must be either extracted to a
  pure-function module with a unit test (as `viewutil.ts`/`contextmenu.ts` are)
  **and/or** covered by `make check-electron` / `make check-e2e`. `webviews.ts`
  (the WebContentsView registry) is the documented bug source and currently has
  no test — anything you touch there needs one.

### 6. Errors must surface. Never swallow to console.

A failure that logs to the console and returns presents to the user as
**"it just disappeared"** — no error, no evidence, no way to root-cause. That is
the silent-disappearance bug class. Surface errors to where they can be seen and
asserted on; an optimistic local mutation that the server rejects must visibly
reconcile, not vanish.

### 7. No client-only state.

"We don't want stuff on the client that doesn't get persisted on the server."
Anything the user can change is a server fact, written through the store and
reflected by an event. Client state is a *cache and a view* of server truth,
never an authority. (This is also why §1 matters: the client copies of framing
are caches that must reconcile to the one server-owned `view_*`.)
The decided exceptions — split-pane layout, rendered caret, selection — are
listed under the guiding rule above; they are ephemeral by owner decision,
not by omission.

### 8. DRY is correctness, not tidiness.

"The idea behind DRY code is that you change something in one place and it fixes
all the places." Duplication here is not a style issue — it is the disease in §1
wearing a different hat. If a fix in one place doesn't fix a visibly identical
behavior elsewhere, you have found duplication; unify it rather than patching
both.

### 9. Commit incrementally; never batch; never silently deviate.

Commit each logical change as it lands — "I don't want files just hanging
around." And: **"Don't silently ignore me."** If an instruction seems wrong, or
the guiding rule and a request conflict, say so and propose the alternative —
do not quietly do something else.

---

## The verification gates, and when to run them

There are **five** gates. `make check` is the fast per-commit gate; it **must
be green on every commit.** But it *cannot see the native shell*, and the native
shell is where the worst bugs live — so it is necessary, never sufficient, for
anything touching live tiles, panes, focus, previews, or the menu.

| Gate | Runs | Sees | Run it when |
|---|---|---|---|
| `make check` | Go build+test, the `GOOS=js` wasm **build**, TS typecheck, `npm test` (main-process unit tests) | pure Go + pure TS logic. **Compiles `client/wasm` but executes none of it.** | every commit, always |
| `make check-electron` | `npm run test:integration` + `test:bridge` under xvfb | the real Electron `WebContentsView` / PTY bridge | any change to the URL/shell live path, the bridge, `webviews.ts`, or the shell IPC relay |
| `make check-e2e` | Playwright drives the **real app** (Electron + Go sidecar) under xvfb | the full renderer→wasm→RPC→server→SQLite composition, as a black box | any `apps/desktop` change, the native layer, or a cross-seam behavior; pre-merge |
| `make check-web` | Playwright drives `gridwell serve` + plain system Chromium, headless | the **browser-mode client** (no Electron bridge): caps gating, the no-live URL affordance, the touch gesture layer via real injected TouchEvents | any change to `client/caps`, `client/touchgest`, `touch.go`, the serve/boot path, or other capability-gated behavior |
| `make check-federation` | the REAL binaries — `gridwell` init/serve, go-plugin subprocess spawn incl. `gridwell-ssh` — through a real ssh tunnel, headless, ~1s after build | the **spawn seam** no in-process test can see (the pluginmeta driver bug lived exactly there) | any change to plugin spawn, `sshdial`, the node export, or id routing |

**The rule:** if a change lives in or affects the native layer (the Electron
main process, preloads, the WebviewRegistry, the live-URL/shell path, IPC, the
menu over a live view, a preview of a live tile), `make check` passing means
nothing. You **must** also run `make check-electron` and/or `make check-e2e`
**and** add or extend a spec that exercises the behavior end to end. The
right-click context-menu regression is the cautionary tale: the canvas-only
harness never touched a live view, so a whole interaction layer had no coverage.

**Invariants that still lack a full test home — give them one when you touch
them** (`ARCHITECTURE.md §11`; each is a tracked GitHub issue):
- **Preview round-trip** (I7, issue #19): the viewport round trip is locked
  (`framing-roundtrip.spec.ts`), but "the well *preview* and a sibling pane are
  byte-identical to before" is still unobservable — the e2e `testhook` does not
  expose a preview signature; extend it.
- **SSE during animation** (I11): the framing/data separation is verified by
  inspection only — no test injects an event mid-transition; a new framing
  write into the SSE path would regress silently. (The other I11 half — the
  optimistic-echo / foreign-writer reconcile — graduated 2026-07-17: stale
  echoes dropped, text bodies aged with the row version, saves claim
  `cache.SaveBasis`; unit-tested in `client/cache` and crossed end-to-end by
  `foreign-writer.spec.ts` and the federation gate's event step.)
(I10 menu persistence and I12 source-sweep stability graduated: single
owners, unit + e2e / seam tests.)

---

## Identity and clone semantics

Nothing the user didn't touch ever re-rows. The consequences:

- **A tile's id is its durable identity.** Assigned once by its owning plugin,
  never changed; editing a tile rewrites it in place. An embed, a deep-link URL,
  or a bookmark *is* that id — it always resolves to the same tile.
- **`version` (int)** bumps on every content mutation: the optimistic-concurrency
  key and the spine of edit history.
- **Framing is not a content edit.** Panning / zooming / scrolling re-frames the
  preview without bumping version. It is still a real, persisted, in-place
  mutation — it just isn't a content change. This split is enforced in **one
  place** (`emitTileChanged` vs `finishContentEdit` in `store/tiles.go`); keep it
  there. It is the model for every other invariant.
- **Clone is an eager copy.** Deep copy of the tile and, for a well, its whole
  child subtree within the *same plugin*: new ids for the copy, blobs shared by
  content address. No structural sharing, no copy-on-write — so an edit to one
  copy can never touch another, and no id is ever reassigned. (Note: the file
  `internal/store/cow.go` is misnamed; COW was removed. See `ARCHITECTURE.md §9`.)
- **Across a plugin boundary there is no move — left-drag links, right-drag
  clones (owner decision 2026-07-19, reversing PR #55's right-drag-link; do
  not re-reverse without a new owner decision).** The invariant: a left-drag
  never duplicates content; a right-drag always does. Identity never migrates
  an id namespace, so the cross-plugin left-drag creates a LINK — the content
  stays where its id lives, the destination gains a reference, the source is
  untouched, and the in-flight ghost teaches this (dashed + chain badge).
  Every primitive has a link variant: a well links via a qualified
  `child_grid_id` (the exit well), a leaf (text/url/shell/pane) via
  `link_target_id` (a qualified tile reference; the row owns no content —
  readers resolve bytes/preview/session/layout through the target, and the
  client's one resolution point is `rpc.Tile.ContentID`). A Plugin tile is
  *always* a link. Cross-plugin right-drag CLONES the dragged tile: a leaf
  copies bytes, a link tile copies as another link (this is also how a mount
  is made from the node grid), and a solid well is refused loudly until deep
  cross-plugin copy exists (`cloneAcrossPlugins`, CodeUnimplemented).
  Relocation across plugins is the explicit two-step: clone, then delete the
  source — the identity break happens where the user can see it.
- Every cross-plugin clone and link carries the source's `object_id` as a
  **provenance marker** (a globally-unique 128-bit hex — these came from the
  same origin) — used only by future horizontal-navigation gestures, never for
  identity or routing. The plugin-scoped numeric id is identity.

### Why clone is eager (the deciding case)

When a design choice is in question, run it through one test: **after this
change, does everything the user didn't touch stay byte-for-byte the same,
and does every reference still resolve to the thing it named?**

A copy-on-write design shares a subtree on clone and forks it on the first edit.
A fork *re-rows* tiles — the edited instance's id moves and a sibling inherits
the old id. A stored reference (an embed link, a saved URL, a bookmark) can then
silently resolve to the *wrong instance's* content. No patch recovers it. The
option that keeps the principle: **never reassign ids.** Editing is in-place;
copying happens only at the explicit clone gesture, as an eager, independent deep
copy. The cost lands where the user asked for it: clone is O(rows) of cheap
metadata (blobs shared by content address), paid exactly at the "make me a copy"
gesture. Edits stay O(1). This is also why COW was torn out of the live codebase:
the patches could not make it safe.

## Plugin namespaces and stable identity

Gridwell is a federated space. Each **plugin instance** — a local Gridwell DB,
a mounted filesystem, the process table, a remote server reached over SSH —
owns its own id space and is identified by an id assigned once and stored
permanently (in the plugin's own DB and referenced in `server.yaml`).

**Id shape (owner decision 2026-07-25):** new plugin and node ids are **7
characters of lowercase base36 with a leading letter** (`store.NewShortID`,
e.g. `k3x9m2q`) — short enough to read in a URL path, and the leading letter
guarantees an id can never be purely numeric, which is how paths and embed
hrefs tell a namespace segment from a tile id (`config.Load` enforces it).
Ids minted earlier are 32-char hex; **an id is immutable once minted** (it
lives inside other plugins' stored references, Electron session partitions,
and tmux socket names), so both shapes are valid everywhere, forever. The
per-tile provenance `object_id` is unrelated and stays full 128-bit hex.

A globally qualified id is `<plugin_id>/<local_integer>`. The local integer
is allocated by the plugin (SQLite AUTOINCREMENT; never reused once issued).
The plugin id makes it globally unique. Both are necessary in every stored
cross-plugin reference.

**Every plugin has its own SQLite DB.** This is how the plugin allocates stable
numeric ids and stores tile positions durably. A filesystem plugin maps paths →
integers; a proc plugin maps PIDs → integers; a localdb plugin *is* its DB.
Plugin storage is the authoritative state for the plugin's tiles. The localdb
format is **frozen and forward-compatible** — see `internal/store/CLAUDE.md` for
the additive-only schema-evolution contract; never delete a DB to absorb a change.

**Every node exposes a node grid; home is the first plugin.** A node's plugin
list is a real, read-only grid — the **node grid**, `<node_id>/0` (`node_id`
lives in server.yaml; `internal/server/nodegrid.go`) — one dashed **link
tile** per plugin (tile id = the plugin's uuid, child = its qualified root,
framing = its persisted root view). It is the federation surface (an ssh
mount lands on the remote's node grid; `?a=<node>/0` addresses it), but NOT
the local landing page: the client boots into **home**, the first configured
plugin's root grid (`rpc.HomeGrid`, one derivation; node grid as fallback for
a node with no rooted plugin), and "/" is home's URL. **Plugins live on the +
menu's top row** (above the primitives): clicking a swatch descends into the
plugin, dragging one drops an exit-well link — owner decision 2026-07-19,
reversing the launcher-as-landing-page decision of PR #41; do not re-reverse
without a new owner decision. Descending into ANY link tile — or a + menu
plugin swatch — is a **portal** (the pane's anchor swaps to the link's target
and a frame is pushed for the return trip), so navigation has one vocabulary
whether the link points at a local plugin, a mounted directory, or a remote
node. A menu portal's ascent writes the plugin's root view back via
`SetRootView` directly (`savePluginRootViewBeforeAscent`) — the same fact a
node-grid tile write routes onto.

**Remote is just a transport, and the mount is the whole node.** The `ssh`
plugin tunnels to a remote node's one HTTP/h2c port (its `bind:`) and dials
the **node export** (`internal/server/nodeexport.go`): the full Gridwell
service over raw gRPC, routed by the **qualified ids** each request carries —
the same routing implementation the browser front door uses, plus the
plugin-grade streams (`OpenShell`, `GetSession`, `PutSession`) routed by the
id in their first message. There is no selector and no name-based routing.
The mount's root is the remote's node grid, so descending into the ssh plugin
shows the remote's own launcher; every remote plugin is a link tile there.
Ids **chain**: each hop peels one segment from requests and prepends one to
responses (`ssh` is a *transit* plugin — `Registry.Transit`), so
`<ssh>/<plugin>/<id>` routes through any number of hops, and the ssh binary
itself stays a dumb pipe (`internal/plugin/proxy`). The dial path (key auth,
mandatory known_hosts, tunnel, gRPC-over-h2c) is `internal/plugin/sshdial`,
seam-tested against a real in-process ssh server fronting a two-plugin node.

**Cross-plugin link is the left-drag; clone is the right-drag (2026-07-19).**
Left-dragging any tile into another plugin's grid creates a LINK in the
DESTINATION — an exit well sharing the source's grid, or a leaf link
(`link_target_id`) naming the source tile — committed as a plain `CreateTile`
(the same shape a + menu swatch drop uses; `commitLinkDrop`). A dashed border
always means link: deleting it only unlinks, and content ops read/write
through the target (`rpc.Tile.ContentID`, the one resolution point). A leaf
link dragged onward links to the original TARGET, never to the middleman.
Right-drag CLONES: leaves copy bytes through `cloneAcrossPlugins`, link tiles
copy as links, and a solid well is refused (unimplemented) — there is no
cross-plugin move (identity doesn't migrate; `DecideDrop` verdicts `DropLink`
instead).

**URL and path format (2026-07-25: anchor-as-path).** A pane's URL is its
ANCHOR — the qualified grid id it sits inside — as LEADING PATH SEGMENTS,
then bare tile-id segments within that namespace: `/<plugin>/<grid>/3/4`
(e.g. `/k3x9m2q/1/3/4`). The grammar has one rule: leading non-numeric
segments are the namespace chain, the first numeric segment is the anchor
grid id, the rest are tile ids — sound because plugin/node ids can never be
purely numeric (short-id leading letter; `config.Load` enforces it). Home is
the empty anchor, so "/" (and bare `/3/4`) is home. Crossing a plugin
boundary is a PORTAL — the anchor swaps — so tile ids never mix namespaces;
a remote anchor is simply more leading segments (`/<ssh>/<plugin>/1/4`).
The legacy `?a=<anchor>` form still decodes (old bookmarks) but is never
emitted. Two kinds of links:
- **URL bar / bookmarks** — anchor + path + viewport. A *place*. The
  ascent-return target depends on it.
- **Markdown embed links** — a single qualified id (`<plugin_uuid>/<tile_id>`).
  A *thing*: globally routable, stable, survives restructuring of the containing
  space. The server resolves it directly without the descent path.

## The plugin gRPC interface (same as the server interface)

Every Gridwell node — a local server, a plugin, a remote server reached over
SSH — implements the **same single gRPC service**, defined once in
`api/gridwell/v1/data.proto` and buf-generated (**reduced to 17 RPCs in the
2026-07-26 redesign** — eleven owner decisions, recorded in the git history
of `interface-redesign-plan.md`). The client calls the local server; the
local server calls plugins; an SSH gateway proxies to a remote server. Same
wire protocol at every hop — **every byte crosses this interface**, including
the live shell PTY. The proto is the single source of truth for *both* the
wire types and the DB columns; a drift-lint test fails the build if
`Grid`/`Tile` and `schema.go` diverge.

**Every plugin is a separately-compiled Hashicorp go-plugin binary** — including
the localdb. The server spawns each binary named in `server.yaml` over go-plugin's
gRPC transport. There is no in-process plugin in production (the in-process path
survives only as a test harness; its loader comment claiming otherwise is stale —
`ARCHITECTURE.md §9`).

**The whole contract for any caller is: qualified id, version claim,
kind-dispatched semantics.** No request carries a descent path (the server
derives location facts from rows it owns — the move-cycle refusal is a
store-side ancestor walk), no session or network facts cross the wire, and
there are exactly two byte-moving shapes: the content streams (values) and
the shell wire (bytes in motion).

- **Reads:** `GetGrid`, `GetTile`, `GetTilePreview`.
- **Content bytes:** `ReadContent` / `WriteContent` — the ONE way content
  bytes move. Values: a read is finite and repeatable, chunk 1 pairing the
  bytes with the row version they belong to (the save basis, never split); a
  write claims a version and COMMITS AT CLOSE — a broken stream leaves the
  old value byte-for-byte intact. Version semantics stay kind-determined in
  the store's one table: a text body bumps; a pane layout is framing-class
  and never does. `ReadContent` (and `GetTilePreview`) on a leaf link
  resolves to the target AT THE SERVING NODE (`contentRoute` — one
  resolution point every caller inherits); writes never resolve (a link owns
  no content and the store refuses its row).
- **`CreateTile`** — one create for every primitive, METADATA ONLY (a body
  follows as a `WriteContent`; one way to write bytes). `tile.kind` selects
  the fields; a file/process/remote well is just a `well` tile with a
  cross-plugin `child_grid_id`.
- **`SetTile`** — one writeback for framing/preview and the two absorbed
  scalar operations, exactly one operation per call: the kind-dispatched
  writeback (well/text framing never bumps `version`; url/shell freeze
  does — face #3 of the primary rule, enforced in one place), `rename` (the
  versioned user rename; latches `alt_user` so automatic captures defer),
  or `content_zoom` (framing).
- **`PlaceTile`** — the single placement writeback: placement is one fact,
  `(grid_id, x, y, w, h)`, and one verb owns it (a move, a resize, or both
  in one write). Plus `CloneTile` and `DeleteTile`.
- **Lifecycle:** `Info` reports the plugin's kind, label, and **default root grid
  id** — the whole handshake; there is no `Attach`/`Detach`. `Probe` confirms a
  single tile's presence (a failed read must never sweep a tile — only `GONE`
  does); `ListPlugins` enumerates a node's plugins. `SetRootView` persists a
  plugin root's framing (the lone structural special case — roots have no
  tile row).
- **Live bytes:** `OpenShell` streams a PTY both ways — deliberately the ONE
  live wire, PTY-shaped (`ShellSessionAlive` gates the refresh affordance).
  A future live tile kind adds its own verb; there is no speculative
  generic channel.
- **Events:** `Subscribe` streams change events (localdb emits; fs/proc are
  polled via `GetGrid`).

**The Chromium session is host-local (owner decision 2026-07-26, reversing
"the plugin is the session boundary").** Every live `url` tile — local or
through a mount — renders on the ONE shared persistent Electron partition
(`persist:gridwell`): your logins are your logins everywhere. Nothing about
sessions or networks crosses the interface, and live tiles always browse
from the host's own network (the tunnel SOCKS proxy is gone). Chromium's own
disk persistence is the session's system of record — a documented exception
to charter §7 alongside the split-pane session tree (machine-local state,
like processes and files). The shell PTY transport is likewise host-side:
the Electron main process dials the sidecar's node export and relays
`OpenShell` per pane over IPC (`shellstreams.ts`); browsers get frozen shell
previews, caps-gated like live url tiles.

---

## Making changes: the checklist

No part of Gridwell may be constructed without tests. Small, trivial glue layers
are the only exception, and they must be minimized. Prefer slowing down to fix a
test gap over getting a feature "finished" — a feature that isn't tested at the
seam where it breaks is not finished.

Before you commit, every one of these is true:

- [ ] I named the **root-cause mechanism** in one sentence (for a fix), or the
      **one owner** of any new fact (for a feature). I did not guess.
- [ ] I did **not** add a new copy of an existing fact. If anything, I reduced
      the copies (`ARCHITECTURE.md §8`).
- [ ] A **test fails before my change and passes after**, and it crosses the
      **seam** where the behavior actually lives.
- [ ] For a bug fix, the commit message answers **"why was this not caught?"** and
      I closed that gap.
- [ ] `make check` is green. If I touched the native/live layer, **`make
      check-electron` and/or `make check-e2e` are green and I added/extended a
      spec.**
- [ ] No error is swallowed to the console; failures surface and reconcile.
- [ ] Nothing the user can change lives only on the client.
- [ ] I committed this logical change on its own, and updated any stale comment in
      a file I touched (`ARCHITECTURE.md §9`).
- [ ] The guiding rule still holds: everything the user didn't touch is
      byte-for-byte the same, and every reference resolves to what it named.
