.PHONY: build bin plugins mac-bins wasm fmt-check proto-check check check-electron check-e2e check-web check-connections serve clean launch vendor dist dist-mac dist-win stamp-version node-modules

# Every plugin kind with a binary in $(PLUGINS_DIR). `plugins` builds from
# this list and `clean` removes from it.
ALL_PLUGIN_KINDS := fs proc gitlab pages hey gmail

# HOST_GOOS is what this machine builds for. The release builds run on native
# runners, one per OS, so nothing here cross-compiles a distribution.
HOST_GOOS := $(shell go env GOOS)

ifeq ($(HOST_GOOS),windows)
# Windows names a built binary <name>.exe. exeSuffixFor in
# internal/cli/serve.go and apps/desktop/src/main/paths.ts are the loader's
# side of the same fact.
EXE := .exe
# proc reads /proc, so it is a unix plugin and the Windows build ships
# without it rather than shipping one that can never answer.
PLUGIN_KINDS := $(filter-out proc,$(ALL_PLUGIN_KINDS))
else
EXE :=
PLUGIN_KINDS := $(ALL_PLUGIN_KINDS)
endif

BIN := ./gridwell$(EXE)
# clean removes every kind, whatever this host builds, so switching hosts in
# one checkout leaves nothing behind.
ALL_PLUGIN_BIN := $(addsuffix $(EXE),$(addprefix ./gridwell-plugin-,$(ALL_PLUGIN_KINDS)))

# VERSION is the release version and the git tag owns it, through the release
# workflow's VERSION=$${GITHUB_REF_NAME#v}. Nothing in the tree carries a
# version between releases, so an unset VERSION is a development build and
# reports itself as "dev".
VERSION ?=
GO_LDFLAGS := -X github.com/josephburnett/gridwell/internal/cli.Version=$(VERSION)

# The plugins live in their own repository, and PLUGINS_DIR is the one place
# that says where that checkout is. It defaults to beside this one and is
# overridable from the environment.
PLUGINS_DIR ?= ../gridwell-plugins
WASM := ./web/gridwell.wasm
WASM_EXEC := ./web/wasm_exec.js
# On Windows `go env GOROOT` answers C:\..., and every recipe here is a POSIX
# shell script where a backslash is an escape, so the path would silently fail
# to exist. Git Bash reads C:/... fine.
GOROOT := $(subst \,/,$(shell go env GOROOT))

DESKTOP := apps/desktop

# Repo-local caches. One online `make vendor` populates them, after which
# `make dist` and `make launch` need no network. The variables are exported so
# the npm, electron and electron-builder toolchain honours them.
CACHE := $(CURDIR)/$(DESKTOP)/.cache
NPM_CACHE := $(CACHE)/npm
export electron_config_cache := $(CACHE)/electron
export ELECTRON_BUILDER_CACHE := $(CACHE)/electron-builder

# `bin`, `plugins` and `wasm` are phony so they always invoke `go build`, and
# no stale binary or wasm artifact is ever served. Go's build cache keeps that
# fast. Every plugin is its own separately compiled go-plugin binary, laid out
# beside $(BIN) so the server resolves it by `gridwell-plugin-<kind>`.
build: bin plugins wasm

# CGO_ENABLED=0 makes the sidecar a static binary with no libc-version
# coupling, which works because modernc.org/sqlite is pure Go. web/embed.go
# embeds the web client, so the built gridwell and gridwell-plugin-<kind>
# binaries are the whole distribution and the browser client serves from the
# binary itself. bin depends on wasm so the embed carries the current
# client.
bin: wasm
	cd apps/gridwell && CGO_ENABLED=0 go build -ldflags "$(GO_LDFLAGS)" -o ../../gridwell$(EXE) .

