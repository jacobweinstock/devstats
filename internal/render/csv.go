package render

import (
	"encoding/csv"
	"io"
	"strconv"

	"github.com/jacobweinstock/devstats/internal/contrib"
)

// CSV writes the leaderboard as comma-separated values.
func CSV(w io.Writer, rows []contrib.Row, breakdown bool) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if breakdown {
		if err := cw.Write([]string{"rank", "login", "contributions", "commits", "issues", "prs", "reviews", "comments"}); err != nil {
			return err
		}
		for _, r := range rows {
			c := r.Counts
			if err := cw.Write([]string{
				strconv.Itoa(r.Rank), r.Login, strconv.Itoa(c.Contributions()),
				strconv.Itoa(c.Commits), strconv.Itoa(c.Issues), strconv.Itoa(c.PRs),
				strconv.Itoa(c.Reviews), strconv.Itoa(c.Comments),
			}); err != nil {
				return err
			}
		}
		return cw.Error()
	}

	if err := cw.Write([]string{"rank", "login", "contributions"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{strconv.Itoa(r.Rank), r.Login, strconv.Itoa(r.Counts.Contributions())}); err != nil {
			return err
		}
	}
	return cw.Error()
}
