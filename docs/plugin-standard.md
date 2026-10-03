# The plugin standard

Every shipped plugin is brought to this standard (Joe, 2026-10-02), and a
third-party plugin should meet it too. `docs/plugin-authoring.md` says how the
door works; this says what a good plugin does at it.

A plugin serves keys and content; the node owns ids and layout. It holds no
node fact. It answers in its own stable keys, keeps its own memory of its
source in `state_dir` under `cache.db`'s contract, and everything else it
gets from the node comes from what it declared on the wire. The rules below
apply the promises in `CLAUDE.md` one hop further, inside the plugin.

Each rule has the rule, the promise or decision it serves, an example, the
test that pins it, and where the six shipped plugins stand today. Code paths
are in the plugins repository (`github.com/josephburnett/gridwell-plugins`)
at main `7a8f614`, unless they start with `internal/`. Where no plugin does it
right yet, the example is a sketch and says so.

## 1. Declare what you implement

**Rule.** Declare every capability you implement in `Info` (`watch`), and test
the declaration.

**Why.** "A tile behaves the same wherever its content comes from." The node
never switches on a plugin kind; every behavior rides a wire declaration
(CLAUDE.md, Node). A `Watch` the plugin never declares is never opened, so its
grids go stale while the code that would have told the node sits unused.

**Example.** `fs/plugin/plugin.go:118-121`:

```go
// The one collection this plugin serves: the configured tree. It
// declares no label, so the swatch reads as the configured instance.
resp.MenuEntries = []*pluginv1.MenuEntry{{Id: ".", Context: "."}}
resp.Watch = true
```

**Test.** `fs/plugin/watch_test.go:TestInfoDeclaresWatch`: `Info` answers
`watch` set. Every plugin with a `Watch` method carries one.

**Today.** Meet: fs, gmail, hey; pages and proc implement no `Watch` and
declare none. Fail: gitlab implements `Watch` (`gitlab/plugin/watch.go`) and
its `Info` never declares it (`gitlab/plugin/plugin.go:278-286`).

## 2. Refuse a config you cannot serve, then latch

**Rule.** Refuse `Info` with a plain sentence for any config you cannot serve,
and latch after the first pass.

**Why.** "A plugin that cannot serve the source its config declares refuses
`Info` with the reason, and is broken with that sentence and no entries until
the fix lands, which needs no restart" (2026-09-30). The latch is what makes
a later outage weather (rule 3) rather than a config verdict.

**Example.** `fs/plugin/plugin.go:112-117`:

```go
if !p.served.Load() {
	if err := readableDir(p.root); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	p.served.Store(true)
}
```

**Test.** `fs/plugin/plugin_test.go:TestInfoRefusesARootItCannotServe` on the
plugin side; `internal/server/info_refusal_seam_test.go` across the seam
(broken with the reason, healthy once fixed, no restart).

**Today.** Meet: fs, proc, hey. Partial: gitlab and gmail check only that
their config is present and their files load, in `FromConfig`; neither asks
the source whether the token works before answering `Info`.

## 3. After Info, unreachable is Unavailable

**Rule.** Once `Info` has passed, an unreachable source is `Unavailable`,
never a verdict.

**Why.** "Dark is not dead" (CLAUDE.md, Experience). A transport-shaped code
means "not right now", and the node serves its rows and marks the source dark
(`gwerr.IsTransport`, `internal/pluginhost/adapter.go:synthesize`). A coded
answer is a verdict the user is shown, and a swallowed failure is an empty
grid that looks true.

**Example.** `fs/plugin/plugin.go:174-180`:

```go
entries, readErr := fssource.Read(dir)
if readErr != nil {
	if errors.Is(readErr, iofs.ErrNotExist) {
		return &pluginv1.ListResponse{Authoritative: true, SourceLabel: dir}, nil
	}
	return nil, status.Errorf(codes.Unavailable, "fs plugin: read %s: %v", dir, readErr)
}
```

**Test.** To write, per plugin: pass `Info`, then make the source unreachable
(remove the CLI, stop the fake server, deny the directory) and assert `List`
answers `codes.Unavailable`, not an empty listing and not `FailedPrecondition`.

**Today.** Meet: fs, pages; gitlab and gmail map a network failure to
`Unavailable` (`gitlab/gitlabapi/client.go`, `gmail/gmailapi/client.go`) and
keep `PermissionDenied` for a refused token. Fail: hey answers a CLI it
cannot run with `FailedPrecondition` (`hey/heycli/heycli.go:349`), so a CLI
that goes missing after `Info` passed reads as a verdict. proc swallows a
failed children read and answers an empty listing
(`proc/plugin/plugin.go:168-176`).

