# State of the Project — Gridwell

*A holistic review of coherency, consistency, and completeness against the
project's one principle: **things stay as you left them.** Produced from a
full read of the codebase (~49.5k LOC: every file in `internal/`, the proto,
the client packages, the wasm orchestration, the Electron main process, the
three test gates, and all four guidance documents), July 2026.*

---

## Verdict

**The foundation is genuinely excellent, and the axiomatic design is real, not
aspirational.** The store, the proto contract, the server router, and the pure
client packages form a system where the guiding rule is mostly *enforced by
construction* — the version split, id stability, eager clone, blob
refcounting, and the frozen schema are the strongest parts of the codebase,
and they are tested to match. The stabilization effort visibly worked: the
menu seam is cured, the framing round trip has its net, the App god-object is
dissolved, and the docs are (now) an honest map.

The gap between "good" and "squeaky clean" is concentrated in exactly three
places, in descending order of concern:

1. **One real behavioral bug against the guiding rule** — the fs plugin
   destroys user placement and tile identity when a directory is *transiently*
   unreadable (findings F1).
2. **Two federation seams where the server cheats on its own interface** —
   capabilities dispatched on the kind string instead of the `Info` handshake,
   which quietly breaks the "remote is just a transport" promise (F2).
3. **The untestable remainder** — ~10.7k LOC of wasm orchestration with zero
   unit tests, within which embeds, touch, and file-drop are reachable by *no*
   gate at all (F5).

Nothing here requires rearchitecting. The architecture is right; the remaining
work is finishing what the charter already prescribes.

---

## 1. Does the system achieve its thesis?

Walking the four faces of the rule through the code:

**Face 1 — placement is persistent.** Enforced in the store (`MoveTile` /
`ResizeTile` are the only writers of x/y/w/h; overlap is checked; no
auto-layout exists anywhere in localdb). The fs/proc plugins persist positions
in their own DBs and only auto-lay *new* arrivals
(`TestNewFileAppearsKeepingExistingPlacement`). One violation: F1 below.

**Face 2 — identity is persistent and stable.** The strongest invariant in the
system. `AUTOINCREMENT` everywhere with written-down rationale (schema.go
comments explain the client-cache collision that motivates it), clone is a
genuinely eager deep copy (`cloneSubtree` — fresh row ids, blobs shared by
content address with refcounts, `object_id` carried only as provenance), and
the COW removal was completed rather than patched. Pane ids on the client are
monotonic too (`Tree.nextID`). I found no path that reassigns an id.

**Face 3 — preview = descent target = ascent return.** The `view_*` triple is
one value with three readings, the framing/content version split lives in
exactly one place (`emitTileChanged` vs `finishContentEdit`), and
`UpdateText` even refuses to bump the version on byte-identical content so a
debounced autosave is a true no-op. The client side is the convention-held
half (five framing roles synced in `input.go`), but the round trip is now
locked by `framing-roundtrip.spec.ts`. This face is in good shape.

**Face 4 — mutation is local and reflected.** `withMutation` collects events
inside the transaction and publishes on commit — the right shape. Two
qualifications: the subscriber channel silently drops events when full
(events.go `publish`; the client only self-heals a dropped `TileChanged` when
some later event triggers a refetch), and the optimistic-edit echo has no
version interlock (harmless single-tenant, but it is the one reflected-state
path with no test).

**The exceptions are principled.** The scratch grid (ephemeral URL visits that
persist as history), the menu-on-focused-pane rule, and shells being
host-owned state are all documented as deliberate exceptions rather than
leaks. That discipline — knowing *exactly* where the rule doesn't apply — is a
mark of a coherent design.

---

## 2. Layer-by-layer

### The proto contract — very good, one method per concept

