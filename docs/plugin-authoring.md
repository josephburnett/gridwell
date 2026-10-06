# Writing a plugin

A plugin is a separate program that serves the `plugin.v1` gRPC service
(`api/plugin/v1/plugin.proto`); `compose.LoadPlugin` spawns its binary, and
that is the only way a node reaches one. It answers in its
own stable string keys — a path, a message id, a todo id — and never sees
ids, layout, or a database. It keeps no node fact; what it may keep is its own
memory of its source, in the directory the node hands it (`state_dir`). The node mints ids against your keys, stores
placement and framing, and serves the full Gridwell surface on your behalf.
The host never switches on which plugin you are; every behavior you get
comes from something you declared on the wire.

The shipped plugins live in their own repository,
`github.com/josephburnett/gridwell-plugins` (`fs`, `proc`, `pages`, `gitlab`,
`gmail`, `hey`), and use the same door as anyone else's: each is its own Go
module importing only the api. `pages` is the smallest one, and the example
for serving web content: no config, no state, and every page generated in the
plugin. `fs` is the fullest surface. `hey` and `gmail` are the examples for
links, and `fs` lists its symlinks as links.

This doc is the door. `docs/plugin-standard.md` is what a good plugin does at
it, rule by rule, with a checklist to tick before you ship.

## The contract

- **Module**: depend on `github.com/josephburnett/gridwell/api` only. Its
  packages: `gen/plugin/v1` (implement `PluginServer`), `compose` (the
  handshake, and how a node loads you), `gwerr` (the error vocabulary). The
  guest-side helper for your main is a second small module in the plugins
  repository, `github.com/josephburnett/gridwell-plugins/guest`. A plugin that
  remembers its source takes the cache file, the shared walks, the `Watch`
  fan-out and the calendar placement from a third,
  `github.com/josephburnett/gridwell-plugins/memo`. Not Go? The service is
  plain gRPC behind hashicorp go-plugin's handshake.
- **Binary**: `gridwell-plugin-<kind>` beside the `gridwell` binary, on
  `GRIDWELL_PLUGIN_DIR`, on PATH, or named by `binary:` in `server.yaml`.
  Register with a `plugins:` entry (`kind`, optional `label`, your `config`
  keys). The first serve mints the entry's id.
- **Main**: `guest.Main(YourFromConfig)`, or `guest.Serve(yourImpl)`. Config
  arrives as a JSON map in `GRIDWELL_PLUGIN_CONFIG` (`guest.Config()`): your
  keys plus `uuid`, `kind` and `state_dir`.
- **State**: `state_dir` is a directory of your own, `<home>/plugins/<id>`,
  minted 0700 before you start. Hold no node facts — no ids, no layout, no
  database. Your own memory of your source is welcome there, under `cache.db`'s
  contract: disposable, safe for the user to delete, rewarmed by use. Nothing
  deletes it for you, not even removing your entry from `server.yaml`. Write
  atomically (temp file, rename), and come up correctly when it is empty.
- **Keys are forever**: a key names the same thing for the life of the
  plugin. Changing your key scheme orphans every stored reference.
- **Unimplemented is fine**: a minimal plugin is `Info` + `List` +
  `ReadContent`. Search = no results, ServeContent = 404, Watch = the
  node learns of changes only when it next lists, WriteContent =
  read-only, GetPreview = no thumbnail, Delete = refused.
- **Errors**: transport-shaped failures (Unavailable, DeadlineExceeded)
  mean "not right now" and the node serves what it has.
  Coded answers mean what they say. Never answer NotFound for something
  that exists but is unreachable.

## Info

`InfoResponse` is your one handshake: `kind`, `display_name`, `glyph`
(`folder`/`process`/`well`/empty), `watch`, `writable`, and `menu_entries`
(your collections).

