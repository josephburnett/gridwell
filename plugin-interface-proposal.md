# Gridwell source plugins — interface design (v3, ground-up)

Status: **proposal for discussion.** Supersedes v2. The crystallizing
ideas:

- **A source is another Gridwell DB.** A plugin projects a thing shaped
  like a Gridwell database — grids, tiles, a session. "Remote" is
  incidental: the ssh plugin opens a DB on another machine; a local-file
  plugin opens a DB on disk; fs/proc *synthesize* a DB-shaped projection
  out of non-DB state. The host treats them all identically.
- **Plugins extend the *space*, not the *vocabulary*.** Every projected
  node is one of Gridwell's existing primitives; the client is plugin-blind.
- **Session boundary = DB boundary.** Each DB has exactly one session
  (cookie jar / storage), shared by all its tiles. Session is stored *in*
  the DB, so it syncs like any other data.

## 1. Thesis

A plugin projects an external **space** into Gridwell, and that space is a
Gridwell DB (real or synthesized). The host renders and activates every
node with native machinery; nothing about the plugin reaches the client.

Consequences:

1. **Client is plugin-blind** — it only ever sees `well`/`text`/`url`/
   `shell`. A projected tile is indistinguishable from a native one. This
   *is* the "same consistent experience" requirement, made structural.
2. **Nothing is unrepresentable** — the plugin vocabulary is Gridwell's, so
   "can we represent X?" reduces to "is X a Gridwell primitive?"
3. **The DB is the whole system of record** — grids, tiles, content, *and
   session*. Copy the DB and you copy your logins; sync the DB and your
   session follows.
4. **"Remote" is not a concept in the interface** — a source declares its
   network context and what live capabilities it offers. A local DB and an
   ssh DB run the same code path; they differ only in what they declare.

## 2. The primitive vocabulary

A plugin `Node` is exactly one of these four kinds — the whole surface the
host renders:

| Kind    | Carries                                 | Static face          | Live face                              |
|---------|-----------------------------------------|----------------------|----------------------------------------|
| `well`  | `child` (node to descend into), `frame` | framed child thumbnail | — (descend, don't attach)            |
| `text`  | markdown `body`, scroll `view`          | rendered markdown    | — (editable in place)                  |
| `url`   | `url`, `title`, frozen `preview`        | the frozen jpeg      | **native local WebContentsView**, bound to the source DB's session + network |
| `shell` | frozen `preview`                         | the frozen jpeg      | **live PTY streamed from the source** |

Mappings, no special cases: fs dir → `well`, fs file → `text`; proc
process → `well`, proc `@info` → `text`; remote/foreign tiles → same kind.
No `file-well`/`process-well` kinds — a descendable external thing is a
`well` whose child grid is source-backed.

## 3. Sessions: boundary = DB boundary

This is the load-bearing idea, so it gets its own section.

- **One session per DB.** A Gridwell DB stores its Chromium session
  (cookies + Local Storage / IndexedDB / Service Workers) as data *in the
  DB*. All `url` tiles in that DB share it — tabs stay logged in. This is
  true of your own DB too: the host captures its live session into the main
  DB, so the DB is self-contained.
- **Capture = snapshot-on-flush.** At quiescent points (blur / idle /
  ascent) the host calls Chromium's `session.flushStorageData()` +
  `cookies.flushStore()`, then snapshots the *durable* session (cookies,
  Local Storage, IndexedDB, Service Workers — **not** HTTP/code cache,
  which is regenerable and huge) into the DB. The on-disk Chromium
  partition is a working copy; the DB is the system of record. (A FUSE
  virtual FS could make this continuous instead of snapshot-based, but it
  fights cross-platform portability — macFUSE / WinFsp — so snapshot-on-
  flush is the baseline.)
- **A projected DB brings its session.** Because session is DB content, the
  ssh plugin syncs B's session as part of B's data; a local-file plugin
  reads it from the file. Either way the host gets the source DB's session
  blob through the data plane.
- **Per-DB Electron partition.** The host hydrates a source DB's session
  into a dedicated partition `persist:source-<root>`. The source's `url`
  tiles render in **local, native WebContentsViews bound to that
  partition** — real Chromium, warm real profile (the thing that beat
  Cloudflare). On ascent the host flushes + dehydrates that partition back
  into the source DB.
- **Network follows the source, not the renderer.** The source declares a
  `NetworkContext`: `direct` (use the host's own network — a local DB) or a
  plugin-provided `proxy` (e.g. a SOCKS endpoint the ssh plugin stands up
  over its tunnel — B's network). The host points the partition's
  `setProxy` at it. So a projected `url` is: **local render + source's
  session + source's network**, with no pixel streaming.

This is why `url` leaves the live plane entirely (next section).

## 4. The two planes (and why `url` left the live one)

- **Data plane** — structure + static content + **session**: grids, tiles,
  markdown, frozen previews, framing, and the per-DB session blob. Pull-
  based reconcile, lazy content-addressed blobs.
- **Live plane** — now **shell only**. A `shell` is a live OS **process**
  on the source; a process can't be snapshotted and resumed elsewhere, so
  it must be **streamed** (`OpenShell`). A `url`, by contrast, is a
  stateless renderer over **portable state** (session) — move the state,
  render locally. That's the categorical split:

  > **shell = stream the process. url = move the state, render local.**

  So `caps.live` is a shell concept (is a live PTY attachable now). A
  source that can't host processes (an archived/file DB) simply offers
  frozen `shell` previews. A source that offers no `NetworkContext` /
  session offers frozen `url` previews. Liveness is a *declared* capability,
  never assumed from "remote."

## 5. Identity & storage

- **Identity is opaque.** `source_id` is a plugin-owned token Gridwell
  never interprets, carrying whatever path context the plugin needs to be
  deterministic and COW-correct (fs → path, proc → PID, ssh → the remote
  descent path so the remote forks its own COW spine). `source_key` is the
  per-child dedup key within a parent.
- **Content is opaque + content-addressed.** `ContentRef =
  (blob_ref, media_type, hash, size)`; the host stores bytes in the hash-
  deduped `blobs` table and skips re-fetch when `hash` is unchanged. The
  host never learns what a blob is.
- **Provenance lives on the grid, not the tile.** A projected node is a
  native tile of its kind in a cache grid whose `(source_kind, source_id)`
  names the plugin + parent node. Which plugin owns a tile? Its grid's
  `source_kind`; for a `well`, its child grid's (recoverable even after the
  well is cloned out, since clone shares the source child grid). **No
  `tiles.source_kind` column, no `source-well` kind.**
- **Session storage.** Each DB gets a singleton session record (a selective
  Chromium-partition snapshot, stored as a blob). Keyed per DB; hydrated
  into `persist:source-<root>` at attach, dehydrated at flush.
- **Schema delta.** Keep `grids.(source_kind, source_id)` and
  `tiles.source_key`. **Drop** `tiles.fs_path`/`tiles.pid` and the
  `file-well`/`process-well` kinds. **Add** a generic `tiles.source_ref`
  (a pending `ContentRef.blob_ref`, used until a leaf's blob materializes)
  and a per-DB **session** record. Source grids stay in the ephemeral
  cache, shared by identity, never refcounted.

Clone semantics: cloning a projected **well** out → live link (shares the
source child grid). Cloning a projected **leaf** out → frozen snapshot
(copies the materialized blob). Snapshot for content, link for spaces.

## 6. The protobuf

```protobuf
syntax = "proto3";
package gridwell.plugin.v1;
option go_package = "github.com/josephburnett/gridwell/api/gen/plugin/v1;pluginv1";

// Source is what a plugin implements. Gridwell is the gRPC CLIENT
// (Hashicorp go-plugin: the plugin binary serves this). Host -> plugin
// except OpenShell + Watch.
service Source {
  // identity & lifecycle
  rpc Info(InfoRequest)     returns (InfoResponse);
  rpc Attach(AttachRequest) returns (AttachResponse);  // config -> a source DB (root node + network context)
  rpc Detach(DetachRequest) returns (DetachResponse);

  // data plane: structure + static content
  rpc List(ListRequest)         returns (ListResponse);
  rpc Probe(ProbeRequest)       returns (ProbeResponse);
  rpc ReadBlob(ReadBlobRequest) returns (stream BlobChunk);

  // data plane: the source DB's session (cookies + web storage)
  rpc GetSession(SessionRequest)    returns (stream BlobChunk);   // hydrate a partition from it
  rpc PutSession(stream SessionChunk) returns (PutSessionResponse); // dehydrate back on flush/ascent

  // data plane: mutations (each gated by a Caps bit; carry x,y where relevant)
  rpc Delete(DeleteRequest)   returns (DeleteResponse);
  rpc Move(MoveRequest)       returns (NodeResponse);
  rpc Clone(CloneRequest)     returns (NodeResponse);
  rpc Write(WriteRequest)     returns (NodeResponse);   // edit a text body
  rpc SetView(SetViewRequest) returns (NodeResponse);   // well frame / text scroll writeback

  // data plane: live change notification
  rpc Watch(WatchRequest)     returns (stream Change);

  // live plane: shell only. A url is rendered locally from session +
  // network context, so it needs no stream. A shell is a live process on
  // the source and must be streamed.
  rpc OpenShell(stream ShellInput) returns (stream ShellOutput);
}

enum Kind { KIND_UNSPECIFIED = 0; WELL = 1; TEXT = 2; URL = 3; SHELL = 4; }

message Caps {
  bool delete = 1;
  bool clone  = 2;
  bool move   = 3;
  bool write  = 4;   // text editable
  bool live   = 5;   // SHELL: a live PTY is attachable now
  bool accept = 6;   // WELL: accepts children moved/cloned IN
}

message ContentRef { string blob_ref = 1; string media_type = 2; string hash = 3; int64 size = 4; }
message Frame  { int64 view_x = 1; int64 view_y = 2; double view_zoom = 3; }
message Scroll { int64 x = 1; int64 y = 2; int64 w = 3; int64 h = 4; string mode = 5; }

// Node is Gridwell's Tile with identity made opaque (key/child for ids).
// Only the fields for its kind are set.
message Node {
  string key   = 1;          // dedup within parent -> tile.source_key
  Kind   kind  = 2;
  string label = 3;          // -> tile.alt_text
  int64  x = 4; int64 y = 5; int64 w = 6; int64 h = 7;
  int64  version = 8;
  Caps   caps = 9;

  string     child   = 10;   // WELL: child node id
  Frame      frame   = 11;   // WELL

  ContentRef body      = 12; // TEXT
  Scroll     text_view = 13; // TEXT

  string     url     = 14;   // URL
  string     title   = 15;   // URL
  ContentRef preview = 16;   // URL / SHELL: frozen jpeg
}

// NetworkContext: how the source's url tiles should reach the network.
// direct = use the host's own network (a local DB). proxy = route through
// a plugin-provided endpoint (the ssh plugin stands up a SOCKS proxy over
// its tunnel = the source machine's network).
message NetworkContext {
  oneof via {
    bool          direct = 1;
    ProxyEndpoint proxy  = 2;
  }
}
message ProxyEndpoint { string scheme = 1; string address = 2; } // e.g. "socks5", "127.0.0.1:41xxx"

message InfoRequest {}
message InfoResponse { string kind = 1; string display_name = 2; int64 schema_version = 3; bool watch = 4; }

message AttachRequest  { map<string,string> config = 1; }        // {path}/{pid}/{host,user,key}/{db_file}...
message AttachResponse {
  string root_source_id = 1;
  string label = 2;
  Caps   caps = 3;
  NetworkContext network = 4;   // empty => url tiles are frozen-only
  bool   has_session = 5;       // whether GetSession/PutSession are meaningful
}
message DetachRequest  { string root_source_id = 1; }
message DetachResponse {}

message ListRequest  { string source_id = 1; }
message ListResponse {
  repeated Node nodes = 1;
  bool  authoritative = 2;  // true: sweep absent keys now (fs). false: Probe each, sweep on GONE (proc).
  int64 version       = 3;
}

enum Presence { PRESENCE_UNKNOWN = 0; PRESENCE_PRESENT = 1; PRESENCE_GONE = 2; }
message ProbeRequest  { string source_id = 1; string key = 2; }
message ProbeResponse { Presence presence = 1; }

message ReadBlobRequest { string source_id = 1; string blob_ref = 2; }
message BlobChunk       { bytes data = 1; }

message SessionRequest     { string root_source_id = 1; }
message SessionChunk       { string root_source_id = 1; bytes data = 2; } // first chunk binds the id
message PutSessionResponse {}

message DeleteRequest  { string source_id = 1; string key = 2; int64 version = 3; }
message DeleteResponse { bool settled = 1; } // true: gone now (fs). false: best-effort, reconcile sweeps (proc).

message MoveRequest  { string source_id = 1; string key = 2; int64 version = 3;
                       string dest_source_id = 4; int64 x = 5; int64 y = 6; }
message CloneRequest { string source_id = 1; string key = 2; int64 version = 3;
                       string dest_source_id = 4; int64 x = 5; int64 y = 6; }
message WriteRequest { string source_id = 1; string key = 2; int64 version = 3; bytes data = 4; }
message SetViewRequest { string source_id = 1; string key = 2; int64 version = 3;
                         Frame frame = 4; Scroll scroll = 5; }
message NodeResponse { Node node = 1; }

message WatchRequest { string root_source_id = 1; }
message Change       { string source_id = 1; }

// live plane — shell. Raw PTY bytes both ways. First ShellInput binds the
// tile (data empty); then keystrokes up / terminal output down.
message ShellInput  { string source_id = 1; string key = 2; bytes data = 3; PTYSize resize = 4; }
message ShellOutput { bytes data = 1; }
message PTYSize     { int32 cols = 1; int32 rows = 2; }
```

## 7. How the host uses it

- **Create a source well:** `CreateSourceWell(kind, config)` → `Attach` →
  store a `well` whose child grid carries `(source_kind, root_source_id)`,
  and register the source's `NetworkContext` + session under `root`.
- **Descend / reconcile:** `GetGrid` on a source grid → `List` → upsert one
  native tile per `Node` (dedup by `key`), stamp label/placement, link
  `child`, stash content refs. Sweep: `authoritative` ⇒ now; else `Probe`
  ⇒ on `GONE`, never on a failed read.
- **Read content:** first read of a leaf → hash miss → `ReadBlob` → cache →
  serve via native `GetBlob`. Free thereafter; shared by hash across clones.
- **Render a `url`:** hydrate `persist:source-<root>` from `GetSession`,
  mount a native local `WebContentsView` bound to that partition with
  `setProxy(NetworkContext)`. On ascent: flush + `PutSession`.
- **Attach a `shell`:** `OpenShell`; bridge the client terminal ⇄ plugin ⇄
  source PTY. On ascent the source captures the last frame as `preview`.
- **Mutate:** native `DeleteTile`/`MoveTile`/`CloneTile`/`UpdateText`/
  `SetWellView` forward to `Delete`/`Move`/`Clone`/`Write`/`SetView`, gated
  by `Caps`; the source enforces versioning.
- **Watch:** optional; source changes → local reconcile + store events.

Dispatch is a registry: `Store.sources map[string]Source` keyed by
`source_kind`. The host applies the same session capture to its *own* DB,
so the main DB is self-contained too.

## 8. Two worked sources (same code path)

- **Local-DB plugin** (`config:{db_file:"/archive/2024.gwdb"}`): `Attach`
  opens the file, returns its root + `network:direct` + `has_session:true`.
  `List` reads its grids/tiles. `url` tiles render locally with that DB's
  session over the host's own network. No process host ⇒ `shell` tiles are
  frozen (`caps.live:false`).
- **SSH plugin** (`config:{host,user,key}`): `Attach` dials SSH, tunnels to
  B's `gridwell` gRPC, returns B's root + `network:proxy(socks5 over the
  tunnel)` + `has_session:true`. `List` maps B's tiles → `Node`s.
  Mutations decode `source_id` → B's `Path` and forward to B's RPCs (B
  enforces COW + versioning). `url` renders locally with B's session over
  B's network; `shell` streams B's live PTY (`caps.live:true`). `Watch`
  bridges B's `Subscribe`.

Same host code; they differ only in declared `NetworkContext` and whether a
live PTY is available. Recursion composes (descend B's fs-well → B's
filesystem through B's reconciler; ssh B→C → descend further).

## 9. Version & durability contract

- **Load-time:** go-plugin handshake (magic cookie + `ProtocolVersion`)
  rejects incompatible binaries; `VersionedPlugins` lets old/new coexist.
- **Contract semantics:** proto3 makes wire evolution free; `schema_version`
  is for *semantic* breaks the wire can't catch.
- **Durable tiles outliving a plugin version:** storage is **version-
  agnostic by construction** — only opaque tokens (`source_id`,
  `source_key`, `source_ref`) and hash-addressed blobs, which the host
  never interprets and so can't corrupt. Host persists/returns them
  version-blind; **plugin** keeps its own token format backward-compatible
  or migrates it.
- **Plugin absent this session:** *unknown ≠ gone; a failed read is never a
  deletion.* The well renders **dormant** (greyed, not swept) and
  re-resolves when the plugin returns.
- **Not plugin versioning:** `Node.version`/`tiles.version` is per-record
  optimistic concurrency (for ssh, the source's version, relayed).

## 10. Open decisions

1. **Session concurrency model.** Whole-DB session is **checkout/checkin**
   (last-writer-wins; no merge). Fine for single-tenant one-place-at-a-time;
   under concurrent use of the same source DB, one side's session writes get
   clobbered. Accept checkout/checkin, or invest in finer-grained (per-
   cookie / per-origin) sync later?
2. **Session security.** The DB now holds live web credentials and syncs
   them. Needs encryption at rest + in transit and a policy for which
   machines a session replicates to. Required before any sync ships.
3. **Selective capture set.** Confirm capture = cookies + Local Storage +
   IndexedDB + Service Workers; exclude HTTP/code cache. Right cut for
   size vs. fidelity?
4. **Live plane in v1.** Frozen-preview-only first (no `OpenShell`, no live
   `url` render) is a coherent v1. Then live `url` (session + tunnel +
   local render) and `OpenShell` can land independently.
5. **First build step.** Re-implement fs/proc behind the new `Source`
   interface **in-process** first (proves it against the existing test
   suite, zero IPC), then lift out via go-plugin; the local-DB and ssh
   plugins validate the out-of-process + session paths. Recommended.
```
