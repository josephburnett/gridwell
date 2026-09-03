# Release builds

## Goal

Pushing a tag `v0.1.0` makes CI build one installable artifact per OS and
upload them as workflow artifacts — no GitHub release yet. The artifacts:

- Linux: `Gridwell-0.1.0.AppImage`
- macOS: `Gridwell-0.1.0-arm64.dmg` and `Gridwell-0.1.0-x64.dmg`
- Windows: `Gridwell-0.1.0.exe` (portable)

Each bundles Electron plus the four Go binaries (`gridwell`,
`gridwell-plugin-fs/proc/gitlab`) as extraResources; the server binary
already embeds the whole web client, so the bundle is self-contained.

## Where builds stand today

**Linux works.** `make dist` produces the AppImage; `apps/desktop/out/`
holds one. The Go binaries land in `resources/` beside `app.asar`, and
both resolution paths already work: Electron finds the sidecar at
`process.resourcesPath/gridwell` (`apps/desktop/src/main/paths.ts`), and
the server finds plugins beside its own executable
(`internal/cli/serve.go` `resolveBinary`).

**macOS compiles clean today** — `GOOS=darwin go build ./...` (both
arches, CGO_ENABLED=0 throughout) succeeds for the server and all three
plugins with zero code changes. Nothing invokes the dmg target, and
electron-builder cannot build a dmg from Linux; it needs a macOS runner.

**Windows does not compile.** Exactly two failure sites, both PTY/signal
code:

- `internal/local/shelldriver`: `SysProcAttr{Setsid, Setctty}`,
  `syscall.Getpgid`/`Kill`, and the `creack/pty` dependency (no ConPTY
  support).
- gridwell-plugins `guest/guest.go`: the host-death watchdog probes with
  `syscall.Kill(pid, 0)`, breaking all three plugin builds; the proc
  plugin additionally uses `syscall.Kill` and is `/proc`-bound anyway.

**No version exists anywhere.** `package.json` says `0.0.0` (the only
version consumer — it names the artifact), the Go side has no ldflags,
no version variable, no `version` subcommand.

## What each OS needs

### All three: version stamping

In the tag workflow, `VERSION=${GITHUB_REF_NAME#v}`, then:

- `npm version "$VERSION" --no-git-tag-version` in `apps/desktop`
  (workspace-only rewrite, nothing committed) — electron-builder names
  the artifacts from it.
- `var version = "dev"` in `apps/gridwell/main.go`, stamped with
  `-ldflags "-X main.version=$VERSION"`, printed in the serve banner and
  by a `gridwell version` subcommand. One owner: the tag; nothing in the
  tree carries a version between releases.

### Linux (nothing to fix)

The existing `make dist` recipe, run on `ubuntu-latest`. The
`vendor`/offline-cache design is for laptop builds; CI downloads fresh
(setup-node's npm cache keyed on `package-lock.json` is already the
pattern in `check.yml`).

### macOS (runner + config, no code)

- A `macos-latest` (arm64) job. Go cross-compiles the amd64 sidecars on
  the same runner; electron-builder downloads x64 Electron fine.
- Two dmgs, not a universal binary: `"arch": ["arm64", "x64"]` on the
  dmg target. Universal would need `lipo`'d Go sidecars for nothing —
  the Go binaries are single-arch and extraResources supports
  `${os}-${arch}` staging paths, so one config serves both arches from a
  per-arch staging dir.
- Signing: ad-hoc (`mac.identity: "-"`). Apple Silicon refuses to
  execute a wholly unsigned binary, and electron-builder skips signing
  entirely when no identity is configured, so ad-hoc must be explicit.
  No notarization. Since macOS Sequoia the right-click-open bypass is
  gone: first launch needs System Settings → Privacy & Security → "Open
  Anyway", or `xattr -rd com.apple.quarantine Gridwell.app`. That is the
  documented cost of testing pre-release; real signing is a later
  decision.
