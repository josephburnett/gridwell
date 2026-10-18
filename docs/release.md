# Releasing

A `v*` tag is the whole procedure. Push one and
`.github/workflows/release.yml` builds one installable artifact per OS and
uploads them to the run.

```
git tag v0.1.0
git push origin v0.1.0
```

Then open the `release` run for that tag and download the three artifacts:

| Artifact | Contents |
|---|---|
| `gridwell-linux` | `Gridwell-<ver>.AppImage` |
| `gridwell-macos` | `Gridwell-<ver>-arm64.dmg`, `Gridwell-<ver>-x64.dmg` |
| `gridwell-windows` | `Gridwell-<ver>.exe` (portable) |

Each bundles the Electron runtime, the `gridwell` binary, and the plugin
binaries. The server embeds the web client (`web/embed.go`), so nothing else
is needed alongside them.

`workflow_dispatch` runs the same three jobs without a tag, naming the
artifacts `0.0.0-dev`. Use it to check the pipeline without spending a
version.

## Cadence

Alpha builds are patch bumps: `v0.1.0`, `v0.1.1`, `v0.1.2`. **No GitHub
release object is created before 1.0** — the workflow has no publish step,
deliberately. Artifacts live on the run.

A tag that failed to build is not spent: fix, commit, then
`git tag -f v0.1.0 && git push -f origin v0.1.0`.

## The version has one owner

The tag. Nothing in the tree carries a version between releases, so cutting
one bumps no file and leaves no churn to review.

Every job derives `VERSION=${GITHUB_REF_NAME#v}` and passes it to make,
which spends it twice:

- `npm version` writes it into `apps/desktop/package.json`, which is where
  electron-builder reads the artifact's name from. This is a build-time
  write; the committed file keeps its `0.0.0` placeholder.
- `-ldflags -X …/internal/cli.Version` stamps the `gridwell` binary.
  `gridwell version` prints it, or `dev` when unstamped.

Running `make dist VERSION=0.1.0` locally therefore leaves `package.json`
and `package-lock.json` modified. Revert them; do not commit the stamp.

## What each platform does not get

The Linux AppImage is the reference build — everything works there.

**macOS.** The bundle is ad-hoc signed (`mac.identity: "-"` in
`apps/desktop/package.json`). Unsigned refuses to launch on Apple Silicon at
all, so this is not optional. It is *not* notarized, so Gatekeeper stops the
first launch. Either right-click → Open and then "Open Anyway" in System
Settings → Privacy & Security, or:

```
xattr -rd com.apple.quarantine /Applications/Gridwell.app
```

Both dmgs carry the same Go binaries, built universal: the Makefile's
`mac-bins` compiles each for amd64 and arm64, `lipo`s them together, and
ad-hoc signs the result — Apple Silicon refuses to exec an unsigned Mach-O,
and `lipo` writes a new file whose inherited signature no longer covers it.
The app bundle's signing pass does not reach a plain executable in
`Contents/Resources`, so that step is what makes the sidecar runnable.

`hardenedRuntime` is off. It exists to satisfy notarization, which this
build does not do, and with an ad-hoc signature it turns on library
validation, which stops the app from launching at all.

**Windows.** Accepted degradations, all of them states rather than faults:

- No live shells. A PTY is a unix facility, so `internal/local/shelldriver`
  refuses there with `ErrShellsUnavailable`, which travels the ordinary
  shell-open path and lands on the client as the reason the shell would not
  attach — the same route a dead tmux session takes.
- No serve lock. `internal/cli/servelock.go`'s flock has no Windows
  equivalent, so nothing stops a second `gridwell serve` over the same home.
- No `proc` plugin. It reads `/proc`; the artifact ships without it rather
  than with one that can never answer.
- No connection API, so `federation:` in `server.yaml` stays unset. That
  API is a 0600 unix socket, and the model does not hold on Windows.

## Adding a binary to the bundle

Nothing to do. `extraResources` in `apps/desktop/package.json` is a filter
over the repo root (`gridwell`, `gridwell.exe`, `gridwell-plugin-*`), so the
Makefile's `ALL_PLUGIN_KINDS` stays the one list of what exists and the
bundle follows it.

## Smoke test

Only the Linux AppImage is exercised by CI beyond building. The dmg and the
exe need a person: download, launch, confirm the window comes up and a grid
loads.
