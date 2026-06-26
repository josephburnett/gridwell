.PHONY: build bin plugins wasm test test-cover check check-electron serve clean launch vendor dist node-modules

BIN := ./gridwell
FS_BIN := ./gridwell-fs
PROC_BIN := ./gridwell-proc
LOCALDB_BIN := ./gridwell-localdb
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
# pure Go, so nothing pulls cgo and the result has no libc-version coupling.
# The AppImage bundles this binary as-is — it's the one piece of Gridwell that
# genuinely has zero system dependencies.
bin:
	CGO_ENABLED=0 go build -o $(BIN) ./cmd/gridwell

plugins: $(LOCALDB_BIN) $(FS_BIN) $(PROC_BIN)

$(LOCALDB_BIN):
	CGO_ENABLED=0 go build -o $(LOCALDB_BIN) ./cmd/plugin/localdb

$(FS_BIN):
	CGO_ENABLED=0 go build -o $(FS_BIN) ./cmd/plugin/fs

$(PROC_BIN):
	CGO_ENABLED=0 go build -o $(PROC_BIN) ./cmd/plugin/proc

wasm: $(WASM_EXEC)
	mkdir -p web
	GOOS=js GOARCH=wasm go build -o $(WASM) ./client/wasm

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

# check is the per-commit verification gate: every commit must leave all four
# green. The wasm build catches GOOS=js breakage that `go build ./...` (host
# arch) misses; the typecheck catches Electron-side TS drift. No display or
# network needed.
check:
	go build ./...
	go test ./...
	GOOS=js GOARCH=wasm go build -o /tmp/gridwell.wasm ./client/wasm
	cd $(DESKTOP) && npm run typecheck

# check-electron runs the live-tile harnesses under a virtual display. Needed
# only for phases that touch the URL/shell live path (and the final pass), since
# they exercise the real Electron WebContentsView / PTY bridge. Requires xvfb +
# a prior `make vendor` for node_modules.
check-electron: node-modules
	cd $(DESKTOP) && npm run test:integration && npm run test:bridge

# serve runs the backend on its own (the desktop app spawns it as a sidecar;
# this target is for poking at the RPC/SSE surface or loading the wasm client
# in a plain browser — note live URL tiles only work inside the Electron app).
serve: build
	$(BIN) serve --db ./gridwell.db $(SERVE_FLAGS)

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
# TS, and launch Electron against this repo's gridwell.db so your existing
# grids are right there. Runs WITH Chromium's OS sandbox on (this box's kernel
# allows unprivileged user namespaces, so no setuid helper is needed) — live
# URL tiles load untrusted web content, so the sandbox is the containment that
# matters. Needs a prior `make vendor` for node_modules.
#
#   make launch                         # use ./gridwell.db
#   make launch LAUNCH_DB=/path/to.db   # use another db
LAUNCH_DB ?= $(CURDIR)/gridwell.db
launch: build node-modules
	cd $(DESKTOP) && npm run build && \
		GRIDWELL_DB="$(LAUNCH_DB)" ./node_modules/.bin/electron .

# node-modules guards the offline targets: if the desktop deps aren't present,
# point the user at the single online bootstrap instead of silently reaching
# for the network.
node-modules:
	@test -d $(DESKTOP)/node_modules || { \
		echo "$(DESKTOP)/node_modules missing — run 'make vendor' once (online) first"; \
		exit 1; \
	}

clean:
	rm -f $(BIN) $(LOCALDB_BIN) $(FS_BIN) $(PROC_BIN) $(WASM) $(WASM_EXEC)
	rm -rf $(DESKTOP)/dist $(DESKTOP)/out