**Refuse a config you cannot serve.** If the source your config names is not
there — no such directory, no CLI, a token that does not load — answer `Info`
with an error whose message is one plain sentence saying why. The node shows
your plugin broken with that sentence and no entries, and asks again until
the fix lands, with no restart. Latch the first pass: once `Info` has
answered, a source that goes away is weather — a token revoked or a CLI
signed out included — and your reads answer `Unavailable`, or from memory
with `unreachable` set (see Listings), never a verdict.

**Declare one `menu_entries` row per collection**, each naming its context
key. That is how your plugin is reached: each entry becomes a + menu swatch,
and the node stamps them onto every grid it serves for you. You have no
landing grid and no primary collection — a plugin is not a place, it
contributes doorways — so a mail plugin declares "inbox", "reply later" and
"set aside", all three the same way. Do not invent a wrapper context whose
entries are wells onto the others: that is a grid the user did not make and
cannot arrange.

An entry's `label` is optional. Leave it empty when you serve one collection
and the swatch reads as the configured instance; set it and the swatch reads
"<label> (<instance>)". Declaring no entries at all is legal: your plugin is
listed, contributes nothing to the menu, and is healthy — not an error.

`root_context` is RETIRED. Leave it empty. The node reads it in one case, so
a binary built against the older proto keeps presenting: a plugin that
answers a `root_context` and no `menu_entries` gets one derived entry onto
it, wearing your own name and face. Declare both and the entries win.

## Listings

