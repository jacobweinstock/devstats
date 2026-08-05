package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/jacobweinstock/devstats/internal/contrib"
)

// Markdown writes a GitHub-flavored markdown leaderboard.
func Markdown(w io.Writer, rows []contrib.Row, m Meta) {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s contributors\n\n", m.Org)
	fmt.Fprintf(&b, "_%s → %s · %d repos · %d of %d contributors shown_\n\n",
		m.Since, m.Until, m.NumRepos, len(rows), m.NumTotal)

	totals := contrib.Totals(rows)
	if m.Breakdown {
		b.WriteString("| # | login | contributions | commits | issues | prs | reviews | comments |\n")
		b.WriteString("|--:|---|--:|--:|--:|--:|--:|--:|\n")
		for _, r := range rows {
			c := r.Counts
			fmt.Fprintf(&b, "| %d | [%s](https://github.com/%s) | %d | %d | %d | %d | %d | %d |\n",
				r.Rank, r.Login, r.Login, c.Contributions(), c.Commits, c.Issues, c.PRs, c.Reviews, c.Comments)
		}
		fmt.Fprintf(&b, "| | **total** | **%d** | **%d** | **%d** | **%d** | **%d** | **%d** |\n",
			totals.Contributions(), totals.Commits, totals.Issues, totals.PRs, totals.Reviews, totals.Comments)
	} else {
		b.WriteString("| # | login | contributions |\n")
		b.WriteString("|--:|---|--:|\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "| %d | [%s](https://github.com/%s) | %d |\n",
				r.Rank, r.Login, r.Login, r.Counts.Contributions())
		}
		fmt.Fprintf(&b, "| | **total** | **%d** |\n", totals.Contributions())
	}
	b.WriteString("\n")
	io.WriteString(w, b.String())
}
