# The v2 design: content providers and the presiding node

Status: implementation design (commissioned 2026-08-22). Successor to
`docs/content-presentation.md` §9 (the seed) — this document is the one
to build from. The migration section (§8) is deliberately the most
detailed: the data is a daily driver's, and the switchover is one-time.

## 1. Tenets

1. **Things stay as the user left them.** Arrangement is the USER's
   fact; the node owns it, for every grid, uniformly — including the
   launcher. A provider may *hint* placement for an entry the user has
   never placed; the user's arrangement always wins thereafter.
2. **One node surface.** Clients and federation speak the full Gridwell
   service, unchanged. Providers are invisible on that wire — only
   nodes talk to providers.
3. **Providers are stateless.** A provider answers from its source
   (disk, /proc, IMAP) in its own stable keys. No ids, no layout, no
   database, no migrations. Key stability is the provider's one
   contract: a changed key scheme orphans references, exactly as
   re-minting ids would today.
4. **The node DB is self-describing in isolation.** Native content and
   its layout, complete on their own. Everything remembered about an
   external lives in that external's one memory file.
5. **Memory is forgettable, and interpretable.** Losing an external's
   memory DB dangles links and resets arrangement — their nature. The
   plugin uuid inside every qualified id keeps a dangling link
   readable, and reuniting with the file reconnects it.
6. **Caching is uniform.** One read-through cache fronts every
   external. A dark directory, a dead process table, and an
   unreachable remote all serve the remembered answer, stamped stale.
   This SUBSUMES I12 — the sweep rule stops being per-plugin code.
7. **Identity is minted once, never reassigned** (unchanged). The node
   mints numeric tile/grid ids against provider keys; AUTOINCREMENT;
   tombstones, never reuse.
8. **Config declares topology; data holds content.** server.yaml names
   the providers and the connections (a connection name is an
   IMMUTABLE ID by rule — renaming it dangles references; the template
   says so loudly). This REVERSES #199 (owner decision, Joe
   2026-08-22): no connection creation from the client.

## 2. The wire: two services

### 2.1 Gridwell (the node surface) — unchanged

Every node serves today's full contract to clients and to other nodes.
No RPC is added or removed. Field-level deltas only:

- `Grid.writable` returns to meaning exactly "content can be created
  here". Rearrangeability is universal (every grid's layout is
  node-hosted), so the #266 client inference (`SameGrid &&
  !TargetImmovable`) DELETES — placement is simply allowed. The node
  grid stops being immovable.
- Everything else (`reference`, `serves_page`, `stale`,
  `status_detail`, transit qualification, the version rules) carries
  over with its meaning intact.

### 2.2 ContentProvider (the plugin surface) — new proto

`api/contentprovider/v1/provider.proto`. Everything is keyed by the
provider's own stable STRING KEYS; ids never appear. Sketch:

```proto
service ContentProvider {
  rpc Info(InfoRequest) returns (InfoResponse);
  rpc List(ListRequest) returns (ListResponse);
  rpc ReadContent(ReadRequest) returns (stream Chunk);
  rpc WriteContent(stream WriteRequest) returns (WriteResponse);
  rpc ServeContent(ServeRequest) returns (stream ServeChunk);
  rpc GetPreview(PreviewRequest) returns (PreviewResponse);
  rpc Probe(ProbeRequest) returns (ProbeResponse);
  rpc Delete(DeleteRequest) returns (DeleteResponse);
  rpc Search(SearchRequest) returns (SearchResponse);
  rpc Watch(WatchRequest) returns (stream Change);
}

message InfoResponse {
  string kind, display_name, glyph;
  string root_context;              // key of the landing context
  bool   watch;
  map<string,string> create_schemas;
  repeated MenuEntry menu_entries;  // same shapes as today
}

