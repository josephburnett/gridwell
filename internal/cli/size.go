package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/josephburnett/gridwell/client/fileinfo"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// RunSize reports what gridwell.db holds, read-only, so it may run beside a
// serving node.
//
//	gridwell size [--json]
func RunSize(args []string) int {
	fs := flag.NewFlagSet("size", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	_ = fs.Parse(args)
	dir, err := config.Dir()
	if err != nil {
		return die("size", err)
	}
	if err := sizeOf(os.Stdout, dir, *asJSON); err != nil {
		return die("size", err)
	}
	return 0
}

// RunClean removes what nothing reaches in gridwell.db, then compacts the
// file. It refuses while the node serves, and without --yes it only reports.
//
//	gridwell clean [--yes]
func RunClean(args []string) int {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	yes := fs.Bool("yes", false, "remove what nothing reaches")
	_ = fs.Parse(args)
	dir, err := config.Dir()
	if err != nil {
		return die("clean", err)
	}
	return clean(os.Stdout, dir, *yes)
}

func clean(w io.Writer, dir string, yes bool) int {
	banner, serving, err := probeServeLock(dir)
	if err != nil {
		return die("clean", err)
	}
	if serving {
		return die("clean", fmt.Errorf("refusing: the node is serving (%s); stop it first", banner))
	}
	// Held until clean ends, so no serve starts over the file meanwhile.
	lock, err := acquireServeLock(dir)
	if err != nil {
		return die("clean", err)
	}
	defer lock.Release()
	path := config.DBFile(dir)
	if _, err := os.Stat(path); err != nil {
		return die("clean", err)
	}
	id, err := nodeID(dir)
	if err != nil {
		return die("clean", err)
	}
	st, err := local.OpenVerified(path, id, "home")
	if err != nil {
		return die("clean", err)
	}
	defer st.Close()
	ctx := context.Background()
	before, err := st.Analysis(ctx)
	if err != nil {
		return die("clean", err)
	}
	printReport(w, dir, before)
	if !yes {
		fmt.Fprintf(w, "\n%d rows nothing reaches would go, and the file would be compacted; run again with --yes to remove them\n", before.Removable())
		return 0
	}
	fmt.Fprintln(w, "\n`gridwell backup DEST` keeps a copy first; removing what nothing reaches")
	if err := st.Clean(ctx); err != nil {
		return die("clean", err)
	}
	if err := st.Compact(ctx); err != nil {
		return die("clean", err)
	}
	after, err := st.Analysis(ctx)
	if err != nil {
		return die("clean", err)
	}
	fmt.Fprintln(w)
	printReport(w, dir, after)
	fmt.Fprintf(w, "\ngridwell.db: %s before, %s after\n", fileinfo.Size(dbBytes(before)), fileinfo.Size(dbBytes(after)))
	return 0
}

func sizeOf(w io.Writer, dir string, asJSON bool) error {
	id, err := nodeID(dir)
	if err != nil {
		return err
	}
	st, err := store.OpenReadOnly(config.DBFile(dir))
	if err != nil {
		return err
	}
	defer st.Close()
	st.SetNodeID(id)
	a, err := st.Analysis(context.Background())
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			FileBytes  int64          `json:"file_bytes"`
			WALBytes   int64          `json:"wal_bytes"`
			CacheBytes int64          `json:"cache_db_bytes"`
			Analysis   store.Analysis `json:"gridwell_db"`
		}{fileBytes(config.DBFile(dir)), fileBytes(config.DBFile(dir) + "-wal"), fileBytes(config.CacheFile(dir)), a})
	}
	printReport(w, dir, a)
	return nil
}

// nodeID is the id server.yaml gives the node, which its layouts qualify
// rows by; "" when none is minted yet.
func nodeID(dir string) (string, error) {
	cfg, err := config.Load(config.File(dir))
	if err != nil {
		return "", err
	}
	return cfg.ID, nil
}

func fileBytes(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// dbBytes is the database as SQLite sees it, the WAL's pages included; it is
// the file's size once the node stops. Every share is of it.
func dbBytes(a store.Analysis) int64 { return a.PageSize * a.Pages }

func printReport(w io.Writer, dir string, a store.Analysis) {
	total := dbBytes(a)
	share := func(n int64) string {
		if total == 0 {
			return ""
		}
		return fmt.Sprintf("%5.1f%%", 100*float64(n)/float64(total))
	}
	line := func(label string, count int, bytes int64) {
		fmt.Fprintf(w, "  %-42s %6d %7s %s\n", label, count, fileinfo.Size(bytes), share(bytes))
	}
	count := func(label string, n int) { fmt.Fprintf(w, "  %-42s %6d\n", label, n) }
	db := config.DBFile(dir)
	fmt.Fprintf(w, "gridwell.db  %s (%d pages of %s; file %s, wal %s); %s free, which a clean's VACUUM reclaims\n",
		fileinfo.Size(total), a.Pages, fileinfo.Size(a.PageSize), fileinfo.Size(fileBytes(db)), fileinfo.Size(fileBytes(db+"-wal")), fileinfo.Size(a.FreePages*a.PageSize))
	fmt.Fprintf(w, "cache.db     %s, disposable, rewarmed by use\n", fileinfo.Size(fileBytes(config.CacheFile(dir))))
	fmt.Fprintln(w, "\ntables (dbstat pages, indexes folded in)")
	for _, t := range a.Tables {
		fmt.Fprintf(w, "  %-42s        %7s %s\n", t.Name, fileinfo.Size(t.Bytes), share(t.Bytes))
	}
	fmt.Fprintf(w, "\ntiles %-10s", "")
	for _, p := range store.Places {
		fmt.Fprintf(w, " %10s", p)
	}
	fmt.Fprintln(w)
	for _, k := range kindOrder(a) {
		fmt.Fprintf(w, "  %-14s", k)
		for _, p := range store.Places {
			fmt.Fprintf(w, " %10d", a.Kinds[k][p])
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "\nblobs")
	line("content", a.Content.Count, a.Content.Bytes)
	line("previews", a.Previews.Count, a.Previews.Bytes)
	line("only the trash names", a.TrashOnly.Count, a.TrashOnly.Bytes)
	fmt.Fprintln(w, "\nwhat nothing reaches (clean removes it; its tiles are the unreached and gone_grid columns)")
	count("grids no root or door reaches", a.UnreachedGrids)
	count("ephemeral visits no layout names", a.UnreachedScratch)
	line("blobs no row names", a.Unnamed.Count, a.Unnamed.Bytes)
	line("blobs only those rows name", a.UnreachedOnly.Count, a.UnreachedOnly.Bytes)
	fmt.Fprintln(w, "\nfound, never removed")
	count("refcounts off their rows (clean sets them)", a.RefcountDrift)
	count("rows naming a gone blob", a.MissingBlobs)
	count("doors owning a gone grid", a.MissingGrids)
}

// kindOrder is the kinds the analysis counted, in name order.
func kindOrder(a store.Analysis) []string {
	var out []string
	for k := range a.Kinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
