# Plan: Tinkerbell Contribution Stats — GitHub Pages Site

A static, free, self-updating website that displays Tinkerbell contribution
statistics with dropdowns for **repository**, **time range**, and
**metric** — modeled after the look and feel of `tinkerbell.devstats.cncf.io`
(the Grafana "Developer Activity Counts" table), but without the heavy
Postgres + Grafana backend that CNCF DevStats runs.

The core idea: reuse the existing `tink-contributions` Go tool as the data
producer, add a machine-readable **web export** format, publish that data to
GitHub Pages, and build a tiny client-side front-end that does all the
filtering (range / repo / metric) in the browser. A scheduled GitHub Action
keeps the data fresh.

---

## 1. Goals & non-goals

**Goals**
- Public, zero-cost, no-server-to-run site (GitHub Pages).
- Dropdowns: **Time range**, **Repository**, **Metric** — matching the
  reference UI's interaction model.
- A ranked leaderboard table (Rank · GitHub login · Number), sortable.
- Auto-refresh daily via GitHub Actions with no manual steps.
- Data lives in a public, open, easily-integrated location (the repo itself /
  Pages), not behind an API key or paywall.

**Non-goals (for v1)**
- The "Country" dropdown from the CNCF UI. That relies on CNCF's
  developer-affiliation database (login → company/country). We don't have that
  mapping and won't reproduce it in v1. See §9 for an optional path.
- Time-series/line charts, PR-lifetime, review latency, and the dozens of
  other DevStats panels. Start with the leaderboard table; add later.
- A database. Everything is precomputed JSON served statically.

---

## 2. Why not literally run CNCF DevStats?

CNCF DevStats (`devstats.cncf.io`) is a large system: it ingests the GitHub
Archive firehose into Postgres, runs SQL metrics, and renders Grafana
dashboards behind a hosted server. It is powerful but operationally heavy
(a persistent DB + Grafana + Postgres tuning) and not "free static hosting."

**And, more damning: the hosted Tinkerbell instance is currently producing
wrong numbers.** Compare the "Developer Activity Counts" table across two
ranges (same Metric=Contributions, Repository group=All, bots excluded):

| range | contributors shown | notable |
|---|---|---|
| Last **month** | 4 (jacobweinstock 81, mcanevet 19, rpardini 13, **brk0v 12**) | |
| Last **quarter** | 3 (jacobweinstock 113, rpardini 31, mcanevet 19) | **brk0v gone** |

A quarter strictly *contains* its most recent month, so it must be a superset:
every contributor active in the last month has to appear in the last quarter
with a count **≥** their monthly count. Yet `brk0v` (12 contributions last
month) vanishes entirely from the quarter — an impossibility, since the quarter
includes that very month. The range roll-ups are inconsistent; the totals can't
be trusted. Whatever the cause (range boundary handling, a broken affiliation
join, stale materialized views), the public dashboard is giving nonsensical
results today.

This repo already has a purpose-built tool that computes exactly the metric we
want (contributions per login) with caching and bot filtering, and \u2014 crucially
\u2014 whose logic we can inspect, test, and correct. The pragmatic architecture is:
**precompute a compact aggregate with the Go tool → ship JSON → filter in the
browser.** No server, no DB, and a monotonicity invariant we can actually
assert (a wider range never drops a contributor present in a narrower
sub-range) as a data-quality check.

---

## 3. Architecture overview

```
                    ┌─────────────────────────────────────────┐
                    │  GitHub Action (daily cron)              │
                    │  1. go run ./cmd/tink-contributions       │
                    │       --emit-web ./site/data              │
                    │     → writes site/data/events.json + cache│
                    │  2. commit cache + data (incremental)     │
                    │  3. publish site/ to GitHub Pages         │
                    └──────────────────┬──────────────────────┘
                                       │ static files
                                       ▼
                    ┌─────────────────────────────────────────┐
                    │  GitHub Pages (free static host)         │
                    │   index.html + app.js + data/events.json  │
                    └──────────────────┬──────────────────────┘
                                       │ fetch() JSON
                                       ▼
                    ┌─────────────────────────────────────────┐
                    │  Browser front-end                       │
                    │   dropdowns → filter/aggregate in JS →    │
                    │   ranked table + sparklines + breakdown   │
                    └─────────────────────────────────────────┘
```

