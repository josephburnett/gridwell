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

The codebase already contains the cure four times. **Copy these shapes; do not
invent a parallel one:**
- `Tile.reference` — "is this well a link" derived once in `qualifyTiles`, read
  by render/delete/clone.
- `emitTileChanged` / `finishContentEdit` — "does this mutation bump version"
  decided in one place in `store/tiles.go`.
- `classifyStoreError` — one error→status mapping.
- `zoomtrans.LiveFromIntrinsic` / `IntrinsicFromLive` — the viewport transform,
  one pure pair.

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
cautionary tale: the store side is tested (`framing_version_test.go`), the
client geometry is tested (`client/preview`), and the round-trip — descend →
reframe → ascend → assert the sibling pane is byte-identical — **is tested
nowhere**, which is exactly where it breaks. When you fix a seam bug, the test
must **cross the seam**. The one bug that has stayed fixed (the live-view context
menu) is the one with both a unit test *and* a real-stack e2e — that is the
standard.

### 5. The wasm orchestration and the native layer are where bugs hide. Treat them accordingly.

- **`client/wasm` is 10,660 LOC with zero unit tests**, and `make check`
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

There are **three** gates. `make check` is the fast per-commit gate; it **must
be green on every commit.** But it *cannot see the native shell*, and the native
shell is where the worst bugs live — so it is necessary, never sufficient, for
anything touching live tiles, panes, focus, previews, or the menu.

| Gate | Runs | Sees | Run it when |
|---|---|---|---|
| `make check` | Go build+test, the `GOOS=js` wasm **build**, TS typecheck, `npm test` (main-process unit tests) | pure Go + pure TS logic. **Compiles `client/wasm` but executes none of it.** | every commit, always |
| `make check-electron` | `npm run test:integration` + `test:bridge` under xvfb | the real Electron `WebContentsView` / PTY bridge | any change to the URL/shell live path, the bridge, or `webviews.ts` |
| `make check-e2e` | Playwright drives the **real app** (Electron + Go sidecar) under xvfb | the full renderer→wasm→RPC→server→SQLite composition, as a black box | any `apps/desktop` change, the native layer, or a cross-seam behavior; pre-merge |

**The rule:** if a change lives in or affects the native layer (the Electron
main process, preloads, the WebviewRegistry, the live-URL/shell path, IPC, the
menu over a live view, a preview of a live tile), `make check` passing means
nothing. You **must** also run `make check-electron` and/or `make check-e2e`
**and** add or extend a spec that exercises the behavior end to end. The
right-click context-menu regression is the cautionary tale: the canvas-only
harness never touched a live view, so a whole interaction layer had no coverage.

**Invariants that currently have no test home — give them one when you touch
them** (`ARCHITECTURE.md §11`, I7–I11):
- **Preview round-trip** (I7): descend → reframe → ascend → the well preview and
  a sibling pane are byte-identical to before. The e2e `testhook` does not yet
  expose preview state; extend it.
- **Menu persistence** (I10): the menu closes on focus change and is gone/restored
  correctly across ascent — assert it, don't eyeball it.
- **Pane-content stability** (I11): an unfocused pane's content/preview does not
  change when another pane is acted on.

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
- **Cross-plugin clone is a link, not a copy.** Cloning a well across plugin
  boundaries creates a tile in the destination plugin whose child points back at
  the source plugin's grid (a qualified cross-plugin reference). The source
  grid is shared, not duplicated. Cloning a leaf (text/url/shell) across
  plugins copies the bytes into the destination plugin's blob store.
