# Freshness

How an answer gets old, who notices, and what the user sees. Seven layers
own a rule each; no layer knows the next one's. This traces a fact through
all of them, so "why is this stale" can be looked up instead of dug out of
the code.

The one rule underneath: a memory may be served, but it is always labelled,
and nothing the user made is dropped without a server verdict.

## The layers

Reads travel client → router → source cache → transport or plugin → source.
Freshness is decided on the way back.

**1. The source.** A plugin's directory, API, or process table; a far node's
store. It has no freshness rule of its own — it is the truth everything else
is a memory of. What it owes the node is an honest failure: a transport-shaped
error means "not right now", any coded error is a verdict. `api/gwerr`'s
`IsTransport` is the one classifier that separates them, and every layer
below branches on it.

**2. The plugin overlay** — `internal/pluginhost/adapter.go`. A grid is a
JOIN: the plugin's `List` supplies the entries, `store.Namespace.Overlay`
lays the minted rows over them. `Adapter.synthesize` degrades by whose fact
is missing. A listing that fails transport-shaped is replaced by an empty,
NON-AUTHORITATIVE one: every row the node minted still reads, with the same
ids, placement, and labels, and nothing retires. An entry with no row has
nothing to answer from and is simply absent. The adapter publishes that
outage as this namespace's health (`Adapter.noteSource`) — the transition
only, on the same event and uuid the supervisor uses for the subprocess,
because "a declared source is not answering" is one fact whichever half of
the plugin it is — so the client marks the rooms it serves as memories with
no call of its own having to fail. A listing the plugin answered from its
own memory carries `ListResponse.unreachable`, the reason: its entries serve
and refresh rows as a live listing's do, the reason is published as the
source's health by the same `noteSource`, and it retires nothing, since a
memory is not a verdict even when it claims `authoritative`. Retirement needs a verdict — an
authoritative listing sweeps by `mem.Sweep`, a live non-authoritative one
sweeps only rows whose `Probe` answers a definitive `PRESENCE_GONE`. A dark *plugin* — the subprocess
itself gone — fails the read outright at `cp.Info`, because the declared
face is the plugin's own fact and nothing can supply it. A source that
changes on its own says so on `Watch` (`internal/pluginhost/watch.go`): the
node holds one stream per subprocess while some client shows one of its
grids, scoped to the contexts shown and those their link entries point into,
and re-opened with the new set when that changes. A `ContextChanged` is
checked, not relayed: the node lists the context and publishes the
`GridChanged` a write would, on it and on every grid linking into it, each
once per batch, only when the answer differs from what `GetGrid` served or
the node last announced (`Adapter.settle`), so a grid open on
screen refetches what moved and a directory whose files are written but whose
names stay announces nothing. An `EntryChanged` is the entry's own
`TileChanged`, its row as `GetGrid` serves it, flagged `content_changed`
(`Adapter.applyEntry`): the listing did not move, and a plugin row has no
version to say its bytes did. A page the plugin serves that changed this way
is not the page its screenshot shows, so the row gives the screenshot up and
its face is the plugin's picture until the next capture, unless the user
froze it (`pageFaceStale`). A coded refusal is not darkness: the listings
still answer, so it rides a healthy `EventPluginHealth` as
`live_updates_off` (`Adapter.noteWatch`), told once and cleared when a
re-opened stream is open. A share of interest the adapter cannot take leaves
the scope where it was, so it is the same notice, cleared when one lands. Every open,
first, after a drop, or for a moved scope, is one path
(`Adapter.followScope`): the new stream replaces the old only once it is
open, so a context in both is watched throughout, and then each context it
adds (the whole scope after a drop) is checked the same way, since nothing
announced a change sent while no stream watched it; one it cannot list right
now is announced, and the same listing is never announced twice. One nobody
read is noted, not announced: after a node restart the client's own
reconnect resync (`retryKick(true, cache.EverySource)`) re-reads every grid
it holds. The check and the note are one critical section, and a `GetGrid`
in flight across it is announced when it lands only if the listing it took
differs from the one noted meanwhile. Descending into a directory of fifty
subdirectories, read for their previews, so announces nothing. Only a
plugin whose `InfoResponse.watch` declares it is asked; one that declares it
and answers Unimplemented has a broken declaration, and that turns live
updates off too.

**3. The transport** — `internal/connection/connection.go`. Reachability is
remembered, not only announced when it changes, and every way a connection
fails is the same record: a refused dial, a failed learn, a dead stream
(`fanInRemote`, looping `namespace.Follow`) and a moved landing all go
through `note`, the one writer of `s.health`, which publishes one
`EventPluginHealth` per transition, not one per retry. `Server.Subscribe`
opens with `darkNow()`, so a subscriber attaching after the machine died is
told. Landing is checked, not learned: `learnRoot` refuses a connection
whose far node answers a home other than the stored one
(`noteLandingMismatch`), and that refusal is a FailedPrecondition on every
read, never a staleness.

