.PHONY: build bin plugins wasm test test-cover fmt-check check check-electron check-e2e check-web check-federation serve init clean launch vendor dist node-modules

BIN := ./gridwell
FS_BIN := ./gridwell-fs
PROC_BIN := ./gridwell-proc
LOCALDB_BIN := ./gridwell-localdb
SSH_BIN := ./gridwell-ssh
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
# resolves them by `gridwell-<kind>`.
build: bin plugins wasm

# CGO_ENABLED=0 makes the sidecar a fully static binary: modernc.org/sqlite is
# pure Go, so nothing pulls cgo and the result has no libc-version coupling —
# and since the web client (index.html, wasm, vendor) is EMBEDDED (web/embed.go),
# the built gridwell + gridwell-<kind> binaries are the whole distribution:
# copy them anywhere and the browser client serves from the binary itself.
# bin depends on wasm so the embed always carries the current client.
bin: wasm
	CGO_ENABLED=0 go build -o $(BIN) ./cmd/gridwell

# Phony so a source change always rebuilds (Go's build cache keeps it fast);
# file-target rules would skip the build whenever the binary already existed.
plugins:
	CGO_ENABLED=0 go build -o $(LOCALDB_BIN) ./cmd/plugin/localdb
	CGO_ENABLED=0 go build -o $(FS_BIN) ./cmd/plugin/fs
	CGO_ENABLED=0 go build -o $(PROC_BIN) ./cmd/plugin/proc
	CGO_ENABLED=0 go build -o $(SSH_BIN) ./cmd/plugin/ssh

# The .gz sidecar rides along: the server serves it with
# Content-Encoding: gzip when the client accepts it (staticOrSPA's
# serveGzipSidecar) — the wasm is ~33 MB raw, ~8 MB gzipped, and a phone
# on a relayed tailscale link downloads it every boot. gzip runs after the
# build so the sidecar is always at least as new as the raw file (the
# server refuses a stale one).
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

test:
	go test ./...

test-cover:
	go test -cover ./...

# fmt-check fails if any hand-written Go file isn't gofmt-clean (generated code
# under api/gen is excluded — it's regenerated, not hand-edited). Kept as the
# first check step so formatting drift can't accumulate the way it had: several
# files were committed non-gofmt because nothing enforced it. Fix with `gofmt -w`.
fmt-check:
	@bad=$$(gofmt -l $$(git ls-files '*.go' | grep -v '/gen/')); \
	if [ -n "$$bad" ]; then echo "gofmt needed (run: gofmt -w <file>):"; echo "$$bad"; exit 1; fi

# proto-check regenerates the wire code (local buf plugins — offline) and
# fails when api/gen differs from the git INDEX or carries untracked files.
# This catches all three ways generated code goes wrong: a proto edit
# without `buf generate`; a hand-edit to generated code; and a PARTIAL
# `git add` of the generated set — the 2026-08-04 `make launch` break,
# where data.pb.go was committed but data_grpc.pb.go/data.connect.go
# stayed in the working tree, so every working-tree gate was green while
# the pushed history didn't compile. Staged-but-uncommitted generated
# files pass (worktree == index is the invariant), so the normal
# edit → regen → git add → make check → commit loop is unaffected.
proto-check:
	@command -v buf >/dev/null || { echo "buf not found — install buf (+protoc-gen-go, -connect-go, -go-grpc) to run proto-check"; exit 1; }
	buf generate
	@git diff --exit-code -- api/gen || { echo "api/gen differs from the index — run 'git add api/gen' (or commit the regen with the proto change)"; exit 1; }
	@untracked=$$(git ls-files --others --exclude-standard api/gen); \
	if [ -n "$$untracked" ]; then echo "untracked generated files — run 'git add api/gen':"; echo "$$untracked"; exit 1; fi

# check is the per-commit verification gate: every commit must leave all of these
# green. fmt-check enforces gofmt; the wasm build catches GOOS=js breakage that
# `go build ./...` (host arch) misses; the typecheck catches Electron-side TS
# drift; `npm test` runs the desktop main-process unit tests (menu/geometry logic
# that never reaches the heavier display-bound gates). No display or network needed.
check: fmt-check proto-check
	go build ./...
	go test ./...
	GOOS=js GOARCH=wasm go build -o /tmp/gridwell.wasm ./client/wasm
	cd $(DESKTOP) && npm run typecheck
	cd $(DESKTOP) && npm test

# check-electron runs the live-tile harnesses under a virtual display. Needed
# only for phases that touch the URL/shell live path (and the final pass), since
# they exercise the real Electron WebContentsView / PTY bridge. Requires xvfb +
# a prior `make vendor` for node_modules.
check-electron: node-modules
	cd $(DESKTOP) && npm run test:integration && npm run test:bridge