# Phony so a source change always rebuilds; a file target would skip the build
# whenever the binary already existed. The sources are in $(PLUGINS_DIR) and
# the binaries land beside $(BIN), where the loader and every Go test look.
plugins:
	@test -d $(PLUGINS_DIR) || { \
		echo "$(PLUGINS_DIR) missing — the plugins live in their own repository:"; \
		echo "  git clone git@github.com:josephburnett/gridwell-plugins.git $(PLUGINS_DIR)"; \
		echo "(or point PLUGINS_DIR at an existing checkout)"; \
		exit 1; \
	}
	@set -e; for k in $(PLUGIN_KINDS); do \
		echo "cd $(PLUGINS_DIR)/$$k && go build -o $(CURDIR)/gridwell-plugin-$$k$(EXE)"; \
		(cd $(PLUGINS_DIR)/$$k && CGO_ENABLED=0 go build -o $(CURDIR)/gridwell-plugin-$$k$(EXE) ./cmd/gridwell-plugin-$$k); \
	done

# The .gz sidecar rides along, and the server serves it with
# Content-Encoding: gzip when the client accepts it. The wasm is tens of
# megabytes raw and a fraction of that gzipped, and a phone on a relayed link
# downloads it every boot. gzip runs after the build, so the sidecar is at
# least as new as the raw file and the server refuses a stale one.
#
# Both embedded artifacts are written to a private temp name in the same
# directory and then renamed into place, because `go build` rewrites its
# output incrementally over about a second and a reader of the growing file
# would see a prefix. A gzip of a prefix is a valid gzip and exits 0, so
# nothing would complain and the browser would get a clean 200 of a truncated
# module. Rename is atomic on one filesystem, so the published name only ever
# holds a complete file.
#
# The temp name carries the shell's pid, so two concurrent makes in one tree
# do not share it. There is no flock, which would cover only the
# sub-millisecond window between the two renames and is not portable off
# Linux. The byte comparison below is what makes a mismatched pair
# impossible, and web/embed_test.go holds the same property over the bytes
# actually embedded.
wasm: $(WASM_EXEC)
	mkdir -p web
	@set -e; \
	tmp=$(WASM).$$$$.tmp; \
	trap 'rm -f "$$tmp" "$$tmp.gz"' EXIT; \
	echo "GOOS=js GOARCH=wasm go build -o $(WASM) ./client/wasm"; \
	GOOS=js GOARCH=wasm go build -o "$$tmp" ./client/wasm; \
	echo "gzip -9 $(WASM) -> $(WASM).gz"; \
	gzip -9 -c "$$tmp" > "$$tmp.gz"; \
	gzip -dc "$$tmp.gz" | cmp -s - "$$tmp" || { \
		echo "$(WASM).gz does not decompress to $(WASM) — the sidecar is short; rerun make wasm"; \
		exit 1; \
	}; \
	mv -f "$$tmp.gz" $(WASM).gz; \
	mv -f "$$tmp" $(WASM)

$(WASM_EXEC):
	mkdir -p web
	@set -e; \
	tmp=$(WASM_EXEC).$$$$.tmp; \
	trap 'rm -f "$$tmp"' EXIT; \
	if [ -f $(GOROOT)/lib/wasm/wasm_exec.js ]; then \
		cp $(GOROOT)/lib/wasm/wasm_exec.js "$$tmp"; \
	elif [ -f $(GOROOT)/misc/wasm/wasm_exec.js ]; then \
		cp $(GOROOT)/misc/wasm/wasm_exec.js "$$tmp"; \
	else \
		echo "wasm_exec.js not found in GOROOT"; exit 1; \
	fi; \
	mv -f "$$tmp" $(WASM_EXEC)

# fmt-check fails when a hand-written Go file is not gofmt-clean. Generated
# code under api/gen is excluded, since it is regenerated rather than edited.
# It runs first so formatting drift cannot accumulate. Fix with `gofmt -w`.
fmt-check:
	@bad=$$(gofmt -l $$(git ls-files '*.go' | grep -v '/gen/')); \
	if [ -n "$$bad" ]; then echo "gofmt needed (run: gofmt -w <file>):"; echo "$$bad"; exit 1; fi

