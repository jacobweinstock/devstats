package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jacobweinstock/devstats/internal/contrib"
)

func TestSparkBar(t *testing.T) {
	if got := sparkBar(0, 0, 8); got != strings.Repeat(" ", 8) {
		t.Errorf("sparkBar(0,0,8) = %q, want 8 spaces", got)
	}
	full := sparkBar(10, 10, 8)
	if got := []rune(full); len(got) != 8 || got[0] != '█' {
		t.Errorf("sparkBar(10,10,8) = %q, want 8 full blocks", full)
	}
	// Rune width is preserved even with partial blocks.
	if got := len([]rune(sparkBar(3, 10, 8))); got != 8 {
		t.Errorf("sparkBar width = %d runes, want 8", got)
	}
}

func sampleRows() []contrib.Row {
	return []contrib.Row{
		{Rank: 1, Login: "bob", Counts: contrib.Counts{Commits: 2, Issues: 1}},
		{Rank: 2, Login: "alice", Counts: contrib.Counts{PRs: 1}},
	}
}

func TestMarkdownBreakdown(t *testing.T) {
	var b bytes.Buffer
	Markdown(&b, sampleRows(), Meta{Org: "tinkerbell", Since: "2025-01-01", Until: "2025-02-01", NumRepos: 3, NumTotal: 2, Breakdown: true})
	out := b.String()
	for _, want := range []string{
		"# tinkerbell contributors",
		"[bob](https://github.com/bob)",
		"| 1 | [bob](https://github.com/bob) | 3 | 2 | 1 | 0 | 0 | 0 |",
		"**total** | **4** |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, out)
		}
	}
}

func TestCSVPlain(t *testing.T) {
	var b bytes.Buffer
	if err := CSV(&b, sampleRows(), false); err != nil {
		t.Fatal(err)
	}
	want := "rank,login,contributions\n1,bob,3\n2,alice,1\n"
	if b.String() != want {
		t.Errorf("csv =\n%q\nwant\n%q", b.String(), want)
	}
}

func TestCSVBreakdown(t *testing.T) {
	var b bytes.Buffer
	if err := CSV(&b, sampleRows(), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "rank,login,contributions,commits,issues,prs,reviews,comments") {
		t.Errorf("csv breakdown header missing:\n%s", b.String())
	}
}
