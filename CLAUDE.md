# Gridwell

Gridwell is a personal space: tiles on an infinite grid, nested, federated
across plugins and machines. Four primitives (text, url, shell, well) and a
closed, stable experience over an open set of content. `ARCHITECTURE.md`
says how it works. Read it first. This file says how to change it.

## The rule: things stay as you left them

Nothing changes except by the user's explicit action. Step out and look back
and it is byte-identical; step back in and it is the same. Reading never
mutates.

When a decision is unclear, this rule wins — over performance, elegance, and
convenience. Ask of every change: is everything the user didn't touch
byte-for-byte the same, and does every reference still resolve to the thing
it named? That is why ids are never reassigned, clone is a deep copy, framing
never bumps `version`, and there is no cross-plugin move.

## How to work

1. **One fact, one owner.** Before adding a field, map, or piece of state,
   ask where the fact already lives and read it from there. If two layers
   need it, one derives and the rest read. `ARCHITECTURE.md` lists the
   shapes to copy. A change that leaves the copy-count the same has not
   fixed the class; aim for the design where the bug cannot be written.
2. **Root-cause first.** State the mechanism in one sentence before touching
   anything. If you cannot name it, keep digging. A retry, a `setTimeout`, or
   a nil-check that hides the nil is a smell, not a fix.
3. **Every bug fix is a test first.** A reproducing test that fails for the
   real reason; a fix for the class, not the instance; a commit message that
   answers "why was this not caught?" and closes the gap. If the code isn't
   testable, make it testable.
4. **Test the seam.** A unit test on each side of a contract does not catch
   a contract mismatch. A seam bug's test crosses the seam.
5. **The shim and the Electron main process are where bugs hide.**
   `client/wasm` has no unit tests and `make check` executes none of it.
   Put decisions in a js-free `client/*` package with tests; the shim is
   glue. Same for `apps/desktop/src/main`: pure modules with unit tests, and
   e2e coverage. `webviews.ts` has no direct test; anything you touch there
   gets one.
6. **Errors surface.** A failure that logs and returns looks to the user
   like "it just disappeared." Route through `client/errsurface` or
   `sendError`. A rejected optimistic mutation reconciles visibly.
7. **No client-only state.** Anything the user can change is a server fact,
   written through the store and reflected by an event. The exceptions are
   decided, not accidental: the session pane tree, the selection, the outer
   frames of a pane's place, the pane-tile level stack, Chromium's own
   session storage. Do not add to that list without a decision.
8. **DRY is correctness.** If a fix in one place doesn't fix an identical
   behavior elsewhere, unify them.
9. **Commit each logical change on its own.** Never batch. If an
   instruction seems wrong, say so and propose the alternative — never
   quietly do something else. Fix a stale comment in the same commit that
   touches the file.

## Decisions

These were decided deliberately. Do not reverse one without a new decision.

**Data**

- The node is its home. One id, one `server.yaml` (`id`, `web`,
  `federation`, `connections`, `plugins`), one `gridwell.db`. `cache.db` is
  disposable. Serve mints what is absent, and a pre-one-node home converts
  itself at the first load — the config shape and the `db/<id>/` layout
  both, originals set aside, never deleted. Those are the only config
  writes.
- `gridwell.db` holds node facts only: minted ids, layout, framing, the
  user's bytes, connections, tombstones. What a connection last answered is
  cache.
- The storage format is additive by default. Dead storage — no released
  binary reads it for a user-visible meaning, shown by grep — may be
  retired by a rebuild migration that preserves every surviving row. A row
  the new shape cannot hold is converted, never deleted. Never delete a DB
  to absorb a schema change. Retired wire fields are `reserved`. The
  contract is `internal/local/store/CLAUDE.md`.
- `version` means the user's content bytes: a text body, a typed url, a
  typed name. Captures, framing, and layout carry no claim and cause no
  bump.
- Framing is a float center plus a pane-size-independent zoom on the row
  that owns the doorway; a root keeps the same shape on its grid row. One
  wire verb (`SetFraming`), one store writer, one client function.
- Ids are 7-char lowercase base36 with a leading letter. Both shapes (short
  and legacy 32-hex) are valid forever. The leading letter is how a URL
  tells a namespace segment from a tile id.
- Clone is an eager deep copy. COW was tried and torn out.

**Node**

- Inside the node a namespace is a Go value (`internal/namespace`). gRPC
  survives at two hops only: the plugin subprocess and the connection
  door. Both node-side doors are codecs over the one router.
- Plugins are the third-party door, and they live in their own repository,
  `github.com/josephburnett/gridwell-plugins` — the shipped fs, proc, gitlab
  and pages on the same footing as anyone else's. This repo owns the door: the
  proto, `api/gen/plugin/v1`, and the go-plugin handshake (`api/compose`).
  No gridwell package, TEST FILES INCLUDED, imports a plugin implementation
  or names that repository in a go.mod, and nothing switches on a plugin
  kind; every plugin behavior rides a wire declaration. `test/boundary`
  enforces it. A test that needs a real plugin spawns the shipped binary
  (`internal/plugintest`). `make build` compiles those binaries out of
  `$(PLUGINS_DIR)` (default `../gridwell-plugins`) into the repo root, so
  clone the plugins repo beside this one.
- A plugin serves keys and content; the node owns ids and layout. A plugin
  holds no node fact, and keeps its own memory of its source in the private
  directory the node hands it at spawn (`<home>/plugins/<id>`, as
  `state_dir`), under `cache.db`'s contract: disposable, safe to delete,
  rewarmed by use. Nothing deletes one automatically. A connection is
  config: an immutable name, a label, how to dial. Retiring a name is
  forever, and EXPLICIT: `retired_names:` is the one owner, and the stored
  `deleted` flag is only that list mirrored. Boot never retires a name
  because the config stopped declaring it — the row and its landing stay,
  its links go dead by the roster, and the stanza brings them back.
  Secrets stay host-local file paths.
