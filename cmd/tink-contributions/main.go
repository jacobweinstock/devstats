// Command tink-contributions prints a leaderboard of GitHub contributors for
// an org over a date range. A contribution is a commit authored, an issue or
// PR opened, a PR review submitted, or a comment authored.
//
// Auth uses GITHUB_TOKEN/GH_TOKEN, falling back to `gh auth token`.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"

	"github.com/jacobweinstock/devstats/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, ff.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := ff.NewFlagSet("tink-contributions")
	var (
		since       = fs.StringLong("since", "", "start of range (YYYY-MM-DD, inclusive, UTC) [required]")
		until       = fs.StringLong("until", "", "end of range (YYYY-MM-DD, exclusive, UTC) [required]")
		org         = fs.StringLong("org", "tinkerbell", "GitHub org to scan")
		format      = fs.StringLong("format", "", "output format: table, md, csv (default: table on TTY, md when piped)")
		breakdown   = fs.BoolLong("breakdown", "md/csv: include per-component columns")
		top         = fs.IntLong("top", 0, "show only the top N contributors (0 = all)")
		noMerges    = fs.BoolLong("no-merges", "exclude merge commits from the commit count")
		excludeBots = fs.BoolLong("exclude-bots", "filter out bot accounts")
		anonymize   = fs.BoolLong("anonymize", "replace logins with contributor-N")
		cacheDir    = fs.StringLong("cache", "./cache", "cache directory")
		noCache     = fs.BoolLong("no-cache", "disable read+write of the cache")
		refreshRepo = fs.BoolLong("refresh-repos", "re-fetch the repo list from GitHub")
		onlyRepos   = fs.StringLong("only-repos", "", "comma-separated subset of repo names to scan")
		concurrency = fs.IntLong("concurrency", 4, "number of repos fetched concurrently")
		heartbeat   = fs.DurationLong("heartbeat", 5*time.Second, "progress heartbeat interval (0 to disable)")
		emitWeb     = fs.StringLong("emit-web", "", "web-export mode: write <dir>/events.json for the static site and exit")
		serve       = fs.BoolLong("serve", "serve the static site over HTTP and exit")
		serveAddr   = fs.StringLong("serve-addr", ":8080", "address for --serve")
		siteDir     = fs.StringLong("site", "site", "directory served by --serve")
	)

	if err := ff.Parse(fs, args, ff.WithEnvVarPrefix("TINK")); err != nil {
		if errors.Is(err, ff.ErrHelp) {
			fmt.Fprintln(os.Stderr, ffhelp.Flags(fs))
			return err
		}
		return err
	}

	// Serving is a standalone mode: no GitHub access or date range needed;
	// it serves the full cache regardless of --since.
	if *serve {
		return app.Serve(ctx, *serveAddr, *siteDir, *cacheDir, *org)
	}

	if *since == "" || *until == "" {
		// Web export covers all of history by default, so it does not require
		// an explicit range: backfill from a pre-org date up to tomorrow (until
		// is exclusive, so tomorrow includes everything through today).
		if *emitWeb != "" {
			if *since == "" {
				*since = "2015-01-01"
			}
			if *until == "" {
				*until = time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
			}
		} else {
			fmt.Fprintln(os.Stderr, ffhelp.Flags(fs))
			return errors.New("--since and --until are required")
		}
	}
	sinceT, err := parseDate(*since)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	untilT, err := parseDate(*until)
	if err != nil {
		return fmt.Errorf("--until: %w", err)
	}
	if !untilT.After(sinceT) {
		return errors.New("--until must be after --since")
	}
	if *format != "" && *format != "table" && *format != "md" && *format != "csv" {
		return fmt.Errorf("--format: must be table, md, or csv")
	}
	if *concurrency < 1 {
		return errors.New("--concurrency must be >= 1")
	}

	cfg := app.Config{
		Org:          *org,
		Since:        sinceT,
		Until:        untilT,
		Format:       *format,
		Breakdown:    *breakdown,
		Top:          *top,
		NoMerges:     *noMerges,
		ExcludeBots:  *excludeBots,
		Anonymize:    *anonymize,
		CacheDir:     *cacheDir,
		NoCache:      *noCache,
		RefreshRepos: *refreshRepo,
		OnlyRepos:    splitCSV(*onlyRepos),
		Concurrency:  *concurrency,
		Heartbeat:    *heartbeat,
		EmitWebDir:   *emitWeb,
	}
	return app.Run(ctx, cfg, os.Stdout)
}

func parseDate(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected YYYY-MM-DD, got %q", s)
	}
	return t, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