**4. The source cache** — `internal/sourcecache/`. In front of the
transport only, over the disposable `cache.db`. A remembered grid serves
FIRST (`Layer.GetGrid`), exactly as it was remembered: past `freshWindow`
(30s) or with the source known dark, one background `revalidateGrid` is
kicked behind it, single-flight per grid id. Only a miss waits on the source.
Nothing on the answer says it is a memory — that is the source's health, and
the client derives it from there (`client/cache.SourceDark`). Darkness is
`Layer.dark`, keyed by
connection segment (`sourceOf`), learned from two directions that are the
same fact — `noteReach`, from any pass-through call that failed
transport-shaped, and `applyEvent`'s health arm, from the connection's own
health on the stream this layer relays — and written through one door,
`setDark`, whose transition back to light is also what re-warms that source.
Every other read passes through and remembers, falling back to the remembered
answer on a transport-class failure only. Writes always pass through and fold
their responses into the remembered rows (`foldWrite`). The relayed stream
folds in the same way (`applyEvent`): `TileChanged` and `TileRemoved` into
the tile rows, a well's framing riding its tile, and `GridFramingChanged` into
every remembered handshake's doorways rooted at that grid (`Layer.reframe`,
over `rpc.Reframe`), which is also where an accepted root `SetFraming` lands,
so a far grid reopens dark where it was last left. A write the source did not
accept is remembered nowhere. `prefetch.go` warms
every source on Subscribe and one source when it comes back;
`servecontent.go` gives the `/content/` door the same treatment under its own
caps.

**5. The server fan-in** — `internal/server/router.go`. `Subscribe` starts
one `watchPlugin` per namespace plus one for the transport. `watchPlugin`
retries `Info` with backoff and hands off to `fanInEvents`, which re-dials
the namespace's stream forever and reports the down/up transitions as
`EventPluginHealth` through `reportHealth` — down on the first failure, up
on recovery, never once per retry. Every relayed event is re-qualified by
`qualifyEvent` → `rpc.QualifyEventIDs`: one segment prepended per hop, the
health uuid included, so a far namespace's health stays addressable here.
A source the user disabled (`plugin.Registry.Disable`) is told as disabled
whatever a layer under the router says of it (`Server.asSwitched`), and a
stream opens with every disabled one.
The router holds no freshness state; it is a relay with a health contract.

