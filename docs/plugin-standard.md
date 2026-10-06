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
at main `729ba73`, unless they start with `internal/`.

## 1. Declare what you implement

**Rule.** Declare every capability you implement in `Info` (`watch`), and test
the declaration.

**Why.** "A tile behaves the same wherever its content comes from." The node
never switches on a plugin kind; every behavior rides a wire declaration
(CLAUDE.md, Node). A `Watch` the plugin never declares is never opened, so its
grids go stale while the code that would have told the node sits unused.

**Example.** `fs/plugin/plugin.go:178-181`:

```go
// The one collection this plugin serves: the configured tree. It
// declares no label, so the swatch reads as the configured instance.
resp.MenuEntries = []*pluginv1.MenuEntry{{Id: ".", Context: "."}}
resp.Watch = true
```

**Test.** `fs/plugin/watch_test.go:TestInfoDeclaresWatch`: `Info` answers
`watch` set. Every plugin with a `Watch` method carries one
(`TestInfoDeclaresWatch` in proc and gitlab, the `Info` tests in gmail and
hey); pages pins that it declares none
(`pages/plugin/plugin_test.go:TestInfoDeclaresTheCollectionAndNotHostContent`).

**Today.** Meets: fs (`fs/plugin/plugin.go:181`), proc
(`proc/plugin/plugin.go:136`), gitlab (`gitlab/plugin/plugin.go:286`), gmail
(`gmail/plugin/plugin.go:307`), hey (`hey/plugin/plugin.go:265`); pages
implements no `Watch` and declares none.

## 2. Refuse a config you cannot serve, then latch

**Rule.** Refuse `Info` with a plain sentence for any config you cannot serve,
and latch after the first pass.

**Why.** "A plugin that cannot serve the source its config declares refuses
`Info` with the reason, and is broken with that sentence and no entries until
the fix lands, which needs no restart" (2026-09-30). The latch is what makes
a later outage weather (rule 3) rather than a config verdict.

**Example.** `fs/plugin/plugin.go:172-177`:

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
(broken with the reason, healthy once fixed, no restart), and on the real
binaries `internal/server/fs_info_refusal_seam_test.go`,
`gitlab_info_refusal_seam_test.go` and
`gmail_seam_test.go:TestGmailRefusesATokenGoogleRefuses`.

**Today.** Meets: fs (`fs/plugin/plugin.go:172-177`), proc
(`proc/plugin/plugin.go:111-117`), gitlab, which asks GitLab whether it takes
the token (`gitlab/plugin/plugin.go:272-279`), gmail, which asks Google for the
account's history id (`gmail/plugin/plugin.go:289-296`), and hey, which asks
whether the CLI runs (`hey/plugin/plugin.go:248-254`). N/A: pages, which
takes no config.

## 3. After Info, unreachable is Unavailable

**Rule.** Once `Info` has passed, an unreachable source is `Unavailable`,
never a verdict.

**Why.** "Dark is not dead" (CLAUDE.md, Experience). A transport-shaped code
means "not right now", and the node serves its rows and marks the source dark
(`gwerr.IsTransport`, `internal/pluginhost/adapter.go:synthesize`). A coded
answer is a verdict the user is shown, and a swallowed failure is an empty
grid that looks true.

**Example.** `fs/plugin/plugin.go:244-250`:

```go
entries, readErr := fssource.Read(dir)
if readErr != nil {
	if gone(readErr) {
		return &pluginv1.ListResponse{Authoritative: true, SourceLabel: dir}, nil
	}
	return nil, status.Errorf(codes.Unavailable, "fs plugin: read %s: %v", dir, readErr)
}
```