- A clone may carry the source's `object_id` as a **provenance marker** (these
  came from the same origin) — used only by future horizontal-navigation
  gestures, never for identity or routing. The plugin-scoped numeric id is identity.

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
owns its own id space and is identified by a UUID assigned once and stored
permanently (in the plugin's own DB and referenced in `server.yaml`).

A globally qualified id is `<plugin_uuid>/<local_integer>`. The local integer
is allocated by the plugin (SQLite AUTOINCREMENT; never reused once issued).
The plugin UUID makes it globally unique. Both are necessary in every stored
cross-plugin reference.

**Every plugin has its own SQLite DB.** This is how the plugin allocates stable
numeric ids and stores tile positions durably. A filesystem plugin maps paths →
integers; a proc plugin maps PIDs → integers; a localdb plugin *is* its DB.
Plugin storage is the authoritative state for the plugin's tiles. The localdb
format is **frozen and forward-compatible** — see `internal/store/CLAUDE.md` for
the additive-only schema-evolution contract; never delete a DB to absorb a change.

**Remote is just a transport.** The `ssh` plugin dials a remote host's Gridwell
gRPC endpoint and serves a **transparent proxy** (`internal/plugin/proxy`) that
forwards the *entire* service — unary calls, the `OpenShell` PTY stream, and the
session blob — with full fidelity. So a remote plugin's wells, tiles, live
shells, and session reach the local server over the same interface as a local
plugin; "remote" adds nothing to the vocabulary.

**URL and path format.** The descent path is encoded as alternating
plugin-uuid / tile-id segments, with the plugin uuid omitted when it equals the
previous segment: `/<p1>/<t1>/<t2>/<p2>/<t3>`. Two kinds of links:
- **URL bar / bookmarks** — the full descent path + viewport. A *place*. Must be
  the full path; the ascent-return target depends on it.
- **Markdown embed links** — a single qualified id (`<plugin_uuid>/<tile_id>`).
  A *thing*: globally routable, stable, survives restructuring of the containing
  space. The server resolves it directly without the descent path.

## The plugin gRPC interface (same as the server interface)

Every Gridwell node — a local server, a plugin, a remote server reached over
SSH — implements the **same single gRPC service**, defined once in
`api/gridwell/v1/data.proto` and buf-generated. The client calls the local
server; the local server calls plugins; an SSH gateway proxies to a remote
server. Same wire protocol at every hop — **every byte crosses this interface**,
including live shell PTY and the Chromium session blob. The proto is the single
source of truth for *both* the wire types and the DB columns; a drift-lint test
fails the build if `Grid`/`Tile` and `schema.go` diverge.

**Every plugin is a separately-compiled Hashicorp go-plugin binary** — including
the localdb. The server spawns each binary named in `server.yaml` over go-plugin's
gRPC transport. There is no in-process plugin in production (the in-process path
survives only as a test harness; its loader comment claiming otherwise is stale —
`ARCHITECTURE.md §9`).

The surface is orthogonal — one method per concept, not one per kind:

- **Reads:** `GetGrid`, `GetTile`, `GetTileContent`, `GetTilePreview`.
- **`CreateTile`** — one create for every primitive; `tile.kind` selects the
  fields. A file/process/remote well is just a `well` tile with a cross-plugin
  `child_grid_id`.
- **`SetTile`** — one writeback for every framing/preview change. Pure framing
  (`view_*`, text scroll) never bumps `version`; a content change (frozen
  preview, url, title, text body) does. Face #3 of the primary rule, enforced in
  one place.
- **Placement mutations:** `MoveTile`, `CloneTile`, `ResizeTile`, `DeleteTile`.
- **Lifecycle:** `Info` reports the plugin's kind, label, and **default root grid
  id** — the whole handshake; there is no `Attach`/`Detach`. `Probe` confirms a
  single tile's presence (a failed read must never sweep a tile — only `GONE`
  does); `ListPlugins` enumerates a node's plugins.
- **Live bytes:** `OpenShell` streams a PTY both ways; `GetSession`/`PutSession`
  move the plugin's Chromium session blob.
- **Events:** `Subscribe` streams change events (localdb emits; fs/proc are
  polled via `GetGrid`).

**The plugin is the session boundary.** Each plugin owns exactly one Chromium
session (cookies + web storage), stored in its own DB and moved over the
interface. When you enter a plugin's space the host pulls that blob down with
`GetSession` and hydrates a dedicated Electron partition (`persist:plugin-<uuid>`);
every live `url` tile in that plugin renders in a `WebContentsView` bound to that
partition. On ascent the host flushes and writes the blob back with `PutSession`.
Copy the plugin's DB and you copy its logins.

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
