# Gridwell

Gridwell is a personal space: tiles on an infinite grid, nested, federated
across plugins and machines. Four primitives (text, url, shell, well) and a
closed, stable experience over an open set of content. `ARCHITECTURE.md`
says how it works. Read it first. This file says how to change it.

## The rule: things stay as you left them

Nothing changes except by the user's explicit action. Step out and look back:
byte-identical. Step back in: the same. Reading never mutates.

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
   anything. If you cannot name it, keep digging. A retry, a `setTimeout`, a
   nil-check that hides the nil — smells, not fixes.
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
  disposable. Serve mints what is absent; that is the only config write.
- The forever file holds node facts only: minted ids, layout, framing, the
  user's bytes, connections, tombstones. What a source last said is cache.
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
  survives at two hops only: the plugin subprocess and the federation
  socket. Both node-side doors are codecs over the one router.
- Plugins are the third-party door. The host never imports a plugin
  implementation and never switches on a plugin kind; every plugin behavior
  rides a wire declaration. Only a leaf binary may enumerate what it ships.
  `test/boundary` enforces it.
- A plugin serves keys and content; the node owns ids and layout. A
  connection is config: an immutable name, a label, how to dial. Retiring a
  name is forever. Secrets stay host-local file paths.
- A node has no grid of its own. A mount lands on the remote's home.
  Plugins and connections live on the + menu's top row.
- The web door always has a password (the minted 0600 `web-password` file;
  delete it to rotate). The federation door is a 0600 unix socket, never
  TCP.
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
  origin. Every host with the web client has shells. The only thing that
  turns shells off is the node (`disable_shells`; a phone cannot host tmux).
- Session-ephemeral: the pane tree, the selection, the level stack, and the
  outer frames' viewports. The durable home for a layout is the pane tile.
- Cross-plugin: left-drag links, right-drag clones. There is no move.
  Dashed means link; deleting a link unlinks.
- Every pane wears the bar. Clicks act in the focused pane; a click in an
  unfocused pane moves focus, nothing else. A crumb click ascends;
  middle-click is the in-pane shortcut.
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
| `make check-federation` | the real binaries through a real ssh tunnel | plugin spawn, the export, id routing |

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
- [ ] No host or client import of a plugin, no switch on a plugin kind.
- [ ] This is one logical change, and the comments in the files I touched
      are true.
- [ ] Things stay as the user left them.