**Test.** Per plugin: pass `Info`, then make the source unreachable and assert
the read answers `codes.Unavailable` (or memory, rule 7), not an empty listing
and not `FailedPrecondition`:
`fs/plugin/plugin_test.go:TestReadContentOfAnUnreadableFileIsUnavailable`,
`proc/plugin/plugin_test.go:TestListOfAnUnreadableTableIsUnavailable`,
`gitlab/plugin/info_test.go:TestInfoPassesWhenGitLabDoesNotAnswer`,
`gmail/plugin/lifetime_test.go:TestATransportFailureAtFirstInfoPassesAndReadsDark`;
across the seam `internal/server/hey_dark_seam_test.go:TestHeyCLIGoneAfterInfoReadsDark`
and `gitlab_outage_seam_test.go`.

**Today.** Meets: fs; proc answers a table it cannot read `Unavailable`
(`proc/plugin/plugin.go:192-195`, `207-209`); hey turns every failed walk,
a cold one included, into memory with its reason (`hey/plugin/plugin.go:406-420`),
and a CLI it cannot run is `Unavailable` (`hey/heycli/heycli.go:357`).
Partial: gitlab and gmail map a network failure to `Unavailable`
(`gitlab/gitlabapi/client.go:105-110`, `142`; `gmail/gmailapi/client.go`), but
a cold read, one memory has no answer for, whose walk fails still fails with
the walk's own code (`memo/flights.go:119`, pinned by `memo/flights_test.go`,
"a cold read whose walk failed answers the failure";
`gitlab/plugin/plugin.go:301-304`, `gmail/plugin/plugin.go:470-472`). So a
token revoked after `Info` reads `PermissionDenied` on a context neither has
read yet. N/A: pages, whose site is its own code and is never unreachable.

## 4. Authoritative when definitive

**Rule.** A listing is authoritative when it enumerates the context
definitively: a whole directory read, or a whole walk of a box while its live
feed is connected. It is not authoritative when the read was capped or the
feed is down or resyncing, because absence is never inferred from silence,
and a feed that is down is silence.

**Why.** "Absence is never inferred from silence" (`docs/freshness.md`). An
authoritative listing lets the node retire a gone key in one pass; a
non-authoritative one costs a `Probe` per missing key, and a wrongly
authoritative one retires a row the user placed.

**Example.** `pages/plugin/plugin.go:65-72`:

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
For a source that is sometimes definitive, the test shape is a table: a whole
read with the feed connected answers authoritative; a capped read, and a whole
read while the feed is down or resyncing, do not.

Per plugin: `gmail/plugin/allmail_test.go:TestALabelListingIsAuthoritativeOnlyWhenDefinitive`,
`hey/plugin/watch_test.go:TestAuthoritativeOnlyWhenDefinitive`.

**Today.** Meets: fs (`fs/plugin/plugin.go:255`), pages; gmail, a label read
whole with memory current to Gmail's history (`gmail/plugin/plugin.go:503`);
hey, a box whose catch-up walk read it whole with the feed live
(`hey/plugin/plugin.go:295-299`, `450`). All mail and everything are never
authoritative, by decision. proc and gitlab are never definitive by design
and never claim it.

## 5. One home per thing

**Rule.** A key lives in one context; every other context that shows it
lists a link to it.

**Why.** "A plugin entry may be a link to another of its entries" and "one
thing listed in many collections is one tile" (2026-10-02). Two entries with
one key are two tiles: placement, a clone and a link each attach to one of
them, and the user sees one thing twice.

**Example.** `hey/mail/entries.go:33-44`:

```go
// BoxEntries derives one box's grid: CollectionEntries, each a link to the
// same thread in everything carrying its seen state in this box. The content
// facts stay, so a node that predates link_target still shows the box as
// pages of its own.
func BoxEntries(threads []Thread) []*pluginv1.Entry {
	out := CollectionEntries(threads)
	for i, e := range out {
		e.LinkTarget = &pluginv1.EntryRef{Context: EverythingContext, Key: e.Key}
		e.StatusDetail = threads[i].StatusDetail()
	}
	return out
}
```

