package cache

import "time"

// FetchRange composes the items in [since, until) from per-month cache shards
// keyed "<prefix>/<YYYY-MM>". This makes the cache range-independent: any two
// overlapping ranges reuse the same month shards instead of each being its own
// exact-range entry.
//
// Months strictly before volatileFrom are cached permanently (a closed month
// can no longer gain events). Months on or after volatileFrom are always
// refetched and rewritten, since an open month is still accumulating activity.
//
// To keep a cold backfill efficient, missing (and volatile) months are not
// fetched one call at a time — most of the underlying GitHub queries have no
// server-side lower bound and would re-walk everything newer than each month.
// Instead, contiguous runs of to-fetch months are fetched in a single call over
// the whole span, then partitioned into per-month shards by item timestamp.
//
// fetchMonth must return the items created within the [start, end) it is given
// (called with a whole-run span, not necessarily one month). at extracts an
// item's timestamp, used both to partition a span into months and to trim the
// concatenated result to the exact [since, until) window.
func FetchRange[T any](
	c *Cache,
	prefix string,
	since, until, volatileFrom time.Time,
	at func(T) time.Time,
	fetchMonth func(start, end time.Time) ([]T, error),
) ([]T, error) {
	months := monthsBetween(since, until)
	if len(months) == 0 {
		return nil, nil
	}

	results := make(map[string][]T, len(months))

	// flush bulk-fetches one contiguous run of to-fetch months in a single call
	// and partitions the returned items into their month shards.
	flush := func(run []time.Time) error {
		if len(run) == 0 {
			return nil
		}
		spanStart := run[0]
		spanEnd := run[len(run)-1].AddDate(0, 1, 0)
		items, err := fetchMonth(spanStart, spanEnd)
		if err != nil {
			return err
		}
		bucket := make(map[string][]T, len(run))
		for _, it := range items {
			bucket[at(it).UTC().Format("2006-01")] = append(bucket[at(it).UTC().Format("2006-01")], it)
		}
		for _, m := range run {
			k := m.Format("2006-01")
			results[k] = bucket[k]
			overwrite(c, prefix+"/"+k, bucket[k]) // write shard, empty months included
		}
		return nil
	}

	var run []time.Time
	for _, m := range months {
		k := m.Format("2006-01")
		if m.Before(volatileFrom) {
			if items, ok := readCached[T](c, prefix+"/"+k); ok {
				if err := flush(run); err != nil {
					return nil, err
				}
				run = nil
				results[k] = items
				continue
			}
		}
		run = append(run, m) // missing closed month, or volatile month
	}
	if err := flush(run); err != nil {
		return nil, err
	}

	var out []T
	for _, m := range months {
		out = append(out, results[m.Format("2006-01")]...)
	}
	trimmed := out[:0]
	for _, it := range out {
		t := at(it)
		if !t.Before(since) && t.Before(until) {
			trimmed = append(trimmed, it)
		}
	}
	return trimmed, nil
}

// monthsBetween returns the first-of-month (UTC) timestamp for every calendar
// month that [since, until) touches.
func monthsBetween(since, until time.Time) []time.Time {
	if !until.After(since) {
		return nil
	}
	start := time.Date(since.Year(), since.Month(), 1, 0, 0, 0, 0, time.UTC)
	var months []time.Time
	for m := start; m.Before(until); m = m.AddDate(0, 1, 0) {
		months = append(months, m)
	}
	return months
}