## 4. Authoritative when complete

**Rule.** Mark a listing authoritative whenever it enumerates the context
completely, and only then.

**Why.** "Absence is never inferred from silence" (`docs/freshness.md`). An
authoritative listing lets the node retire a gone key in one pass; a
non-authoritative one costs a `Probe` per missing key, and a wrongly
authoritative one retires a row the user placed.

**Example.** `pages/plugin/plugin.go:65-71`:

```go
// List enumerates the site. It is authoritative — the site is fixed, so a key
// absent from this listing is gone rather than unseen — and every context
// other than the root answers an empty listing, because there is only one.
func (p *Plugin) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	resp := &pluginv1.ListResponse{Authoritative: true, SourceLabel: displayName}
	if req.Context != site.RootContext {
		return resp, nil
	}
```

**Test.** `pages/plugin/plugin_test.go:TestAnyOtherContextIsEmptyAndAuthoritative`.
For a source that is sometimes complete, the test shape is a pair: a whole
read answers authoritative, a capped or partial read does not.

**Today.** Meet: fs, pages; proc and gitlab are never complete by design.
Fail: gmail answers non-authoritative even when a read stayed under
`max_messages`. Open for hey: a whole box walk is complete, and the
2026-10-02 link decision says "listings stay non-authoritative". The outcome
is the same either way, since `Probe` answers GONE for the box; Joe decides
which wording stands.

## 5. One home per thing

**Rule.** A key lives in one context; every other context that shows it
lists a link to it.

**Why.** "A plugin entry may be a link to another of its entries" and "one
thing listed in many collections is one tile" (2026-10-02). Two entries with
one key are two tiles: placement, a clone and a link each attach to one of
them, and the user sees one thing twice.

**Example.** `hey/mail/entries.go:37-46`:

```go
// BoxEntries derives one box's grid: CollectionEntries, each a link to the
// same thread in everything. The content facts stay, so a node that predates
// link_target still shows the box as pages of its own.
func BoxEntries(threads []Thread) []*pluginv1.Entry {
	out := CollectionEntries(threads)
	for _, e := range out {
		e.LinkTarget = &pluginv1.EntryRef{Context: EverythingContext, Key: e.Key}
	}
	return out
}
```

**Test.** `hey/plugin/everything_test.go:TestEverythingIsEveryThreadOnceAndBoxesLinkToIt`
on the plugin side; `internal/server/link_entry_seam_test.go` across the
seam.

**Today.** Meet: fs, gitlab, pages, proc (each key in one context), hey.
Fail: gmail lists one message in inbox and in starred as two plain entries
(`gmail/plugin/plugin_test.go:TestAMessageKeepsItsKeyAcrossCollections`).

## 6. Probe answers for the context asked

**Rule.** Answer `Probe` for the key in the context the request names
(`ProbeRequest.context`), and for the plugin as a whole when it names none.

**Why.** "`Probe` answers for the context it names, so leaving one collection
is not being gone" (2026-10-02). A thread that left the Imbox is usually in
Reply Later; GONE for the plugin would retire its tile everywhere.

**Example.** `hey/plugin/plugin.go:589-598`:

```go
default:
	if _, ok := mail.LookupCollection(req.Context); !ok {
		return presence(pluginv1.ProbeResponse_PRESENCE_GONE), nil // a context this plugin never lists
	}
	switch p.mem.InBox(req.Context, id) {
	case mail.Present:
		return presence(pluginv1.ProbeResponse_PRESENCE_PRESENT), nil
	case mail.Gone:
		return presence(pluginv1.ProbeResponse_PRESENCE_GONE), nil
	}
```

**Test.** `hey/plugin/everything_test.go:TestProbeAnswersForTheContextAsked`;
`internal/server/link_entry_seam_test.go:TestTheSweepProbesTheContextItSweeps`
across the seam.

**Today.** Meet: hey; fs, pages and proc, whose keys live in one context, so
the plugin-wide answer is the context's. Fail: gmail answers for the plugin
as a whole while one message is listed in two contexts.

## 7. Memory answers when a refresh fails

**Rule.** When a refresh fails, serve the warm memory and report the failure
as health; never fail a read the memory can answer.

**Why.** "Nothing is done for nobody" has a twin at the node: "A remembered
grid serves FIRST … Nothing on the answer says it is a memory — that is the
source's health" (`docs/freshness.md`, layer 4, `internal/sourcecache/`).
A failed `List` costs the user every entry the node has not minted a row for,
which is most of a mailbox. Decided for plugins 2026-10-02 (CLAUDE.md, Node).