# proto-check regenerates the wire code with local buf plugins and fails when
# the generated set differs from the git index or carries untracked files.
#
# That catches a proto edit without `buf generate`, a hand-edit to generated
# code, and a partial `git add` of the generated set, which would leave every
# working-tree gate green while the pushed history does not compile. The
# invariant is worktree equals index, so staged but uncommitted generated
# files pass and the usual edit, regen, add, check, commit loop is
# unaffected.
GENERATED := api/gen

proto-check:
	@command -v buf >/dev/null || { echo "buf not found — install buf (+protoc-gen-go, -connect-go, -go-grpc) to run proto-check"; exit 1; }
	buf generate
	@git diff --exit-code -- $(GENERATED) || { echo "generated code differs from the index — run 'git add $(GENERATED)' (or commit the regen with the proto change)"; exit 1; }
	@untracked=$$(git ls-files --others --exclude-standard $(GENERATED)); \
	if [ -n "$$untracked" ]; then echo "untracked generated files — run 'git add $(GENERATED)':"; echo "$$untracked"; exit 1; fi

# check is the per-commit gate, and every commit must leave all of it green.
# The wasm build catches GOOS=js breakage a host-arch `go build ./...` misses,
# and the windows and darwin builds catch the same class for the release
# targets, where a `//go:build unix` half whose other half went stale would
# only fail at a tag. The typecheck catches Electron-side TS drift and `npm
# test` runs the desktop main-process unit tests, which never reach the
# display-bound gates. check-exception-owners fails when a declared exception
# field is read outside its owning predicate, and check-docpaths fails when a
# doc or workflow names a repo path that no longer exists. None of it needs a
# display or a network.
#
# MODULES lists every in-repo Go module beyond the root. check builds and
# tests each one standalone with GOWORK=off, so no module can lean on the
# workspace. The plugins are another repository's modules, gated by its own
# `make check`.
MODULES := api internal/doctype apps/gridwell

# check depends on wasm because web/embed.go embeds the built gridwell.wasm,
# so a fresh checkout cannot `go build ./...` before one exists. It depends on
# plugins because the seam tests spawn the real binaries, which is the only
# door a plugin has into this repo. `go vet ./...` runs at the host GOOS and
# so sees none of the js/wasm-tagged files, which is why they are vetted
# again under GOOS=js: this is the only gate that looks at them at all.
check: fmt-check proto-check wasm plugins
	go build ./...
	go vet ./...
	go test ./...
	cd test/boundary && go test -count=1 .
	@for m in $(MODULES); do \
		echo "== module $$m (standalone)"; \
		(cd $$m && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...) || exit 1; \
	done
	GOOS=js GOARCH=wasm go build -o /tmp/gridwell.wasm ./client/wasm
	GOOS=js GOARCH=wasm go vet ./client/...
	GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
	GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
	./scripts/check-tracked-binaries.sh
	./scripts/check-vocabulary.sh
	./scripts/check-deadcode.sh
	./scripts/check-exception-owners.sh
	./scripts/check-docpaths.sh
	go tool staticcheck ./...
	cd $(DESKTOP) && npm run typecheck
	cd $(DESKTOP) && npm run typecheck:e2e
	cd $(DESKTOP) && npm test

# The heavy gates below are the one recipe for each gate. CI invokes these
# targets rather than re-spelling them, so the two cannot drift. PW_FLAGS
# passes extra Playwright flags through to check-e2e and check-web:
#   make check-e2e PW_FLAGS=--retries=1     # CI's one-retry flake discipline
PW_FLAGS ?=

# A retry that passes leaves Playwright's exit code 0, so each suite is
# followed by an audit of its JSON report: a spec that needed a retry and has
# no row on docs/flake-ledger.md fails the gate. The report paths are the
# playwright configs' outputFile, which test/boundary pins against these.
FLAKY_REPORT := node scripts/flaky-report.mjs

# check-electron runs the live-tile harnesses under a virtual display,
# exercising the real Electron WebContentsView, so it is needed only for a
# change to the live url path. Shells ride a WebSocket on the web door, so
# check-web owns that path. It needs xvfb and a prior `make vendor` for
# node_modules. The npm scripts wrap xvfb-run themselves, so do not wrap them
# again.
check-electron: node-modules
	cd $(DESKTOP) && npm run test:integration && npm run test:bridge