message ListRequest  { string context; }        // a context ≈ a grid
message ListResponse {
  repeated Entry entries;
  bool authoritative;   // true: an absent key is GONE (fs, readable);
                        // false: absent means "not this pass" (proc)
  string source_label;  // Grid.source_id equivalent (path, pid)
}
message Entry {
  string key;                 // stable, unique within the provider
  string kind;                // well | text | url
  string label;
  string child_context;       // wells: the context this entry opens into
  bool   serves_page;
  string text_presentation;
  string status_detail;
  Hint   placement_hint;      // optional; used only at first sight
}
```

Notes:

- **Contexts replace grids** on this surface: `List("path:/home/joe")`
  is what `GetGrid` used to be, minus every presentation field. The
  node maps context keys to grid ids in the memory DB, exactly as it
  maps entry keys to tile ids.
- **`authoritative`** is the one sweep fact only the provider knows.
  With it, tenet 6 does the rest: authoritative listing → cache drops
  absent keys (their layout rows tombstone); non-authoritative →
  absent keys keep serving from cache until `Probe` answers GONE.
- **Unimplemented stays polite**: Search = no results, ServeContent =
  404, Watch = no events, WriteContent = read-only, GetPreview = no
  thumbnail. A minimal provider is `Info` + `List` + `ReadContent`.
- **No versions**: provider content is not version-edited (today's
  fs/proc rule, now structural). Optimistic concurrency lives
  entirely in the node's layout rows and native store.
- **No shells**: shells are native content (node-owned tmux, as
  today's local plugin). A provider cannot serve a shell in v2; noted
  as a possible later extension, not designed here.
- Go authors implement the generated interface and
  `guest.ServeProvider`; any other language speaks the service behind
  the same go-plugin handshake. A `plugin.ProviderFactory` is the
  bundled-leaf shape (gridwell-all, mobile).

## 3. The databases

### 3.1 The node DB (one file; the source of truth)

Today's local store, absorbed — grids, tiles, blobs, system, session,
the framing/content version split (`emitTileChanged` /
`finishContentEdit`), clone, trash — plus:

- **The node grid becomes ordinary rows.** A reserved grid whose well
  tiles link to provider roots and connection roots. It RECONCILES
  against server.yaml at boot (config is its "source"; the same
  first-sight/auto-place path every provider listing uses): a new
  config entry mints a tile, a removed one tombstones it, and
  placement is the user's, durable. The launcher finally rearranges.
- Shell machinery (tmux, per-DB socket) and the event stream move in
  with the store.
- The frozen-format contract (`plugins/local/store/CLAUDE.md`) moves
  with it, unchanged: additive-only, never delete.

### 3.2 The external memory DB (one file per provider AND per connection)

Everything the node remembers about one external. Single writer: the
node. Schema (dbformat-versioned; durable-but-forgettable):

```sql
meta      (k, v)                  -- uuid, kind, format version
contexts  (grid_id PK AUTOINCREMENT, key UNIQUE, root_cx, root_cy, root_zoom)
idmap     (tile_id PK AUTOINCREMENT, grid_id, key, tombstoned;
           key UNIQUE among LIVE rows only — a retired key's row stays,
           and a recreated key mints a FRESH id, the legacy fs identity)
layout    (tile_id PK, x, y, w, h, view_x, view_y, view_zoom,
           text_x, text_y, text_w, text_h, text_mode, content_zoom)
userdocs  (tile_id PK, params)    -- provider tool rows (fs search):
                                  -- user-created, provider-validated
