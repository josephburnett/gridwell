# Gridwell

Gridwell is a single-tenant personal operating environment: tiles on a 2D
grid, nested, federated across plugins and machines. `README.md` says what
it is and how it behaves. `ARCHITECTURE.md` says how the machine works —
the layers, the wire contract, the seams, and where each invariant is
enforced. Read both before changing anything. This file is what's different
about working on *this* project: the rule that decides judgment calls, the
charter that keeps fixes fixed, the owner decisions you must not silently
reverse, and the verification gates.

## The guiding rule: things stay as you left them

**This is the deciding factor.** When a technical decision is unclear, the
option that preserves this wins — over performance, over elegance, over
convenience. Gridwell is a physical space: write-heavy, mutating freely,
but **nothing changes except by the user's explicit action.** Step out and
look back: byte-identical. Step back in: the same. Reading never mutates.

Run every judgment call through one test: after this change, is everything
the user didn't touch byte-for-byte the same, and does every reference
still resolve to the thing it named? That test is why ids are never
reassigned, why clone is an eager deep copy (COW forks re-row tiles — it
was tried and torn out), why framing writes never bump `version`, and why
there is no cross-plugin move — only link, or copy-then-delete where the
user can see the identity break.

## The engineering charter

The instability this project once had was not bad luck. It had one root
cause: **the same fact stored in several parallel copies with no single
owner, written from many code paths.** A fix corrects one copy; another
path keeps writing the rest; the symptom returns. The charter exists to
stop creating new copies.

