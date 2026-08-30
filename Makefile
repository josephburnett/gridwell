.PHONY: build bin plugins wasm fmt-check proto-check check check-electron check-e2e check-web check-federation serve clean launch vendor dist node-modules

BIN := ./gridwell
# Built plugin binaries — the plugins target below and clean
# must agree; this is the one list.
ALL_PLUGIN_BIN := ./gridwell-plugin-fs ./gridwell-plugin-proc ./gridwell-plugin-gitlab

# The plugins live in their own repository — gridwell owns the door, the
# plugins repo owns the plugins. PLUGINS_DIR is the one place that says where
# that checkout is: beside this one by default, overridable from the
# environment (PLUGINS_DIR=/path/to/gridwell-plugins make build).
PLUGINS_DIR ?= ../gridwell-plugins
WASM := ./web/gridwell.wasm
WASM_EXEC := ./web/wasm_exec.js
GOROOT := $(shell go env GOROOT)

DESKTOP := apps/desktop

# Repo-local caches. A single online `make vendor` populates them; after that
# every `make dist` (and `make launch`) is fully offline — no GitHub, no npm
# registry, no network at all. Work on a plane. The vars are exported so the
# npm / electron / electron-builder toolchain underneath honours them.
CACHE := $(CURDIR)/$(DESKTOP)/.cache
NPM_CACHE := $(CACHE)/npm
export electron_config_cache := $(CACHE)/electron
export ELECTRON_BUILDER_CACHE := $(CACHE)/electron-builder

# `bin`, `plugins`, and `wasm` are phony so they always invoke `go build`. Go's
# build cache makes this fast when nothing changed, but it guarantees
# we never serve a stale binary or wasm artifact. Every plugin is its own
# separately-compiled go-plugin binary, laid out beside $(BIN) so the server
# resolves them by `gridwell-plugin-<kind>`.
build: bin plugins wasm

# CGO_ENABLED=0 makes the sidecar a fully static binary: modernc.org/sqlite is
# pure Go, so nothing pulls cgo and the result has no libc-version coupling —
# and since the web client (index.html, wasm, vendor) is EMBEDDED (web/embed.go),
# the built gridwell + gridwell-plugin-<kind> binaries are the whole distribution:
# copy them anywhere and the browser client serves from the binary itself.
# bin depends on wasm so the embed always carries the current client.
bin: wasm
	cd apps/gridwell && CGO_ENABLED=0 go build -o ../../gridwell .

# Phony so a source change always rebuilds (Go's build cache keeps it fast);
# file-target rules would skip the build whenever the binary already existed.
# The sources are in $(PLUGINS_DIR); the binaries land here, beside $(BIN),
# which is where the loader and every Go test look for them.
plugins:
	@test -d $(PLUGINS_DIR) || { \
		echo "$(PLUGINS_DIR) missing — the plugins live in their own repository:"; \
		echo "  git clone git@github.com:josephburnett/gridwell-plugins.git $(PLUGINS_DIR)"; \
		echo "(or point PLUGINS_DIR at an existing checkout)"; \
		exit 1; \
	}
	cd $(PLUGINS_DIR)/fs && CGO_ENABLED=0 go build -o $(CURDIR)/gridwell-plugin-fs ./cmd/gridwell-plugin-fs
	cd $(PLUGINS_DIR)/proc && CGO_ENABLED=0 go build -o $(CURDIR)/gridwell-plugin-proc ./cmd/gridwell-plugin-proc
	cd $(PLUGINS_DIR)/gitlab && CGO_ENABLED=0 go build -o $(CURDIR)/gridwell-plugin-gitlab ./cmd/gridwell-plugin-gitlab

# The .gz sidecar rides along: the server serves it with
# Content-Encoding: gzip when the client accepts it (staticOrSPA's
# serveGzipSidecar). The wasm is tens of megabytes raw and a fraction of
# that gzipped, and a phone on a relayed link downloads it every boot.
# gzip runs after the build so the sidecar is always at least as new as
# the raw file; the server refuses a stale one.
wasm: $(WASM_EXEC)
	mkdir -p web
	GOOS=js GOARCH=wasm go build -o $(WASM) ./client/wasm
	gzip -9 -kf $(WASM)

$(WASM_EXEC):
	mkdir -p web
	@if [ -f $(GOROOT)/lib/wasm/wasm_exec.js ]; then \
		cp $(GOROOT)/lib/wasm/wasm_exec.js $(WASM_EXEC); \
	elif [ -f $(GOROOT)/misc/wasm/wasm_exec.js ]; then \
		cp $(GOROOT)/misc/wasm/wasm_exec.js $(WASM_EXEC); \
	else \
		echo "wasm_exec.js not found in GOROOT"; exit 1; \
	fi

