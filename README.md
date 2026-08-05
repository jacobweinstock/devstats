# devstats

Prints a leaderboard of GitHub contributors for an organization over a date
range. A **contribution** is any of:

- a commit authored
- an issue opened
- a pull request opened
- a PR review submitted
- a comment authored (on an issue, a PR review, or a commit)

The tool walks every repository in the org (via the GitHub GraphQL API) and
tallies these events per author, then renders a ranked table. The command is
`tink-contributions`, documented below.

## Requirements

- Go 1.26+
- A GitHub token with `public_repo` scope. The tool reads it from
  `GITHUB_TOKEN` or `GH_TOKEN`, and falls back to `gh auth token` if neither is
  set.

## Build

```sh
go build -o tink-contributions ./cmd/tink-contributions
```

Or run without building:

```sh
go run ./cmd/tink-contributions --help
```

## Usage

```sh
# One year of tinkerbell contributions, bots excluded, rendered as a table
go run ./cmd/tink-contributions \
  --since 2025-05-01 --until 2026-05-01 --exclude-bots

# Per-component breakdown as CSV
go run ./cmd/tink-contributions \
  --since 2025-05-01 --until 2026-05-01 --breakdown --format csv > contribs.csv

# Scope to a subset of repos, skipping the cache
go run ./cmd/tink-contributions \
  --since 2025-05-01 --until 2026-05-01 --only-repos smee,tink --no-cache
```

`--since` and `--until` are required. Dates are `YYYY-MM-DD` in UTC; `--until`
is **exclusive** (i.e. `--until 2026-05-01` includes everything up to, but not
including, midnight on that day).

### Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--since` | *(required)* | Start of range (`YYYY-MM-DD`, inclusive, UTC). |
| `--until` | *(required)* | End of range (`YYYY-MM-DD`, exclusive, UTC). |
| `--org` | `tinkerbell` | GitHub org to scan. |
| `--format` | `table` on a TTY, `md` when piped | Output format: `table`, `md`, or `csv`. |
| `--breakdown` | off | Include per-component columns (md/csv). |
| `--top` | `0` (all) | Show only the top N contributors. |
| `--no-merges` | off | Exclude merge commits from the commit count. |
| `--exclude-bots` | off | Filter out bot accounts. |
| `--anonymize` | off | Replace logins with `contributor-N`. |
| `--cache` | `./cache` | Cache directory. |
| `--no-cache` | off | Disable reading and writing the cache. |
| `--refresh-repos` | off | Re-fetch the repo list from GitHub. |
| `--only-repos` | *(all)* | Comma-separated subset of repo names to scan. |
| `--concurrency` | `4` | Number of repos fetched concurrently. |
| `--heartbeat` | `5s` | Progress heartbeat interval (`0` to disable). |

Flags may also be set via environment variables prefixed with `TINK_`, e.g.
`TINK_ORG=charmbracelet`.

## Caching

API responses are cached under `--cache` (default `./cache`) keyed by repo and
date range, so re-runs over the same window are fast and cheap on API quota.
Use `--no-cache` to bypass it entirely, or `--refresh-repos` to only re-fetch
the repository list.

## Development

```sh
go test ./...
go vet ./...
```