**Test.** `hey/plugin/everything_test.go:TestEverythingIsEveryThreadOnceAndBoxesLinkToIt`,
`gmail/plugin/allmail_test.go:TestAllMailIsEveryMessageOnceAndLabelsLinkToIt`
and `fs/plugin/symlink_test.go:TestSymlinksListAsLinksToTheOneKey` on the
plugin side; `internal/server/link_entry_seam_test.go` across the seam.

**Today.** Meets: gitlab, pages, proc (each key in one context); fs, whose
symlinks are links to the one key (`fs/plugin/plugin.go:299-318`); gmail,
whose labels link into all mail (`gmail/mailbox/entries.go:27-33`); hey.

## 6. Probe answers for the context asked

**Rule.** Answer `Probe` for the key in the context the request names
(`ProbeRequest.context`), and for the plugin as a whole when it names none.

**Why.** "`Probe` answers for the context it names, so leaving one collection
is not being gone" (2026-10-02). A thread that left the Imbox is usually in
Reply Later; GONE for the plugin would retire its tile everywhere.

**Example.** `hey/plugin/plugin.go:566-575`:

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

**Today.** Meets: hey; gmail (`gmail/plugin/plugin.go:564-601`); pages
(`pages/plugin/plugin.go:165-170`) and proc (`proc/plugin/plugin.go:259-283`),
which answer for the context named too; fs and gitlab, whose keys live in one
context each, so the plugin-wide answer is the context's. Every plugin but fs
and gitlab has a `TestProbeAnswersForTheContextAsked`.

## 7. Memory answers when a refresh fails

**Rule.** When a refresh fails but memory can answer, answer from memory and
say so: `ListResponse.unreachable` (an additive string, empty when live, the
reason when served from memory); the node reports it as the source's health,
as it does a failed read, and keeps serving the rows.

**Why.** "Nothing is done for nobody" has a twin at the node: "A remembered
grid serves FIRST … Nothing on the answer says it is a memory — that is the
source's health" (`docs/freshness.md`, layer 4, `internal/sourcecache/`).
A failed `List` costs the user every entry the node has not minted a row for,
which is most of a mailbox. Decided for plugins 2026-10-02 (CLAUDE.md, Node).

**Example.** `gitlab/plugin/plugin.go:300-308`, over `memo.Flights.Read`:

```go
start := since(req.Context)
unreachable, err := p.flights.Read(ctx, req.Context, p.mem.Shows(start))
if err != nil {
	return nil, err
}
if req.Context != todos.RootContext {
	return &pluginv1.ListResponse{Entries: todos.WeekEntries(start, p.mem.Week(start)),
		SourceLabel: req.Context, Unreachable: unreachable}, nil
}
```

**Test.** The field is `ListResponse.unreachable`, in api v0.5.0; the node
side is pinned by
`internal/server/memory_answer_seam_test.go:TestAMemoryAnswerKeepsEveryRowAndReportsTheReason`.
Per plugin: warm the memory with one walk, make the next walk fail, and assert
`List` answers the remembered entries with no error and `unreachable` set:
`gitlab/plugin/plugin_test.go:TestAWarmReadAnswersMemoryWithTheLastWalksFailure`,
`gmail/plugin/plugin_test.go:TestAWarmReadAnswersMemoryAndSaysWhyTheWalkFailed`,
`hey/plugin/plugin_test.go:TestAWarmReadAnswersMemoryAndTheLastFailure`; across
the seam `internal/server/gitlab_outage_seam_test.go`,
`gmail_watch_seam_test.go:TestGmailWarmGridSurvivesGmailGoingAway` and
`hey_unreachable_seam_test.go:TestHeySignedOutKeepsTheGridAndSaysWhy`.

**Today.** Meets: gitlab (`gitlab/plugin/plugin.go:294-319`), gmail
(`gmail/plugin/plugin.go:469-475`), hey (`hey/plugin/plugin.go:406-420`),
all through `memo.Flights.Read` (`memo/flights.go:102-125`). A cold read has
no memory to answer from, and is rule 3's. N/A: fs, proc and pages, which keep
no memory.

## 8. Work only while watched