**Example.** A sketch; no plugin does this yet. The shape is hey's `sync`
with the warm arm changed:

```go
if warm {
	p.reportHealth(c.Key, last) // see below; nil clears it
	return nil                  // memory answers; the failure is not this read's
}
```

**Test.** To write, per plugin: warm the memory with one walk, make the next
walk fail, and assert `List` answers the remembered entries with no error,
and that the failure reached the health channel. It inverts
`hey/plugin/plugin_test.go:TestAWarmReadAnswersTheLastFailedWalk` and
`gitlab/plugin/plugin_test.go:TestAWarmReadAnswersTheLastWalksFailure`, which
pin today's behavior.

**Today.** Nobody. gitlab and hey answer a warm read with the last walk's
error; gmail likewise. Open: the wire has no health field on an answered
`List`. The one health a plugin speaks while its listings answer is a `Watch`
stream ending with a coded error, which the node shows as "live updates
off" in the plugin's words. Either that is the channel, or the wire gains
one; that needs a decision before this rule can be met.

## 8. Work only while watched

**Rule.** Walk, glance or follow a feed only to answer a call, or while the
node holds a `Watch` whose scope includes the context.

**Why.** "Nothing is done for nobody: no capture, fetch or walk runs unless
something on screen or in the store needs its result," and "a clock is for a
source that cannot tell, and then it is the source's." The node holds a
stream only while a client shows one of the plugin's grids, so the stream is
the plugin's signal that someone is looking. Decided for plugins 2026-10-02
(CLAUDE.md, Node).

**Example.** `fs/plugin/watch.go:241-248`: the OS watch exists only while
some stream's scope needs it.

```go
func (w *watcher) detachIfIdle() *fsnotify.Watcher {
	if len(w.watched) > 0 || w.fsw == nil {
		return nil
	}
	fsw := w.fsw
	w.fsw = nil
	return fsw
}
```

**Test.** `fs/plugin/watch_test.go:TestWatchWithNoScopeWatchesNothing` and
`TestWatchFollowsTheScope`. For a polling plugin the shape is: with no
stream open, advance the clock past several refresh windows and assert the
fake source saw no request; open a stream and assert it did.