Three pieces to build:
1. **Data producer** — a new `--emit-web` mode on the existing Go tool.
2. **Publishing** — a scheduled GitHub Action + GitHub Pages.
3. **Front-end** — a small static site consuming the JSON.

The same binary can also **serve the site locally** (`--serve`), building
`events.json` on the fly from the full cache (no date range, no GitHub calls).

---

## 4. Data model — the key design decision

The current tool renders a *single* leaderboard for *one* fixed
`--since/--until` range across *all* repos, then throws the detail away. The
front-end needs to slice by **arbitrary (day-precise) range × repo ×
metric**. The requirement to support *any* start/end date (not fixed dropdowns,
not month-rounded) is what drives the storage decision below.

### Measured scale — this decides everything

Before reaching for a "real" store, measure the data. Across the entire
Tinkerbell org, all repos, all event types, for the whole ~decade of history:

| category | decade-wide count |
|---|---|
| commits (all repos) | ~18,900 |
| issues + PRs + reviews + comments (all repos) | ~15,000–20,000 |
| **total events** | **≈ 35,000–40,000** |
| current on-disk cache (messy, with range-overlap dupes) | **39 MB** |

This is a *tiny* dataset. The entire decade of raw contribution events is
tens of thousands of rows. That single fact makes most of the heavy-machinery
options (Parquet, columnar engines, an embedded analytics DB in the browser)
unnecessary — see "Do we need a columnar store?" below.

### Recommendation: ship the raw event list (day-precise)

Because the whole dataset is ~35k events, don't pre-aggregate for the UI at
all — **ship the raw events** and let the browser filter by timestamp. Each
event is already `contrib.Event{login, kind, at}`
([internal/contrib/event.go](../internal/contrib/event.go)); add the repo it
came from. Emit a compact, positional, day-precise array:

```jsonc
// site/data/events.json
{
  "schema": 1,
  "org": "tinkerbell",
  "generated_at": "2026-08-05T00:00:00Z",
  "logins": ["jacobweinstock", "mmlb", "..."],   // index space
  "repos":  ["tinkerbell", "smee", "rufio", "..."],
  "bot_logins": ["mergify[bot]", "dependabot[bot]", "..."],
  "kinds": ["commit", "issue", "pr", "review", "comment"],
  // one row per event: [loginIdx, repoIdx, kindIdx, "YYYY-MM-DD"]
  "events": [
    [0, 0, 0, "2026-04-18"],
    [5, 2, 2, "2025-05-14"]
  ]
}
```

Why this is the right call at this scale:
- **Arbitrary ranges just work.** The browser holds every event's date, so any
  `[start, end)` the user picks — day-precise, no month rounding — is a single
  in-memory filter over ~35k rows (sub-millisecond). This directly answers the
  UI requirement.
- **Tiny payload.** ~35k rows × ~20 bytes positional ≈ **~0.7 MB raw, roughly
  150–300 KB gzipped** (Pages serves gzip). One `fetch()` on page load.
- **Client computes everything else.** Metric selection, repo filtering,
  bot exclusion, ranking, *and* any monthly/weekly time-series are all derived
  from the same array. No second artifact to keep in sync.
- Day precision is enough (a UI date-range picker is day-granular); dropping
  the intra-day time shrinks the payload with zero loss for this use case.

Bots: keep the `bot_logins` list (reuse `contrib.IsBot`) so the UI's
"exclude bots" toggle needs no re-derivation.

### The monthly cube — now optional, not primary

