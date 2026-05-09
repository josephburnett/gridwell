.PHONY: build wasm test test-cover serve clean

BIN := ./ascent
WASM := ./web/ascent.wasm
WASM_EXEC := ./web/wasm_exec.js
GOROOT := $(shell go env GOROOT)

build: $(BIN) wasm

$(BIN):
	go build -o $(BIN) ./cmd/ascent

wasm: $(WASM) $(WASM_EXEC)

$(WASM):
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
	$(BIN) serve --db ./ascent.db

clean:
	rm -f $(BIN) $(WASM) $(WASM_EXEC)
