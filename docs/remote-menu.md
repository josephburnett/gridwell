# The remote menu: "when I descend into a node, I am there"

Options doc (2026-08-16, audited against the code the same day). The
ask: descending a connection should land on the remote's HOME — its
first plugin's root grid — and while a pane is inside a remote node,
the + menu should show THAT node's plugins, exactly as if the client
were connected to it directly. The menu may therefore differ per pane.
What falls out: a menu belongs to a node, so its creations cannot cross
into another node's grids, and menu contents become a fact plugins/nodes
surface rather than a boot-time constant. Nothing here is decided.

## Why this is an alignment, not a rework

The standing owner decision (2026-07-19) already says it for the LOCAL
node: *the node grid is the federation surface, not the landing page* —
you boot into home, plugins live in the + menu. The remote experience
today is the inconsistency: a connection lands you on the remote's node
grid (the synthetic plugin list) with your LOCAL menu. The ask makes
every node behave like the local one.

## Where the machinery stands (the audit)

- **The menu is one boot-time list.** `paletteItems` = `a.plugins`
  (the boot `ListPlugins`, local node, immutable) + primitives gated by
  the per-grid `writable` stamp; shells gated by the boot-time local
  `shells_disabled`. One footgun to fix in passing: the layout's
  `TopRow` reads `a.plugins` independently of the item list — two
  readers of one fact.
- **The per-grid precedent already federates.** `Grid.writable`,
  `scratch_grid_id`, `create_schemas` are stamped by the serving node
  and chained one segment per hop. Concretely: a remote pane ALREADY
  gets the remote's scratch grid (ephemeral url visits land remotely)
  and the remote's creation schemas (the picker form works over a
  chain). This is the pattern the menu needs at node granularity.
- **`ListPlugins` is the one un-routed RPC.** The remote's node export
  SERVES it (full PluginInfo: roots, instance grids, glyphs, InfoError,
  shells_disabled) — but the remote plugin never calls it; connections
  learn only `Info.root_grid_id` (the remote node grid) at params
  commit, which is exactly why you land on the plugin list.
- **The client cannot name a pane's node.** Nothing says "grid X is
  served by node N" or gives N's chain prefix. `UUIDOf(anchor)` is the
  local transit plugin; `NamespaceOf` is the owning-plugin chain, one
  segment too deep.
- **Cross-node hazards in today's menu flows** (verified sites):
  a plugin-swatch drop writes `RootGridID` verbatim (a remote list's ids
  would be qualified for the WRONG receiver unless re-qualified per
  hop); an unconfigured plugin well stores a bare `configure_plugin_id`
  uuid that only resolves against the local registry; every
  health/enterable/parameterized decision looks up `pluginByUUID`
  against the local list; and the palette's plugin arm bypasses the
  cross-namespace `DecideDrop` machinery entirely (no guard exists).

## Decision 1 — the landing

**L1. A connection's child IS the remote home** (the ask). The remote
plugin resolves it at params commit / root fetch: call the remote's
`ListPlugins` and apply the SAME `HomeGrid` rule the local boot uses
(first plugin with a root; node grid as fallback), store that as the
connection's child. Descending a connection then lands where a direct
client boots. The node grid remains addressable (`<chain>/0`) — it just
stops being where you land, same as locally.
*Cost:* small (one call swap in the root fetch + a re-learn for
existing connections — the stored root is a CACHE, so a one-shot clear
or a "stored root is the node grid → re-resolve" rule during a dated
window). *Risk:* a remote with a broken first plugin lands you on a
dead grid — HomeGrid's fallback-past-broken-plugins rule must ride
along (it needs the remote's InfoError bits, which ListPlugins carries).

**L2. Keep the node-grid child; the client auto-descends to home.**
Client-side anchor gymnastics duplicating HomeGrid at the wrong layer.
Mentioned for completeness; it fights the grain.

## Decision 2 — where the menu comes from

**M1. Route `ListPlugins` by namespace** (the parity answer). Add
`namespace` to the request: `""` = this node (today's behavior,
unchanged); otherwise the server peels the first segment and forwards
through the chain — the remote plugin forwards to its connection's
export — and each hop RE-QUALIFIES the response's ids (roots, instance
grids, scratch) with its prefix, exactly like every other transit
response. `shells_disabled` and per-plugin `InfoError` ride verbatim;
node-local fields (content_token, node view) are zeroed on forward —
they answer only for the node you ask.
The client keys a MENU CONTEXT per pane — `{namespace, plugins,
shellsDisabled}` — fetched when a pane enters a node (portal descent is
the natural prefetch point) and cached per namespace. `paletteItems`
and `TopRow` read the one context (fixing the desync footgun).
*Pros:* exact "as if connected directly" parity — same shape, same
capabilities, same picker, same glyphs; the ids arrive pre-qualified
for this receiver, so the swatch flows need no client-side id surgery.
*Cons:* one new request field + hop forwarding; a per-namespace cache
with a staleness story (a snapshot per visit is honest; live remote
plugin-health stays mount-level).
*Offline:* the mountcache wraps the remote plugin's client — teach it
to cache the ListPlugins forward and the remote MENU works offline,
same as the remote's grids.