# check-e2e drives the REAL Electron app end to end: Playwright launches the same
# `electron .` as `make launch` (which spawns the Go sidecar), points it at a
# fresh throwaway home (seeded via `gridwell init`), and drives the wasm canvas with synthetic mouse input —
# asserting outcomes against the live server over Connect-RPC. This is the only
# test that exercises the full renderer→wasm→RPC→server→SQLite composition (e.g.
# drag-create in a descended grid). Heavier than `make check` (it builds the
# binaries and boots Electron), so it's a pre-merge full-stack gate alongside
# check-electron, not part of the fast per-commit `check`. Requires xvfb + a
# prior `make vendor` for node_modules + Playwright.
check-e2e: build node-modules
	cd $(DESKTOP) && npm run build && xvfb-run -a npm run test:e2e

# check-web drives the BROWSER-MODE client: `gridwell serve` + plain Chromium
# (the system /usr/bin/chromium — no browser download, so the repo stays
# offline-buildable). This is the only gate that sees the degraded phone/
# tablet client: no Electron bridge, caps-gated live-URL affordances, and the
# touch gesture layer (client/touchgest) driven by real injected TouchEvents.
# Headless — no xvfb needed. Run for any change to client/caps,
# client/touchgest, touch.go, or the browser-serving path.
check-web: build node-modules
	cd $(DESKTOP) && npm run test:e2e:web

# check-federation is the SPAWN GATE (issue #58): the real binaries —
# gridwell init/serve and the go-plugin subprocesses including gridwell-ssh —
# through a real ssh tunnel, one write/read crossing every hop. The in-process
# seam tests cannot see go-plugin spawn: the pluginmeta sqlite-driver bug kept
# every test green while every production spawn failed. Guarded by the
# `federation` build tag so make check stays fast. Headless, ~1s after build.
# Run for any change to plugin spawn, sshdial, the node export, or routing.
check-federation: build
	go test -tags federation -count=1 ./test/federation/

# serve runs the backend on its own (the desktop app spawns it as a sidecar;
# this target is for poking at the RPC/SSE surface or loading the wasm client
# in a plain browser — note live URL tiles only work inside the Electron app).
# It requires ~/.gridwell/server.yaml (run `make init` once to create it); every
# plugin's DB path is derived from its id, so there is no --db flag.
serve: build
	$(BIN) serve $(SERVE_FLAGS)

# SERVE_FLAGS passes extra flags through, e.g.
# `make serve SERVE_FLAGS="--bind 0.0.0.0:8080"`.
SERVE_FLAGS ?=

# init bootstraps a fresh home: it mints a plugin id, creates its DB (with
# identity metadata) under ~/.gridwell/db/<id>/, and registers it in
# ~/.gridwell/server.yaml. Run once before `make serve` / `make launch`.
#   make init                                  # localdb named "home"
#   make init INIT_FLAGS="--kind fs --name files --config root=/home/joe"
INIT_FLAGS ?= --kind localdb --name home
init: bin
	$(BIN) init $(INIT_FLAGS)

# vendor is the ONE online step. It pins and caches everything the desktop
# build needs — npm packages (into $(NPM_CACHE) + node_modules), the Electron
# runtime zip (into electron_config_cache), and the electron-builder helper
# binaries incl. the AppImage runtime (into ELECTRON_BUILDER_CACHE) — by
# running `npm ci` against the committed lockfile and then building the
# AppImage once. After this completes, `make dist` needs no network.
vendor: bin wasm
	cd $(DESKTOP) && npm ci --cache $(NPM_CACHE)
	# Electron ≥42 no longer downloads its binary in postinstall (it defers to
	# first run) — materialize it NOW, into the repo-local cache, or the first
	# offline `make launch`/harness run would try to hit the network.
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

# `make launch` is the one-shot dev run: build the sidecar + wasm, compile the
# TS, and launch Electron against ~/.gridwell (server.yaml + the plugin DBs it
# names) so your existing grids are right there. Requires ~/.gridwell/server.yaml
# — run `make init` once first; there is no fallback DB. Point at a different
# home with GRIDWELL_HOME. Runs WITH Chromium's OS sandbox on (this box's kernel
# allows unprivileged user namespaces, so no setuid helper is needed) — live URL
# tiles load untrusted web content, so the sandbox is the containment that
# matters. Needs a prior `make vendor` for node_modules.
#
#   make init && make launch                       # ~/.gridwell
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
	rm -f $(BIN) $(LOCALDB_BIN) $(FS_BIN) $(PROC_BIN) $(SSH_BIN) $(WASM) $(WASM_EXEC)
	rm -rf $(DESKTOP)/dist $(DESKTOP)/out