**Today.** Meet: fs; pages and proc do no background work. Fail: gitlab
(`Plugin.Run`, a ticker for the process's life), gmail (`Plugin.Run`) and hey
(`Plugin.Run`, the refresher and the live feed) all start in `FromConfig` and
run whether or not anything is shown.

## 9. Watch: header, collapse, overflow

**Rule.** Send the header when you accept the stream, collapse a burst into
one change per context, and announce the whole scope when you lose track.

**Why.** "The node learns of a change by being told." The node counts the
stream open at the header and lists what it adds only then
(`internal/pluginhost/watch.go`); every change costs each showing client a
listing; a dropped change is a grid that never repaints.

**Example.** `fs/plugin/watch.go:65-81`:

```go
for {
	select {
	case <-stream.Context().Done():
		return nil
	case key := <-s.ch:
		if err := send(key); err != nil {
			return err
		}
	case <-s.lost:
		for _, key := range s.keys() {
			if err := send(key); err != nil {
```

The burst is collapsed by `watcher.touch` (one announcement per directory per
`DebounceWindow`), and a full queue raises `lost`.

**Test.** `fs/plugin/watch_test.go:TestWatchAnnouncesABurstOnceForItsDirectory`;
`TestWatchSendsItsHeaderOnAccept` in gitlab, gmail and hey. To write for fs:
an OS overflow (`fsnotify.ErrEventOverflow`) announces every context in
scope.

**Today.** Meet: fs; hey and gmail send the header and collapse. Partial:
gitlab collapses an overflowed backlog to the root alone, not the scope
(`gitlab/plugin/watch.go:watchBuffer`).

## 10. Placement hints are a function of the key

**Rule.** A placement hint is a pure function of the key and its source
facts, never of the entry's position in a listing.

**Why.** "Things stay as you left them." A hint that depends on what else is
listed moves when a neighbor arrives late, and differs between two contexts
that list the same thing, so first placement depends on the order things
were read.

**Example.** `pages/plugin/plugin.go:74-78`:

```go
e := &pluginv1.Entry{
	Key:           d.Key,
	Label:         d.Label,
	PlacementHint: &pluginv1.PlacementHint{X: d.Col, Y: d.Row, W: 2, H: 2},
}
```

gitlab's weeks are also right: `todos.WeekCell(w.Start)` reads only the week.

**Test.** To write, per plugin: derive the entries for a set, then for the
same set with one more entry earlier in the order, and assert every shared
key's hint is unchanged. `hey/mail/entries_test.go:TestCollectionEntriesHintAsACalendar`
pins today's index-based column and changes with the fix.

**Today.** Meet: pages, gitlab weeks; fs and proc send no hints. Fail: gitlab
todos (`todos.WeekEntries`, row by order within the day), gmail and hey
(`Cell(date, index)`, column by arrival order within the day).

## 11. State is status_detail, and quiet

**Rule.** A tile's state lives in `status_detail` only, only when worth
noticing; the label stays stable and the normal state sends nothing.

**Why.** "`status_detail` is one word about its state … a note on the name,
never a second name" (`docs/plugin-authoring.md`). A label that carries state
changes the tile's name as the state changes, and a word on every tile is
noise that hides the one that matters.

**Example.** A sketch; no plugin does all of it. hey's `StatusDetail` with the
normal state silent, and `Label` without `UnseenMark`:

```go
func (t *Thread) StatusDetail() string {
	if t.Seen {
		return ""
	}
	return "unseen"
}
```

**Test.** To write, per plugin: a seen and an unseen thread (a done and an
open todo) have the same label, and only the one worth noticing carries a
`status_detail`.

**Today.** Nobody fully. fs, pages and proc send no state, which is quiet but
untested. hey and gitlab put state in the label (`UnseenMark`, `DoneMark`,
week counts in `todos.WeekLabel`); gmail, gitlab and hey send the normal
state ("read", "pending", "seen") on every tile.

## 12. Declare text_presentation

**Rule.** Declare `text_presentation` on every text entry.

**Why.** "A tile behaves the same wherever its content comes from." An entry
with no declaration presents however the client defaults, and a markdown body
shown verbatim (or a log rendered as markdown) is a different tile.

**Example.** `fs/plugin/plugin.go:202-205`:

```go
} else {
	out.Kind = "text"
	out.TextPresentation = fsfile.TextPresentation(e.Name)
}
```

**Test.** `pages/plugin/plugin_test.go:TestAPageDocIsAURLRowAndANoteATextRow`.
A table test per plugin: every text entry its listings answer carries
`plain` or `both`.

**Today.** Meet: fs, pages; gmail and hey list no text entries. Fail: gitlab
(todo tiles, markdown), proc (`@info` tiles).

## 13. Pass the request's context

**Rule.** Pass the request's context into every source call made for that
request.

**Why.** "Nothing is done for nobody." When the node or the user abandons a
call (`gwerr.IsAbandoned`), the work behind it should stop. A detached shared
walk is the one exception: it outlives any reader by design, and runs under
the plugin's lifetime context, not `context.Background()`.

**Example.** `hey/plugin/plugin.go:578-582`:

```go
case mail.EverythingContext:
	if p.mem.Member(id) {
		return presence(pluginv1.ProbeResponse_PRESENCE_PRESENT), nil
	}
	_, err := p.src.ThreadHTML(ctx, id)
```

**Test.** To write: a fake source that records its context; cancel the
request and assert the source's context is done.

**Today.** Fail: gmail (`gmail/plugin/plugin.go:552`) and hey
(`hey/plugin/plugin.go:533`) fetch `ServeContent` under
`context.Background()`; their detached walks also use it instead of the
lifetime context. fs, pages and proc make no cancellable calls.

## 14. Log once per episode

**Rule.** Log a condition once when it starts, and again only after it has
cleared.

**Why.** "Errors surface" (CLAUDE.md, How to work) through health and
verdicts, not the log. A log that repeats every glance buries the one line
that says something changed, and the trace of ordinary use is read line by
line.

**Example.** `fs/plugin/watch.go:226-232`:

```go
verdict := status.Errorf(codes.ResourceExhausted,
	"fs plugin: the OS refused another change watch at %s (%v): raise its watch limit (fs.inotify.max_user_watches on Linux, the open-file limit on macOS) or show fewer directories", dir, err)
if !w.limitLogged {
	w.limitLogged = true
	log.Print(verdict)
}
return verdict
```

`limitLogged` clears when a later subscribe succeeds.

**Test.** To write: inject the limit twice and assert one log line; clear it,
inject again, and assert a second.

**Today.** Meet: fs. Fail: gitlab logs every page of every glance
(`gitlab/todos/memory.go:367-370`); gitlab, gmail and hey log every walk's
start and finish.

## 15. Share the memory code once

**Rule.** The cache file, the single-flight walk and the change fan-out are
one package in the plugins repository, not a copy per plugin.

**Why.** "DRY is correctness" (CLAUDE.md, How to work). Three copies drift:
gitlab's fan-out collapses an overflow to the root, hey's to every
collection; a fix to one (rule 7, rule 9) must be made three times.

**Example.** A sketch of the package to create,
`github.com/josephburnett/gridwell-plugins/memo`, a module beside `guest`:

```go
package memo

// File is a versioned JSON snapshot in state_dir, written atomically.
type File[T any] struct{ Path string; Version int }

// Flights runs one detached walk per collection, shared by every reader.
type Flights struct{ /* … */ }

// Changes fans one change out to every Watch stream, collapsed per context;
// a stream that falls behind is told its whole scope.
type Changes struct{ /* … */ }
```

The copies it replaces: `gitlab/todos/store.go`, `gmail/mailbox/store.go`,
`hey/mail/store.go` (the cache file); the `flight` maps in each
`plugin.go`; `gitlab/plugin/watch.go`, `gmail/plugin/watch.go`, and the
`fanout` in `hey/plugin/watch.go`.

**Test.** The package's own tests, moved from the three copies; each plugin
keeps one test that its memory survives a restart through it.

**Today.** Nobody: three copies.

## 16. Seam tests with the real binary

**Rule.** Every verb a plugin serves has a seam test that spawns the real
binary (`internal/plugintest`), and every plugin that declares `Watch` has a
`Watch` seam test.

**Why.** "Test the seam" (CLAUDE.md, How to work): a unit test on each side
does not catch a contract mismatch. The node reaches a plugin only through its
binary, so only the binary proves the wire.

**Example.** `internal/server/fs_watch_seam_test.go:43`:

```go
cp := plugintest.Spawn(t, "fs", map[string]string{"root": root})
```

A plugin over a network source spawns against a fake of that source, as
gitlab does with `internal/plugintest/gitlabfake`.

**Test.** This rule is its own test. To write: a `Watch` seam test on the
real binary for gitlab (against `gitlabfake`), gmail (against the recorded
Gmail in `internal/server/gmail_seam_test.go`) and hey (against the stand-in
CLI in `internal/server/hey_seam_test.go`).

**Today.** All six are spawned by gridwell's tests
(the seam tests in `internal/server`). Only fs has `Watch` opened on the real
binary (`internal/server/fs_watch_seam_test.go`); no seam test opens it on
gitlab, gmail or hey.

## 17. The README matches the code

**Rule.** Each plugin's README says what the code does today.

**Why.** "Stale is worse than absent" (CLAUDE.md, Comments). The README is
what a user configures from.

**Example.** `pages` has no README and needs none: it takes no config.

**Test.** Read the README against the code when the plugin changes; a
config key named in the README and absent from `FromConfig` (or the reverse)
is the mechanical check to write.

**Today.** Stale: fs (the `ResourceExhausted` paragraph, `fs/README.md:45`),
gitlab ("tells the node which grids to repaint", `gitlab/README.md:18`, while
`Watch` is undeclared), gmail and hey ("a tile's face is …", while the face
is the node's screenshot), gmail (its claims about the `max_messages` cap).
proc has no README.

## Checklist

- [ ] 1. `Info` declares every capability implemented, and a test pins it.
- [ ] 2. `Info` refuses a config it cannot serve with a sentence, and latches.
- [ ] 3. After `Info` passed, an unreachable source answers `Unavailable`.
- [ ] 4. A complete listing is authoritative; an incomplete one is not.
- [ ] 5. Each key lives in one context; other contexts list links to it.
- [ ] 6. `Probe` answers for the context it names.
- [ ] 7. A failed refresh serves memory and reports health; no read fails that memory answers.
- [ ] 8. No walk, glance or feed runs except for a call or a `Watch` in scope.
- [ ] 9. `Watch` sends its header on accept, collapses bursts, re-announces the scope on overflow.
- [ ] 10. Placement hints depend on the key, not on list position.
- [ ] 11. State is in `status_detail` only, the normal state sends nothing, the label is stable.
- [ ] 12. Every text entry declares `text_presentation`.
- [ ] 13. Every request-scoped source call takes the request's context.
- [ ] 14. Each condition logs once per episode.
- [ ] 15. Cache file, flights and fan-out come from the shared package.
- [ ] 16. A real-binary seam test per verb, and one for `Watch` if declared.
- [ ] 17. The README matches the code.