- A plugin's collections are + menu entries, one per collection
  (`InfoResponse.menu_entries`, each naming a context key). A plugin never
  wraps them in a synthetic root grid of wells. `root_context` is the
  primary collection and gets no menu entry of its own.
- A node has no grid of its own. A mount lands on the far node's home.
  Plugins and connections live on the + menu's top row. That row is an open
  set, so it is folded: a menu opens on the primitives with a chevron strip
  for the section above it, and every opening starts folded — the fold is the
  menu's live state and dies with it (`client/palette` decides what a state
  shows, `client/menu` owns the flag).
- The web door always has a password (the minted 0600 `web-password` file;
  delete it to rotate). The connection door is a 0600 unix socket, never
  TCP. Its `server.yaml` key stays `federation:` and its file stays
  `federation.sock`: an existing home already has them written down.
- Plugins serve web content through `/content/<token>/<tile-id>/<subpath>`.
  Every response is sandboxed and gated by the content token, which is never
  interchangeable with the cookie.

**Experience**

- A pane's place is one stack of frames. Descending through any doorway
  pushes; ascending pops. The URL and the layout blob encode the stack; the
  crumbs project it. The pane-tile level is the one second axis.
- Every stacked level stays alive. Descending into a pane tile parks the
  outer level; resources close only when the pane closes. One live surface
  per content tile — opening it elsewhere takes over.
- Descent goes live. The frozen preview is what a tile looks like from
  outside; entering a url reopens it, entering a shell reconnects. One
  owner decides (`shellconn.DecideAutoLive`).
- Shells ride the web door: a WebSocket at `/shell`, same cookie, same
  origin. Every host with the web client has shells — a phone reaches them
  through the browser, like everything else. The only thing that turns
  shells off is the node (`disable_shells`).
- Session-ephemeral: the pane tree, the selection, the level stack, and the
  outer frames' viewports. The durable home for a layout is the pane tile.
- Left-drag moves, right-drag clones, ctrl + right-drag links — the
  modifier flips the right button from copy to link, in the namespace the
  drop lands in or across one. Cross-plugin a left-drag links too, because
  there is no cross-plugin move. Dashed means link; deleting a link
  unlinks. Ctrl + left-click descends in a new pane split below — the
  same split a link out of a live tile opens; in an unfocused pane it
  still only moves focus.
- A link into a namespace the node does not declare is DEAD: greyed, inert,
  still labelled, still deletable. It is a state, not an error — nothing is
  asked for it and nothing is said about it. Dead is not dark: a declared
  plugin that is down and a declared connection that will not answer are
  health and staleness, and they come back. Nor is dead always forever: a
  retired connection name never returns, but a namespace merely undeclared
  is live again the moment it is declared again. `client/deadref`.
- One bar, at the bottom of the window, always visible, riding the focused
  pane: it spans that pane and slides under it as focus moves, inside a
  full-width row reserved once, so no pane ever resizes. Panes end at that
  row's top edge; the background beside the bar is nobody's and swallows
  clicks. Clicks act in the focused pane; a click in an unfocused pane moves
  focus, nothing else. Every button obeys that, the right one included: a
  live url view's native context menu names the pane it acts in and focus
  follows before any item can run. A crumb click ascends; middle-click is
  the in-pane shortcut.
- A left-drag on a pane boundary resizes it. A press grabs at most one
  divider per axis, so at the corner of three panes it grabs both and the
  one drag moves both: two ordinary resizes sharing one gesture, rather than
  a gesture of its own. `pane.GrabDividers`.
- Pane closing is progressive crush: drag-through with red warning.
- The rendered view is a sanitized HTML overlay. Task-list checkboxes are
  the one interactive control; everything else is read-only.
- The Chromium session is host-local: one partition for every live url
  tile. Nothing about sessions or networks crosses the wire.
- No parameterized plugins. Modals center on the active pane.

## Gates

`make check` must be green on every commit. It cannot see the native layer,
and that is where the worst bugs live.

| Gate | Sees | Run it when |
|---|---|---|
| `make check` | Go + TS logic; compiles wasm, executes none of it | every commit |
| `make check-electron` | the `WebContentsView` bridge under xvfb | the live url path, `webviews.ts`, the preload |
| `make check-e2e` | the full app as a black box | any `apps/desktop` change, the native layer, cross-seam behavior |
| `make check-web` | the browser-mode client: caps, touch, shells | `client/caps`, `client/touchgest`, the shell door |
| `make check-connections` | the real binaries through a real ssh tunnel | plugin spawn, the export, id routing |

If a change touches the native layer, `make check` passing means nothing.
Run the electron or e2e gate and add a spec. The gates rebuild at start, so
never edit sources while one runs. A flake rerun only counts against a
freshly built tree. A spec on `docs/flake-ledger.md` still gets a fresh look
before you blame a change.

## Before you commit

- [ ] I named the mechanism (fix) or the one owner of any new fact
      (feature) in one sentence.
- [ ] I added no copy of an existing fact.
- [ ] A test fails before and passes after, across the seam.
- [ ] For a bug fix, the commit message says why it was not caught.
- [ ] `make check` is green; the native gates too if I touched that layer.
- [ ] No error is swallowed. Unacknowledged writes park in `client/outbox`
      and nothing user-made is dropped without a server verdict.
- [ ] Nothing the user can change lives only on the client.
- [ ] No import of a plugin implementation anywhere, tests included, and no
      switch on a plugin kind.
- [ ] This is one logical change, and the comments in the files I touched
      are true.
- [ ] Things stay as the user left them.