The earlier monthly `(login × repo × month) → Counts` cube is no longer the
primary artifact: at 35k events the raw list is already small *and* strictly
more capable (day precision, arbitrary ranges). Keep the cube only as an
**optional pre-rolled aggregate** if a future time-series chart wants
ready-made monthly series without the browser re-bucketing — but the browser
can compute those from `events.json` trivially, so v1 likely skips it.

### Do we need a columnar store / Parquet / DuckDB-WASM?

Short answer: **no, not at this scale — it would be over-engineering.** Longer:

- **Parquet / columnar files.** Columnar formats earn their keep with millions+
  of rows, wide schemas, and analytic scans where you read a few columns out of
  many. Here the schema is 4 tiny columns and the whole table is ~35k rows /
  <1 MB. A gzipped positional JSON array is already columnar-ish in effect and
  smaller than Parquet's per-file overhead would justify. Parquet also isn't
  natively queryable in a browser without a WASM engine (below).
- **DuckDB-WASM in the browser.** This is the "columnar store in the browser"
  option, and it's genuinely powerful (full SQL, arbitrary ranges, HTTP
  range-requests over remote Parquet). But the DuckDB-WASM runtime is a
  **multi-MB** download — roughly **10× larger than the entire dataset it would
  query**. Loading a database engine to filter 35k rows a plain array handles in
  microseconds is the wrong trade.
- **SQLite (as a browser asset via sql.js).** Same story — the engine dwarfs
  the data.

**The threshold where this flips.** Revisit columnar/embedded-DB **only if** the
shipped dataset grows past roughly **10–20 MB gzipped** or into the **millions
of rows** (e.g. expanding to many orgs, or storing every event with full
metadata for richer panels). Concrete migration path if that day comes:
1. First, cheap mitigations: split `events.json` per year and lazy-load
   (`data/events-2025.json`), or drop unused columns.
2. If still too big: emit **Parquet** partitioned by year, load
   **DuckDB-WASM** with `httpfs`, and let it pull only the row groups a query
   needs via HTTP range requests — no full download. This is the clean
   "scales to millions" endgame, deferred until the data actually demands it.

(The *on-disk CI cache* is a separate question — see §6. Parquet doesn't help
there either, because that layer is bottlenecked on GitHub API calls, not on
local query speed. A single SQLite cache file is the option worth weighing
there, discussed in §6.)

### Alternative considered: precomputed fixed-range tables
Emit one JSON leaderboard per (range × metric × group) combination, like the
reference's fixed dropdowns.
- Pro: dead-simple front-end.
- Con: ranges are fixed at build time — the *opposite* of the arbitrary-range
  requirement — and the file count combinatorially explodes.
Recommendation: **ship the raw day-precise event list.** It's the smallest thing
that fully satisfies arbitrary-range queries, and the browser derives every
metric/group/aggregate from it.

---

## 5. Data producer — changes to the Go tool

Add a new output path that emits `site/data/events.json` (the raw day-precise
event list from §4) instead of a single rendered leaderboard. Two clean
options; recommend **B**.

**Option A — new `--format web`.** Overloads the existing `--format` flag, but
`web` writes a whole-history data file to a directory and doesn't fit the
"render `rows` to `out`" shape the other formats use.

