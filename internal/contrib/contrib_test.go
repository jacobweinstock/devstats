package contrib

import (
	"testing"
	"time"
)

func TestIsBot(t *testing.T) {
	cases := map[string]bool{
		"":                   true,
		"dependabot":         true,
		"DependaBot":         true,
		"github-actions":     true,
		"some-bot":           true,
		"foo[bot]":           true,
		"copilot":            true,
		"jacobweinstock":     false,
		"mmlb":               false,
		"renovate":           true,
		"tinkerbell-ci":      true,
		"not-a-robot-really": false,
	}
	for login, want := range cases {
		if got := IsBot(login); got != want {
			t.Errorf("IsBot(%q) = %v, want %v", login, got, want)
		}
	}
}

func TestAggregateAndRank(t *testing.T) {
	at := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Login: "Alice", Kind: KindCommit, At: at},
		{Login: "alice", Kind: KindPR, At: at},
		{Login: "bob", Kind: KindIssue, At: at},
		{Login: "bob", Kind: KindReview, At: at},
		{Login: "bob", Kind: KindComment, At: at},
		{Login: "", Kind: KindCommit, At: at}, // unattributed, dropped
		{Login: "dependabot", Kind: KindPR, At: at},
	}

	counts, display := Aggregate(events)
	if display["alice"] != "Alice" {
		t.Errorf("display[alice] = %q, want Alice (first-seen case)", display["alice"])
	}
	if c := counts["alice"]; c.Commits != 1 || c.PRs != 1 || c.Contributions() != 2 {
		t.Errorf("alice counts = %+v, want 1 commit + 1 pr", c)
	}
	if _, ok := counts[""]; ok {
		t.Error("empty login should not be aggregated")
	}

	rows, total := Rank(counts, display, RankOptions{ExcludeBots: true})
	if total != 2 {
		t.Fatalf("total = %d, want 2 (bots excluded)", total)
	}
	// bob has 3 contributions, alice 2 -> bob ranks first.
	if rows[0].Login != "bob" || rows[0].Rank != 1 {
		t.Errorf("row0 = %+v, want bob rank 1", rows[0])
	}
	if rows[1].Login != "Alice" || rows[1].Rank != 2 {
		t.Errorf("row1 = %+v, want Alice rank 2", rows[1])
	}

	rows, _ = Rank(counts, display, RankOptions{ExcludeBots: true, Top: 1})
	if len(rows) != 1 || rows[0].Login != "bob" {
		t.Errorf("Top=1 rows = %+v, want just bob", rows)
	}

	rows, _ = Rank(counts, display, RankOptions{ExcludeBots: true, Anonymize: true})
	if rows[0].Login != "contributor-1" {
		t.Errorf("anonymized row0 login = %q, want contributor-1", rows[0].Login)
	}
}