**Rule.** Walk, glance or follow a feed only to answer a call, or while the
node holds a `Watch` whose scope includes the context.

**Why.** "Nothing is done for nobody: no capture, fetch or walk runs unless
something on screen or in the store needs its result," and "a clock is for a
source that cannot tell, and then it is the source's." The node holds a
stream only while a client shows one of the plugin's grids, so the stream is
the plugin's signal that someone is looking. Decided for plugins 2026-10-02
(CLAUDE.md, Node).

**Example.** `fs/plugin/watch.go:250-257`: the OS watch exists only while
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
fake source saw no request; open a stream and assert it did:
`memo/changes_test.go:TestPollOnlyWhileWatched`,
`proc/plugin/watch_test.go:TestNoStreamNoReads`,
`gitlab/plugin/plugin_test.go:TestTheRefresherRunsOnlyWhileWatched`,
`gmail/plugin/watch_test.go:TestHistoryPollsOnlyWhileAContextIsInScope`,
`hey/plugin/watch_test.go:TestTheFeedRunsOnlyWhileWatched`, and across the
seam `internal/server/hey_watch_seam_test.go:TestHeyFeedRunsOnlyWhileAGridIsShown`.

**Today.** Meets: fs; proc polls a pid only while a stream shows it
(`proc/plugin/watch.go:27-43`); gitlab's refresher, gmail's history poll and
hey's live feed are `memo.Changes` work, which runs only while a stream needs
it and a linger after (`gitlab/plugin/plugin.go:152-158`,
`gmail/plugin/plugin.go:211-217`, `hey/plugin/plugin.go:216-222`). pages does
no background work.

## 9. Watch: header, collapse, overflow

**Rule.** Send the header when you accept the stream, collapse a burst into
one change per context, and announce the whole scope when you lose track.

**Why.** "The node learns of a change by being told." The node counts the
stream open at the header and lists what it adds only then
(`internal/pluginhost/watch.go`); every change costs each showing client a
listing; a dropped change is a grid that never repaints.

**Example.** `fs/plugin/watch.go:70-80`:

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
`DebounceWindow`), and a full queue raises `lost`. On the `plugin-content`
branch the queue is a set of what each stream is owed, so a stream that falls
behind owes each change once and loses none, and an OS overflow owes the
whole scope (rule 18).

**Test.** `fs/plugin/watch_test.go:TestWatchAnnouncesABurstOnceForItsDirectory`
and `TestWatchOverflowAnnouncesTheWholeScope`; `memo/changes_test.go:TestChangesFanOut`
(the header on accept, a burst once per context, a stream that falls behind
told its whole scope); `TestWatchSendsItsHeaderOnAccept` in proc, gitlab,
gmail and hey.

**Today.** Meets: fs; proc, gitlab, gmail and hey, which serve `Watch` through
`memo.Changes.Serve` (`memo/changes.go:106-131`, the overflow at `224-247`).
N/A: pages.

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

The mail and todo plugins share one rule, `memo/calendar`: `calendar.Cell`
reads only the thing's creation time, and gitlab's weeks'
`calendar.WeekCell(w.Start)` only the week.

**Test.** Per plugin: derive the entries for a set, then for the same set
with one more entry earlier in the order, and assert every shared key's hint
is unchanged: `hey/mail/entries_test.go:TestEntriesHintIsPureInTheCreationTime`,
`gmail/mailbox/mailbox_test.go:TestHintsAreTheCalendarCellOfTheDate`,
`gitlab/todos/todos_test.go:TestTodoHintsAreAFunctionOfTheTodo` and
`TestWeekHintsAreTheSharedWeekCell`, and `memo/calendar/calendar_test.go`.

**Today.** Meets: pages; gitlab (`gitlab/todos/entries.go:16`, `43`), gmail
(`gmail/mailbox/entries.go:38`) and hey (`hey/mail/entries.go:21`), each a
calendar cell of the creation time. fs and proc send no hints.

## 11. State is status_detail, and quiet