cache_listings  (context_id PK, proto_bytes, fetched_at)
cache_content   (key, proto_bytes, fetched_at)   -- bounded
cache_previews  (key, jpeg, fetched_at)          -- bounded
```

- Layout rows are UNVERSIONED (a deviation from this doc's first
  draft, decided at implementation): provider tiles serve version 0
  today (griddb never versioned placement), and the parity gate holds
  the new stack to the same wire. Optimistic concurrency for provider
  placement can arrive later as an additive column.
- Renaming provider tiles stays refused for now (the legacy fs
  behavior — a file tile's name IS the filename); a user-label
  override is a possible later additive column, not a v2 fact.
- For a CONNECTION the layout half sits empty (the remote hosts its
  own); the cache half is the old mountcache, same disposable
  semantics, now sharing the file with nothing it can hurt (dropping
  cache rows never touches idmap/layout — and for connections there
  is nothing else there).
- Text framing rows mean a read-only file's scroll position finally
  has a durable home — the #236 client special-case deletes.

### 3.3 server.yaml

Providers (as today) and connections. A connection block carries the
name (immutable id — the old NS segments arrive here verbatim, §8),
endpoint, ssh fields, key path. The template comments the immutability
rule loudly.

## 4. The node: new and changed pieces

1. **The layout engine** (new; the heart). One package, pure logic +
   the memory-DB store. Given a provider listing and the memory DB:
   resolve keys→ids (minting for new keys), overlay layout, first-sight
   placement (hint if given, else auto-place — `NextEmptyCell`
   heritage), tombstone on authoritative absence. Terminates
   `PlaceTile`, `SetTile` framing/rename/content_zoom arms, and
   `SetRootView` for provider grids. THE seam of the design: it joins
   two owners' facts by key, so it is built as a pure, exhaustively
   unit-tested package, and the provider→wire round trip gets its own
   seam tests.
2. **The uniform read-through cache** (generalizes mountcache). Fronts
   every external. Transport-class failure → remembered answer,
   `stale` stamped; an answered GONE is never masked. Tees Watch/
   Subscribe to stay fresh. I12 becomes this component's unit tests.
3. **The provider host** (evolves the loader). Same spawn/in-process
   compose mechanics and identity injection; speaks ContentProvider;
   caches `Info`.
4. **The builtin transport** (absorbs the remote plugin). Per
   connection from yaml: lazy dial, self-heal, `status_detail` on the
   connection's node-grid tile, raw-gRPC forward of the full Gridwell
   service with today's transit qualification (chain ids pass through;
   the node never mints or overlays on them).
5. **The native store** (absorbs the local plugin). A module move, not
   a rewrite.
6. **ListPlugins** synthesizes rows for providers AND connections —
   connections present as plugins (globe glyph, the connection name as
   label). The picker flow for creating connections goes; the client's
   instance-picker machinery survives only if some future provider
   declares parameterized instances (out of scope for v2 — fs/proc/
   mail don't).
7. **Events**: provider `Watch` changes arrive in keys; the node
   translates through the idmap and fans into `Subscribe` as today's
   tile/grid events.

Dissolved outright: the local and remote plugin processes, griddb, the
fs/proc reconcile-and-sweep code, the mountcache as a separate wrapper,
the `ssh_connections` layout columns, the node grid's synthesized row
and JSON viewport file, and the #266/#236 client special cases.

## 5. The provider implementations

- **fs**: keys are paths relative to the configured root (`"."` the
  root context). List reads the directory (authoritative when
  readable; on error return the error — the node's cache serves
  stale). ReadContent/ServeContent/GetPreview as today. Delete =
  trash. Search as today, results as key-paths. Its DB, schema,
  migrations, reconcile: gone. Estimated residue: fssource + trash +
  search + servecontent, roughly half the current module.
- **proc**: keys `"pid:<n>"` and `"@info"`; non-authoritative
  listings; Probe by /proc existence; Delete = SIGTERM.
- **The stranger's example (mail)**: contexts are folders, keys are
  message-ids, entries are text tiles with `text_presentation:
  rendered`. Zero layout code — this is the door the design is judged
  by, and it becomes the worked example in `plugin-authoring.md`.

## 6. Client impact

Deliberately near-zero: the wire is the same service. Deletions and
simplifications only — the `TargetImmovable` arm (node grid moves now),
the #236 read-only-framing special case (framing writes always land),
the instance-picker connection-create flow. The rendering of remotes as
plugins already rides `ListPlugins` declarations.

## 7. Implementation order

The oracle comes first; every later stage is judged against it.

1. **The parity harness** (§8.4) — built and green against TODAY's
   binaries before anything changes. This is the program's `make
   check`.
2. **ContentProvider proto + provider host + layout engine + memory
   DB**, landing fs first behind a config flag (`fs` runs as provider
   OR legacy plugin); parity crawl compares the two stacks serving the
   same home.
3. **proc** follows on the same machinery.
4. **Native fold**: local store into the node; node grid to real rows.
5. **Transport fold**: builtin connections from yaml; remote plugin
   retired.
6. **Client deletions**, gate updates, docs.
7. **The converter + cutover** (§8), then delete the legacy paths.

Each stage keeps every gate green; the federation and parity gates are
the ones that matter most here.

## 8. The migration (designed first, executed last)

The constraint: this is daily-driver data — the local stores, the
fs/proc id maps (saved deep links resolve through them), the ssh NS
segments (stored references contain them). One-time switchover; bigger
than the dbformat chain, so it does not use it.

### 8.1 Shape: an offline one-shot converter

`gridwell convert-v2 --from <old-home> --to <new-home>`. Reads the old
files, writes a complete fresh home. NEVER in place; the source is
opened read-only and is untouched, so rollback is "start the old
binary" — valid until the first write in the new world, and the old
home stays archived after that.

### 8.2 What maps where (identity verbatim, nothing re-minted)

| Old | New | Notes |
|---|---|---|
| local store DB | node DB | near-copy: same tables, same ids, same blobs; plus the version-rule columns unchanged |
| node-grid JSON viewport | node DB (the launcher grid's root view) | |
| launcher arrangement | node DB launcher rows | old node grid had no placement (synthesized row) — tiles arrive at the synthesized positions as their first-sight placement, then belong to the user |
| fs DB grids (path→grid-id) | fs memory DB `contexts` | grid ids verbatim; paths become keys (rebased to the configured root) |
| fs DB tiles (name→tile-id, x/y/w/h, view_*) | fs memory DB `idmap` + `layout` | tile ids verbatim; name joins the dir path to form the key |
| fs tool rows (#258 search wells: params, snapshots) | fs memory DB `idmap`/`layout`/`userdocs`; snapshot child grids into `contexts`/`idmap` | user state, preserved |
| proc DB | proc memory DB | same treatment; pid keys |
| ssh connection rows | server.yaml connection blocks + node DB launcher tiles | `name:` = the old NS segment VERBATIM (chained ids keep resolving); alt/alt_user and x/y/w/h/view_* become the launcher tile's facts in the node DB; params fields become yaml keys; tombstoned rows become a reserved-names list in yaml so a name is never reused |
| mountcache DBs | dropped | disposable by contract; caches re-warm |
| plugin uuids | unchanged everywhere | they live inside every stored qualified reference |

The subtle rows are the ssh ones: a stored reference like
`<ssh-uuid>/<ns>/<plugin>/<id>` must resolve identically when `<ns>`
is a yaml name instead of a DB row. The converter emits the yaml; the
transport resolves names by exact match; `idshape.ValidateSegment`
gates the yaml at load so an invalid or duplicate name refuses to boot.

### 8.3 What is deliberately NOT carried

- Disposable caches (above).
- Nothing else. If the converter finds a table or column it does not
  recognize, it REFUSES to run — an unknown fact must never be
  silently dropped (the guiding rule applied to migration).

### 8.4 Verification: the parity crawl

The oracle is the wire, not the files. A harness starts the OLD binary
and the NEW binary against (a copy of) the same home — old directly,
new through the converter — and walks both:

- `ListPlugins`, then BFS every grid from every root: `GetGrid`,
  `GetTile` per tile, `ReadContent` per content tile,
  `GetTilePreview`, `Search` id-lookups for a sample of ids.
- Every answer must be equal field-for-field, with an explicit
  allowlist for fields that legitimately differ (new fields defaulting
  empty; `stale`; ordering normalized by id).
- Remote/connection subtrees are crawled with a fixture remote in the
  loop (the federation gate's harness), so transit resolution through
  yaml names is proven, not assumed.
- The crawl runs in CI against a synthetic home covering every shape
  (links, clones, tool rows, tombstones, deep chains) — and, before
  the real cutover, against a COPY of each production home. Zero
  diffs or no cutover.

### 8.5 Cutover procedure (per node)

1. Stop the old server (both machines can migrate independently;
   the wire is unchanged, so a v1 node and a v2 node federate).
2. Copy the home aside (this copy is the rollback and the archive).
3. `gridwell convert-v2`; run the parity crawl old-vs-new on this
   very home; require zero diffs.
4. Start the new server; spot-check by hand (the launcher, a deep fs
   grid, a remote descent, a workspace).
5. First write in the new world ends easy rollback; the archived home
   remains readable forever (old binaries stay tagged).

### 8.6 Why this is safe enough for a daily driver

Identity is copied, never derived; the converter refuses the unknown;
the oracle is byte-level wire parity on the real data; rollback is a
directory that was never written to. The riskiest single artifact —
the yaml that now carries NS segments — is validated at load and
exercised by the federation-fixture crawl before cutover.

## 9. Open questions (small, none blocking)

- Whether `Entry.kind` needs `url` at birth (fs serves pages via
  `serves_page`; a bookmarks provider would want real url tiles).
- Preview freshness policy for provider tiles (today: fs thumbnails on
  demand; cache bound needs a number).
- Whether provider grids ever want `create_schemas` beyond menu-entry
  tools (deferred with parameterized instances).