# fmt-check fails if any hand-written Go file isn't gofmt-clean (generated code
# under api/gen is excluded — it's regenerated, not hand-edited). It is the
# first check step so formatting drift cannot accumulate. Fix with `gofmt -w`.
fmt-check:
	@bad=$$(gofmt -l $$(git ls-files '*.go' | grep -v '/gen/')); \
	if [ -n "$$bad" ]; then echo "gofmt needed (run: gofmt -w <file>):"; echo "$$bad"; exit 1; fi

# proto-check regenerates the wire code (local buf plugins — offline) and
# fails when the generated set differs from the git INDEX or carries
# untracked files. GENERATED covers both halves: api/gen (the protobuf +
# connect code) and api/rpc/wire_gen.go (api/rpc's Go records and their
# conversions, derived from the same proto).
# This catches all three ways generated code goes wrong: a proto edit
# without `buf generate`; a hand-edit to generated code; and a partial
# `git add` of the generated set, which leaves every working-tree gate
# green while the pushed history does not compile. Staged-but-uncommitted
# generated files pass, since worktree == index is the invariant, so the
# normal edit, regen, git add, make check, commit loop is unaffected.
GENERATED := api/gen api/rpc/wire_gen.go

proto-check:
	@command -v buf >/dev/null || { echo "buf not found — install buf (+protoc-gen-go, -connect-go, -go-grpc) to run proto-check"; exit 1; }
	buf generate
	@git diff --exit-code -- $(GENERATED) || { echo "generated code differs from the index — run 'git add $(GENERATED)' (or commit the regen with the proto change)"; exit 1; }
	@untracked=$$(git ls-files --others --exclude-standard $(GENERATED)); \
	if [ -n "$$untracked" ]; then echo "untracked generated files — run 'git add $(GENERATED)':"; echo "$$untracked"; exit 1; fi

# check is the per-commit verification gate: every commit must leave all of these
# green. fmt-check enforces gofmt; the wasm build catches GOOS=js breakage that
# `go build ./...` (host arch) misses; the typecheck catches Electron-side TS
# drift; `npm test` runs the desktop main-process unit tests (menu/geometry logic
# that never reaches the heavier display-bound gates); check-docpaths fails
# when a doc or workflow names a repo path that no longer exists. No display
# or network needed.
# MODULES lists every in-repo Go module beyond the root: the api and the
# shared nested modules. check builds and tests each one standalone
# (GOWORK=off) so no module can quietly lean on the workspace. The plugins
# are not here: they are another repository's modules, gated by its own
# `make check`.
MODULES := api internal/doctype apps/gridwell

# check depends on wasm: web/embed.go EMBEDS the built gridwell.wasm, so
# a fresh checkout (CI) cannot even `go build ./...` before one exists. It
# depends on plugins because the seam tests spawn the real binaries: the only
# door a plugin has into this repo.
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
	./scripts/check-tracked-binaries.sh
	./scripts/check-vocabulary.sh
	./scripts/check-deadcode.sh
	./scripts/check-docpaths.sh
	go tool staticcheck ./...
	cd $(DESKTOP) && npm run typecheck
	cd $(DESKTOP) && npm run typecheck:e2e
	cd $(DESKTOP) && npm test

# The heavy gates below are the one recipe for each gate: CI
# (.github/workflows/gates.yml) invokes these targets rather than
# re-spelling them, so the two cannot drift. PW_FLAGS passes extra
# Playwright flags through to check-e2e / check-web:
#   make check-e2e PW_FLAGS=--retries=1     # CI's one-retry flake discipline
PW_FLAGS ?=

# check-electron runs the live-tile harnesses under a virtual display. It is
# needed only for a change that touches the live url path, since it exercises
# the real Electron WebContentsView; shells ride a WebSocket on the web door,
# so check-web owns that path. Requires xvfb and a prior `make vendor` for
# node_modules. The npm scripts wrap xvfb-run themselves — do not wrap them
# again.
check-electron: node-modules
	cd $(DESKTOP) && npm run test:integration && npm run test:bridge