1. **One fact, one owner. Derive once, read everywhere.** Before adding any
   field, map, or piece of state, ask: does this fact already live
   somewhere? If yes, read it from there. If a fact must exist in two
   layers, one place derives it and everything else reads it — never a
   second writer. `ARCHITECTURE.md §7` lists the worked exemplars
   (`Tile.reference`, the version-bump split, `client/menu`, the content
   entries, …); copy those shapes. In the fragile areas (framing, native
   bounds — §8's seam catalog), a change that leaves the copy-count the
   same has not fixed the class; it has added a patch that will regress.
   The goal of every change is the design where the bug **cannot be
   written**, not the design where it is merely fixed this once.

2. **Root-cause before you touch anything.** State the mechanism in one
   sentence ("the menu stays open because the right-drag end path at
   `right_button.go:276` is the only one that clears it, and this gesture
   ends through `input.go:419`"). If you cannot name the mechanism, you are
   guessing — keep digging. A reload, a retry, a `setTimeout`, a defensive
   nil-check that hides the nil: smells, not fixes. Evidence, not
   medium-confidence guesswork.

3. **Every bug fix is a test first.** Three parts, asked every time:
   a reproducing test that fails for the real reason before the fix; a
   commit message that answers **"why was this not caught?"** and closes
   that gap; and a fix for the *class*, not the instance. If the code isn't
   testable as written, make it testable — extract the logic into a pure
   package. "It can only be tested by running the app" is a structural
   defect, not an excuse.

4. **Test the seam, not just each side.** A unit test on each side of a
   contract will not catch a contract mismatch — and the mismatch is the
   bug. The #196 identity bug is the standing example: the store side was
   unit-tested, the binary never called it, and everything looked green.
   When you fix a seam bug, the test must cross the seam.

5. **`client/wasm` and the Electron main process are where bugs hide.**
   The wasm shim (~13.6k LOC) has zero unit tests and `make check` executes
   none of it. Do not add untested orchestration there: extract the
   decision into a js-free `client/*` package (as `pane`, `gesture`,
   `zoomtrans`, `wsbar` are) and unit-test it. Same rule for
   `apps/desktop/src/main` — pure-function modules with unit tests
   (`viewutil.ts`, `contextmenu.ts`) and/or e2e coverage. `webviews.ts` is
   the documented bug source and has no direct test; anything you touch
   there needs one.

6. **Errors must surface.** A failure that logs to console and returns
   presents to the user as "it just disappeared." Route failures through
   the error surface (`client/errsurface`, `sendError` in main); an
   optimistic mutation the server rejects must visibly reconcile.

7. **No client-only state.** Anything the user can change is a server
   fact, written through the store and reflected by an event. The decided
   exceptions — the session split-pane tree, the selection, the workspace
   stack, Chromium's own session storage — are ephemeral by owner decision,
   not by omission. Do not add to that list without a new owner decision.

8. **DRY is correctness, not tidiness.** If a fix in one place doesn't fix
   a visibly identical behavior elsewhere, you have found duplication;
   unify it rather than patching both.

9. **Commit incrementally; never batch; never silently deviate.** Commit
   each logical change as it lands. If an instruction seems wrong, say so
   and propose the alternative — do not quietly do something else. And keep
   comments true: fix a stale comment in the same commit that touches the
   file.

## Owner decisions (do not re-reverse without a new one)

Each of these was decided deliberately, some reversing an earlier decision.
Re-litigating them silently is how churn happens.

- **Every stacked level stays alive (#249, 2026-08-06).** Liveness
  follows PANE EXISTENCE: descending into a pane tile parks the outer
  level (views keep rendering off-screen, shells stay attached); a pane's
  resources close only when the pane closes, and leaving a view closes
  that view's panes. ONE live surface per content tile, at any level:
  opening a tile live elsewhere freezes the other pane's stream — the
  opener takes over. This REVERSED the workspace-boundary freeze.
- **Descent goes live (#202).** The frozen preview is what a tile looks
  like from outside; DESCENDING is the engagement gesture — a url reopens,
  a shell reconnects. One owner decides (`shellconn.DecideAutoLive`); call
  sites never hand-roll go-live.
- **Session-ephemeral by decision (#13).** The window-root pane tree, the
  selection, and the workspace stack live only in the session. The durable
  home for a layout is the **pane tile** (a workspace as a thing).
- **Cross-plugin: left-drag links, right-drag clones (2026-07-19).** A
  left-drag never duplicates content; a right-drag always does; there is no
  cross-plugin move. Dashed always means link; deleting a link unlinks.
- **Home is the first configured plugin; plugins live in the + menu's top
  row (2026-07-19).** The node grid (`<node_id>/0`) is the federation
  surface, not the landing page.
- **Pane closing is progressive crush (#217).** The split side follows the
  drag; closing is drag-through with red warning accumulation. The old
  corridor-edge one-click close stays dead.
- **The rendered view is a sanitized-HTML overlay (#218).** goldmark +
  go-org + bluemonday. Embeds, tile-links-in-docs, and rendered-mode
  editing were deleted with the custom canvas engine; a doc link is just a
  link.
- **The Chromium session is host-local (2026-07-26).** One partition
  (`persist:gridwell`) for every live url tile, local or mounted; live
  tiles browse from the host's own network. Nothing about sessions or
  networks crosses the wire.
- **Connections are data (#199).** The ssh plugin's remotes are connection
  wells, not config entries. Deleting one tombstones its namespace segment
  forever. Secrets stay host-local file paths.
- **The bar lives in the active pane; the crumb click is the ascent
  (#220/#222).** Bar contents are per-pane facts. The old right-click
  ascends (corner circle, empty bar, slot) are gone; middle-click remains
  the in-pane shortcut.
- **Id shape (2026-07-25).** New plugin/node ids are 7-char lowercase
  base36 with a leading letter (`store.NewShortID`); an id is immutable
  once minted, and both shapes (short + legacy 32-hex) are valid forever.
  The leading letter is load-bearing: it is how URL paths tell a namespace
  segment from a tile id.
- **The storage format is frozen and additive-only.** The contract is
  `internal/store/CLAUDE.md`. Never delete a DB to absorb a schema change.

## The verification gates

`make check` must be green on every commit — but it *cannot see the native
shell*, and the native shell is where the worst bugs live. It is necessary,
never sufficient, for anything touching live tiles, panes, focus, previews,
or the bar.

| Gate | Sees | Run it when |
|---|---|---|
| `make check` | pure Go + TS logic; *compiles* wasm but executes none of it | every commit, always |
| `make check-electron` | the real `WebContentsView` / PTY bridge under xvfb | the live URL/shell path, the bridge, `webviews.ts`, shell IPC |
| `make check-e2e` | the full app (Electron + Go sidecar) as a black box | any `apps/desktop` change, the native layer, any cross-seam behavior; pre-merge |
| `make check-web` | the browser-mode client (no Electron): caps gating, touch | `client/caps`, `client/touchgest`, the serve/boot path |
| `make check-federation` | the real binaries through a real ssh tunnel | plugin spawn, `sshdial`, the node export, id routing |

If a change lives in or affects the native layer, `make check` passing
means nothing — run the electron and/or e2e gate AND add or extend a spec
that crosses the behavior. Gate discipline: the gates rebuild at start, so
never edit sources while one runs; a "vindicated isolated" flake rerun only
counts against a freshly built tree; a spec on the known-flake ledger still
gets a fresh look before you blame a change.

## Making changes: the checklist

Before you commit, every one of these is true:

- [ ] I named the root-cause mechanism (fix) or the one owner of any new
      fact (feature) in one sentence. I did not guess.
- [ ] I did not add a new copy of an existing fact; if anything, I reduced
      the copies.
- [ ] A test fails before my change and passes after, and it crosses the
      seam where the behavior actually lives.
- [ ] For a bug fix, the commit message answers "why was this not caught?"
      and I closed that gap.
- [ ] `make check` is green. If I touched the native/live layer, the
      electron/e2e gates are green and a spec covers the behavior.
- [ ] No error is swallowed to the console; failures surface and reconcile.
- [ ] Nothing the user can change lives only on the client.
- [ ] I committed this logical change on its own and fixed any stale
      comment in the files I touched.
- [ ] The guiding rule still holds: everything the user didn't touch is
      byte-for-byte the same, and every reference resolves to what it
      named.