# check-e2e drives the real Electron app end to end. Playwright launches the
# same `electron .` as `make launch`, which spawns the Go sidecar against a
# fresh throwaway home, and drives the wasm canvas with synthetic mouse input,
# asserting outcomes against the live server over Connect-RPC. It is the only
# test that exercises the whole renderer, wasm, RPC, server and SQLite
# composition, and it builds the binaries and boots Electron, so it is a
# pre-merge gate rather than part of the per-commit check. It needs xvfb and a
# prior `make vendor` for node_modules and Playwright.
check-e2e: build node-modules
	cd $(DESKTOP) && npm run build && xvfb-run -a npm run test:e2e -- $(PW_FLAGS)
	$(FLAKY_REPORT) $(DESKTOP)/playwright-report/e2e.json

# check-web drives the browser-mode client: `gridwell serve` and the system
# Chromium, so the repo stays offline-buildable. It is the only gate that sees
# the phone and tablet client, with no Electron bridge, caps-gated live-url
# affordances, and client/touchgest driven by real injected TouchEvents. It is
# headless. Run it for any change to client/caps, client/touchgest, touch.go
# or the browser-serving path.
check-web: build node-modules
	cd $(DESKTOP) && npm run test:e2e:web -- $(PW_FLAGS)
	$(FLAKY_REPORT) $(DESKTOP)/playwright-report/web.json

# check-connections is the spawn gate: the real binaries through a real ssh
# tunnel, with one write and read crossing every hop. The in-process seam
# tests cannot see go-plugin spawn, so a failure that only happens in a
# spawned process leaves them green. The `connections` build tag keeps `make
# check` fast. It is headless. Run it for any change to plugin spawn, the
# dialer, the node export or routing.
check-connections: build
	cd test/connections && go test -tags connections -count=1 .

# serve runs the backend on its own, for poking at the RPC surface or loading
# the wasm client in a plain browser, where live url tiles need the Electron
# app. A missing ~/.gridwell/server.yaml is a fresh home, and the first serve
# mints the node's id and writes the file.
serve: build
	$(BIN) serve $(SERVE_FLAGS)

# SERVE_FLAGS passes extra flags through, e.g.
# `make serve SERVE_FLAGS="--bind 0.0.0.0:8080"`.
SERVE_FLAGS ?=

# vendor is the one online step. It runs `npm ci` against the committed
# lockfile and builds the AppImage once, which caches the npm packages, the
# Electron runtime zip and the electron-builder helper binaries. After it
# completes `make dist` needs no network.
vendor: build
	cd $(DESKTOP) && npm ci --cache $(NPM_CACHE)
	# Electron defers its binary download to first run, so materialize it
	# into the repo-local cache here, or the first offline `make launch`
	# reaches for the network.
	cd $(DESKTOP) && node node_modules/electron/install.js
	$(MAKE) dist
	@echo "vendored: caches warm under $(CACHE); 'make dist' is now offline"

# stamp-version writes VERSION into the desktop package.json, where
# electron-builder reads the version it names every artifact with. It is a
# build-time write and never a commit, so the tree keeps its 0.0.0 placeholder
# and cutting a release bumps no file. An empty VERSION leaves package.json
# alone.
stamp-version:
	@if [ -n "$(VERSION)" ]; then \
		(cd $(DESKTOP) && npm version "$(VERSION)" --no-git-tag-version --allow-same-version >/dev/null); \
		echo "stamped $(DESKTOP)/package.json version = $(VERSION)"; \
	else \
		echo "no VERSION: a development build, package.json untouched"; \
	fi

