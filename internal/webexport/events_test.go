package webexport

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jacobweinstock/devstats/internal/contrib"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestBuild(t *testing.T) {
	perRepo := map[string][]contrib.Event{
		"smee": {
			{Login: "alice", Kind: contrib.KindCommit, At: day("2025-05-14")},
			{Login: "", Kind: contrib.KindCommit, At: day("2025-05-14")}, // dropped
		},
		"tink": {
			{Login: "bob", Kind: contrib.KindPR, At: day("2025-05-20")},
			{Login: "dependabot[bot]", Kind: contrib.KindCommit, At: day("2025-05-01")},
		},
	}

	d := Build("tinkerbell", perRepo)

	if got, want := len(d.Events), 3; got != want {
		t.Fatalf("events = %d, want %d (empty login must be dropped)", got, want)
	}
	if got := d.Logins; len(got) != 3 || got[0] != "alice" {
		t.Fatalf("logins not sorted/deduped: %v", got)
	}
	if got := d.Repos; got[0] != "smee" || got[1] != "tink" {
		t.Fatalf("repos not sorted: %v", got)
	}
	// events must be sorted by day ascending.
	if d.Events[0].day > d.Events[len(d.Events)-1].day {
		t.Fatalf("events not sorted by day: %+v", d.Events)
	}
	// bot detection.
	if len(d.BotLogins) != 1 || d.BotLogins[0] != "dependabot[bot]" {
		t.Fatalf("bot_logins = %v, want [dependabot[bot]]", d.BotLogins)
	}

	// positional encoding round-trips to [loginIdx, repoIdx, kindIdx, day].
	b, err := json.Marshal(d.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	var row []any
	if err := json.Unmarshal(b, &row); err != nil {
		t.Fatal(err)
	}
	if len(row) != 4 {
		t.Fatalf("event encoding has %d fields, want 4: %s", len(row), b)
	}
}
