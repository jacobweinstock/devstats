package cache

import (
	"testing"
	"time"
)

type item struct {
	At time.Time
}

func mustDay(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestMonthsBetween(t *testing.T) {
	got := monthsBetween(mustDay("2025-05-15"), mustDay("2025-08-02"))
	want := []string{"2025-05", "2025-06", "2025-07", "2025-08"}
	if len(got) != len(want) {
		t.Fatalf("got %d months, want %d: %v", len(got), len(want), got)
	}
	for i, m := range got {
		if m.Format("2006-01") != want[i] {
			t.Fatalf("month[%d] = %s, want %s", i, m.Format("2006-01"), want[i])
		}
	}
	if monthsBetween(mustDay("2025-05-01"), mustDay("2025-05-01")) != nil {
		t.Fatal("empty range should yield no months")
	}
}

// TestFetchRangeReusesShardsAcrossRanges is the core regression test: a second,
// overlapping range must hit the cache for shared months and never re-invoke
// the fetcher for them. It also asserts a cold multi-month range is satisfied
// by a single bulk fetch, not one call per month.
func TestFetchRangeReusesShardsAcrossRanges(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, false)

	calls := 0
	at := func(it item) time.Time { return it.At }
	// fetch returns one item per whole month within [start, end).
	fetch := func(start, end time.Time) ([]item, error) {
		calls++
		var items []item
		for m := start; m.Before(end); m = m.AddDate(0, 1, 0) {
			items = append(items, item{At: m.AddDate(0, 0, 14)})
		}
		return items, nil
	}

	// volatileFrom in the far future so every month is treated as closed.
	volatile := mustDay("2999-01-01")

	// First range: May 2025 .. Apr 2026 (12 months), all missing → one bulk call.
	r1, err := FetchRange(c, "repo/commits", mustDay("2025-05-01"), mustDay("2026-05-01"), volatile, at, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1) != 12 {
		t.Fatalf("range1 events = %d, want 12", len(r1))
	}
	if calls != 1 {
		t.Fatalf("cold 12-month range made %d fetch calls, want 1 (bulk)", calls)
	}

	// Second range: Jun 2025 .. Apr 2026 — strict subset; every month already cached.
	r2, err := FetchRange(c, "repo/commits", mustDay("2025-06-01"), mustDay("2026-05-01"), volatile, at, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2) != 11 {
		t.Fatalf("range2 events = %d, want 11", len(r2))
	}
	if calls != 1 {
		t.Fatalf("second overlapping range triggered new fetches: calls = %d, want 1", calls)
	}
}

func TestFetchRangeTrimsPartialMonths(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, false)
	at := func(it item) time.Time { return it.At }
	// Each month within the span yields events on the 1st, 14th, and 28th.
	fetch := func(start, end time.Time) ([]item, error) {
		var items []item
		for m := start; m.Before(end); m = m.AddDate(0, 1, 0) {
			items = append(items, item{At: m}, item{At: m.AddDate(0, 0, 13)}, item{At: m.AddDate(0, 0, 27)})
		}
		return items, nil
	}
	// Ask for May 15 .. Jun 15: should drop May 1 & 14, keep May 28, Jun 1 & 14.
	got, err := FetchRange(c, "k", mustDay("2025-05-15"), mustDay("2025-06-15"), mustDay("2999-01-01"), at, fetch)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range got {
		if it.At.Before(mustDay("2025-05-15")) || !it.At.Before(mustDay("2025-06-15")) {
			t.Fatalf("event %s outside requested window", it.At.Format("2006-01-02"))
		}
	}
	if len(got) != 3 {
		t.Fatalf("trimmed events = %d, want 3", len(got))
	}
}

func TestFetchRangeVolatileMonthAlwaysRefetched(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, false)
	at := func(it item) time.Time { return it.At }
	calls := 0
	fetch := func(start, end time.Time) ([]item, error) {
		calls++
		return []item{{At: start.AddDate(0, 0, 1)}}, nil
	}
	// Single-month range where that month is volatile (>= volatileFrom).
	win := func() {
		_, _ = FetchRange(c, "k", mustDay("2025-05-01"), mustDay("2025-06-01"), mustDay("2025-05-01"), at, fetch)
	}
	win()
	win()
	if calls != 2 {
		t.Fatalf("volatile month fetched %d times over 2 runs, want 2 (always refetch)", calls)
	}
}
