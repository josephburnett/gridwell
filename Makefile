.PHONY: build bin wasm test test-cover serve clean desktop desktop-dev desktop-dist

BIN := ./gridwell
WASM := ./web/gridwell.wasm
WASM_EXEC := ./web/wasm_exec.js
GOROOT := $(shell go env GOROOT)

# `bin` and `wasm` are phony so they always invoke `go build`. Go's
# build cache makes this fast when nothing changed, but it guarantees
# we never serve a stale binary or wasm artifact.
build: bin wasm

bin:
	go build -o $(BIN) ./cmd/gridwell

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

# serve runs the backend on its own (the desktop app spawns it as a sidecar;
# this target is for poking at the RPC/SSE surface or loading the wasm client
# in a plain browser — note live URL tiles only work inside the Electron app).
serve: build
	$(BIN) serve --db ./gridwell.db $(SERVE_FLAGS)

# SERVE_FLAGS passes extra flags through, e.g.
# `make serve SERVE_FLAGS="--bind 0.0.0.0:8080"`.
SERVE_FLAGS ?=

# Desktop app (Electron shell). `desktop-dev` runs it against this repo's
# freshly built sidecar + wasm; `desktop-dist` packages an unpacked app that
# bundles the host-arch sidecar + web assets under resources/. Cross-platform
# packaging cross-compiles the sidecar first (GOOS/GOARCH) before electron-
# builder — see apps/desktop/README.md.
desktop: build
	cd apps/desktop && npm install && npm run build

desktop-dev: build
	cd apps/desktop && npm install && npm start

desktop-dist: build
	cd apps/desktop && npm install && npm run dist:dir

clean:
	rm -f $(BIN) $(WASM) $(WASM_EXEC)
	rm -rf apps/desktop/dist apps/desktop/out