**M2. Derive the menu from the node grid.** `GetGrid(<chain>/0)` is
already a federated, mountcached plugin list — one tile per plugin with
label, root child, and framing. The top row could be those tiles.
*Pros:* zero new wire surface; everything stays grid-shaped.
*Cons:* the tiles don't carry what the menu needs beyond entry —
parameterized-ness (instance grid), shells_disabled, glyph, health —
so either the menu degrades (no picker, no shell gating, globe glyphs)
or node-grid tiles get stuffed with plugin capabilities, which is the
exact tile-as-capability-carrier smell the declaration work just
removed. Honest as a DEGRADED fallback when M1's fetch fails, not as
the mechanism.

**M3. Stamp the node's menu onto every Grid.** The per-grid precedent
taken to node scope: every GetGrid response carries the serving node's
plugin list. *Pros:* federates by construction, no new request. *Cons:*
a node-level fact copied onto every grid row — the one-fact-one-owner
charter says no; response bloat; cache invalidation per grid for a fact
that changes per node.

Lean: **M1**, with M2 as the offline/failure degradation if the
mountcache route proves insufficient.

## The missing fact: which node serves this grid

M1 needs the pane's NODE CHAIN (the transit prefix up to the serving
node). Two ways:

- **Stamp it**: `Grid.node_ns` — leaf nodes stamp `""`, each transit
  hop prepends its segment (identical mechanics to `scratch_grid_id`).
  One new wire field, one owner, answers for any depth of chaining.
- **Derive it syntactically**: strip the trailing grid segment (always
  numeric) and the plugin segment (never numeric — the leading-letter
  id rule is load-bearing here again). Zero wire change, but it encodes
  the anchor's shape in the client and treats the node grid's
  `<chain>/0` as a special case. Cute; brittle.

Lean: stamp it. The id-shape trick is exactly the kind of cleverness
that reads fine today and bites the first plugin that nests grids
differently.

## Decision 3 — the gesture rules (the fallout)

- **Primitive creates are same-node only.** The menu context carries
  its namespace; `commitTemplateDrop` refuses a drop whose target
  grid's node differs, with a visible notice (never a silent no-op).
  Server-side nothing changes — a create targets one grid and routes;
  the rule is the product model, enforced where the gesture lives, and
  pinned by a spec.
- **Plugin-swatch drags: two candidate rules.** (a) Same-node only —
  the purist reading of "the menu is that node's". (b) Allow cross-node
  as a LINK — dropping a remote node's plugin into a local grid mints
  an exit-well link to it, which is exactly what a mount already is,
  and left-drag-links is standing law; with M1's pre-qualified ids this
  works without surgery. Lean (b) for rooted plugins. For PARAMETERIZED
  plugins the unconfigured-well drop (`configure_plugin_id`, a bare
  uuid) stays same-node until that field learns to carry a chain — a
  small, honest restriction.
- **Ephemeral visits already behave** (scratch is per-grid): a url
  visit from a remote pane lands in the remote's scratch. One side
  fix while there: the url-suggestion candidate filter compares first
  segments, so a remote pane over-matches every grid behind its mount.
- **Shells**: the menu offers the shell primitive from the CONTEXT's
  `shells_disabled` (today it is the local flag even inside a remote
  pane — wrong in both directions). Live ATTACH capability remains the
  host bridge's (`caps.LiveShell`) — that part is about this machine's
  PTY relay, not the node.
- **Everything else is already node-clean**: descents, links, clones,
  the deep copy, the picker's adopt flow — they ride qualified ids and
  the existing cross-namespace machinery.

## Smaller consequences, called out

- **Health tints**: a remote plugin's health arrives as the routed
  response's `InfoError` snapshot — honest but not live; live health
  stays per-mount (the existing sticky notice). Do not fake liveness.
- **The bar/launcher**: crumbs and portal ascent are untouched (frames
  already own namespace crossings). The launcher start page stays the
  LOCAL node's.
- **Boot**: `a.plugins` becomes just the `""` context; nothing about
  boot, home, or the content token changes.
- **Tests**: palette specs grow a remote-context case; a federation
  spec pins descend-connection → remote home + remote menu → create
  text remotely; a spec pins the cross-node refusal; web-noshells picks
  up the per-context shell gate.

## Status (2026-08-16): IMPLEMENTED — all five recommendations landed

L1 (connection child = remote HomeGrid, node-grid roots re-resolve once
per process), M1 (namespace-routed ListPlugins, per-hop requalification
via rpc.TransitQualifyPluginList, node-local fields zeroed, mountcached
per namespace), Grid.node_ns stamped at both hop kinds, the per-pane
menu context (paletteItems + TopRow from one list; ctx-scoped shell
gating), and the gesture rules (same-node primitives with a visible
refusal; cross-node rooted-plugin links allowed; parameterized drops
same-node). Pinned by: the chain seam test (internal/server/
sshhost_seam_test.go), the federation gates (spawn/direct/partition all
land on remote homes; direct exercises the routed menu), and
web-remote-menu.spec.ts — two real nodes over direct connect: descend →
remote home, + menu = remote plugins, markdown from that menu lands on
the far node, cross-node drop refuses visibly. Parity gate green
(in-process plugins serve remote menus identically).

## Recommendation, in one line each

1. L1 — the connection's child becomes the remote HOME (HomeGrid rule,
   re-learned once for existing connections).
2. M1 — namespace-routed ListPlugins with per-hop re-qualification;
   per-pane menu context; mountcache caches it for offline; M2 as the
   degraded fallback only.
3. Stamp `Grid.node_ns` as the one owner of "which node serves this
   grid".
4. Same-node primitive creates (visible refusal); cross-node plugin
   links allowed for rooted plugins; parameterized drops same-node for
   now.
5. Context-scoped shell gating; attach capability stays host-local.