- Runtime: everything works except live shells until tmux is installed
  (`brew install tmux`); `internal/node/nativelocal.go` already degrades
  gracefully ("live shells disabled on this host"). The proc plugin is
  inert without `/proc` — ships harmlessly, plugins are config-opt-in.

### Windows (small code changes + config)

The smallest set for a working build:

1. Split `internal/local/shelldriver` with `//go:build unix` and a
   windows stub whose `Start` returns an error — one caller
   (`shellsvc.go`), and the existing tmux-missing degradation already
   covers the runtime story. This also drops `creack/pty` from the
   windows build.
2. gridwell-plugins `guest`: build-tag the watchdog probe (unix
   `syscall.Kill(pid, 0)`; windows `os.FindProcess`-based no-op).
3. Don't build the proc plugin for windows — it is `/proc`-bound by
   design.
4. `resolveBinary`/`isExecutable` in `internal/cli/serve.go`: on
   windows accept `.exe` (Go sets no exec bits there, so today every
   probe before `LookPath` misses).
5. `.exe` naming end-to-end: the Electron sidecar lookup
   (`paths.ts` joins `resourcesPath/gridwell`, extension-less) and the
   extraResources entries both need per-OS names.
6. `sidecar.ts` `kill('SIGTERM')` is a hard kill on Windows — the Go
   server gets no graceful shutdown. Acceptable: the DB and locks are
   crash-safe by design; noted, not fixed.

Accepted degradations on Windows: no live shells, no serve-lock (the
honest stub `servelock_windows.go` already exists), and the connection
door stays unset — AF_UNIX works on Win10+ but the 0600 "kernel is the
gate" model does not hold, so a Windows node should not open
`federation:`.

Everything else is portable: paths are `filepath.Join` throughout, ssh
is in-process (`x/crypto/ssh`, no exec of `ssh`), go-plugin falls back
to TCP loopback on Windows on its own, and no code checks a file mode
and refuses (0600/0700 are write-side only; they degrade silently).

## The workflow

`.github/workflows/release.yml`, `on: push: tags: ['v*']`, three
independent jobs on native runners — electron-builder cannot cross-build
a dmg from Linux, and Windows-from-Linux needs Wine; the repo is public
so macOS and Windows runners are free.

Each job: checkout; clone gridwell-plugins beside the checkout (the same
anonymous public `git clone --depth 1` the check workflows use);
setup-go from `go.mod` + setup-node 22 (the existing pattern); build the
Go binaries + wasm; `npm ci`; stamp the version; run electron-builder
for that OS's target; `actions/upload-artifact@v4`. Artifacts sit on the
workflow run page (90-day default retention). No release object is
created — promoting a build to a GitHub release is a separate, later
step.

Two per-OS notes:

- The Linux job can simply run `make dist`. The Windows job scripts its
  steps directly (`go build`, `npm run build`, electron-builder) — the
  Makefile assumes a unix shell.
- Release builds inherit the check workflows' habit of cloning
  gridwell-plugins at main. Good enough for 0.1.0 test artifacts; a real
  release wants a pinned plugins ref (a tag in that repo) recorded in
  the workflow run.

## Decisions to make

- **Windows in 0.1.0, or defer?** macOS + Linux need zero Go changes;
  Windows needs the shelldriver split and the guest watchdog tag (a
  gridwell-plugins change). The workflow can ship with two jobs and grow
  the third.
- **Portable vs nsis for Windows.** Portable is one exe that unpacks
  `resources/` to `%TEMP%` on every launch (~100MB+, slow-ish); nsis is
  one exe that installs once. Both are a single file. Current config
  says portable.
- **Binary size**: the server is ~72MB (embedded web client). Release
  builds could add `-ldflags "-s -w"`; dev builds stay as they are.

## Non-requirements

No GitHub release, no auto-update, no real code signing or
notarization, no icon work (electron-builder falls back to the default
Electron icon), no plugins-repo release process beyond the pinning note
above.
