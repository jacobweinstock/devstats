package contrib

import (
	"fmt"
	"sort"
	"strings"
)

// Row is a single ranked contributor.
type Row struct {
	Rank   int
	Login  string
	Counts Counts
}

// Aggregate folds events into per-login counts keyed by lowercased login,
// while remembering the first-seen original-case spelling of each login.
func Aggregate(events []Event) (counts map[string]Counts, display map[string]string) {
	counts = make(map[string]Counts)
	display = make(map[string]string)
	for _, e := range events {
		if e.Login == "" {
			continue
		}
		key := strings.ToLower(e.Login)
		if _, ok := display[key]; !ok {
			display[key] = e.Login
		}
		c := counts[key]
		switch e.Kind {
		case KindCommit:
			c.Commits++
		case KindIssue:
			c.Issues++
		case KindPR:
			c.PRs++
		case KindReview:
			c.Reviews++
		case KindComment:
			c.Comments++
		}
		counts[key] = c
	}
	return counts, display
}

// RankOptions controls filtering, truncation, and anonymization of the board.
type RankOptions struct {
	ExcludeBots bool
	Top         int
	Anonymize   bool
}

// Rank filters, sorts (by contributions desc, then login asc), and truncates
// aggregated counts into ranked rows. It returns the rows plus the total
// number of qualifying contributors before any Top truncation.
func Rank(counts map[string]Counts, display map[string]string, opts RankOptions) (rows []Row, total int) {
	type entry struct {
		login string
		key   string
		c     Counts
	}
	var list []entry
	for key, c := range counts {
		disp := display[key]
		if opts.ExcludeBots && IsBot(disp) {
			continue
		}
		if c.Contributions() <= 0 {
			continue
		}
		list = append(list, entry{login: disp, key: key, c: c})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].c.Contributions() != list[j].c.Contributions() {
			return list[i].c.Contributions() > list[j].c.Contributions()
		}
		return list[i].key < list[j].key
	})
	total = len(list)
	if opts.Top > 0 && opts.Top < len(list) {
		list = list[:opts.Top]
	}
	rows = make([]Row, len(list))
	for i, e := range list {
		login := e.login
		if opts.Anonymize {
			login = fmt.Sprintf("contributor-%d", i+1)
		}
		rows[i] = Row{Rank: i + 1, Login: login, Counts: e.c}
	}
	return rows, total
}

// Totals sums the counts across all rows.
func Totals(rows []Row) Counts {
	var t Counts
	for _, r := range rows {
		t.Add(r.Counts)
	}
	return t
}