**Option B — a dedicated mode: `--emit-web <dir>`.** A distinct code path in
`internal/app` that:
1. Determines the range (auto: org's first activity → now, or `--since/--until`).
2. Gathers events per repo via the existing cached fetchers in `gatherRepo`,
   but **keeps the repo dimension** (which event came from which repo) instead
   of flattening everything into one pile.
3. Writes `site/data/events.json` (+ `logins`, `repos`, `bot_logins`, `kinds`,
   `generated_at`), events encoded as positional
   `[loginIdx, repoIdx, kindIdx, "YYYY-MM-DD"]` rows.

Concretely:
- Add `internal/webexport/` with:
  - `type Row struct { Login, Repo string; Kind contrib.Kind; Day string }`
  - a builder that de-dupes into the `logins`/`repos` index spaces and emits
    positional rows. Events don't currently carry their repo, so aggregate
    **per repo** inside `gatherRepo` (we already iterate per repo) and hand
    `(repo, []Event)` to the builder — no schema change to `Event` needed.
  - `func WriteJSON(dir string, ...) error` with stable ordering (sort rows) so
    daily commits diff minimally.
- Reuse `contrib.IsBot` for the `bot_logins` list.
- Keep the existing table/md/csv formats untouched.

**Incremental daily runs (important).** This depends entirely on the cache
redesign in §6: once the cache is sharded by month instead of by exact range,
closed months are permanent cache hits and only the current month (plus a short
late-edit window) is refetched each day. The daily Action then does near-zero
API work after the first backfill. See §6 for the mechanism.

---

## 6. Cache redesign — range-independent (month-sharded)

**The problem you hit.** The cache key today embeds the *exact* range:
`gatherRepo` builds keys like `"{repo}/issues-{since}-{until}"`
([internal/app/run.go](../internal/app/run.go)) and `cache.Fetch` maps one key
to one file ([internal/cache/cache.go](../internal/cache/cache.go) — "a simple
exact-key JSON blob cache"). So `2025-05-01→2026-05-01` and
`2025-06-01→2026-05-01` are different keys and share nothing, even though the
second range is a strict subset of the first. Every range shift is a full
refetch of every repo and event type.

**The fix: shard the cache by calendar month, not by range.** Cache one blob
per `(repo, eventType, month)` — e.g. key `"{repo}/issues/2025-06"` → the
issue-events created in June 2025. An arbitrary request range is *composed* from
month shards. But a naive "loop months, fetch each individually" is a trap (see
bulk fetching below), so the real composition reads cached months and
bulk-fetches the gaps:

```
FetchRange(repo, type, since, until):
  months = enumerateMonths(since, until)          # e.g. 2025-05 … 2026-04
  results = {}
  need = []                                       # contiguous run of gaps
  for m in months:
      if m is closed and shard(m) on disk:
          flush(need); need = []                  # bulk-fetch the run so far
          results[m] = read shard(m)
      else:                                        # missing-closed or volatile
          need.append(m)
  flush(need)
  return filter(concat(results in month order), since <= e.At < until)

flush(run):                                        # one call for the whole run
  items = fetchSpan(run.first, run.last+1month)    # single paginated walk
  for m in run: writeShard(m, items where At in m) # empty months included
```

Why this solves the reported case: both `2025-05-01→…` and `2025-06-01→…` read
the *same* per-month shards; the second range simply reads one fewer shard
(skips `2025-05`). Every overlapping month is a cache hit. No range is ever
special. (Verified: after warming, an overlapping subset range returns in
~0.16 s with zero API calls.)

**Partial months are still exact.** Ranges rarely land on month boundaries. A
month shard always stores the *whole* month; the final in-memory
`since <= At < until` filter (events already carry `At`, see
[internal/contrib/event.go](../internal/contrib/event.go)) trims the leading and
trailing partial months precisely. Correctness is unchanged; only the cache
granularity changes.

**The volatile boundary.** A month shard is immutable *once the month has
closed* — a comment or commit dated June 2025 can't appear after June ends
(edits don't change `createdAt`). So:
- Closed months (`< current month`): cache forever. Never refetch.
- Current month: always refetch (it's still accumulating).
- Optional 1-month "late-edit" safety window (refetch `current-1`) to catch
  backdated-looking edge cases; cheap insurance.

Implement as: `cache.Fetch` is bypassed (force `fn`) for shards whose month is
`>= volatileFrom`, where `volatileFrom = firstOfCurrentMonth` (or one month
earlier). This is the single mechanism that makes daily runs near-free.

**How fetchers change.** The GraphQL/REST fetchers
([internal/github/fetch.go](../internal/github/fetch.go)) already take
`(since, until)` and stop paging once they pass the window — **no signature
change**. The range-composition layer ([internal/cache/range.go](../internal/cache/range.go))
calls them with a *whole-run span* and partitions the returned items into month
shards by timestamp. `cache`'s generic `[T any]` helpers (`readCached`,
`overwrite`) are reused per shard.

**Bulk fetching — the reason a naive per-month loop is wrong.** Only the commit
query is server-side date-bounded (GraphQL `history(since,until)`). Issues, PRs,
reviews, and all three comment endpoints walk **newest→oldest with no lower
bound**, stopping when they pass `since`. So fetching one *old* month in
isolation re-walks everything newer than it — summed over N months that is
roughly O(N²). A cold multi-year backfill fetched month-by-month crawls. The
fix above fetches each contiguous run of missing months in a **single**
paginated walk and partitions the result, restoring the one-walk-per-(repo,type)
cost of the old design while still producing month shards. (Measured: full
2-year, 31-repo org backfill in ~23 s; the day-precise `events.json` is ~210 KB
raw / ~15 KB gzipped.)

**Cost tradeoff (honest).** Cold backfill cost is back to ~one paginated walk
per (repo,type) — the same as the old exact-range design — because contiguous
gaps are bulk-fetched, not walked per month. Every closed month is then cached
permanently, so steady state is ~1–2 months refetched per day across all repos —
dramatically less than the old "refetch-everything-on-any-range-change." The
backfilled shards get committed to `cache/` (repo convention) so CI starts warm.
Repo-level concurrency (`--concurrency`, default 4; 7 event types also run
concurrently within a repo) is a secondary lever — raise it cautiously, since
GitHub's secondary rate limits punish bursts (the transport already backs off).

**Backfilling *older* months is the expensive direction.** Only commits are
server-side date-bounded; issues/PRs/reviews/comments page newest-first with no
lower bound, so reaching an old month walks past everything newer than it. The
cheap operating model is therefore **one full-history backfill** (committed to
`cache/`), after which only the newest month is ever fetched — that data sits at
the front of the walk and is cheap. Extending a range further into the past is a
one-time cost per newly-covered month. (If cheap arbitrary-old-month fetches
ever matter, the lever is GitHub's Search API with a server-side `created:`
bound, at the cost of a 30 req/min limit and 1000-result cap.)

**Migration.** The existing ~1200 range-keyed files use the old scheme and
won't match the new month keys — they'd go stale/dead. Options:
1. Leave them (harmless clutter) and let month shards accrue. Simplest.
2. One-time re-bucket: load old range blobs, split events by
   `At.Format("2006-01")`, write month shards, delete originals.
3. Just delete `cache/` and re-backfill once (commit the result).
Recommend **2** if we want to preserve the API calls already spent; otherwise 3.

**Synergy with the export.** The web export (§5) reads the *same* month shards
and concatenates them into the raw-event list — no separate fetch path. Month
is the cache-key grain for API-fetch economy; the *shipped* data stays
day-precise (§4). The two granularities are independent: shard monthly, ship
daily.

**Alternative considered — interval/coverage cache.** Keep range keys but track,
per (repo,type), the set of already-covered `[start,end)` intervals plus their
events; a new range reads the covered overlap and fetches only the gaps. More
flexible (day-precise fetch windows) but materially more complex (interval
bookkeeping, merging, invalidation). Recommend **month shards** for simplicity;
keep interval caching in reserve only if sub-month *fetch* precision is ever
needed (note the UI is already day-precise regardless, via §4).

### 6.1 Cache backend — loose JSON shards vs. a single SQLite file

The user asked whether a "different local DB type" would serve the cache better.
Worth weighing, because the current cache is **5,813 loose JSON files / 39 MB**
(measured) with lots of range-overlap duplication — messy to reason about and
to commit.

**Option 1 — month-sharded JSON files** (the §6 plan). One small file per
`(repo, type, month)`.
- Pro: zero new dependencies; human-readable; git-diffable; trivial to reason
  about; already matches `cache.Fetch`'s model.
- Con: still thousands of tiny files (fewer than today after de-dup, but many);
  no ad-hoc querying of the cache itself.

**Option 2 — a single SQLite file** (`cache/events.db`). One table
`events(repo, kind, login, at)` plus a `fetched(repo, kind, month)` ledger that
records which shards have been fetched.
- Pro: **one file** instead of thousands; real indexes → arbitrary-range
  `SELECT ... WHERE at >= ? AND at < ?` on the *CI side* for free; atomic
  writes; the exporter just runs one query to build `events.json`; the
  `fetched` ledger cleanly encodes the month-shard + volatile-boundary logic
  (a month is cached iff a ledger row exists and it's not volatile). Pure-Go
  driver available (`modernc.org/sqlite`, no cgo) so the build stays simple.
- Con: a binary blob in git (larger, opaque diffs — though at ~1–2 MB for this
  dataset that's fine, and it can live on `gh-pages` to keep `main` clean); one
  new dependency.

**Recommendation.** For *this* dataset either is fine; **SQLite is the cleaner
long-term backend** and is the honest answer to "use a different local DB": it
collapses 5,813 files into one, gives indexed arbitrary-range reads on the CI
side, and models the shard ledger naturally. The month-shard *concept* from §6
stays identical — SQLite just stores the shards as rows + a ledger instead of as
filenames. If minimizing dependencies/diff-noise matters more than tidiness,
start with JSON shards (Option 1) and migrate to SQLite only if the file count
or commit noise becomes annoying. Parquet is **not** the right cache backend:
this layer is bottlenecked on GitHub API calls, not local scan speed, and
Parquet buys nothing over SQLite for point-lookups and small range reads.

---

## 7. Repository selection

The **Repository** dropdown lists `All` followed by every individual repo, built
directly from the `repos` index in `events.json` (already sorted). There is no
group config: `All` aggregates across every repo; any other choice filters to
that single repo. The org scanned is whatever `--org` targets (default
`tinkerbell`) via `ListRepos`.

(An earlier design had curated repo *groups* loaded from a `site/config.json`.
That was dropped in favor of listing every repo individually; the config file,
its loader, and the `groups` field in `events.json` were removed.)

---

## 8. Publishing — GitHub Action + Pages

**Repo location note:** this module is `github.com/jacobweinstock/devstats`.
The Action's built-in `GITHUB_TOKEN` can read **public** repos in any org
(public read needs no special grant), so no PAT is required to gather public
Tinkerbell activity. Watch GraphQL rate limits, but incremental fetching keeps
usage tiny after backfill.

### Workflow sketch — `.github/workflows/stats.yml`

```yaml
name: update-stats
on:
  schedule:
    - cron: "17 6 * * *"   # 06:17 UTC daily; odd minute dodges on-hour queueing
  workflow_dispatch: {}      # manual trigger
permissions:
  contents: write            # to commit refreshed cache + data
  pages: write               # if using the Pages deploy action
  id-token: write
concurrency:
  group: update-stats
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      - name: Generate events.json
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: go run ./cmd/tink-contributions --emit-web ./site/data
      - name: Commit refreshed cache + data
        run: |
          git config user.name  "tinkerbell-stats-bot"
          git config user.email "actions@users.noreply.github.com"
          git add cache site/data
          if git diff --cached --quiet; then
            echo "no changes"
          else
            git commit -m "refresh contribution stats"
            git push
          fi
          git push
      - name: Upload Pages artifact
        uses: actions/upload-pages-artifact@v3
        with: { path: ./site }
  deploy:
    needs: build
    runs-on: ubuntu-latest
    environment: { name: github-pages, url: "${{ steps.deploy.outputs.page_url }}" }
    steps:
      - id: deploy
        uses: actions/deploy-pages@v4
```

### Where the data lives — options weighed
1. **Commit into the repo `site/data/` + GitHub Pages** ✅ recommended.
   Free, open, versioned, trivially fetchable (`fetch('data/events.json')`),
   diffable in git history. Publish via `actions/deploy-pages` (artifact-based,
   keeps `main` as the only branch) **or** a `gh-pages` branch via
   `peaceiris/actions-gh-pages`.
2. **`gh-pages` branch only** (data not on `main`). Cleaner `main`, but the
   cache/data isn't versioned alongside code. Fine if you don't want big JSON
   diffs on `main`.
3. **GitHub Releases assets** ❌ avoid for the front-end. Release assets are
   served as `application/octet-stream` from a redirecting host and are
   awkward for `fetch()`/CORS; a known footgun. Good for `.box`/binaries, bad
   for a JSON API the browser polls.
4. **External object storage (R2/S3/etc.)** — not "free + zero-setup." Skip.

Recommendation: **option 1**, Pages via the official `deploy-pages` artifact
flow. Keep the big `cache/` committed (already the repo's convention) so
incremental runs stay cheap; keep `site/data/*.json` committed so the data is
open and diffable.

> Consideration: committing multi-MB JSON daily grows history. Mitigations:
> stable key ordering in the exporter (minimal diffs), or move `cache/` +
> `data/` to the `gh-pages` branch (option 2) so `main` history stays lean.

---

## 9. Front-end — tooling & design

Keep it **build-free and dependency-light** so GitHub Pages just serves files.

**Stack (as built)**
- Plain `index.html` + one vanilla `app.js` + `style.css` — no bundler, no
  framework, no CDN dependency.
- Sparklines are tiny inline SVG (no chart library).
- Hand-rolled dark theme echoing the Grafana look.

**UI layout (mirrors the reference screenshot)**
- A header row of controls:
  - **Range** dropdown: `Last day`, `Last week`, `Last 10 days`, `Last 30
    days`, `Last 90 days`, `Last year`, `Last decade`, `All time`, **plus a
    `Custom…` option with two date pickers** (To is inclusive) for an arbitrary
    day-precise window. Because the browser holds every event's date (§4), any
    range — not just the presets — filters exactly, with no month rounding.
    Editing a date field auto-switches Range to `Custom…`.
  - **Metric** dropdown: `Contributions`, `Commits`, `Issues`, `PRs`,
    `Reviews`, `Comments`.
  - **Repository** dropdown: `All` plus every repo in `data.repos`.
  - **Exclude bots** toggle (default on).
- A title line summarizing the selection (like the reference's
  "Tinkerbell Developers statistics (…)").
- The table: `Rank · GitHub login · Number`, sorted desc by the chosen metric,
  login linking to `https://github.com/<login>`. Sortable headers.

**Client-side compute** (all cheap, runs on the ~35k-row event array):
```
1. Expand data.events → objects {login, repo, kind, day} via the index arrays.
2. selectedRepos = All ? every repo : the single selected repo.
3. filter events where repo ∈ selectedRepos AND start <= day < end.
4. tally per login per kind; contributions = sum of the 5 kinds.
5. drop bots if toggle on (data.bot_logins).
6. sort by selected metric desc, assign ranks, render rows.
```

A few dozen lines of JS; filtering 35k rows on every dropdown change is
instant (sub-millisecond).

Implemented niceties: a per-contributor sparkline, a click-to-expand per-repo
breakdown row, shareable URL query-params, and a footer showing the data's date
span (`data <first> → <last>`).

**"Country" dropdown (deferred).** To ever add it, we'd need a login→country
map. Option: check in an optional `site/affiliations.json`
(login → {company, country}) maintained by hand or scraped from CNCF's public
`developers_affiliations.txt`. Then add a Country dropdown that filters events
by login membership. Explicitly out of scope for v1.

---

## 10. Repository / file layout (proposed)

```
devstats/
├── cmd/tink-contributions/main.go      # --emit-web and --serve modes
├── internal/
│   ├── app/
│   │   ├── run.go                      # web-export path (runWebExport)
│   │   └── serve.go                    # --serve: build from cache, serve site
│   ├── cache/
│   │   ├── cache.go                    # exact-key store (reused per shard)
│   │   └── range.go                    # FetchRange: month shards + bulk fetch (§6)
│   ├── webexport/
│   │   ├── events.go                   # Build + WriteJSON (events.json)
│   │   ├── cache.go                    # BuildFromCache (full cache → events.json)
│   │   └── events_test.go
│   └── contrib/…                       # reuse Kind, IsBot
├── site/                               # static site (Pages root)
│   ├── index.html
│   ├── app.js
│   ├── style.css
│   └── data/
│       └── events.json                 # generated; committed (~15 KB gzipped / 2yr)
├── .github/workflows/stats.yml         # daily cron + Pages deploy
├── cache/                              # committed month shards for incremental
└── docs/PLAN.md                        # this file
```

---

## 11. Phased delivery

**Phase 0 — spike (prove the data path).**
- Hand-write a small `events.json` from one existing run and build the
  front-end against it. Validates the JSON shape and arbitrary-range UX before
  touching the Go tool.

**Phase 1 — cache redesign + exporter.**
- First, month-shard the cache (§6): add `FetchRange` (compose months + trim
  partial ends), switch `gatherRepo` keys to `{repo}/{type}/{YYYY-MM}`, and add
  the volatile-boundary bypass for the current month. Verify the reported case
  (`2025-05-01→…` then `2025-06-01→…`) is now a cache hit. Optionally re-bucket
  existing cache files (or adopt the SQLite backend, §6.1).
- Then add `internal/webexport` + `--emit-web` mode, emitting the raw
  day-precise event list straight off the shards. Unit-test the builder against
  known events. Generate a real `site/data/events.json`.

**Phase 2 — front-end.**
- `index.html` + `app.js` with Range (incl. custom date pickers) / Metric /
  Repository dropdowns + sortable table + bots toggle. Style to resemble the
  reference.

**Phase 3 — automation.**
- `.github/workflows/stats.yml`: daily cron, incremental fetch, commit
  cache+data, deploy Pages. Enable Pages in repo settings once.

**Phase 4 — polish (optional).**
- **Done:** sparkline activity bars per contributor (inline SVG), a per-repo
  breakdown expander (click a row), and shareable URL query-params
  (`?range=…&metric=…&group=…&start=…&end=…&bots=…`) that restore on load.
- Remaining/deferred: the Country dropdown (needs an affiliations file, §9).

**Also built — local preview.** A `--serve` mode previews the site locally,
building `events.json` on the fly from the *full* cache (ignoring `--since`), so
you always see everything cached without regenerating the committed file.

---

## 12. Open questions / decisions to confirm

1. **Pages source:** deploy from `main` via `deploy-pages` artifact, or push to
   a `gh-pages` branch to keep big JSON diffs off `main`? (Recommend artifact
   from `main`; revisit if history bloat bites.)
2. **Org scope:** just `tinkerbell`, or also pull `jacobweinstock/*` kits and
   others in? Affects `--org` handling.
3. **Shipped-data granularity:** day-precise raw events (recommended — enables
   arbitrary ranges at ~0.5 MB gzipped) vs. pre-aggregated monthly cube
   (smaller-but-month-rounded). **Decided: raw events.**
4. **Bots:** default the site to bots-excluded (recommended) with a toggle?
5. **Metric parity:** the reference's "Contributions" — confirm our definition
   (commits+issues+prs+reviews+comments) is the intended headline number.
6. **History bloat mitigation:** stable-ordered output is a must; decide
   whether to also gzip-commit or split `events.json` per year.
7. **Cache backend:** month-sharded JSON files (simple, git-diffable, no deps)
   vs. a single SQLite file (one blob, indexed arbitrary-range reads, models the
   shard ledger) — see §6.1. **Built: month-sharded JSON** (revisit SQLite if
   file count / commit noise annoys).
8. **Cache migration:** on switching off exact-range keys (§6), re-bucket the
   existing range-keyed cache files to preserve spent API calls, or just delete
   `cache/` and re-backfill once? (Recommend re-bucket.)
9. **Late-edit window:** refetch only the current month, or also `current-1` as
   insurance? Trade one extra month of daily API calls for robustness.
```
