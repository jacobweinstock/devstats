package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/sync/errgroup"

	"github.com/jacobweinstock/devstats/internal/cache"
	"github.com/jacobweinstock/devstats/internal/contrib"
	"github.com/jacobweinstock/devstats/internal/github"
	"github.com/jacobweinstock/devstats/internal/render"
	"github.com/jacobweinstock/devstats/internal/webexport"
)

// Config is the resolved runtime configuration for a single run.
type Config struct {
	Org          string
	Since        time.Time
	Until        time.Time
	Format       string // "table", "md", "csv", or "" for auto
	Breakdown    bool
	Top          int
	NoMerges     bool
	ExcludeBots  bool
	Anonymize    bool
	CacheDir     string
	NoCache      bool
	RefreshRepos bool
	OnlyRepos    []string
	Concurrency  int
	Heartbeat    time.Duration

	// EmitWebDir, when non-empty, switches to web-export mode: write the raw
	// day-precise event list to <dir>/events.json and return, skipping the
	// leaderboard render.
	EmitWebDir string
}

// Run executes the leaderboard: list repos, fetch contributions per repo,
// aggregate, rank, and render to out.
func Run(ctx context.Context, cfg Config, out io.Writer) error {
	token, err := github.Token()
	if err != nil {
		return err
	}
	cl := github.New(token)
	ca := cache.New(cfg.CacheDir, cfg.NoCache)

	reposKey := "repos/" + cfg.Org
	if cfg.RefreshRepos {
		reposKey = ""
	}
	repos, err := fetchRepos(ctx, cl, ca, cfg.Org, reposKey)
	if err != nil {
		return fmt.Errorf("listing repos: %w", err)
	}
	if len(cfg.OnlyRepos) > 0 {
		want := make(map[string]struct{}, len(cfg.OnlyRepos))
		for _, r := range cfg.OnlyRepos {
			want[r] = struct{}{}
		}
		filtered := repos[:0]
		for _, r := range repos {
			if _, ok := want[r]; ok {
				filtered = append(filtered, r)
			}
		}
		repos = filtered
	}

	fmt.Fprintf(os.Stderr, "Scanning %d repos in %q from %s to %s\n",
		len(repos), cfg.Org, cfg.Since.Format("2006-01-02"), cfg.Until.Format("2006-01-02"))

	if cfg.EmitWebDir != "" {
		return runWebExport(ctx, cl, ca, cfg, repos)
	}

	prog := newProgress(len(repos), cfg.Heartbeat)
	prog.start()

	var (
		mu  sync.Mutex
		all []contrib.Event
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.Concurrency)
	for _, repo := range repos {
		g.Go(func() error {
			events, err := gatherRepo(gctx, cl, ca, cfg, repo)
			if err != nil {
				return fmt.Errorf("repo %s: %w", repo, err)
			}
			mu.Lock()
			all = append(all, events...)
			mu.Unlock()
			prog.repoDone(repo)
			return nil
		})
	}
	werr := g.Wait()
	prog.stop()
	if werr != nil {
		return werr
	}

	counts, display := contrib.Aggregate(all)
	rows, total := contrib.Rank(counts, display, contrib.RankOptions{
		ExcludeBots: cfg.ExcludeBots,
		Top:         cfg.Top,
		Anonymize:   cfg.Anonymize,
	})

	meta := render.Meta{
		Org:       cfg.Org,
		Since:     cfg.Since.Format("2006-01-02"),
		Until:     cfg.Until.Format("2006-01-02"),
		NumRepos:  len(repos),
		NumTotal:  total,
		Breakdown: cfg.Breakdown,
	}

	format := cfg.Format
	if format == "" {
		if f, ok := out.(*os.File); ok && isatty.IsTerminal(f.Fd()) {
			format = "table"
		} else {
			format = "md"
		}
	}
	switch format {
	case "table":
		render.Table(out, rows, meta)
	case "md":
		render.Markdown(out, rows, meta)
	case "csv":
		if err := render.CSV(out, rows, cfg.Breakdown); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown format %q", format)
	}
	return nil
}

