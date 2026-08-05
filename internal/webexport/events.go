// Package webexport builds the static data file consumed by the GitHub Pages
// front-end: a compact, day-precise list of every contribution event. The
// browser filters this list by arbitrary date range, repo, and metric, so no
// server or database is needed. See docs/PLAN.md §4.
package webexport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jacobweinstock/devstats/internal/contrib"
)

// kindNames indexes contrib.Kind values (KindCommit=0 … KindComment=4).
var kindNames = []string{"commit", "issue", "pr", "review", "comment"}

// event is one contribution, encoded positionally as
// [loginIdx, repoIdx, kindIdx, "YYYY-MM-DD"] to keep the payload small.
type event struct {
	login int
	repo  int
	kind  int
	day   string
}

func (e event) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{e.login, e.repo, e.kind, e.day})
}

// Data is the shape written to events.json. logins and repos are index spaces
// referenced by each event's loginIdx/repoIdx.
type Data struct {
	Schema      int      `json:"schema"`
	Org         string   `json:"org"`
	GeneratedAt string   `json:"generated_at"`
	Kinds       []string `json:"kinds"`
	Logins      []string `json:"logins"`
	Repos       []string `json:"repos"`
	BotLogins   []string `json:"bot_logins"`
	Events      []event  `json:"events"`
}

// Build assembles the event list from per-repo events. Events with an empty
// login (unattributed activity) are dropped. Output ordering is stable so daily
// regenerations produce minimal diffs.
func Build(org string, perRepo map[string][]contrib.Event) Data {
	repos := make([]string, 0, len(perRepo))
	loginSet := make(map[string]struct{})
	for repo, events := range perRepo {
		repos = append(repos, repo)
		for _, e := range events {
			if e.Login != "" {
				loginSet[e.Login] = struct{}{}
			}
		}
	}
	sort.Strings(repos)

	logins := make([]string, 0, len(loginSet))
	for l := range loginSet {
		logins = append(logins, l)
	}
	sort.Strings(logins)

	loginIdx := make(map[string]int, len(logins))
	for i, l := range logins {
		loginIdx[l] = i
	}
	repoIdx := make(map[string]int, len(repos))
	for i, r := range repos {
		repoIdx[r] = i
	}

	var events []event
	for repo, evs := range perRepo {
		ri := repoIdx[repo]
		for _, e := range evs {
			if e.Login == "" {
				continue
			}
			events = append(events, event{
				login: loginIdx[e.Login],
				repo:  ri,
				kind:  int(e.Kind),
				day:   e.At.UTC().Format("2006-01-02"),
			})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		a, b := events[i], events[j]
		switch {
		case a.day != b.day:
			return a.day < b.day
		case a.login != b.login:
			return a.login < b.login
		case a.repo != b.repo:
			return a.repo < b.repo
		default:
			return a.kind < b.kind
		}
	})

	var bots []string
	for _, l := range logins {
		if contrib.IsBot(l) {
			bots = append(bots, l)
		}
	}

	return Data{
		Schema:      1,
		Org:         org,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Kinds:       kindNames,
		Logins:      logins,
		Repos:       repos,
		BotLogins:   bots,
		Events:      events,
	}
}

// WriteJSON writes d to <dir>/events.json, creating dir if needed.
func WriteJSON(dir string, d Data) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "events.json"), b, 0o644)
}