**Rule.** A tile's state lives in `status_detail` only, as one emoji, only
when worth noticing; the label stays stable and the normal state sends nothing.

**Why.** "`status_detail` is one emoji about its state … a note on the name,
never a second name" (`docs/plugin-authoring.md`). The client draws it before
the name at every zoom the name is drawn (`tileface.BannerText`). A label that carries state
changes the tile's name as the state changes, and a word on every tile is
noise that hides the one that matters.

**Example.** `hey/mail/mail.go:168-175`, beside a `Label` that reads the
same seen or unseen (`hey/mail/mail.go:155-165`):

```go
// StatusDetail is UnseenMark while the thread is unseen in the box it was
// listed from, and nothing once it is seen.
func (t *Thread) StatusDetail() string {
	if t.Seen {
		return ""
	}
	return UnseenMark
}
```

**Test.** Per plugin: a seen and an unseen thread (a done and an open todo)
have the same label, and only the one worth noticing carries a
`status_detail`: `hey/mail/mail_test.go:TestLabelIsTheSenderAndSubjectWhateverTheSeenState`
and `hey/mail/entries_test.go:TestStatusIsTheUnseenMarkInABoxOnly`,
`gmail/mailbox/mailbox_test.go:TestTheLabelIsTheSubjectAndStateIsQuiet`,
`gitlab/todos/todos_test.go:TestDoneIsTheStatusNotTheName`,
`proc/plugin/plugin_test.go:TestStateIsQuietAndNeverInTheLabel`,
`pages/plugin/plugin_test.go:TestEntriesAreQuietAndHintedByTheirDoc`,
`fs/plugin/presentation_test.go:TestEveryTextEntryDeclaresAPresentation`. The
client draws the status before the name (`client/tileface/banner_test.go`).

**Today.** Meets: fs and pages, which send none; proc, 💀 or ⏸
(`proc/plugin/plugin.go:220-231`); gitlab, ✅ once done
(`gitlab/todos/todos.go:112-121`), its week wells named by the Monday alone;
gmail, ● unread, else ★ starred outside the starred grid
(`gmail/mailbox/mailbox.go:185-193`); hey, ● unseen in a box and nothing in
everything.

## 12. Declare text_presentation

**Rule.** Declare `text_presentation` on every text entry.

**Why.** "A tile behaves the same wherever its content comes from." An entry
with no declaration presents however the client defaults, and a markdown body
shown verbatim (or a log rendered as markdown) is a different tile.

**Example.** `fs/plugin/plugin.go:289-290`:

```go
out.Kind = "text"
out.TextPresentation = fsfile.TextPresentation(name)
```

**Test.** `pages/plugin/plugin_test.go:TestAPageDocIsAURLRowAndANoteATextRow`.
A table test per plugin: every text entry its listings answer carries
`plain` or `both`: `fs/plugin/presentation_test.go:TestEveryTextEntryDeclaresAPresentation`,
`proc/plugin/plugin_test.go:TestEveryTextEntryDeclaresItsPresentation`,
`gitlab/todos/todos_test.go:TestEveryTextEntryDeclaresItsPresentation`.

**Today.** Meets: fs, pages (`pages/plugin/plugin.go:89-91`), proc's `@info`
(`proc/plugin/plugin.go:200`), gitlab's todos (`gitlab/todos/entries.go:47`).
N/A: gmail and hey, which list no text entries.

## 13. Pass the request's context

**Rule.** Pass the request's context into every source call made for that
request.

**Why.** "Nothing is done for nobody." When the node or the user abandons a
call (`gwerr.IsAbandoned`), the work behind it should stop. A detached shared
walk is the one exception: it outlives any reader by design, and runs under
the plugin's lifetime context, not `context.Background()`.

**Example.** `hey/plugin/plugin.go:555-559`:

```go
case mail.EverythingContext:
	if p.mem.Member(id) {
		return presence(pluginv1.ProbeResponse_PRESENCE_PRESENT), nil
	}
	_, err := p.src.ThreadHTML(ctx, id)
```