// runWebExport gathers events per repo (keeping the repo dimension) and writes
// the raw day-precise event list to cfg.EmitWebDir/events.json for the static
// front-end. Bot filtering is deliberately not applied here: all events are
// emitted and the UI decides whether to exclude bots via the shipped bot list.
func runWebExport(ctx context.Context, cl *github.Client, ca *cache.Cache, cfg Config, repos []string) error {
	prog := newProgress(len(repos), cfg.Heartbeat)
	prog.start()

	var (
		mu      sync.Mutex
		perRepo = make(map[string][]contrib.Event, len(repos))
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.Concurrency)
	for _, repo := range repos {
		g.Go(func() error {
			events, err := gatherRepo(gctx, cl, ca, cfg, repo)
			if err != nil {
				return fmt.Errorf("repo %s: %w", repo, err)
			}
			mu.Lock()
			perRepo[repo] = events
			mu.Unlock()
			prog.repoDone(repo)
			return nil
		})
	}
	werr := g.Wait()
	prog.stop()
	if werr != nil {
		return werr
	}

	data := webexport.Build(cfg.Org, perRepo)
	if err := webexport.WriteJSON(cfg.EmitWebDir, data); err != nil {
		return fmt.Errorf("writing web export: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Wrote %d events (%d contributors, %d repos) to %s/events.json\n",
		len(data.Events), len(data.Logins), len(data.Repos), cfg.EmitWebDir)
	return nil
}

func fetchRepos(ctx context.Context, cl *github.Client, ca *cache.Cache, org, key string) ([]string, error) {
	if key == "" {
		return cl.ListRepos(ctx, org)
	}
	return cache.Fetch(ca, key, func() ([]string, error) {
		return cl.ListRepos(ctx, org)
	})
}

// gatherRepo runs all per-repo fetchers concurrently. Each fetcher's events are
// served from month-sharded cache (see cache.FetchRange), so overlapping ranges
// reuse the same shards and only the still-open month is refetched.
func gatherRepo(ctx context.Context, cl *github.Client, ca *cache.Cache, cfg Config, repo string) ([]contrib.Event, error) {
	merges := 0
	if cfg.NoMerges {
		merges = 1
	}
	volatileFrom := firstOfMonthUTC(time.Now())
	at := func(e contrib.Event) time.Time { return e.At }

	jobs := []struct {
		prefix string
		fn     func(since, until time.Time) ([]contrib.Event, error)
	}{
		{repo + "/issues", func(since, until time.Time) ([]contrib.Event, error) {
			return cl.IssuesOpened(ctx, cfg.Org, repo, since, until)
		}},
		{repo + "/prs", func(since, until time.Time) ([]contrib.Event, error) {
			return cl.PRsOpened(ctx, cfg.Org, repo, since, until)
		}},
		{repo + "/reviews", func(since, until time.Time) ([]contrib.Event, error) {
			return cl.Reviews(ctx, cfg.Org, repo, since, until)
		}},
		{repo + "/issue_comments", func(since, until time.Time) ([]contrib.Event, error) {
			return cl.IssueComments(ctx, cfg.Org, repo, since, until)
		}},
		{repo + "/pr_comments", func(since, until time.Time) ([]contrib.Event, error) {
			return cl.PRComments(ctx, cfg.Org, repo, since, until)
		}},
		{repo + "/commit_comments", func(since, until time.Time) ([]contrib.Event, error) {
			return cl.CommitComments(ctx, cfg.Org, repo, since, until)
		}},
		// The no-merges flag changes the result, so it is part of the shard key.
		{fmt.Sprintf("%s/commits-%d", repo, merges), func(since, until time.Time) ([]contrib.Event, error) {
			return cl.Commits(ctx, cfg.Org, repo, since, until, cfg.NoMerges)
		}},
	}

	var (
		mu  sync.Mutex
		out []contrib.Event
	)
	g, _ := errgroup.WithContext(ctx)
	for _, j := range jobs {
		g.Go(func() error {
			events, err := cache.FetchRange(ca, j.prefix, cfg.Since, cfg.Until, volatileFrom, at, j.fn)
			if err != nil {
				// A repo private to this token, renamed, or deleted can't be
				// resolved; skip it instead of failing the whole run.
				if github.IsRepoNotFound(err) {
					return nil
				}
				return err
			}
			mu.Lock()
			out = append(out, events...)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// firstOfMonthUTC returns midnight on the first day of t's month, in UTC.
func firstOfMonthUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