# dist, dist-mac and dist-win are the three release builds, one per OS. Each
# runs on a native runner, sequenced by .github/workflows/release.yml, because
# a dmg cannot be produced off macOS and the portable exe wants a Windows
# host. Each bundles the Electron runtime, the Go binaries as extraResources,
# and through web/embed.go the whole web client, so an artifact is
# self-contained.
#
# dist is also the offline AppImage build for local use and assumes a prior
# `make vendor`. It produces Gridwell-<ver>.AppImage under $(DESKTOP)/out/.
dist: build node-modules stamp-version
	cd $(DESKTOP) && npm run build && ./node_modules/.bin/electron-builder --linux AppImage
	@echo "AppImage: $(DESKTOP)/out/"

# dist-mac produces both dmgs, Gridwell-<ver>-arm64.dmg and -x64.dmg, from
# mac-bins' universal Go binaries. package.json's mac.identity is "-", ad-hoc
# signing, because an unsigned bundle refuses to launch on Apple Silicon. It
# is not notarized, so a first launch needs "Open Anyway"; see
# docs/release.md.
dist-mac: mac-bins wasm node-modules stamp-version
	cd $(DESKTOP) && npm run build && ./node_modules/.bin/electron-builder --mac
	@echo "dmg: $(DESKTOP)/out/"

# dist-win produces the portable Gridwell-<ver>.exe. Its accepted
# degradations are in docs/release.md: no live shells, no serve lock, no proc
# plugin, and no connection door, whose 0600 unix socket has no Windows
# equivalent.
dist-win: build node-modules stamp-version
	cd $(DESKTOP) && npm run build && ./node_modules/.bin/electron-builder --win portable
	@echo "portable exe: $(DESKTOP)/out/"

# mac-bins makes the Go binaries universal. macOS ships two dmgs, one per
# arch, but extraResources is one set of files for both, so an arm64-only
# sidecar would ride inside the Intel dmg and never start. Each binary is
# compiled twice and lipo'd into one file, at the path the Linux and Windows
# builds write, so package.json names one thing everywhere.
#
# codesign --sign - follows, because Apple Silicon refuses to exec an unsigned
# Mach-O. Go's linker ad-hoc signs each arm64 build, but lipo writes a new fat
# file whose signature no longer covers what is on disk, and the app bundle's
# own signing pass does not reach a plain executable in Contents/Resources.
mac-bins: wasm
	@set -e; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	fat() { \
		out=$$1; dir=$$2; pkg=$$3; \
		echo "universal $$out"; \
		(cd "$$dir" && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(GO_LDFLAGS)" -o "$$tmp/$$out.amd64" "$$pkg"); \
		(cd "$$dir" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(GO_LDFLAGS)" -o "$$tmp/$$out.arm64" "$$pkg"); \
		lipo -create -output "$(CURDIR)/$$out" "$$tmp/$$out.amd64" "$$tmp/$$out.arm64"; \
		codesign --force --sign - "$(CURDIR)/$$out"; \
	}; \
	fat gridwell apps/gridwell .; \
	for k in $(PLUGIN_KINDS); do fat gridwell-plugin-$$k $(PLUGINS_DIR)/$$k ./cmd/gridwell-plugin-$$k; done

# `make launch` is the one-shot dev run: build the sidecar and wasm, compile
# the TS, and launch Electron against ~/.gridwell. A home with no server.yaml
# is created on the first serve, and GRIDWELL_HOME points at a different one.
# It runs with Chromium's OS sandbox on, because live url tiles load untrusted
# web content. It needs a prior `make vendor` for node_modules.
#
#   make launch                                     # ~/.gridwell
#   GRIDWELL_HOME=/path/to/home make launch         # another home
launch: build node-modules
	cd $(DESKTOP) && npm run build && ./node_modules/.bin/electron .

# node-modules guards the offline targets. With the desktop deps absent it
# points at the online bootstrap instead of reaching for the network.
node-modules:
	@test -d $(DESKTOP)/node_modules || { \
		echo "$(DESKTOP)/node_modules missing — run 'make vendor' once (online) first"; \
		exit 1; \
	}

clean:
	rm -f $(BIN) $(ALL_PLUGIN_BIN) $(WASM) $(WASM_EXEC)
	rm -rf $(DESKTOP)/dist $(DESKTOP)/out
