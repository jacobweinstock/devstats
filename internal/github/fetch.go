package github

import (
	"context"
	"regexp"
	"sort"
	"time"

	"github.com/google/go-github/v75/github"
	"github.com/shurcooL/githubv4"

	"github.com/jacobweinstock/devstats/internal/contrib"
)

// Commits imported into the tinkerbell monorepo from now-archived repos were
// rewritten (new SHAs) but carry origin trailers. Drop them so the originals,
// which still live in the source repos we also scan, are not double-counted.
var legacyTrailerRE = regexp.MustCompile(`(?m)^(Legacy-Repo|Tinkerbell-Legacy-Original-SHA1):`)

// ListRepos returns all repository names in org, sorted ascending.
func (c *Client) ListRepos(ctx context.Context, org string) ([]string, error) {
	var q reposQuery
	vars := map[string]any{
		"org":    githubv4.String(org),
		"cursor": (*githubv4.String)(nil),
	}
	var repos []string
	for {
		if err := c.GraphQL.Query(ctx, &q, vars); err != nil {
			return nil, err
		}
		for _, n := range q.Organization.Repositories.Nodes {
			repos = append(repos, string(n.Name))
		}
		if !q.Organization.Repositories.PageInfo.HasNextPage {
			break
		}
		vars["cursor"] = githubv4.NewString(q.Organization.Repositories.PageInfo.EndCursor)
	}
	sort.Strings(repos)
	return repos, nil
}

// IssuesOpened returns issue-opened events in [since, until).
func (c *Client) IssuesOpened(ctx context.Context, org, repo string, since, until time.Time) ([]contrib.Event, error) {
	var q issuesQuery
	vars := map[string]any{
		"org":    githubv4.String(org),
		"repo":   githubv4.String(repo),
		"cursor": (*githubv4.String)(nil),
	}
	var out []contrib.Event
	for {
		if err := c.GraphQL.Query(ctx, &q, vars); err != nil {
			return nil, err
		}
		stop := false
		for _, n := range q.Repository.Issues.Nodes {
			at := n.CreatedAt.Time
			if at.Before(since) {
				stop = true
				continue
			}
			if !at.Before(until) {
				continue
			}
			out = append(out, contrib.Event{Login: string(n.Author.Login), Kind: contrib.KindIssue, At: at})
		}
		if stop || !bool(q.Repository.Issues.PageInfo.HasNextPage) {
			break
		}
		vars["cursor"] = githubv4.NewString(q.Repository.Issues.PageInfo.EndCursor)
	}
	return out, nil
}

// PRsOpened returns PR-opened events in [since, until).
func (c *Client) PRsOpened(ctx context.Context, org, repo string, since, until time.Time) ([]contrib.Event, error) {
	var q prsQuery
	vars := map[string]any{
		"org":    githubv4.String(org),
		"repo":   githubv4.String(repo),
		"cursor": (*githubv4.String)(nil),
	}
	var out []contrib.Event
	for {
		if err := c.GraphQL.Query(ctx, &q, vars); err != nil {
			return nil, err
		}
		stop := false
		for _, n := range q.Repository.PullRequests.Nodes {
			at := n.CreatedAt.Time
			if at.Before(since) {
				stop = true
				continue
			}
			if !at.Before(until) {
				continue
			}
			out = append(out, contrib.Event{Login: string(n.Author.Login), Kind: contrib.KindPR, At: at})
		}
		if stop || !bool(q.Repository.PullRequests.PageInfo.HasNextPage) {
			break
		}
		vars["cursor"] = githubv4.NewString(q.Repository.PullRequests.PageInfo.EndCursor)
	}
	return out, nil
}

// Reviews returns PR-review events submitted in [since, until). PRs are walked
// newest-updated first; paging stops once a PR was last updated before since.
func (c *Client) Reviews(ctx context.Context, org, repo string, since, until time.Time) ([]contrib.Event, error) {
	var q reviewsQuery
	vars := map[string]any{
		"org":    githubv4.String(org),
		"repo":   githubv4.String(repo),
		"cursor": (*githubv4.String)(nil),
	}
	var out []contrib.Event
	for {
		if err := c.GraphQL.Query(ctx, &q, vars); err != nil {
			return nil, err
		}
		stop := false
		for _, pr := range q.Repository.PullRequests.Nodes {
			if pr.UpdatedAt.Time.Before(since) {
				stop = true
				continue
			}
			for _, rv := range pr.Reviews.Nodes {
				at := rv.SubmittedAt.Time
				if at.IsZero() || at.Before(since) || !at.Before(until) {
					continue
				}
				out = append(out, contrib.Event{Login: string(rv.Author.Login), Kind: contrib.KindReview, At: at})
			}
		}
		if stop || !bool(q.Repository.PullRequests.PageInfo.HasNextPage) {
			break
		}
		vars["cursor"] = githubv4.NewString(q.Repository.PullRequests.PageInfo.EndCursor)
	}
	return out, nil
}

