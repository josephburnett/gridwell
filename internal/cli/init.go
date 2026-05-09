package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/josephburnett/ascent/internal/store"
)

// RunInit creates the SQLite database and applies the schema. Idempotent:
// running twice does nothing harmful — the schema uses CREATE TABLE IF NOT
// EXISTS.
func RunInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	db := resolveDB(fs, "./ascent.db")
	args = reorderFlagsFirst(args, func(name string) bool { return name == "db" })
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		return 1
	}
	defer s.Close()
	fmt.Printf("ascent: initialized database at %s\n", *db)
	return 0
}