`List` enumerates one context. Say whether it is `authoritative`: a key
absent from an authoritative listing is gone; absent from a non-authoritative
one means "not seen this pass", and the node keeps the entry until `Probe`
answers GONE. `ProbeRequest.context` names the context the node is asking
about: answer whether the key is still in THAT context, since a key listed in
several (a mail thread in a box and in everything) can leave one and stay in
another. An empty context is a node from before contexts: answer for the
plugin as a whole. When a refresh fails but your memory can answer, answer
from memory and set `unreachable` to why, in a plain sentence ("the mail
server is not answering"); leave it empty when the listing is live. The node
serves the entries, shows the sentence as the source's health, and retires
nothing on a memory answer, so `authoritative` is ignored while it is set.
The next answer with it empty brings the source back up. A `placement_hint` is a preference for an entry's first
placement only: a hint onto an occupied cell takes the first free rect of its
size below it in the same column. An
entry's `status_detail` is one emoji about its state — ✅ done, ● unread,
★ starred — sent only when there is something to notice, and empty for the
normal state. The client draws it before the tile's name wherever the name is
drawn, so a long name clips and the emoji never does: a note on the name,
never a second name. An entry with `serves_page` presents its `ServeContent` HTML on
descent, sandboxed by the node: scripts run, and on the desktop a
`target=_blank` link opens its address in a new pane below. It is a `url` entry, and only a `url` entry:
serving a page is what that entry's address is, and the node refuses the
declaration on any other kind. Leave `url_string` empty — the node derives the
address when the page is opened — and serve a document body from a `text` entry
instead if you want one, which is a second entry, not a second face on this
one. The page is served at a directory URL, so an ordinary relative URL inside
it comes back as `ServeContent` on the same key with that name as the
`subpath`. Past its address, a served page is a url tile like any other: the
node keeps its screenshot, its standing freeze and its zoom, and you store
none of them.

## Links

One thing listed in several of your collections — a mail thread in a box and
in a list of everything — should be one tile, with the others pointing at it.
Give it one home entry, and in every other collection list an entry with
`link_target` naming that home by your own words: its context and key. The
node draws such an entry as a link: dashed, with its own key, label,
`status_detail` and placement, and its content, face and page read through
the target, so it reads dead once the target's context says the target is
gone. A copy of it, or a drag out of your grid, is another link to the
target, so a reference survives the entry moving between collections. Keep
`kind` the target's kind, and keep the content facts too if a node older than
the field must still present it; a new node reads none of them on a link. The
node mints nothing for the target until the user touches it. A well has no
link variant (its `child_context` already says where it opens), and an entry
may not link to itself.

When the target changes, announce the target's context on `Watch`: the node
passes the change on to every context whose listing links into it, and it
adds those target contexts to your watch scope while a linking grid is shown.

## Changes

Implement `Watch` if your source changes without the user: a background
sync, a new mail, a file edited elsewhere, and set `InfoResponse.watch`. The
node holds one stream open while some client shows one of your grids, on
this node or on any node that reaches it, so hold it open until its context
ends. `WatchRequest.contexts` is its scope: the context keys shown right
now, a file shown in a pane counting through its context. Watch those. When
the set changes the node opens a stream with the new set and ends the old one
once the new one is open, so two may overlap; while nothing of yours is shown
it holds none, so a source you watch by the
path, such as a directory under OS change notifications, costs only what is
on screen. Empty contexts means a node from before scopes: watch what you
judge cheap. A feed that is account-wide rather than per context (a mailbox,
a to-do list) may ignore the scope and watch as it always would. Send the header
(`stream.SendHeader`) as soon as you accept the stream: that is the moment
the node counts it open, and without it the moment is your first change.
Once a stream is open the node lists each context it adds, the whole scope
after a drop, and tells the clients to list again any whose answer moved,
because a change sent into no stream reaches nobody. So a scope change costs
you one `List` per added context.
Send a change when what you would answer now differs from what you last
could have:

- `ContextChanged{context}`: that context's `List` would answer
  differently — an entry arrived, left, moved, or changed its label, status
  or stamp. The node lists the context and tells the clients showing it only
  if the answer moved, so a `ContextChanged` that moves nothing costs you one
  `List` and the user nothing; the listing that follows retires a gone key by
  the usual rule, so list honestly.
- `EntryChanged{context, entry}`: one entry's content changed in place — a
  file's bytes written, a picture redrawn — with the entry re-read, exactly
  as `List` would answer it now. A plugin row carries no version, so this is
  the only way new bytes reach a body a client already holds: the node tells
  every client showing the entry, and each reads the body again. Send it for
  the entry that owns the content; a link to it reads through it.
- `EntryRemoved` is retired: send `ContextChanged`. The node still reads it
  as one, for a binary built before.

Send one when something changed, never one per poll, and collapse a burst
into one per context and one per entry. A change nobody is looking at costs
no listing, and naming a context the node has never listed is harmless.

`InfoResponse.watch` is the declaration. Leave it unset and the node never
asks, which is healthy. Set it and answer Unimplemented and the node tells
the user your live updates are off for the process's life: the handshake said
otherwise. A stream that ends or fails transport-shaped is re-opened
with backoff while you are up; do not end a stream on purpose. Any other coded
error is shown to the user as live updates off, in your words, until a
re-opened stream is open. Neither makes your source dark: your listings still
answer, so a limit you hit (`ResourceExhausted`) costs live updates, not the
source.

## Previews

A tile's face is the screenshot the node took when the user last left it
live, exactly as for a page on the Internet. Before the first visit there is
none, and `GetPreview` is how you supply one: set a positive `preview_stamp`
on the entry, a generation number such as a file's mtime, and the node asks
`GetPreview` for that key, and asks again when the stamp changes. Leave the
stamp 0 and nothing asks. Once a screenshot exists it is the face, and your
picture is not asked for again.

## Deletes

`Delete` is yours to define: remove the thing, or transform it (the gitlab
plugin marks the todo done and the tile stays). The node does not assume. After
your `Delete` succeeds it asks `Probe` for the same key and retires the id it
minted only on a definitive GONE — a minted row retires only when its source
says the key is gone. So answer `Probe` honestly and the user's placement and
every link to the tile survive a delete that keeps the thing.

## Gates

`test/boundary` fails the build if any gridwell package — test files included
— imports a plugin implementation or names the plugins repository in a go.mod,
or if the api gains a dependency outside its budget. `make check` builds every
module standalone and spawns the real shipped binaries through the production
loader. `make check-connections` spawns the real binaries through a
real ssh tunnel.