**Test.** A fake source that records its context; cancel the request and
assert the source's context is done:
`gmail/plugin/lifetime_test.go:TestServeContentTakesTheRequestsContext` and
`TestTheWalkRunsUnderThePluginsLifetime`,
`hey/plugin/plugin_test.go:TestServeContentReadsUnderTheRequestsContext` and
`TestAWalkRunsUnderThePluginsLife`, `memo/flights_test.go:TestFlightsWalkUnderTheLifetime`.

**Today.** Meets: gmail (`ServeContent` under `stream.Context()`,
`gmail/plugin/plugin.go:536`) and hey (`hey/plugin/plugin.go:510`), whose
walks run under `memo.Life` (`memo/life.go`); gitlab (`MarkDone(ctx)`,
`gitlab/plugin/plugin.go:370`; walks under `memo.Life`); proc, whose children
read takes the request's context (`proc/plugin/plugin.go:203`). fs makes no
cancellable calls. N/A: pages, which has no source to call.

## 14. Log once per episode

**Rule.** Log a condition once when it starts, and again only after it has
cleared.

**Why.** "Errors surface" (CLAUDE.md, How to work) through health and
verdicts, not the log. A log that repeats every glance buries the one line
that says something changed, and the trace of ordinary use is read line by
line.

**Example.** `fs/plugin/watch.go:235-241`:

```go
verdict := status.Errorf(codes.ResourceExhausted,
	"fs plugin: the OS refused another change watch at %s (%v): raise its watch limit (fs.inotify.max_user_watches on Linux, the open-file limit on macOS) or show fewer directories", dir, err)
if !w.limitLogged {
	w.limitLogged = true
	log.Print(verdict)
}
return verdict
```

`limitLogged` clears when a later subscribe succeeds (`fs/plugin/watch.go:154`).

**Test.** Inject the condition twice and assert one log line; clear it,
inject again, and assert a second: `fs/plugin/watch_test.go:TestWatchLimitLogsOncePerEpisode`,
`memo/flights_test.go` ("a failure logs once per episode"),
`memo/file_test.go:TestFileSaveLogsOncePerEpisode`,
`gitlab/plugin/log_test.go:TestGitLabFailingLogsOncePerEpisode`,
`gmail/plugin/lifetime_test.go:TestARefreshFailureLogsOncePerEpisode`,
`hey/plugin/plugin_test.go:TestAWalkLogsOnlyTheEdgesOfAFailure`,
`hey/plugin/watch_test.go:TestAFeedThatKeepsEndingLogsOnce`.

**Today.** Meets: fs; gitlab and hey, whose walk failures and cache writes log
through memo once per episode (`memo/flights.go:242-247`, `memo/file.go`), and
hey's feed (`hey/plugin/watch.go:36-73`); proc and pages log nothing.
Partial: gmail logs its walk and message failures once per episode
(`gmail/plugin/episodes.go`), but a token it cannot write back logs on every
access-token refresh for as long as the write keeps failing
(`gmail/plugin/config.go:93`).

## 15. Share the memory code once

**Rule.** The cache file, the single-flight walk and the change fan-out are
one package in the plugins repository, not a copy per plugin.

**Why.** "DRY is correctness" (CLAUDE.md, How to work). Three copies drift:
gitlab's fan-out collapses an overflow to the root, hey's to every
collection; a fix to one (rule 7, rule 9) must be made three times.

**Example.** `github.com/josephburnett/gridwell-plugins/memo`, a module beside
`guest` (`memo/README.md`), as hey holds it, `hey/plugin/plugin.go:86-89`:

```go
life    *memo.Life
file    *memo.File[cache]
flights *memo.Flights
changes *memo.Changes
```

Each plugin keeps only its snapshot's shape (`gitlab/todos/store.go`,
`hey/mail/store.go`, `cache` in `gmail/plugin/plugin.go`).

