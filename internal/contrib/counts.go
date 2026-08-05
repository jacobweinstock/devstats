package contrib

// Counts is a per-contributor tally across the five contribution kinds.
type Counts struct {
	Commits  int `json:"commits"`
	Issues   int `json:"issues"`
	PRs      int `json:"prs"`
	Reviews  int `json:"reviews"`
	Comments int `json:"comments"`
}

// Contributions is the total across all kinds.
func (c Counts) Contributions() int {
	return c.Commits + c.Issues + c.PRs + c.Reviews + c.Comments
}

// Add folds o into c.
func (c *Counts) Add(o Counts) {
	c.Commits += o.Commits
	c.Issues += o.Issues
	c.PRs += o.PRs
	c.Reviews += o.Reviews
	c.Comments += o.Comments
}