**6. The client cache** — `client/cache/cache.go`. The server is canonical.
`Apply` folds one event in under the echo interlock: an event strictly older
than the cached row (`n.Version < cur.Version`) is a stale echo that lost
the race against the mutation response already applied, and is dropped;
same-version events still apply, because framing never bumps `version` but
does change the framing columns. A body is bound to the row it derives from:
`Base`, the version of its bytes, and the blob id it was filed under — the
row's blob as the cache held it when the read was asked
(`Cache.AskContent`), or the save response's. `contentEntry.behind` is the
one rule: a higher row version, or the same version with another blob (a
pane layout write mints a blob without a bump), is a body the row has moved
past. A row with no claim — a plugin's, version 0 and no blob — has no fact
that orders its bytes. Where its source names them (`Tile.content_stamp`,
the plugin's `Entry.content_stamp`, which the read answers with the bytes),
the stamp decides as a version would: a row under another stamp makes a
clean body behind, by event or refetch alike, and one under the body's own
stamp does not, so the echo of the body's own save is no news. Where the
source names none, the event is the change: a `TileChanged` flagged
`content_changed` makes a clean body behind, and nothing else does, so a
refetch after `GridChanged`, whose rows are equal, keeps every open body,
and a framing write on the same row keeps it too. `ageContentLocked`
applies the rule to every row the cache learns — event,
`PutGrid` refetch, or write response, whether or not the row's grid is
cached — and `PutFetchedContent` to a reply whose row moved blob or stamp
while it was in flight. Who reads the body again is whoever draws it: the cache
only drops it. What is drawn from a body (a face's raster, a wrap) is keyed
by `Cache.BodyGen`, the bytes as last given, never by the row's version,
which a plugin body's does not move. `SaveBasis` is what a save claims, never the grid row
version, so a foreign writer's event can advance the row without ever
advancing what this client is allowed to claim. A dirty entry is never
overwritten by a fetch, a save response, or a delete: it is the one copy of
unsaved typing.

A root grid's framing is not in the cache: it rides the handshake, on the
doorways rooted at that grid. Its write announces `GridFramingChanged`, which
carries the three numbers, and `events.Route` applies them through
`rpc.Reframe` and fetches nothing. `GridChanged` means the listing changed
and nothing else.

**7. The outbox and the retry** — `client/outbox/`, `client/inflight/`,
`client/wasm/main.go`. `outbox.Send` is the one order and `outbox.Record` the
one fork: a write parks a retry thunk under `Key{Op, ID}` when it is SENT, and
the answer resolves it — a transport failure leaves it parked, any verdict
acks. One live entry per key, last-writer-wins, order preserved. Parking on
the answer instead was #298: an answer is exactly what a swallowed request
never produces, so the one write the outbox could not hear about was the one
it exists for. While a write is in flight its key is parked, which is the
truth about it; a drain that races the flight re-sends it, and that is safe
because every write that parks is a last-writer-wins overwrite of one key
(the writes that would replay unsafely — a create, a clone, a placement —
carry no id and park nothing; they reconcile visibly instead).

`inflight.Deadline` is the bound on every client RPC, read and write, and
`inflight.Bounded` is the one door to it for a call with no dedupe claim of
its own — a write, a nav walk's read, the shell-alive probe, the boot
handshake. Only the two long-lived streams are unbounded. A call that never
returns is what park-at-send survives and what the bound then ends: the
answer is what acks the parked entry, so without it the entry would sit
parked with no verdict for the life of the page.
`inflight.Reads` adds the claim: it bounds and
cancels fetches so a request that died with its link cannot hold a dedupe
claim forever, and it latches a failure so a read the renderer asks for every
frame is not asked again until something clears it. It is the client's ONE
claim mechanism — the bare claim set is unexported, so a claim cannot be
taken without its latch — and `App.fetchState` holds every one: grids, tiles,
tile content, url previews, and the + menu's per-node context. A deduped read that kept a claim of its own — a bare bool
beside its own cache — was bounded by nothing and cancelled by nothing, so a
request the network swallowed held its key for the life of the page and the
face it fed never loaded, never retried, and never said a word.
`retry.Reconnect` marks a gap on any stream break and tells `App.startSSE` to
fire `retryKick(true, cache.EverySource)` on the next successful subscribe: clear
the failure latches, cancel every fetch set, refetch every named and
known grid, then `syncContentOutbox` and drain. `retryBackstop` runs
`retryKick(false, …)` every `retry.Backstop` while anything is parked, and
re-asks every read latched Unreachable on the same tick: a grid, tile or body
read the renderer re-asks every frame is an `inflight.Reads`, whose failure
latches until a change (a verdict) or the next tick (an outage), never the
next frame.
`client/retry` owns that cadence, the two reconnect waits and the gap they
pace, and the boot handshake's backoff.

The kick's second argument is its SCOPE, and `client/cache` owns what that
covers. A health event names one source; every cached id says which source
serves it, because a hop prepends one segment to a health uuid exactly as it
does to ids; so `cache.ServedBy` is the join of those two facts
(`rpc.ChainedThrough` — a chain prefix, not an equality, so a connection's
flap covers the far node's home store and the far node's plugins alike), and
`Cache.ResyncSet` is the grids one source answers for. `cache.Reaches` is the
same rule asked about a source NAME rather than about a thing a source serves,
for the one claim keyed that way — the menu context, which is a node's fact
about itself. A health flap passes
that source and touches nothing else: only its grids refetch, only its
latches clear, only its in-flight fetches are cancelled — the others kept
their link and are still owed an answer. A stream gap passes
`cache.EverySource` and stays blunt, because a window with no cursor never
says whose events it swallowed. The outbox drain is scoped by neither: a
parked write is owed a verdict whatever flapped.

## Trace (a): a memory corrects itself

A remembered grid past its window, the revalidation behind it, and the room
correcting with no user gesture.

1. `client/wasm/main.go:App.fetchGrid` misses in the cache, claims the id
   through `inflight.Reads.Join`, and calls `App.loadGrid` → `rpc.Client.GetGrid`.
2. `internal/server/router.go:router.GetGrid` peels the node id and the
   connection segment (`Server.resolve`) and lands on the cache layer, which
   is what the registry holds as the transport.
3. `sourcecache.Layer.GetGrid` hits `loadGrid`. `time.Since(fetchedAt)` is
   past `c.window()`, so `revalidateGrid` is kicked. The remembered rows
   return immediately, unchanged — the far round trip never sits on the read
   path.
4. The router qualifies the answer (`qualifyTilesFor`,
   `rpc.TransitQualifyGrid`). An age is not a fact about the grid, so the
   answer carries none: with the source still answering, a memory this fresh
   is what every serve is.
5. `App.loadGrid` → `cache.Cache.PutGrid`, which ages each row's cached
   body. The bar draws no chip, because the source is not dark
   (`client/wasm/bottombar.go:App.drawMemoryChip` reads
   `cache.Cache.SourceDark`). Nothing else about the room changes: the chip
   is bar chrome, never tile styling.
6. In the background, `Layer.revalidateGrid`'s goroutine runs on `pf.ctx`
   (so a cancelled click never kills a refresh other readers want), loads the
   old rows, and calls `getGridLive` → the transport → the far node.
   `noteReachGrid` records reachability from the outcome.
7. The answer is stored by `storeGrid` — the grid row and the whole
   tile set in one transaction, tiles upserted. If `!gridRespEqual(old, resp)`,
   `emitGridChanged(gridID)` goes onto the layer's own subscriber channels. A
   verdict instead (`err != nil && !gwerr.IsTransport(err)`) → `evictGrid` and
   the same event, so the next read passes through and the verdict surfaces
   rather than a ghost answering forever.
8. `Layer.Subscribe` is serving that channel alongside the teed upstream
   stream. Its subscriber is `router.go:fanInEvents`, started by `watchPlugin`
   for the transport with `transit=true`.
9. `qualifyEvent` prepends the node id. The connection segment is already on
   the id, because the cache's own ids are `<conn>/<remote-id>`.
10. `router.Subscribe`'s loop sends it. `App.startSSE` runs `events.Route`'s
    plan for a `GridChanged`: it clears the grid's `fetch.grids` latches
    — the event is the one per-grid signal that something changed, so
    it is also what clears a verdict latch — and calls `App.fetchGrid(gridID)`. It does this
    unconditionally, for grids nobody is looking at too.
11. The refetch re-enters `Layer.GetGrid`. The rows were re-stored moments
    ago, so the hit is inside the window; with the source not dark it serves
    and revalidates nothing. `PutGrid` replaces the cached grid and the
    correction is on screen, with no gesture.

`freshWindow` is also what stops the loop feeding on itself: the client's
refetch lands inside the window of the revalidation that caused it, so a
source whose listing drifts on every walk settles into at most one refresh
per window.

## Trace (b): a connection goes dark and comes back

Both health directions, and what each one costs the user.

**Down, discovered by the transport.** `connection.fanInRemote`'s
`namespace.Follow` returns; `noteHealth(ns, false, detail)` writes
`s.dark[name]` and publishes one `healthEvent` on the hub. `Server.Subscribe`
relays it, and opens with `darkNow()` for anyone attaching later.

`Layer.setDark` is the one writer of the cache's dark map, and the layer
learns from two directions through it. The map is the same fact either way;
the one thing the directions do not share is whether the client has to be
told, which is `setDark`'s `announce` argument and the caller's to state.

**Down, discovered by the cache — direction one.** Any pass-through call that
fails transport-shaped: `Layer.GetTile`, `GetTilePreview`, `ReadContent`,
`ServeContent`, and every write verb call `noteReachTile`/`noteReachGrid` →
`noteReach` → `setDark(source, true, announce)`. It announces, because this
layer discovered the transition alone and nobody else watched the call fail:
on the transition only, `emitGridChanged` names the grid at hand, so a client
already holding that room re-reads rather than sitting on rows nothing is
revalidating. A call its own caller gave up on (`gwerr.IsAbandoned`) is no
news either way, here and in the plugin adapter's listing: the refetch an
announcement causes is cancelled by the next resync, and that cancel would be
news again.

**Down, discovered by the cache — direction two.** The transport's health event
arrives on the stream this layer relays and lands in `Layer.applyEvent`'s
`Event_PluginHealth` arm → `setDark(uuid, !healthy, no announce)`.
At this layer the uuid is the bare connection segment, which is exactly
`sourceOf` of every id chained through it. It does not announce: the same
event is relayed onward to the very client that would be told, in this same
call, so a `GridChanged` of ours would say twice what was already delivered —
and what the client does with it is the client's half. This is the path that
matters in practice — the machine usually dies while nobody is calling it, and
without it the room would look live until some call happened to fail.

**What the user sees while dark.** A remembered grid serves inside its
window and revalidates behind it anyway, because `Layer.GetGrid` consults
`isDark` alongside the age; the bar shows the "cached" chip, which the client
draws from the same health the node learned. Bodies, previews, and door pages
fall back to remembered entries where there are any; where there are none the
transport error stands and the read fails honestly. Links through the
connection are NOT dead — `client/deadref` answers from the node's
declaration, and a declared connection that will not answer is health, not
deadness.

**On the client.** `App.startSSE` routes a `PluginHealth` event to
`App.reportPluginHealth`, which folds the event into `Cache.NoteHealth`
— the client's one copy of which sources are not answering. Every room served
through one is a memory, and that is what the bar's chip says
(`Cache.SourceDark`, joined by `ServedBy`, the rule a resync is scoped by).
`events.ReactHealth` is the one table over the event. Unhealthy posts a
sticky notice keyed `plugin:<node>/<conn>` ("live updates stopped — …");
`live_updates_off` posts its own, keyed `live:<node>/<conn>` ("live updates
off — …"), so each comes down on its own field. A move of the healthy bit
arms `events.Resyncs`, which calls `retryKick(true, h.PluginUUID)` once the
source's health has held for `cadence.HealthSettleMs`: a flapping source
resyncs once, on the state it rests in. A change of `live_updates_off`
alone resyncs nothing: the listings still answer. The notices move at once. The down
direction resyncs exactly as the up one does, and at exactly the same scope:
a source going down changes what its grids ARE, and which grids those are is
the join `cache.ServedBy` makes of the health uuid and the ids the client
already holds. The reads a resync cancels are the client's own doing, so they
say nothing (`clientsync.OutcomeAbandoned`).

**Up.** The next `namespace.Follow` establishes; `noteHealth(ns, true, "")`
publishes the recovery, and `learnRoot` publishes one too on a first or
healed landing. `Layer.applyEvent` clears `dark[conn]`; the next successful
pass-through call would have cleared it anyway through `noteReach`.
`App.reportPluginHealth` clears the darkness, so the chip goes, resolves the
notice, and arms the same settled `retryKick(true, h.PluginUUID)`, which cancels this source's
in-flight fetches, clears its latches, and refetches `Cache.ResyncSet` of it —
every cached grid chained through it, and nobody else's. Those reads hit the
cache inside their windows with the source no longer dark, so they serve what
the revalidation has re-stored by then.

The recovery also re-kicks the prefetch walk, for that one source
(`Layer.kickPrefetch`, from `setDark`'s transition out of darkness). The
`Layer.Subscribe` trigger cannot cover this: the layer's upstream
subscription is the transport's hub stream, which survives one connection's
outage, so nothing else here notices the machine came back. The client's
`retryKick` refetches the grids it holds; the walk is for the grids nobody
re-opened, which is where "a recent copy of what you did not happen to read"
has gone most stale. It is transport-only — reads, no version claims — and
revalidates through the ordinary window rules.

The trigger is the door's, not either direction's, because either can notice
first: in practice the client's refetch answers before the fan-in publishes
health, so a kick hung off the health arm alone did nothing at all, which is
what `test/connections` caught. Only the transition to light kicks, only for
the source that came back, and the walk's single-flight is keyed by source, so
a flapping connection has at most one walk in flight rather than one per
flap.

## Trace (c): a write racing its own echo

The version interlock, the outbox park, and the drain.

1. A keystroke goes through `client/wasm/mutate.go:App.putEditedContent`:
   `cache.PutEditedContent` marks the entry dirty and leaves `Base` alone —
   the edit is based on the bytes already there — and `App.recordContent`
   parks `Key{Op: "Content", ID: tileID}` with `flushTileContent` as the
   thunk. Storing the bytes and recording the debt are one door, so an edit
   typed during an outage cannot stay out of the outbox.
2. The debounce sweep or an ascent flush calls `App.enqueueTextSave`, which
   goes through `contentSaves` (`outbox.SaveQueue`), the per-key serial
   queue a pane layout shares. The basis is claimed AT SEND TIME, after
   any earlier write for the same tile has
   advanced it: `a.c.SaveBasis(tileID)`, never the grid row's.
   The row advances when a foreign writer's event or a refetch lands without
   this client seeing the new bytes; claiming it would carry the current
   version with stale bytes past the server's check. The basis is an
   `rpc.ContentBasis`: the version for a home body, and for a plugin's, which
   has none, the source's stamp the bytes were read under
   (`ContentChunk.content_stamp`).
3. `App.postWriteContent` → `rpc.Client.WriteContent` → the router → the
   owning namespace. Home claims and bumps through `claimContentVersion` +
   `finishContentEdit`, the one pair that may, and emits a `TileChanged`. A
   plugin's adapter writes through by key with the claimed stamp
   (`Adapter.WriteContent`), the plugin refuses a stamp that is not the
   entry's now with the same `FailedPrecondition` a stale version gets, and
   the adapter answers the row with the written bytes' stamp and emits its
   `TileChanged`, flagged `content_changed`.
4. On success the client advances immediately, not when the echo lands:
   `a.c.UpdateTile(tile.GridID, *tile)` and
   `a.c.PutSavedContent(tile, newContent)`, filed under the response row.
   The tile is cached under the RESPONSE row's grid, because a save routed
   through a leaf link answers a row in the target's foreign grid.
   `recordContent` then finds the entry clean and acks the outbox key.
5. The echo arrives later on `startSSE`'s stream and goes to `cache.Apply`.
   If an earlier write's echo (version N-1) is still in flight, the interlock
   `n.Version < cur.Version` drops it: applying it would roll the tile back
   and then forward, a mutation the user never made. The response row at N
   stands. A plugin's write echoes twice — the adapter's `TileChanged` and the
   plugin's own `Watch` `EntryChanged` — both carrying the written bytes'
   stamp, which the body is now filed under, so neither drops it: the typed
   text stays, saved or still being typed.

   There is one door into a grid's tile map, `Cache.putTileLocked`, and both
   `Apply` and `UpdateTile` are it: the interlock and content aging
   belong to the map, not to the path a row arrived on. So a write RESPONSE
   that is the older row is refused on the same rule as an older echo. (The
   one difference is insertion: an event may add a tile the cache has not
   seen; `UpdateTile` only updates a row already held.)
6. Content aging runs on whichever row does apply, whichever door it
   came in by. Clean entry the row has moved past (`contentEntry.behind`)
   → drop the body, so the next render refetches and the
   foreign edit becomes visible. Dirty entry → keep it; its save claims the
   old base, conflicts at the server, and reconciles visibly.
7. Failure, through `clientsync.Of` and `ReactSave`. Conflict and Rejected
   both Log, Refetch, and DropLocal: `a.c.DropTileContent`, then
   `refetchGridOnConflict` or `fetchGrid` — the screen may not keep showing
   bytes the server refused. Transport drops nothing: the entry stays dirty,
   an Info notice says "unsaved changes kept — server unreachable, will
   retry", and `recordContent` re-parks under the same key.
8. Non-content writes take `App.do`, which calls
   `out.Record(o, Key{w.label, w.id}, retry)` on exactly the same fork.
   Framing writes are `optimistic`, so `clientsync.ReactOptimistic` rolls the
   cache patch back on a verdict and keeps it on transport, where it is the
   value the retry will land.
   A placement (`App.postPlacement`, a move or a resize) is optimistic too,
   but parks nothing: any failure puts the tile back and the drag snaps back.
   Layout claims no version, so the interlock in step 5 cannot order two
   placements; `cache.Place` holds the placement against every older row
   instead, until the stream carries it (the stream is ordered, so nothing
   older follows) or, once the write has landed, a grid read answers.
9. The drain. `startSSE` sets `gap` on any stream error and on a clean EOF —
   Subscribe has no cursor, so both are gaps — and calls
   `retryKick(true, cache.EverySource)` on the next successful subscribe. The
   drain itself is never scoped by a source: a parked write is the user's
   bytes and is owed a verdict whatever flapped.
   `syncContentOutbox` re-derives the content
   entries from their one owner, the cache, then `out.Drain()` runs each thunk
   in first-parked order. A thunk that fails on transport again re-parks
   itself through `Record`, so a drain against a still-dead link converges
   instead of losing entries.
10. At unload, `App.doOnUnload` sends each drained write through its beacon
    form (`navigator.sendBeacon`), the one transport that survives the page.
    A write with no beacon form is fired and hoped for, never waited on.

## What is deliberately NOT guaranteed

**An event gap loses events.** `Subscribe` has no cursor. A break on either
side — the client's stream (`startSSE`'s `gap`) or the server's re-dial
(`fanInEvents`) — loses whatever happened in the window, and nothing replays
it. The cure is blunt resync: `retryKick(true, cache.EverySource)` refetches
every known grid. Blunt on purpose, and only here — the gap names no source,
so there is nothing to scope it to. A health flap DOES name one, and resyncs
that source's grids alone (`cache.ServedBy`).

**A slow subscriber loses events.** `Layer.emitGridChanged` and
`Adapter.emit` drop onto a full 64-slot buffer rather than blocking a
revalidation or a write. Every event on those streams is a cue to look
again, never a fact only it carries; the rows are already stored and the
next read serves them.

**Serve-first can show a memory.** Inside `freshWindow`, with the source not
known dark, `Layer.GetGrid` does not even revalidate. A change made on the
far node in the last 30 seconds whose event did not arrive is not on screen
and nothing says so. Only a grid the cache has never seen waits on the
source.

**Framing is last-writer-wins.** `version` means the user's content bytes
and nothing else, so framing, captures, and layout carry no claim and cannot
conflict. Two panes settling the same doorway resolve by arrival order, and
the outbox keeps one live entry per key by design.

**Absence is never inferred from silence.** A non-authoritative listing
retires nothing without a definitive `PRESENCE_GONE`
(`Adapter.synthesize`, `Adapter.DeleteTile`), and a connection that cannot
be resolved answers NOT gone (`connection.Server.Probe`). A row kept on
doubt costs nothing durable; a row retired on doubt loses a placement and
every link to it. Framing is the same: a doorway answered with no framing
(the transport's row for a dark connection) keeps the one the cache
remembers (`sourcecache.keepFraming`), because a visited grid never becomes
unvisited.

**The cache is disposable.** `cache.db` may be deleted at any moment. Every
guarantee here degrades to "the first read pays the source's full latency",
and a cache that cannot remember surfaces as this namespace's health, because
a silently broken cache runs unnoticed for hours. Two shapes, one report:
`noteCache` announces the transitions of an open file whose writes fail, and a
file that never opened at all is `sourcecache.Unavailable` — the store
`node.openCache` hands back either way, fronting pass-through and opening
every subscriber's stream with the reason (`missing.Subscribe`), so the node
has one cache path and no branch.

**Dark is not dead.** Darkness comes back; a namespace the node does not
declare is `client/deadref`'s business, is never fetched for, and raises no
notice at all.

## Seam-test gaps

Each cross-layer behaviour in the three traces, and what pins it.

### Trace (a)

| Behaviour | Pinned by |
|---|---|
| A past-window hit serves the remembering unchanged and kicks one revalidation | `sourcecache_test.go:TestAPastWindowServeIsTheRememberedAnswer`, `TestServeFirstNeverWaitsOnTheSource` |
| Revalidation that finds drift emits `GridChanged` | `sourcecache_test.go:TestRevalidationEmitsGridChanged` |
| A verdict evicts and announces | `sourcecache_test.go:TestRevalidationVerdictEvicts` |
| The event crosses layer stream → fan-in → qualification → client, and the next read serves the correction | `internal/server/servefirst_seam_test.go:TestServeFirstEventReachesTheClient` |
| Refresh after a blind window replaces the whole tile set | `sourcecache_test.go:TestRefreshReconcilesWhatChangedWhileBlind` |
| The client's own arm: `GridChanged` clears `fetch.grids`' latches and calls `fetchGrid` | `internal/server/servefirst_seam_test.go:TestServeFirstEventReachesTheClient`, `client/events/events_test.go:TestRouteTable` |

### Trace (b)

| Behaviour | Pinned by |
|---|---|
| The transport learns darkness from its own stream and publishes once | `internal/connection/fanin_health_test.go:TestFanInRemotePublishesHealthOnStreamDeath` |
| A subscriber arriving after the outage is told (`darkNow`) | `fanin_health_test.go:TestASubscriberArrivingAfterTheOutageIsToldOfIt` |
| Direction one: a failed pass-through is darkness, the remembered room still serves, and the next answer clears it | `sourcecache/dark_test.go:TestAFailedCallIsDarkness` |
| A call its caller abandoned is neither dark nor light, in the cache and the plugin adapter alike | `dark_test.go:TestAnAbandonedCallLearnsNothing`, `internal/server/abandoned_read_seam_test.go` |
| Direction two: the relayed health event alone is darkness | `dark_test.go:TestAConnectionsHealthIsDarkness` |
| Discovering darkness announces the grid at hand | `dark_test.go:TestDarkDiscoveryTellsTheClientToReRead` |
| A plugin's source going dark is that namespace's health, announced on the transition only and replayed to a subscriber arriving mid-outage | `internal/pluginhost/fs_parity_test.go:TestADarkSourceIsPublishedAsHealth` |
| A plugin's `Watch` change is a `GridChanged` at the door, locally and through a connection, only when the listing moved, so a busy directory whose names stay announces nothing; an entry changed in place is its `TileChanged` flagged `content_changed`, and a client's open body and text face show the new bytes, a served page's live view reloads and its unfrozen screenshot gives way; the flag survives each hop and the hub's coalescing; an undeclared plugin is never asked, a refusal or a declared Unimplemented turns live updates off on a healthy event and never darkens the source, a dropped stream re-opens and catches up every shown context whose listing moved, a context a scope adds is announced only when its listing differs from what was served, one nobody read is noted and announced by nothing, a booting node tells a client re-reading what it shows nothing while the disk is quiet, a holder is announced once however many of its links moved, a read in flight across the check is announced only when it differs, a refusal clears when a re-opened stream is open, a respawn gets a fresh one; the stream is opened only while a client shows one of its grids, scoped to their contexts, and a scope change re-opens it without a resync, across a connection too; a grid seen only as a well's preview is shown | `internal/server/plugin_watch_seam_test.go`, `internal/server/fs_watch_seam_test.go`, `internal/server/fs_content_change_seam_test.go`, `internal/server/proc_content_change_seam_test.go`, `internal/server/gitlab_content_change_seam_test.go`, `internal/server/hey_content_change_seam_test.go`, `client/urlview/moved_test.go`, `apps/desktop/e2e/fs-page-changed.spec.ts`, `internal/server/link_entry_seam_test.go:TestATargetsNewBytesReachTheBodyALinkShows`, `client/cache/binding_test.go:TestAClaimlessBodyAgesOnlyWhenAnEventSaysSo`, `internal/server/live_updates_off_seam_test.go`, `internal/server/interest_refused_seam_test.go`, `internal/pluginhost/watch_test.go`, `internal/server/interest_seam_test.go`, `client/pane/showing_test.go`, `apps/desktop/e2e/well-preview-watch.spec.ts` |
| Both directions write the same fact through `setDark`, and differ only in the announcement | `dark_test.go:TestBothDirectionsLearnTheSameDarkness` |
| Serve the remembering when dark; verdicts never masked | `sourcecache_test.go:TestServesStaleWhenDark`, `TestVerdictNeverMasked` |
| A far root's framing event and an accepted root write land on every remembered doorway rooted there; a dark framing write is refused as other dark writes are and remembered nowhere; a tile event keeps a well's framing | `sourcecache/framing_test.go` |
| A doorway answered with no framing keeps the remembered one; an answer replaces it | `sourcecache/framing_test.go:TestSilenceKeepsTheRememberedFramingAndAnAnswerReplacesIt` |
| Across the real transport: a far grid reopens dark at the last pan, whoever made it, on the connection's row, the far menu, and its wells | `internal/server/darkframing_seam_test.go:TestAFarGridReopensWhereItWasLeftWhileDark` |
| Door bodies degrade the same way | `servecontent_test.go:TestServeContentServesStaleWhenDark`, `TestServeContentNeverCachesVerdicts` |
| Real binaries, real ssh: warmed reads serve the remembering, never-read bytes fail honestly, a revived remote answers live | `test/connections/partition_test.go:TestMountPartitionServesCache` (`make check-connections`) |
| A dark source is the bar's cached chip, and the join from a source to the rooms it serves | `client/cache/dark_test.go`; live, `apps/desktop/e2e-web/web-remote-menu.spec.ts` ("a dark mount serves the remembered room, marked stale") |
| Health uuid gains one segment per hop | `internal/server/routing_pure_test.go:TestQualifyEvent` (pure only) |
| A connection's health event reaches a real client stream as `<node>/<conn>` | `internal/server/transport_seam_test.go:TestConnectionHealthArrivesQualified` |
| The client's health arms: `reportPluginHealth` folds the transition in and kicks in BOTH directions, and the notice resolves on recovery | The same revived-mount spec: the `plugin:` notice arrives on the down transition and leaves the strip on recovery, and the chip appears and later clears with no gesture either time |
| A refused Watch is its own sticky notice beside the dark one, each clears on its own field, and the field alone resyncs nothing | `client/events/events_test.go:TestReactHealthTable`, `client/events/resync_test.go`; live, `apps/desktop/e2e/live-updates-off.spec.ts` |
| A flap resyncs the flapping source's grids and NOBODY else's, including a chain through it | `client/cache/resync_test.go:TestAFlapResyncsOnlyTheGridsItsSourceServes`, `TestAConnectionsFlapOwnsEveryGridChainedThroughIt`; the chain rule at its owner, `api/rpc/segment_test.go:TestChainedThroughIsTheWholeChainNotOneNodesPeel` |
| The gap paths keep their breadth: `cache.EverySource` is the whole cache | `client/cache/resync_test.go:TestEverySourceIsTheWholeCache` |
| A flap cancels only the fetches that rode through it | `client/inflight/inflight_test.go:TestCancelIfLeavesTheFetchesThatKeptTheirLink` |
| The menu claim's scope — a source's own name, and every node behind it | `client/cache/resync_test.go:TestReachesCoversTheSourceItselfAndEveryNodeBehindIt` |
| A swallowed menu read does not latch a remote pane's + menu empty: it gives up, surfaces, and the menu fills itself in | `apps/desktop/e2e-web/web-remote-menu.spec.ts` ("a menu read the network swallows does not latch the remote menu empty") |
| A single connection's recovery re-walks that source, over the real transport and its relayed health | `sourcecache/prefetch_seam_test.go:TestOneConnectionsRecoveryReWalksThatSource` |
| A recovery the layer learns from its own answering call walks it too | `prefetch_seam_test.go:TestARecoveryNoticedByAReadWalksTheSourceToo` |
| It walks only the source that recovered | `prefetch_seam_test.go:TestARecoveryWalksOnlyTheSourceThatRecovered` (two connections, two far nodes) |
| A flap storm does not stack walks: single-flight per source | `prefetch_seam_test.go:TestAFlapStormDoesNotStackWalks` |
| Real binaries, real ssh: a revived connection warms what nobody read, so a second partition serves it | `test/connections/partition_test.go:TestMountPartitionServesCache` (`make check-connections`) |
| A cache that never opened reaches a real subscriber as the transport's health | `internal/node/cache_health_test.go:TestAnUnopenableCacheSurfacesAsHealth`, `TestAnOpenedCacheReportsNothing` |

### Trace (c)

| Behaviour | Pinned by |
|---|---|
| A save claims the basis, not the row version | `client/cache/cache_test.go:TestSaveBasisFollowsBytesNotRow` |
| A stale basis conflicts at the real server and the reaction table says surface + refetch + drop | `internal/server/outbox_seam_test.go:TestContentConflictSurfaces` |
| A capture on the edited row does not conflict; exactly one bump | `outbox_seam_test.go:TestCaptureDuringAnEditDoesNotConflict` |
| The echo interlock drops an older `TileChanged` | `client/cache/cache_test.go:TestApplyStaleEchoDropped` (unit) |
| A fetch never clobbers dirty bytes; a stale reply never regresses the basis | `cache_test.go:TestFetchNeverClobbersDirtyContent`, `TestStaleFetchNeverRegressesContent` |
| A save response keeps mid-flight typing and only advances the basis | `cache_test.go:TestSavedContentKeepsMidFlightTyping` |
| A plugin body is aged by its source's stamp: its own save's echo keeps it, another stamp drops it, a reply whose row moved stamp mid-flight is refused; a remembered body keeps its stamp; across the seam the row and the read name a file's bytes by one stamp | `client/cache/binding_test.go:TestAStampedBodyAgesByItsStamp`, `internal/sourcecache/stamp_test.go`, `internal/server/fs_stamp_seam_test.go` |
| A body answers only for the blob it was filed under, whichever door the newer row came by; a save is filed under its response row | `client/cache/binding_test.go:TestABodyIsBoundToItsOwnBlob`, `TestASavedBodyAnswersForItsResponseRow`, `TestDirtyTextSurvivesAForeignRowAnywhere` |
| Transport parks, the drain converges against a dead link, the kick lands it | `outbox_seam_test.go:TestTransportFailureParksAndTheKickLandsIt` |
| The unload drain lands through the beacon transport | `outbox_seam_test.go:TestUnloadDrainsTheOutbox` |
| Live: typing survives a server outage and saves itself after restart; settled framing lands too; a swallowed grid read un-latches | `apps/desktop/e2e-web/web-outage.spec.ts` |
| The backstop re-posts a parked write on its interval and not before, at a cadence the spec retunes | `apps/desktop/e2e-web/web-outage.spec.ts` ("the backstop re-posts a parked write on its interval, not before"), `client/retry/retry_test.go` |
| A foreign edit becomes visible, and opening/closing never stomps it | `apps/desktop/e2e/foreign-writer.spec.ts` |
| The interlock across the seam: real responses and real echoes of two writes, in every order the two paths can produce, never regress the cached row | `outbox_seam_test.go:TestEchoInterlockAcrossTheSeam` |
| An older write RESPONSE is refused by the same interlock an older echo is: one door into the tile map | `outbox_seam_test.go:TestAResponseRowObeysTheInterlock` (seam), `client/cache/cache_test.go:TestUpdateTileTakesTheOneDoor`, `TestUpdateTileAgesTheBodyToo` (unit) |
| A plugin body saves as a home one does: a clean save lands on disk and its own echoes keep the typed text; bytes changed on disk under a dirty edit refuse the save as a conflict and show the file; a write the plugin cannot take parks under its key-form id and lands when it is back; a verdict never parks | `internal/server/fs_write_seam_test.go`, `internal/pluginhost/adapter_caps_test.go:TestAdapterStampsTheWriteFactsThePluginDeclares`, the fs plugin's `fs/plugin/write_test.go`; live, `apps/desktop/e2e/fs-edit.spec.ts` |
| `syncContentOutbox`'s derivation: dirty→park, clean→ack, and the pre-drain sweep over the dirty set | `client/outbox/outbox_test.go:TestRecordContentIsTheDirtinessFork`, `TestSyncContentParksTheDirtySetInOrder` (`Outbox.RecordContent`/`SyncContent`; `mutate.go` is glue) |