**Test.** The package's own tests (`memo/file_test.go`, `flights_test.go`,
`changes_test.go`); each plugin keeps one test that its memory survives a
restart through it: `gitlab/plugin/plugin_test.go:TestRestartAnswersFromTheCacheFileWithoutWalking`,
`gmail/plugin/plugin_test.go:TestTheCacheSurvivesARestart`,
`hey/plugin/plugin_test.go:TestTheCacheSurvivesARestart`.

**Today.** Meets: gitlab, gmail and hey, which take `File`, `Flights`,
`Changes` and `Life` from memo; proc, which takes `Changes` and `Poll`
(`proc/plugin/plugin.go:99-103`). N/A: fs, whose OS watch is its own
(`fs/plugin/watch.go`), and pages, neither of which keeps a memory.

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

**Test.** This rule is its own test. `Watch` on the real binary:
`internal/server/fs_watch_seam_test.go`, `proc_watch_seam_test.go`,
`gitlab_watch_seam_test.go` (against `gitlabfake`), `gmail_watch_seam_test.go`
(against the recorded Gmail) and `hey_watch_seam_test.go` (against the
stand-in CLI).

**Today.** Meets: fs; pages (`internal/server/pages_seam_test.go`, every verb
it serves); gmail (`gmail_seam_test.go`, `gmail_watch_seam_test.go`); hey
(`hey_seam_test.go`, `hey_verbs_seam_test.go`, `hey_watch_seam_test.go`,
`hey_page_seam_test.go`); proc, whose `@info` body
`proc_content_change_seam_test.go` reads. Partial: gitlab, whose `Probe` no
seam test asks.

## 17. The README matches the code

**Rule.** Each plugin's README says what the code does today.

**Why.** "Stale is worse than absent" (CLAUDE.md, Comments). The README is
what a user configures from.

**Example.** `pages` has no README and needs none: it takes no config.

**Test.** Read the README against the code when the plugin changes; a
config key named in the README and absent from `FromConfig` (or the reverse)
is the mechanical check to write.

**Today.** Meets: fs, proc, gmail; pages has none and takes no config.
Partial: gitlab says "a token GitLab stops taking is treated like GitLab being
down" (`gitlab/README.md:25-26`), which holds for a grid memory can answer and
not for a cold one (rule 3); hey says everything is laid out "a row per day,
newest at the top" (`hey/README.md:210-211`), while its hint is
`calendar.Cell`: a column per day, newest to the right, a row per hour.

## 18. Say which entry changed

**Rule.** When an entry's content changes in place, its bytes or its
picture, send `EntryChanged` with the entry re-read exactly as `List` would
answer it; send `ContextChanged` only when the listing may have moved. Never
send `EntryRemoved`, which is retired.

**Why.** "An open grid shows what its source knows" (CLAUDE.md, promises). A
plugin row carries no version, so nothing on a listing says a body moved, and
a client holding the old body keeps it: the node checks a `ContextChanged`
against what it served and, the listing unchanged, tells no one. The
`EntryChanged` is the only news a body has moved (`docs/freshness.md`, layer
6), and it costs the node no listing for an entry with a row.

**Example.** `fs/plugin/watch.go` (`plugin-content` branch): a write, or a
file created over its name as an editor's save does, owes the file's entry;
a name that came, went or moved owes the directory's listing. At the
window's close the stream is sent every listing, then each file re-read
through `entryOf`, the one function `List` answers with.

