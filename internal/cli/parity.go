package cli

// gridwell parity — the migration oracle's CLI face (docs/v2-design.md
// §8.4). Crawls two running nodes serving the same logical data (the
// old binary on the original home, the new binary on the converted
// home) and prints every difference. Exit 0 = parity; 1 = differences;
// 2 = usage/crawl failure.

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/parity"
	"github.com/josephburnett/gridwell/internal/server"
)

// RunParity implements `gridwell parity --a URL --b URL`.
func RunParity(args []string) int {
	fs := flag.NewFlagSet("parity", flag.ContinueOnError)
	aURL := fs.String("a", "", "base URL of side A (e.g. http://127.0.0.1:10010)")
	bURL := fs.String("b", "", "base URL of side B")
	password := fs.String("password", "", "server password (both sides); sets the auth cookie")
	includeTransit := fs.Bool("include-transit", false, "descend through mounts (default: compare each node directly)")
	skipContent := fs.Bool("skip-content", false, "structural pass only: no content hashing")
	skipPreviews := fs.Bool("skip-previews", false, "skip preview hashing")
	maxGrids := fs.Int("max-grids", 0, "abort past this many grids (0 = unlimited)")
	ignore := fs.String("ignore-fields", "", "comma-separated field names to ignore (each is a deliberate, named blind spot)")
	volatile := fs.String("volatile-ns", "", "comma-separated namespaces whose entry sets churn (presence/content diffs suppressed)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *aURL == "" || *bURL == "" {
		fmt.Fprintln(os.Stderr, "parity: --a and --b are required")
		return 2
	}

	clA, err := parityClient(*aURL, *password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parity: side A: %v\n", err)
		return 2
	}
	clB, err := parityClient(*bURL, *password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parity: side B: %v\n", err)
		return 2
	}

	opts := parity.Options{
		IncludeTransit: *includeTransit,
		SkipContent:    *skipContent,
		SkipPreviews:   *skipPreviews,
		MaxGrids:       *maxGrids,
	}
	sa, sb, err := parity.CrawlPair(context.Background(), clA, clB, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parity: crawl: %v\n", err)
		return 2
	}

	pol := parity.Policy{IgnoreFields: csvSet(*ignore), VolatileNS: csvSet(*volatile)}
	diffs := parity.Diff(sa, sb, pol)
	fmt.Printf("parity: %d grids A / %d grids B, %d tiles A / %d tiles B, %d skipped (transit)\n",
		len(sa.Grids), len(sb.Grids), len(sa.Tiles), len(sb.Tiles), len(sa.Skipped))
	for _, s := range sa.Skipped {
		fmt.Printf("skipped: %s\n", s)
	}
	if len(diffs) == 0 {
		fmt.Println("PARITY: no differences")
		return 0
	}
	for _, d := range diffs {
		fmt.Println(d)
	}
	fmt.Printf("PARITY FAILED: %d differences\n", len(diffs))
	return 1
}

func csvSet(s string) map[string]bool {
	if s == "" {
		return nil
	}
	out := map[string]bool{}
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out[f] = true
		}
	}
	return out
}

// parityClient builds the typed client, pre-setting the auth cookie the
// way the desktop sidecar does (server.AuthToken — one derivation).
func parityClient(baseURL, password string) (*rpc.Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{}
	if password != "" {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
		jar.SetCookies(u, []*http.Cookie{{Name: server.AuthCookieName, Value: server.AuthToken(password)}})
		hc.Jar = jar
	}
	return rpc.NewClient(hc, baseURL, connect.WithProtoJSON()), nil
}
