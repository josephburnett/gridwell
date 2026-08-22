# Content and presentation: the split the API implies but does not provide

Status: options report (commissioned 2026-08-22). No decision taken; no
code changes ride with this document.

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
| **Presentation** (framing-class; never bumps `version`) | `PlaceTile` (x, y, w, h) · `SetTile` well arm (`view_*`) · `SetTile` text arm (`text_*`, scroll/window/mode) · `SetTile` `content_zoom` / `url_frozen` arms · `SetRootView` |
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
| **local** | `plugins/local/store` — the reference implementation | The full split (`emitTileChanged` / `finishContentEdit`), the model everything else imitates |
| **fs** | own SQLite schema + migrations + reconcile; placement/framing/root-view via the shared `plugins/griddb` helpers | The DB is ~half presentation plumbing: `x/y/w/h`, `view_*`, `root_cx/cy/zoom`, auto-layout cursor — around the path→id identity map, which is the only part that is genuinely fs's |
| **proc** | same shape as fs, second copy of the schema/migrations/reconcile | griddb deduplicates the *mutations*, not the schema, the migrations, or the reconcile |
| **remote (ssh)** | bespoke — `ssh_connections` carries `x/y/w/h`, `view_*`, `alt_user`, `version` inline (`plugins/remote/db.go`) | A third, hand-rolled copy of placement + framing + rename-latch + version semantics; ~200 of its 296 lines are presentation, not connections |
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
  swallowed `SetRootView`; panning their roots was lost on every
  re-entry. One gap, two migrations, two plugins — because the fact had
  two homes.
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
provider's listings, terminates `PlaceTile`/`SetTile`-framing/
`SetRootView`, and forwards everything else.

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

Formalize `ContentProvider` and `Gridwell` as separate services; nodes
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