# check-e2e drives the real Electron app end to end: Playwright launches the
# same `electron .` as `make launch`, which spawns the Go sidecar, points it at
# a fresh throwaway home that serve mints its config into, and drives the wasm
# canvas with synthetic mouse input, asserting outcomes against the live server
# over Connect-RPC. It is the only test that exercises the full renderer, wasm,
# RPC, server, SQLite composition. It is heavier than `make check`, since it
# builds the binaries and boots Electron, so it is a pre-merge full-stack gate
# rather than part of the fast per-commit check. Requires xvfb and a prior
# `make vendor` for node_modules and Playwright.
check-e2e: build node-modules
	cd $(DESKTOP) && npm run build && xvfb-run -a npm run test:e2e -- $(PW_FLAGS)

# check-web drives the BROWSER-MODE client: `gridwell serve` + plain Chromium
# (the system /usr/bin/chromium — no browser download, so the repo stays
# offline-buildable). This is the only gate that sees the degraded phone/
# tablet client: no Electron bridge, caps-gated live-URL affordances, and the
# touch gesture layer (client/touchgest) driven by real injected TouchEvents.
# Headless — no xvfb needed. Run for any change to client/caps,
# client/touchgest, touch.go, or the browser-serving path.
check-web: build node-modules
	cd $(DESKTOP) && npm run test:e2e:web -- $(PW_FLAGS)

# check-federation is the spawn gate: the real binaries — gridwell serve and
# the go-plugin subprocesses — through a real ssh tunnel, with one write and
# read crossing every hop. The in-process seam tests cannot see go-plugin
# spawn, so a failure that only happens in a spawned process leaves them green.
# Guarded by the `federation` build tag so make check stays fast. Headless.
# Run it for any change to plugin spawn, the dialer, the node export, or
# routing.
check-federation: build
	cd test/federation && go test -tags federation -count=1 .

# serve runs the backend on its own. The desktop app spawns it as a sidecar;
# this target is for poking at the RPC surface or loading the wasm client in a
# plain browser, where live url tiles only work inside the Electron app. A
# missing ~/.gridwell/server.yaml is a fresh home: the first serve mints the
# node's id and writes the file.
serve: build
	$(BIN) serve $(SERVE_FLAGS)

# SERVE_FLAGS passes extra flags through, e.g.
# `make serve SERVE_FLAGS="--bind 0.0.0.0:8080"`.
SERVE_FLAGS ?=

# vendor is the ONE online step. It pins and caches everything the desktop
# build needs — npm packages (into $(NPM_CACHE) + node_modules), the Electron
# runtime zip (into electron_config_cache), and the electron-builder helper
# binaries incl. the AppImage runtime (into ELECTRON_BUILDER_CACHE) — by
# running `npm ci` against the committed lockfile and then building the
# AppImage once. After this completes, `make dist` needs no network.
vendor: bin wasm
	cd $(DESKTOP) && npm ci --cache $(NPM_CACHE)
	# Electron defers its binary download to first run, so materialize it here,
	# into the repo-local cache, or the first offline `make launch` or harness
	# run reaches for the network.
	cd $(DESKTOP) && node node_modules/electron/install.js
	$(MAKE) dist
	@echo "vendored: caches warm under $(CACHE); 'make dist' is now offline"

# dist is the offline AppImage build. It assumes a prior `make vendor` warmed
# the caches and installed node_modules. Produces a single self-contained
# Gridwell-<ver>.AppImage under $(DESKTOP)/out/ that bundles the Electron
# runtime, the static Go sidecar, and the wasm assets.
dist: bin wasm node-modules
	cd $(DESKTOP) && npm run build && ./node_modules/.bin/electron-builder --linux AppImage
	@echo "AppImage: $(DESKTOP)/out/"

# `make launch` is the one-shot dev run: build the sidecar and wasm, compile
# the TS, and launch Electron against ~/.gridwell, so your existing grids are
# right there. A home with no server.yaml is created on the first serve. Point
# at a different home with GRIDWELL_HOME. It runs with Chromium's OS sandbox
# on, since live url tiles load untrusted web content and the sandbox is the
# containment that matters. Needs a prior `make vendor` for node_modules.
#
#   make launch                                     # ~/.gridwell
#   GRIDWELL_HOME=/path/to/home make launch         # another home
launch: build node-modules
	cd $(DESKTOP) && npm run build && ./node_modules/.bin/electron .

# node-modules guards the offline targets: if the desktop deps aren't present,
# point the user at the single online bootstrap instead of silently reaching
# for the network.
node-modules:
	@test -d $(DESKTOP)/node_modules || { \
		echo "$(DESKTOP)/node_modules missing — run 'make vendor' once (online) first"; \
		exit 1; \
	}

clean:
	rm -f $(BIN) $(ALL_PLUGIN_BIN) $(WASM) $(WASM_EXEC)
	rm -rf $(DESKTOP)/dist $(DESKTOP)/out
