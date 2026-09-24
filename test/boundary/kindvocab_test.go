package boundary

import (
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The tile kinds by their spelling, each named for the constant that owns it
// in api/rpc/types.go.
var kindWords = map[string]string{
	"well": "rpc.KindWell", "text": "rpc.KindText", "url": "rpc.KindURL",
	"shell": "rpc.KindShell", "pane": "rpc.KindPane",
}

// kindLiteralOK names each non-test file allowed to write a kind word as a
// string, and why its word is not a kind. A file-wide entry is deliberate: a
// reason names the word it covers, so a new kind literal in one of these files
// arrives with a reader who already knows the file is exempt.
var kindLiteralOK = map[string]string{
	"api/rpc/types.go": "the owner: the kind, glyph and text-mode vocabularies are spelled here",

	// The store is storage: a kind here is a column value, and its SQL must
	// keep spelling what the rows already hold.
	"internal/local/store/external.go": "storage: the kind written into a synthetic source row",
	"internal/local/store/shell.go":    "storage: the kind written into a shell tile's INSERT",

	// The client's own vocabularies, which collide with a kind word by
	// coincidence and mean something else.
	"client/wasm/create_tile.go":         `"url" is an errsurface source label`,
	"client/wasm/render.go":              "the palette's display and alt-text names, a list that also holds \"markdown\"",
	"client/wasm/shell_stream_client.go": `"shell" is an errsurface source label and "text" a DOM property`,
	"client/wasm/testhook.go":            `"pane" is a crumb shape the hook pairs with "chain", "text" a crumb field`,
	"client/traceevent/traceevent.go":    `"pane" is a trace record's src and its key for a pane id, and "text" names the save queue`,
	"client/wasm/url_modal.go":           `"url" is a DOM dataset property`,
	"client/wasm/webview_bridge.go":      `"url" is a bridge event property`,

	"internal/plugintest/gitlabfake/gitlabfake.go": `"url" is a plugin config key`,
}

// No non-test Go file outside the owners above writes a tile kind as a string.
// A re-spelling reads as ordinary code at review, and the kinds are how every
// dispatch partitions a tile, so a stray literal is a second definition of the
// vocabulary that nothing tells you has drifted.
func TestKindVocabularyOwner(t *testing.T) {
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if pruned(root, path, d) || d.Name() == "gen" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, ok := kindLiteralOK[rel]; ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Tokens, not lines: a comment that says "url" is prose, not a second
		// spelling of the vocabulary.
		fset := token.NewFileSet()
		file := fset.AddFile(rel, fset.Base(), len(data))
		var sc scanner.Scanner
		sc.Init(file, data, nil, 0)
		for {
			pos, tok, lit := sc.Scan()
			if tok == token.EOF {
				return nil
			}
			if tok != token.STRING {
				continue
			}
			v, uerr := strconv.Unquote(lit)
			if uerr != nil {
				continue
			}
			konst, ok := kindWords[v]
			if !ok {
				continue
			}
			t.Errorf("%s:%d writes the tile kind %s — the kind vocabulary has one owner, api/rpc/types.go; "+
				"use %s or the predicate that reads it (rpc.IsWellKind, rpc.IsBodyKind, "+
				"rpc.IsContentDescentKind, rpc.IsWorkspaceKind), or add this file to kindLiteralOK with the "+
				"reason its word is not a kind",
				rel, fset.Position(pos).Line, lit, konst)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}