// Commits returns commit events authored in [since, until) on the default
// branch, dropping merge commits when noMerges is set and always dropping
// monorepo-imported commits identified by their origin trailers.
func (c *Client) Commits(ctx context.Context, org, repo string, since, until time.Time, noMerges bool) ([]contrib.Event, error) {
	var q commitsQuery
	vars := map[string]any{
		"org":    githubv4.String(org),
		"repo":   githubv4.String(repo),
		"since":  githubv4.GitTimestamp{Time: since},
		"until":  githubv4.GitTimestamp{Time: until},
		"cursor": (*githubv4.String)(nil),
	}
	var out []contrib.Event
	for {
		if err := c.GraphQL.Query(ctx, &q, vars); err != nil {
			return nil, err
		}
		ref := q.Repository.DefaultBranchRef
		if ref == nil {
			break
		}
		hist := ref.Target.Commit.History
		for _, n := range hist.Nodes {
			if noMerges && n.Parents.TotalCount > 1 {
				continue
			}
			if legacyTrailerRE.MatchString(string(n.MessageBody)) {
				continue
			}
			login := ""
			if n.Author.User != nil {
				login = string(n.Author.User.Login)
			}
			out = append(out, contrib.Event{Login: login, Kind: contrib.KindCommit, At: n.CommittedDate.Time})
		}
		if !hist.PageInfo.HasNextPage {
			break
		}
		vars["cursor"] = githubv4.NewString(hist.PageInfo.EndCursor)
	}
	return out, nil
}

// IssueComments returns repo-wide issue-comment events in [since, until).
func (c *Client) IssueComments(ctx context.Context, org, repo string, since, until time.Time) ([]contrib.Event, error) {
	opts := &github.IssueListCommentsOptions{
		Sort:        github.Ptr("created"),
		Direction:   github.Ptr("desc"),
		ListOptions: github.ListOptions{PerPage: 100},
	}
	var out []contrib.Event
	for {
		items, resp, err := c.REST.Issues.ListComments(ctx, org, repo, 0, opts)
		if err != nil {
			if is404(resp) {
				return out, nil
			}
			return nil, err
		}
		stop := false
		for _, it := range items {
			at := it.GetCreatedAt().Time
			if at.Before(since) {
				stop = true
				continue
			}
			if !at.Before(until) {
				continue
			}
			out = append(out, contrib.Event{Login: it.GetUser().GetLogin(), Kind: contrib.KindComment, At: at})
		}
		if stop || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// PRComments returns repo-wide PR review-comment events in [since, until).
func (c *Client) PRComments(ctx context.Context, org, repo string, since, until time.Time) ([]contrib.Event, error) {
	opts := &github.PullRequestListCommentsOptions{
		Sort:        "created",
		Direction:   "desc",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	var out []contrib.Event
	for {
		items, resp, err := c.REST.PullRequests.ListComments(ctx, org, repo, 0, opts)
		if err != nil {
			if is404(resp) {
				return out, nil
			}
			return nil, err
		}
		stop := false
		for _, it := range items {
			at := it.GetCreatedAt().Time
			if at.Before(since) {
				stop = true
				continue
			}
			if !at.Before(until) {
				continue
			}
			out = append(out, contrib.Event{Login: it.GetUser().GetLogin(), Kind: contrib.KindComment, At: at})
		}
		if stop || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// CommitComments returns repo-wide commit-comment events in [since, until).
// This endpoint has no server-side sort, so every page is scanned.
func (c *Client) CommitComments(ctx context.Context, org, repo string, since, until time.Time) ([]contrib.Event, error) {
	opts := &github.ListOptions{PerPage: 100}
	var out []contrib.Event
	for {
		items, resp, err := c.REST.Repositories.ListComments(ctx, org, repo, opts)
		if err != nil {
			if is404(resp) {
				return out, nil
			}
			return nil, err
		}
		for _, it := range items {
			at := it.GetCreatedAt().Time
			if at.Before(since) || !at.Before(until) {
				continue
			}
			out = append(out, contrib.Event{Login: it.GetUser().GetLogin(), Kind: contrib.KindComment, At: at})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func is404(resp *github.Response) bool {
	return resp != nil && resp.StatusCode == 404
}