**Test.** `fs/plugin/watch_test.go:TestWatchTellsAWrittenFileItsEntry` (the
entry equals `List`'s, and the directory is not announced) and
`TestWatchTellsAFileSavedByRenameItsEntry`; across the seam
`internal/server/fs_content_change_seam_test.go` (an open file, a text face
and an image face show the new bytes) and
`link_entry_seam_test.go:TestATargetsNewBytesReachTheBodyALinkShows`.

**Today.** Meets: fs (`plugin-content` branch, api v0.6.0). N/A: pages,
whose site is its own code. Partial: proc, gitlab, gmail and hey send
`ContextChanged` alone, so a body of theirs that changes in place (proc's
`@info`, a hey thread's card) reaches an open view only when it is next read
from scratch.

## 19. A write claims the stamp it read

**Rule.** If your source can take a text body back, declare `writable` and
serve `WriteContent`; name every text entry's bytes with `content_stamp`, on
the entry and on the read; refuse a write whose claimed stamp is not the
entry's now with `FailedPrecondition`; answer the written bytes' stamp; and
tell the write as the entry's `EntryChanged`, as rule 18 tells any change in
place. A body you cannot take back whole is refused with its reason.

**Why.** "A tile behaves the same wherever its content comes from"
(CLAUDE.md, promises): a home document is typed into and saved, so a file
should be. "Things stay as you left them": a save must never overwrite bytes
the user has not seen, and the stamp is the claim that says which bytes the
edit was typed over, as a version is for a home row (`docs/freshness.md`,
trace (c)). The answered stamp is what the next save claims, and the echo's
stamp is how the client knows its own write and keeps the text.

**Example.** `fs/plugin/plugin.go` (`plugin-edits` branch), `write`:

```go
if now := fsfile.ContentStamp(fi.ModTime(), fi.Size()); claimed != now {
	return "", status.Errorf(codes.FailedPrecondition, "fs plugin: %q changed on disk since it was read", key)
}
```

**Test.** `fs/plugin/write_test.go`: a write claiming the stamp replaces the
file and answers the stamp a read names; a stale stamp is `FailedPrecondition`
and leaves the other writer's bytes; a summary, a page, an oversized body, a
read-only file and a key outside the root are refused with reasons; a broken
stream writes nothing. `fs/plugin/stamp_test.go`: the listing and the read
name one stamp. Across the seam, `internal/server/fs_write_seam_test.go` (a
save lands and its echo keeps the text, a disk change under a dirty edit is
the conflict, a write the plugin cannot take parks and lands) and
`fs_stamp_seam_test.go`.

**Today.** Meets: fs (`plugin-edits` branch). N/A: proc, pages, gitlab,
gmail and hey, which take no body back and declare no `writable`.

## Checklist

Tick each before you ship. At `729ba73` the shipped plugins tick every box
but four: 3 (gitlab, gmail), 14 (gmail), 16 (proc, gitlab) and 17 (gitlab,
hey); rules 18 and 19, added later, are met by fs alone. The rules' Today
lines say what remains.

- [ ] 1. `Info` declares every capability implemented, and a test pins it.
- [ ] 2. `Info` refuses a config it cannot serve with a sentence, and latches.
- [ ] 3. After `Info` passed, an unreachable source answers `Unavailable`.
- [ ] 4. A definitive listing (whole read, feed connected) is authoritative; a capped read or a down feed is not.
- [ ] 5. Each key lives in one context; other contexts list links to it.
- [ ] 6. `Probe` answers for the context it names.
- [ ] 7. A failed refresh answers from memory with `ListResponse.unreachable` set.
- [ ] 8. No walk, glance or feed runs except for a call or a `Watch` in scope.
- [ ] 9. `Watch` sends its header on accept, collapses bursts, re-announces the scope on overflow.
- [ ] 10. Placement hints depend on the key, not on list position.
- [ ] 11. State is one emoji in `status_detail` only, the normal state sends nothing, the label is stable.
- [ ] 12. Every text entry declares `text_presentation`.
- [ ] 13. Every request-scoped source call takes the request's context.
- [ ] 14. Each condition logs once per episode.
- [ ] 15. Cache file, flights and fan-out come from the shared package.
- [ ] 16. A real-binary seam test per verb, and one for `Watch` if declared.
- [ ] 17. The README matches the code.
- [ ] 18. A content change in place is an `EntryChanged` with the entry re-read; `ContextChanged` only for a listing that may have moved.
- [ ] 19. A body you take back is declared `writable`, named by `content_stamp`, refused on a stale stamp with `FailedPrecondition`, and answered with the written bytes' stamp.
