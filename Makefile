.PHONY: build bin wasm test test-cover serve clean

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

serve: build
	$(BIN) serve --db ./gridwell.db

clean:
	rm -f $(BIN) $(WASM) $(WASM_EXEC)