`data.proto` is the best-documented proto I've reviewed: field comments carry
the invariants ("at once the preview frame, the descent target, and the ascent
return value"), and the drift lint against `schema.go` makes wire/storage
divergence a build failure. The surface is genuinely orthogonal — reads,
one create, one writeback, placement, lifecycle, live bytes, events — with
three wrinkles worth knowing about (see §4 below on interface evaluation).

### The store — the model layer, and it earns the title

`internal/store` is the part of this codebase to hold up as the standard.
Single connection with deterministic transactions, every mutation in
`withMutation`, sentinel errors classified once, `loadForEdit` folding
version-check + kind-guard + path-leaf validation into one call *so a new
mutation can't forget one of them* — this is "make the bug unrepresentable"
practiced consistently. The test suite matches: schema equivalence, migration
fixtures on file-backed DBs, refcount property tests, durability tests,
framing-version tests.

Two small notes: `internal/store` imports `client/markdown` for
`AltFromSource` (a pure function, but the dependency arrow points from the
persistence layer into the client tree — move it to a neutral package if it
grows), and `swapTileBlob`/`griddb`'s column-name interpolation is safe today
(trusted literals, whitelisted) but is the kind of pattern that needs its
guard comment, which it has.

### The server — a stateless router that mostly keeps its hands off

`connectHandler` holds no state and does exactly two jobs: strip/re-apply id
qualification and translate error classes. `qualifyTiles` deriving
`Tile.reference` is the exemplar the docs claim it is. The two places it
*doesn't* keep its hands off are findings F2.

### Plugins — one strong contract, unevenly honored

`localdb` is a thin, correct adapter over the store. `proxy` is a genuinely
transparent forwarder (all four stream shapes, full fidelity — the "remote
adds no vocabulary" claim holds at this layer). `griddb` is exactly the right
shared kernel for fs/proc. The unevenness: sweep policy (F1) and the absence
of any schema-versioning in the fs/proc DBs (F3).

### The pure client packages — a quiet strength

Twenty-two `js`-free packages, nearly all with test LOC ≥ source LOC, sharing
one strong convention (caller resolves impure facts into a flat input struct;
a pure function decides). `zoomtrans`, `pane`, `markdown`, and `embed` are
substantial, well-tested libraries. The duplication that exists is small and
geometric (F6) — annoying, not dangerous.

### The wasm orchestration — better than its reputation, still the risk pool

`main.go` after the god-object work is a readable wiring layer: `paneLocal`
with an atomic `forgetPane` lifecycle, the scheduler struct, the menu owner.
The extraction discipline is visibly working — most decisions already live in
tested packages (`DecideDrop`, `gesture.Classify`, `gridpath.ResolveLeafGrid`,
`BootViewport`…). What remains inline: the wheel-routing classification
(`input.go:147-172`), the ascent leaf-walk (`input.go:1195-1211` — the exact
mirror of what `urlwalk` extracts for descent), and the drop-target well
promotion (`drop_target.go:124-158`). Those three are the next extractions,
and F5 lists the files no gate reaches at all.

### The Electron native layer — the risk is contained but not covered

`webviews.ts` is careful code (capture-independent teardown, localStorage
flush before close, per-plugin partitions, bounds churn suppression), and its
pure math is extracted and tested in `viewutil.ts`. But the registry lifecycle
itself still has no unit test, and `session.ts`'s cookie extraction/injection
— the thing that decides whether your logins survive — is untested except by
using the app.

---

## 3. Findings, ranked

### F1 — fs plugin: a transiently unreadable directory destroys placement and identity  ❗ bug

`fs.GetGrid` treats *any* read error as an empty authoritative listing
(`fs.go:167-175`: `readErr != nil → entries = nil`), and `reconcileTiles`
then deletes **every tile row** for the grid — positions and ids. When the
directory comes back (EACCES hiccup, network mount remounted, USB drive
replugged), tiles reappear with **fresh ids at auto-layout positions**: your
arrangement is gone and saved deep links dangle. This is the exact scenario
the project's own norm prohibits ("a failed read must never sweep a tile —
only GONE does"), and `proc` implements that norm correctly
(`proc.go:426-442` keeps a tile on an uncertain read and consults
`procsource.Exists` before every delete). The vanished-dir case is tested and
deliberate (`TestGetGridUnreadablePathIsEmptyNotError`); the conflation of
"gone" with "can't tell" is the bug. **Fix shape:** distinguish
`os.IsNotExist` (authoritative empty — sweep) from other errors (keep rows,
return them as-is), mirroring proc. Recorded as invariant **I12** in
`ARCHITECTURE.md §11`.

### F2 — RESOLVED (413df43): capabilities come from the Info handshake

The kind-string dispatch below was fixed: `Subscribe` fan-in gates on
`Info.watch` (`watchPlugin`), and `buildPluginInfo` reads `Info.writable`.
The remote-transport gap that remained after F2 — no endpoint on a remote
node actually spoke the plugin interface — was closed by the node export
(`internal/server/nodeexport.go`) + `sshdial`. Original text for history:

Two places in `connect_handler.go` check `kind == "localdb"`:

- **`Subscribe` fan-in** (`connect_handler.go:404`) only opens event streams to
  localdb-kind plugins — while the proto already carries
  `InfoResponse.watch` for exactly this, and `proxy` faithfully forwards
  `Subscribe`. A remote node reached over ssh (kind "ssh") therefore gets
  **no live events**, ever.
- **`buildPluginInfo`** (`connect_handler.go:214`) derives `writable` from the
  kind string, so that same remote localdb is presented **read-only** in the
  launcher even though every mutation would route fine.

This is the charter's own disease in a new coat: a fact (the plugin's
capabilities) declared in one place (`Info`) and re-derived, wrongly, in
another. **Fix shape:** honor `Info.watch` for fan-in; add a `writable` bool
to `InfoResponse` (additive proto change) and read it in `buildPluginInfo`.
Do this *before* federation productionization, or every remote feature will be
built on the wrong dispatch.

### F3 — fs/proc plugin DBs have no format contract  ⚠️ durability

The localdb format is frozen, versioned (`application_id` + `user_version`),
migration-chained, and equivalence-tested — genuinely "data lives forever"
grade. The fs and proc DBs, which hold the **same class of forever-data**
(user placement, framing, the path→id identity map that deep links depend
on), have none of that: `createSchema` with `IF NOT EXISTS`, no version
stamp, no migration path, `SchemaVersion: 1` hard-coded in `Info`. The first
schema change to either will face the exact delete-the-DB temptation the
store's CLAUDE.md forbids. **Fix shape:** extract the localdb
`migrateUp`/fixture harness into a shared package and adopt it in `griddb`-
backed plugins while their schemas are still trivial.

### F4 — I11 is guarded by nothing  ⚠️ regression risk

The "reading never mutates" separation (SSE events touch only `cache`; framing
writes live only in input/urlsync) is real in today's code but verified by
inspection only. No test injects an event mid-transition; the optimistic-echo
reconcile has no version interlock and no test. Any future write into the SSE
path regresses this silently — and this is the "previews go wonky" vector.
The docs previously overstated this as tested; corrected in this review.

### F5 — the zero-gate surfaces  ⚠️ testing

Within `client/wasm`: `embed.go`/`embed_drop.go` (and "embed reverts to link
text" is a *named recurring bug* in the stabilization plan's lagging
indicators), `touch.go`, and `file_overlay.go` are exercised by no unit test,
no integration harness, and no e2e. In the Electron layer: `session.ts`
cookie round-trip and `sidecar.ts` lifecycle. These are where the next
"it just disappeared" report will come from. Also noted: the e2e suite's
driver/oracle/testhook triad is genuinely well-architected (independent
server-side ground truth; production hit-test geometry reused; no sleeps) —
extend it, don't work around it.

### F6 — small duplications in the client geometry  🧹 hygiene

- Cover/fit math exists three times under three names: `preview.coverWH` /
  `markdown.CoverScale` / `zoomtrans.Overtake`+`Fit` — one geometric idea.
- `Rect` is defined in `pane` and `palette`, with a third de-facto rectangle in
  `dragdrop.Pane`; `dragdrop.ChildPreviewFor` takes an anonymous struct both
  call sites must spell out verbatim.
- The "inner ⅓×⅓ center" predicate exists in `dragdrop.InTileCenter` and
  `pane.ClassifyRegion`; `pane.nearHalfPx` re-inlines `dragdrop.NearPx`.
- `palette.Default().CellPx == 64` duplicates wasm `cellPx = 64.0`.
- Three JSON vocabularies for the same saved-viewport shape (`pane.Pane`
  `file_*`, `pane.Frame` `tf`/`tm`, `panestate.Saved` `text_*`), and the
  file→text rename is half-done in wasm identifiers.

None of these is a live bug; each is a place where a future fix could land on
one copy. Unify opportunistically (charter §8), starting with a shared fit
primitive and a named `Well` input type.

### F7 — client-only state the reload discards  📝 decide deliberately

Per charter §7 ("nothing the user can change lives only on the client"), four
things currently do: the **split-pane layout** (the whole `pane.Tree` — only
the focused pane's path rides in the URL), the **frozen-URL pan offset**, the
**rendered-mode caret**, and selection. The pane layout is the one users will
actually notice losing. Either persist it (a small server-side session blob,
or encode the tree in the URL) or document it as a deliberate exception next
to the menu-focus one. Deciding is the point; today it's silent.

### F8 — smaller notes

- **Event drop policy:** `store.publish` drops on a full subscriber buffer;
  a dropped `TileChanged` leaves a stale pane until the next event. Fine
  single-tenant; worth a `GridChanged` fallback nudge if it ever bites.
- **`Subscribe` goroutines** swallow stream-open failures silently (a plugin
  that fails to subscribe just never delivers — no log, no retry).
- **`ListPlugins`** now timeout-bounds `Info` (good) but still has no cache;
  every palette open re-handshakes every plugin.
- **`shelldriver.TestWriteAfterCloseReturnsClosedPipe`** flaked once under
  full-suite load in this review's container (`Output() channel not closed
  after Close()` after a `signal: killed`), passing in isolation and on
  retry. Likely a teardown race under resource pressure — worth a look next
  time it appears in CI.
- **Version-string ids** (`strconv` at every boundary) are consistent and
  fine, but the `parseID`-returns-`ErrNotFound`-for-garbage convention in
  `GetGrid`/`GetTile` vs `ErrInvalidArgument` in mutations is a subtle
  asymmetry; harmless, just know it's there.

---

## 4. The plugin/server interface — is it orthogonal?

**Mostly, yes — and the discipline shows.** One method per concept holds up:
`GetGrid`/`GetTile`/`GetTileContent`/`GetTilePreview` are one read each;
`CreateTile` is one create with kind-dispatch at the edge; `SetTile` is one
writeback whose kind→operation mapping *fixes the version semantics* — the
single best design decision in the interface, because it makes face #3 of the
guiding rule a property of the wire contract rather than of client behavior.
`Info` as the entire handshake (no Attach/Detach) is a real simplification
that survived contact with the code. The proxy forwarding all four stream
shapes proves the "same service at every hop" claim.

Wrinkles, in case you want to sand them:

- **`SetTileAlt` overlaps `SetTile`.** A URL tile's title is stamped via
  `SetTile(tile.alt_text)`; a shell tile's foreground command via
  `SetTileAlt`. Two ways to write one column. `SetTileAlt` exists because the
  shell path has no version to claim — but that could be an unversioned
  `SetTile` variant, or shells could carry versions. Low stakes; two methods
  for one fact invites drift.
- **`UpdateText` vs `SetTile`.** Text content is the one content mutation with
  its own verb. Defensible (a bytes payload with different size expectations),
  but note the asymmetry: url/shell content (previews) go through `SetTile`,
  text content doesn't.
- **`Mount` is a convenience composite** (`Info` + `CreateTile` with a
  qualified child). It earns its place by keeping the label agreement rule
  server-side, but it *is* the only RPC that isn't a primitive.
- **`ShellSessionAlive`** is a narrow one-off; a future `Probe`-with-details
  could absorb it. Not worth touching until something else moves.
- **The real problem is not the surface but the dispatch** — F2. The interface
  *declares* capabilities correctly (`watch`, `has_session`); the server just
  doesn't read them. Fix that and the interface is honest end to end.

**Suitability:** for the stated purpose — a closed set of primitives, plugins
projecting external domains *within the constraints of the system* — this
interface is right. It resists feature-per-kind creep structurally (a new
kind must fit create/writeback/placement or it doesn't fit), which is exactly
the property an axiomatic system needs.

---

## 5. The data storage layer

**localdb: this is what "the data lives forever" looks like.** Frozen v1 DDL
kept byte-for-byte as a test fixture; additive-only migrations with
one-fixture-per-migration enforced by a well-formedness test;
`TestSchemaEquivalence` proving fresh-open ≡ v1+chain (which is what makes the
fresh-DB stamp shortcut sound); `application_id` marking the file format;
refusal to open newer-versioned DBs; WAL + `synchronous=NORMAL` pinned on
every open with the durability reasoning written down; file-backed test DBs so
the pragmas under test are actually active. Blobs are content-addressed,
immutable, refcounted, self-describing (`media_type` read back, never assumed)
— and the refcount machinery has a property test. The `serve` path refuses to
create a missing DB so a changed id can't silently spawn an empty store. I
looked for the classic mistakes (schema reconstruction by migration replay,
`:memory:` durability tests, refcount leaks on clone-then-delete) and found
each one specifically defended.

**The gaps are at the edges, not the core:** F3 (fs/proc DBs unversioned) is
the material one. The Chromium session blob is last-writer-wins by design
(fine — one host at a time) and localStorage still isn't in the blob (known,
parked). There is no backup/export story yet; for a system whose thesis is
permanence, a `gridwell backup` (VACUUM INTO per plugin DB + server.yaml) is
cheap insurance and worth a parking-lot slot.

---

## 6. Will the abstractions hold up?

Thought experiments against likely extensions:

**A new leaf kind (image, PDF, canvas sketch).** The path is documented (CHECK
constraint → table-rebuild migration) and the version-split helpers force the
framing/content decision explicitly. The cost is the four kind-switches
(store create, `SetTile` dispatch, client render, client gestures) — a closed
set, acceptably scattered for a personal system. The abstraction holds.

**A new projection plugin (calendar, email, MQTT topics, a camera).** `griddb`
+ `Info` + `GetGrid`-reconcile is exactly the right kit, and `Probe` (with
proc's policy, not fs's) handles flaky sources. Holds — F1's fix makes it
hold *well*.

**A writable non-localdb plugin** (say, notes-in-a-git-repo). Here the walls
appear: `writable` is a kind check (F2), `CreateTile` doc says "only localdb
accepts creates," and event fan-in is kind-gated. The *interface* supports
this future; the *server dispatch* doesn't yet. Fix F2 and this holds too.

**Deeper federation (multi-hop, SOCKS).** `proxy` composes transparently and
`localPathFor` already handles cross-plugin path boundaries. Blocked on F2
(events) and the parked session/network-context work. Right call to sequence
it after client stability.

**Many tiles / large grids.** `GetGrid` is one query with an index; previews
are blob-cached with id-keyed invalidation; the render loop dedupes fetches.
No pagination anywhere — fine for a personal grid, and the id-stability rules
mean pagination could be added without breaking references.

**The one abstraction under real tension** is the pane system. It is
deliberately "above" the grid world, and its state is client-only (F7). As
panes grow features (teleportation already; presumably persistent workspaces
next), the pressure to persist layout will grow — better to decide its
ownership story now than after three features encrust it.

---

## 7. Testing posture

The three-gate structure is sound and honestly documented (the Makefile
comments about what each gate *cannot* see are unusually candid). Store,
server, and plugins are densely tested at the right seams. The
driver/oracle/testhook e2e architecture — independent server ground truth,
production hit-test geometry, no sleeps — is the right foundation and the
recent specs (framing round trip, render seam, control focus, collapse,
menu focus) each closed a real gap.

Remaining, in priority order: the I11 injection test (needs a deterministic
mid-transition event hook), a preview-signature testhook for the full I7
claim, an fs sweep test that fails on F1, the three wasm extractions named in
§2, and coverage for `session.ts`. The `panestate` package (the one thin test
spot among the pure packages) deserves topping up when next touched.

---

## 8. What this review changed

- **`7796b22`** — finished the Phase 0 stale-comment sweep the plan had marked
  done but hadn't fully landed: `config.go` "Attach" ×2,
  `connect_handler.go` "COW spine", the `webviews.ts` shared-partition
  comment, and `cow_test.go` → `clone_test.go`.
- **`f1f1037`** — re-verified `ARCHITECTURE.md`/`CLAUDE.md` against the code:
  menu seam recorded as cured (fifth cure exemplar), framing-round-trip test
  acknowledged, I11 downgraded to "inspected, untested", new findings F1/F2
  recorded as seams #8/#9 and invariant I12, §9 drift list refreshed to the
  actual current drift (naming), wasm numbers refreshed.
- This report.

## 9. Recommended next moves (smallest set with the most effect)

1. **Fix F1** (fs sweep policy) with a failing test first — it is the only
   live violation of the guiding rule in the persistence tier, and the fix is
   small (distinguish not-exist from unreadable, mirror proc).
2. **Fix F2** (capabilities from `Info`, not kind) — additive proto field +
   two call sites; unblocks honest federation and a writable-plugin future.
3. **Adopt the migration harness in fs/proc** (F3) while their schemas are
   two tables each.
4. **Build the I11 injection hook and test** — the last "previews go wonky"
   vector with no net.
5. Then resume the queue the stabilization plan already has (preview
   signature hook, wasm extractions, session.ts coverage), and consider the
   pane-layout ownership decision (F7) before pane features grow further.

The project's own charter is the right medicine; this review found remarkably
little the charter doesn't already cover. The system feels like what it says
it is — the remaining work is making the edges as trustworthy as the core.
