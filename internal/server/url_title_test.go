package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExtractTitle(t *testing.T) {
	cases := map[string]string{
		`<html><head><title>Hello World</title></head>`:                  "Hello World",
		`<title>multi line\nbreaks   collapsed</title>`:                  `multi line\nbreaks collapsed`,
		`<title  data-x="y" >Attrs OK</title>`:                           "Attrs OK",
		`<TITLE>case insensitive</TITLE>`:                                "case insensitive",
		`<head><title>amp &amp; lt &lt;</title></head>`:                  "amp & lt <",
		`no title here`:                                                  "",
		`<title></title><title>second</title>`:                           "",
		`<head><title>first</title>`:                                     "first",
	}
	for in, want := range cases {
		got := ExtractTitle([]byte(in))
		if got != want {
			t.Errorf("ExtractTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTitleCacheHitsAndMisses(t *testing.T) {
	c := newTitleCache()
	calls := 0
	c.fetcher = func(ctx context.Context, url string, max int64) ([]byte, error) {
		calls++
		return []byte(`<title>example</title>`), nil
	}
	got, err := c.Get(context.Background(), "http://example.com")
	if err != nil || got != "example" {
		t.Fatalf("first: got %q err %v", got, err)
	}
	got2, _ := c.Get(context.Background(), "http://example.com")
	if got2 != "example" {
		t.Errorf("second: %q", got2)
	}
	if calls != 1 {
		t.Errorf("expected 1 fetch (cache hit), got %d", calls)
	}
}

func TestTitleCacheFetchFailureIsCached(t *testing.T) {
	c := newTitleCache()
	calls := 0
	c.fetcher = func(ctx context.Context, url string, max int64) ([]byte, error) {
		calls++
		return nil, errors.New("nope")
	}
	got, err := c.Get(context.Background(), "http://x.test")
	if err != nil {
		t.Errorf("Get returned error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty title, got %q", got)
	}
	_, _ = c.Get(context.Background(), "http://x.test")
	if calls != 1 {
		t.Errorf("expected 1 fetch (cached failure), got %d", calls)
	}
}

func TestTitleFetcherTimeout(t *testing.T) {
	// Run defaultFetcher against a URL we can't reach quickly. We just
	// verify it returns within a bounded time and gives an error rather
	// than hanging — exact error text is platform dependent.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := defaultFetcher(ctx, "http://10.255.255.1:1/unreachable", 1024)
	if err == nil {
		t.Fatal("expected error")
	}
	if took := time.Since(start); took > 6*time.Second {
		t.Errorf("fetcher took too long: %v", took)
	}
}

func TestTitleFetcherRejectsUnknownScheme(t *testing.T) {
	_, err := defaultFetcher(context.Background(), "file:///etc/passwd", 1024)
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Errorf("expected scheme error, got %v", err)
	}
}
