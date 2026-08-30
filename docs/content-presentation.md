# Content and presentation: the split the API implies but does not provide

Status: options report (commissioned 2026-08-22), plus §9 — the end
state the walkthrough with Joe converged on (2026-08-22), the seed of a
plan. §9 supersedes §8's reading; no code changes ride with this
document. §9 was then executed by `docs/v2-design.md` (2026-08-24) and
finished by `docs/one-node.md` (2026-08-29); this is the record of the
question, not of the answer.

The question, as posed: plugins provide tile content AND store location,
size, and view information. The whole surface — server (node) and
plugins — is the same gRPC service, and that is worth keeping. Should
Gridwell store layout information *for* plugins — separate content from
presentation in the API — so a plugin can choose to provide only content
(e.g. a view into email) without reimplementing grid layout semantics?

## 1. What the surface already separates

The 17-RPC contract partitions almost perfectly along exactly this line.
Nobody planned it as a content/presentation split, but that is what the
framing-vs-content version rule produced:

| Half | Verbs and fields |
|---|---|
| **Presentation** (framing-class; never bumps `version`) | `PlaceTile` (x, y, w, h) · `SetFraming` (`view_cx/cy/zoom` on a doorway tile, `root_cx/cy/zoom` on a root grid — one verb, one shape) · `SetTile` text arm (`text_*`, scroll/window/mode) · `SetTile` `content_zoom` / `url_frozen` arms |
| **Content** (bumps `version`, or creates/destroys) | `ReadContent` / `WriteContent` · `ServeContent` · `CreateTile` · `CloneTile` · `DeleteTile` · `SetTile` url/shell freeze arms, `rename`, `adopt_child_grid` |
| **Existence & identity** | `Info` · `Probe` · `GetGrid` / `GetTile` / `GetTilePreview` · `Search` · `Subscribe` · `OpenShell` / `ShellSessionAlive` |

The wire-only derived fields (`writable`, `reference`, `serves_page`,
`text_presentation`, `stale`, `status_detail`) are the established
"plugin declares, node stamps" channel — the same channel any new
capability would ride.

So the *verbs* are already split. What is not split is **storage**: every
grid provider must implement both halves, and the presentation half is
the same semantics every time.

## 2. The inventory: one semantics, four implementations

Five things serve grids today. The presentation machinery exists in four
independent copies:

| Provider | Presentation storage | Notes |
|---|---|---|
| **local** | `internal/local/store` (then `plugins/local/store`) — the reference implementation | The full split (`emitTileChanged` / `finishContentEdit`), the model everything else imitates |
| **fs** | own SQLite schema + migrations + reconcile; placement/framing/root-view via the shared griddb helpers (RETIRED 2026-08-24 with the legacy plugins — the v2 layout engine is `internal/layout`) | The DB is ~half presentation plumbing: `x/y/w/h`, `view_*`, `root_cx/cy/zoom`, auto-layout cursor — around the path→id identity map, which is the only part that is genuinely fs's |
| **proc** | same shape as fs, second copy of the schema/migrations/reconcile | griddb deduplicates the *mutations*, not the schema, the migrations, or the reconcile |
| **remote (ssh)** | bespoke — `ssh_connections` carries `x/y/w/h`, `view_*`, `alt_user`, `version` inline (`plugins/remote/db.go`, now `internal/remote/db.go`; connections became yaml config in #269) | A third, hand-rolled copy of placement + framing + rename-latch + version semantics; ~200 of its 296 lines are presentation, not connections |
| **node grid** | none — `PlaceTile` is refused; tiles sit in a synthesized centered row; the root viewport lives in a JSON state file | A fourth root-view implementation; and the launcher — the federation surface itself — **cannot be rearranged at all** |

What a stranger writing a content-shaped plugin (the email view) must
build today, before a single byte of email renders: stable *numeric* id
minting (tile ids must be purely numeric — the URL grammar distinguishes
them from namespace segments by shape, so string keys cannot be ids), a
placement store, well framing, root-view persistence, auto-layout for
never-placed entries, a sweep policy that never drops user placement on
a failed read (I12), `dbformat` migrations under the forever-data
contract, and the framing-vs-content version rule. All of it identical
to fs's, none of it about email.

## 3. The evidence this is a live bug class

Each of these is the same disease — presentation semantics re-decided
per plugin — surfacing as a user-visible defect:

- **The framing audit (2026-08-13).** fs and proc both silently
  swallowed the root framing write; panning their roots was lost on
  every re-entry. One gap, two migrations, two plugins — because the
  fact had two homes. (Since 2026-08-29 there is one verb and one
  shape: `SetFraming`, doorway tile and root grid alike.)
- **#266 (2026-08-21).** fs/proc tiles could not be moved or resized:
  `Grid.writable` — a *content* capability — was gating *presentation*.
  The client now distinguishes (same-grid, non-clone placement is
  allowed on read-only grids unless the grid is a node grid), but that
  vocabulary lives only in client inference; the wire still has one bit
  where there are two facts.
- **Text framing in read-only files.** fs has no `text_*` columns, so
  the scroll position of a file you read is session-only — and the
  client carries a special case (#236) purely to avoid manufacturing an
  error strip against fs's refusal. A plugin gap, patched client-side.
- **The launcher.** The node grid refuses placement outright. The first
  grid every user sees is the one grid that cannot stay as you left it.
- **#265.** The auto-layout fix (footprint occupancy, the placement
  cursor) landed in griddb — so fs and proc got it, and remote and the
  node grid did not, because they are not on the shared implementation.

## 4. What could move, and what cannot

**Cannot move** (entangled with the content itself): identity — the
stable key→id map; existence — Probe/sweep verdicts, and whether a
listing is authoritative (fs) or non-authoritative (proc); structure —
which tile opens into which child grid; content bytes, previews,
`ServeContent`; content-derived labels and declarations (`serves_page`,
`text_presentation`).

**Could move** (pure presentation, semantics identical everywhere):
`x/y/w/h`; well `view_*`; the root viewport; `content_zoom`;
`text_x/y/w/h` + `text_mode`; arguably the user-rename latch
(`alt_user` — the arbitration between a content-derived label and a
user override is the same everywhere it exists).

One consequence worth naming: if the presentation store also owns the
key→id map (minting numeric ids for plugin-provided stable keys), a
content plugin can be **stateless** — it answers in its own keys
(message-ids, paths, PIDs) and never touches a database. That is the
maximal version of the email-view wish.

## 5. The federation question

Today presentation lives *with the data*: the serving node's plugin
stores it, so every client — local, browser, a mount three hops away —
sees the same arrangement, and the offline cache remembers it like any
other wire fact. Any relocation must keep the store at the **serving
node**. Moving it to the viewing side would fork arrangements per
device and break "things stay as you left them" the moment you switch
machines. So "store layout for plugins" always means: at the node where
the plugin is configured — never in the client, never at a transit hop.

## 6. The options

### A. A better library — griddb grows up

Keep the architecture; extract the rest of the duplication into the api
module: the presentation schema + migrations as a kit, the reconcile
scaffold (keys in → upsert/sweep with I12 baked in), root view,
auto-layout, the version rule. Port remote's bespoke copy onto it.

- Cheap, incremental, no wire change, no migration.
- But Go-only — a Python plugin author gets nothing; the class survives
  (every plugin still *wires* it, and can wire it wrong); the node grid
  stays special.

### B. The presentation adapter — a wrapper owns layout

Define the **content-provider subset**: a plugin that serves listings
(stable keys, kinds, labels, child references, declarations) and
content, and simply leaves the presentation verbs unimplemented —
unimplemented already has clean semantics on this surface (Search =
no results, ServeContent = 404). In front of it sits an adapter that
implements the FULL Gridwell service: it owns a SQLite DB with the
key→id map and every presentation fact, merges placement onto the
provider's listings, terminates `PlaceTile`/`SetFraming`/`SetTile`
framing, and forwards everything else.

Ship it twice from one implementation, the compose pattern:
`compose.WithLayout(inner)` for Go authors, and a stock
`gridwell-plugin-adapter` binary that fronts any content-provider
process — so a stranger in any language writes only content.

- The host, the wire contract, and the charter arrows are untouched;
  the server stays a stateless router. One-fact-one-owner is *cleaner*
  than today: the adapter is the single owner of presentation, the
  provider of content.
- The provider must still declare what only it knows: whether a listing
  is authoritative (the sweep rule), and its stable keys.
- Existing plugins migrate opportunistically or never — their DBs are
  forever-data either way. The natural first tenants: the **remote
  instance grid** (retiring the third copy) and the **node grid** (the
  launcher finally rearranges, and proves the adapter on an in-process
  provider).

### C. Hosted layout — a node capability

`Info` declares `layout: hosted`; the serving node keeps a per-plugin
presentation sidecar (the mountcache precedent — one DB per plugin,
except this one is forever-data, not disposable), merges it into every
`GetGrid`/`GetTile`, and terminates the presentation verbs itself.

- Strongest form of "the plugin cannot get it wrong", free for any
  language, and the node grid comes along naturally.
- But it reverses "the server holds no Gridwell state" — the node
  becomes a second durable store with its own backup/migration story.
  And every read becomes a join of two owners' facts at the seam —
  exactly the shape §8 of the architecture spends itself fighting. B
  buys the same outcome with the join owned by one small component that
  can be seam-tested in isolation, instead of by the router everything
  passes through.

### D. Two services in the proto

Formalize `Plugin` and `Gridwell` as separate services; nodes
adapt providers via a built-in layout engine. This is C's end state
with the split made wire-visible. Most invasive; nothing today forces
it; only worth revisiting if B's subset hardens into a shape worth
naming in the proto.

## 7. Decisions any option forces

- **Split `writable`.** #266 proved there are two facts under one bit:
  *content-writable* and *rearrangeable*. Whatever else happens, the
  wire should say both (a `Grid` declaration, stamped like the rest),
  and the client's inference should become a read.
- **Who mints ids.** Today: always the plugin. Under B/C the layout
  owner can mint against provider keys, making providers stateless —
  but then key stability becomes contract (a provider that changes its
  key scheme orphans every reference, same as re-minting ids today).
- **Layout for the gone.** Placement must survive transient absence
  (I12) — layout rows are keyed by key and kept; a definitive GONE may
  sweep them only where the provider's listing is authoritative.
- **Migration.** fs/proc/remote DBs are under the frozen-format
  promise. The cheap path: the adapter is for NEW providers (plus the
  node grid and the instance grid, whose moves are small); fs/proc stay
  on their own DBs until a real reason to import them appears.

## 8. A reading

If the goal is the stated one — a stranger ships a view into email
without writing layout code — **B** is the shape that gets there
without giving anything up: the server stays stateless, the charter
arrows stay intact, the door gets better for strangers in every
language, and the fourth and third copies of the semantics (node grid,
remote instance grid) become the proof it works. **A is B's first
commit** — griddb is the seed of the adapter's store — so starting A
does not foreclose anything. C spends architectural principle (stateless
router, join-free reads) to buy what B buys anyway; D is a formality
that can wait until the subset has earned a name.

## 9. The end state (walkthrough with Joe, 2026-08-22 — supersedes §8)

**Status 2026-08-27: reached.** Two services on the wire; local and
remote are node code; every other kind is a provider; the `gridwell.v1`
subprocess door and the `provider:` flag are deleted.

Working C through the remote plugin — the hardest case — dissolved the
B/C distinction and landed here. This is the seed of a plan, not yet a
program of work.

**Two services on the wire (D, arrived at honestly):**

- **Gridwell** — the node surface, unchanged for clients and
  federation: every node serves the full contract to browsers, the
  desktop, and other nodes.
- **Plugin** — what plugins serve: list a context's entries
  (stable keys, kinds, labels, child-context keys, declarations,
  optional placement *hints* used only at first sight), read/write/
  serve content by key, probe, watch, shells. No ids, no layout, no
  database. Only nodes talk to providers; providers are invisible on
  the federation wire.

**The node absorbs what was never really a plugin:**

- **local folds into the node** — its store becomes the node's native
  content DB (layout and content in one file, one writer).
- **remote folds into the node** — transports are the system's own
  topology, not a door for strangers; tunneling is builtin machinery.
  Remote nodes still *present* as plugins in the menu/launcher — that
  is rendering, already declaration-driven.
- The node hosts layout for every provider and mints numeric ids
  against provider keys. The launcher becomes an ordinary
  rearrangeable grid. The user's arrangement always wins over a
  provider's placement hints (the guiding rule).

**Reversed owner decisions (Joe, 2026-08-22, explicit):**

- **#199 "connections are data" → connections are server.yaml config.**
  Consequences accepted: no creating connections from the client's
  picker, and a yaml connection name is an immutable id by rule —
  renaming one dangles every reference through it (loud comment in the
  yaml template).
- **"The server holds no Gridwell state" → the node owns the durable
  store.** Process isolation for dial code is judged to have never
  mattered.

**The databases (all of them):**

| File | Contents | Nature |
|---|---|---|
| **Node DB** (one) | native user content (text, urls, wells, panes, blobs, shells), its layout and versions, the node-grid arrangement | source of truth; self-describing in isolation |
| **External memory DB** (one per provider AND per connection) | everything the node remembers about that external: key→id map, user layout (providers; remotes host their own), read-through cache | durable-but-forgettable: lose it and links dangle (interpretable via the uuid in qualified ids — reuniting with the file reconnects them), arrangement resets, cache re-warms |
| server.yaml | plugins, connections | config |

**Caching is uniform, not optional:** the node runs the same
read-through cache for every external thing. This subsumes I12 — a dark
directory, a dead process table, and an unreachable remote all serve
the remembered answer, stamped stale, by the one mechanism.

**What dissolves:** the local and remote plugin processes, griddb, the
per-plugin content DBs, all four layout implementations, the fs/proc
reconcile-and-sweep machinery, and the `writable`-conflation class
(#266) — rearrangeability is the node's fact everywhere.

**Migration honesty:** every current DB is forever-data. The fs/proc
path→id and pid→id maps, the local store, and the ssh connection rows
(minted NS segments live inside stored references) all have to arrive
in the new homes without minting anything new. That migration is the
riskiest part of the whole plan and gets designed first, not last.
