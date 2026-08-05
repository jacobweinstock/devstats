package render

import (
	"fmt"
	"io"
	"strconv"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"

	"github.com/jacobweinstock/devstats/internal/contrib"
)

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Padding(0, 1)
	loginStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14")).Padding(0, 1)
	barStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Padding(0, 1)
	numStyle    = lipgloss.NewStyle().Align(lipgloss.Right).Padding(0, 1)
	dimStyle    = lipgloss.NewStyle().Faint(true).Padding(0, 1)
)

// Table writes a colored leaderboard table to w (intended for a TTY).
func Table(w io.Writer, rows []contrib.Row, m Meta) {
	top := 0
	for _, r := range rows {
		if v := r.Counts.Contributions(); v > top {
			top = v
		}
	}
	totals := contrib.Totals(rows)

	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("240"))).
		Headers("#", "login", "contribs", "", "commits", "issues", "prs", "reviews", "comments").
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				if col == 1 {
					return headerStyle
				}
				return headerStyle.Align(lipgloss.Right)
			}
			switch col {
			case 1:
				return loginStyle
			case 3:
				return barStyle
			case 0, 2, 4, 5, 6, 7, 8:
				return numStyle
			default:
				return dimStyle
			}
		})

	for _, r := range rows {
		c := r.Counts
		t.Row(
			strconv.Itoa(r.Rank),
			r.Login,
			strconv.Itoa(c.Contributions()),
			sparkBar(c.Contributions(), top, 8),
			dot(c.Commits, "·"),
			dot(c.Issues, "·"),
			dot(c.PRs, "·"),
			dot(c.Reviews, "·"),
			dot(c.Comments, "·"),
		)
	}
	t.Row(
		"", "total", strconv.Itoa(totals.Contributions()), "",
		strconv.Itoa(totals.Commits), strconv.Itoa(totals.Issues), strconv.Itoa(totals.PRs),
		strconv.Itoa(totals.Reviews), strconv.Itoa(totals.Comments),
	)

	fmt.Fprintf(w, "%s contributors  %s → %s  (%d repos, %d of %d shown)\n",
		m.Org, m.Since, m.Until, m.NumRepos, len(rows), m.NumTotal)
	fmt.Fprintln(w, t.Render())
}
