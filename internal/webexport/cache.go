package webexport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jacobweinstock/devstats/internal/contrib"
)

// shardRE matches a month-shard cache filename, capturing the repo and month.
// The type token is fixed so the repo (which may contain dots/hyphens) parses
// unambiguously; the event Kind is already stored inside each shard, so only
// the repo dimension needs recovering here.
var shardRE = regexp.MustCompile(
	`^(.+)_(?:issues|prs|reviews|issue_comments|pr_comments|commit_comments|commits-[01])_(\d{4}-\d{2})\.json$`,
)

// BuildFromCache assembles the event list from every month-shard already on
// disk under cacheDir, ignoring any date range. This is what `--serve` uses so
// it always reflects the full cached history rather than a pre-generated,
// possibly range-limited events.json.
func BuildFromCache(cacheDir, org string) (Data, error) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return Data{}, err
	}
	perRepo := make(map[string][]contrib.Event)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := shardRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue // not a month shard (e.g. old range-keyed or repo-list files)
		}
		b, err := os.ReadFile(filepath.Join(cacheDir, e.Name()))
		if err != nil {
			continue
		}
		var evs []contrib.Event
		if json.Unmarshal(b, &evs) != nil {
			continue
		}
		perRepo[m[1]] = append(perRepo[m[1]], evs...)
	}
	return Build(org, perRepo), nil
}
