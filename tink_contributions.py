#!/usr/bin/env python3
"""
tink_contributions.py — leaderboard of GitHub contributors for the `tinkerbell` org.

A "contribution" is one of: a commit authored, an issue opened, a PR opened,
a PR review submitted, or a comment authored (issue / PR review / commit).
Bots are excluded by default.

Run with uv (no project setup required):

    uv run --with rich,httpx /path/to/tink_contributions.py --since 2025-05-01 --until 2026-05-01

Auth: uses `gh auth token` to get a GitHub token. Run `gh auth login` first.
"""

from __future__ import annotations

import argparse
import asyncio
import csv
import json
import os
import re
import shutil
import subprocess
import sys
from collections import defaultdict
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

import httpx

ORG = "tinkerbell"
GH_GRAPHQL = "https://api.github.com/graphql"
GH_REST = "https://api.github.com"
USER_AGENT = "tink-contributions/0.1"

# Mirrors devstats' bot exclusions plus tinkerbell-specific bots.
# Anything ending in [bot] is also dropped.
BOT_LOGINS = {
    "dependabot",
    "dependabot-preview",
    "renovate",
    "renovate-bot",
    "github-actions",
    "codecov",
    "codecov-io",
    "codecov-commenter",
    "mergify",
    "mergify-bot",
    "tinkerbell-ci",
    "tinkerbell-bot",
    "k8s-ci-robot",
    "stale",
    "imgbot",
    "pre-commit-ci",
    "allcontributors",
    "copilot",
    "copilot-pull-request-reviewer",
}


def is_bot(login: str | None) -> bool:
    if not login:
        return True  # treat unknown authors as bots (drop them)
    low = login.lower()
    if low.endswith("[bot]") or low.endswith("-bot"):
        return True
    return low in BOT_LOGINS


# ---------------------------------------------------------------------------
# Auth
# ---------------------------------------------------------------------------


def get_token() -> str:
    tok = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if tok:
        return tok.strip()
    if not shutil.which("gh"):
        sys.exit("error: no GITHUB_TOKEN/GH_TOKEN env var and `gh` CLI not found. Run `gh auth login`.")
    try:
        out = subprocess.check_output(["gh", "auth", "token"], text=True).strip()
    except subprocess.CalledProcessError as e:
        sys.exit(f"error: `gh auth token` failed: {e}")
    if not out:
        sys.exit("error: `gh auth token` returned empty. Run `gh auth login`.")
    return out


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------


@dataclass
class Counts:
    commits: int = 0
    issues: int = 0
    prs: int = 0
    reviews: int = 0
    comments: int = 0

    @property
    def contributions(self) -> int:
        return self.commits + self.issues + self.prs + self.reviews + self.comments


@dataclass
class RateInfo:
    rest_remaining: int | None = None
    graphql_remaining: int | None = None
    graphql_cost_total: int = 0


import time as _time


class Progress:
    """Tracks per-(repo, task) progress and prints a periodic heartbeat to stderr."""

    TASKS = ("issues", "prs", "reviews", "icomments", "prcomments", "ccomments", "commits")

    def __init__(self, total_repos: int, interval: float = 5.0):
        self.total_repos = total_repos
        self.interval = interval
        self.repos_done = 0
        # state[(repo, task)] = (status, pages, started_at)
        # status in {"queued", "running", "done", "cached"}
        self.state: dict[tuple[str, str], tuple[str, int, float]] = {}
        self._task: asyncio.Task | None = None
        self._stop = asyncio.Event()
        self._lock = asyncio.Lock()

    def queue(self, repo: str) -> None:
        for t in self.TASKS:
            self.state[(repo, t)] = ("queued", 0, 0.0)

    def start(self, repo: str, task: str) -> None:
        self.state[(repo, task)] = ("running", 0, _time.monotonic())

    def tick(self, repo: str, task: str) -> None:
        st = self.state.get((repo, task))
        if not st:
            return
        status, pages, started = st
        self.state[(repo, task)] = (status, pages + 1, started)

    def done(self, repo: str, task: str, *, cached: bool = False) -> None:
        st = self.state.get((repo, task))
        started = st[2] if st else _time.monotonic()
        pages = st[1] if st else 0
        self.state[(repo, task)] = ("cached" if cached else "done", pages, started)

    def repo_done(self) -> None:
        self.repos_done += 1

    def _snapshot(self) -> str:
        now = _time.monotonic()
        # Group active tasks by repo.
        by_repo: dict[str, list[str]] = defaultdict(list)
        for (repo, task), (status, pages, started) in self.state.items():
            if status == "running":
                elapsed = int(now - started) if started else 0
                by_repo[repo].append(f"{task}(p{pages},{elapsed}s)")
        if not by_repo:
            return "  ... waiting ..."
        lines = []
        for repo in sorted(by_repo):
            lines.append(f"    {repo}: {' '.join(by_repo[repo])}")
        return "\n".join(lines)

    async def _loop(self) -> None:
        try:
            while not self._stop.is_set():
                try:
                    await asyncio.wait_for(self._stop.wait(), timeout=self.interval)
                    return
                except asyncio.TimeoutError:
                    pass
                snap = self._snapshot()
                print(
                    f"  [{self.repos_done}/{self.total_repos}] in flight:\n{snap}",
                    file=sys.stderr,
                    flush=True,
                )
        except asyncio.CancelledError:
            return

    def start_heartbeat(self) -> None:
        self._task = asyncio.create_task(self._loop())

    async def stop_heartbeat(self) -> None:
        self._stop.set()
        if self._task:
            try:
                await self._task
            except asyncio.CancelledError:
                pass


class Client:
    def __init__(self, token: str, cache_dir: Path, no_cache: bool):
        self.token = token
        self.cache_dir = cache_dir
        self.no_cache = no_cache
        self.rate = RateInfo()
        # Generous timeouts; some history pages are large.
        timeout = httpx.Timeout(60.0, connect=15.0)
        # Modest concurrency to stay friendly with secondary rate limits.
        limits = httpx.Limits(max_connections=8, max_keepalive_connections=4)
        self.http = httpx.AsyncClient(
            timeout=timeout,
            limits=limits,
            headers={
                "Authorization": f"Bearer {token}",
                "Accept": "application/vnd.github+json",
                "X-GitHub-Api-Version": "2022-11-28",
                "User-Agent": USER_AGENT,
            },
        )

    async def aclose(self) -> None:
        await self.http.aclose()

    # --- caching ---------------------------------------------------------

    def _cache_path(self, key: str) -> Path:
        safe = re.sub(r"[^A-Za-z0-9._-]+", "_", key)
        return self.cache_dir / f"{safe}.json"

    def _cache_get(self, key: str) -> Any | None:
        if self.no_cache:
            return None
        p = self._cache_path(key)
        if p.exists():
            try:
                return json.loads(p.read_text())
            except Exception:
                return None
        return None

    def _cache_put(self, key: str, value: Any) -> None:
        if self.no_cache:
            return
        p = self._cache_path(key)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(json.dumps(value))

    # --- low-level requests with retry/backoff ---------------------------

    async def _send(self, method: str, url: str, **kw) -> httpx.Response:
        backoff = 2.0
        for attempt in range(6):
            r = await self.http.request(method, url, **kw)
            # Track rate limits.
            if "X-RateLimit-Remaining" in r.headers:
                try:
                    self.rate.rest_remaining = int(r.headers["X-RateLimit-Remaining"])
                except ValueError:
                    pass
            if r.status_code in (403, 429):
                # Secondary rate limit or abuse detection.
                retry_after = r.headers.get("Retry-After")
                wait = float(retry_after) if retry_after else backoff
                print(f"  rate-limited ({r.status_code}); sleeping {wait:.0f}s", file=sys.stderr)
                await asyncio.sleep(wait)
                backoff = min(backoff * 2, 60)
                continue
            if r.status_code >= 500:
                await asyncio.sleep(backoff)
                backoff = min(backoff * 2, 60)
                continue
            return r
        r.raise_for_status()
        return r

    async def graphql(self, query: str, variables: dict[str, Any]) -> dict[str, Any]:
        r = await self._send(
            "POST",
            GH_GRAPHQL,
            json={"query": query, "variables": variables},
        )
        if r.status_code != 200:
            raise RuntimeError(f"GraphQL HTTP {r.status_code}: {r.text[:500]}")
        data = r.json()
        if "errors" in data and data["errors"]:
            raise RuntimeError(f"GraphQL errors: {data['errors']}")
        rl = data.get("data", {}).get("rateLimit") if data.get("data") else None
        if rl:
            self.rate.graphql_remaining = rl.get("remaining")
            self.rate.graphql_cost_total += int(rl.get("cost") or 0)
        return data["data"]

    async def rest_paginate(
        self,
        path: str,
        params: dict[str, Any],
        on_page: Any | None = None,
    ) -> list[dict[str, Any]]:
        url = f"{GH_REST}{path}"
        params = {**params, "per_page": 100}
        items: list[dict[str, Any]] = []
        while url:
            r = await self._send("GET", url, params=params)
            params = {}  # only on first call; Link URL contains its own
            if r.status_code == 404:
                return items
            if r.status_code != 200:
                raise RuntimeError(f"REST HTTP {r.status_code} for {url}: {r.text[:300]}")
            page = r.json()
            if not isinstance(page, list):
                raise RuntimeError(f"REST unexpected payload for {url}: {type(page)}")
            items.extend(page)
            if on_page:
                on_page()
            url = _next_link(r.headers.get("Link"))
        return items


def _next_link(link_header: str | None) -> str | None:
    if not link_header:
        return None
    for part in link_header.split(","):
        seg = part.strip()
        m = re.match(r"<([^>]+)>;\s*rel=\"next\"", seg)
        if m:
            return m.group(1)
    return None


# ---------------------------------------------------------------------------
# Data fetchers
# ---------------------------------------------------------------------------

REPOS_QUERY = """
query($org: String!, $cursor: String) {
  organization(login: $org) {
    repositories(first: 100, after: $cursor, orderBy: {field: NAME, direction: ASC}) {
      pageInfo { endCursor hasNextPage }
      nodes { name isArchived isFork }
    }
  }
  rateLimit { remaining cost resetAt }
}
"""


async def list_repos(c: Client, org: str, refresh: bool) -> list[str]:
    cache_key = f"repos/{org}"
    if not refresh:
        cached = c._cache_get(cache_key)
        if cached:
            return cached
    repos: list[str] = []
    cursor: str | None = None
    while True:
        data = await c.graphql(REPOS_QUERY, {"org": org, "cursor": cursor})
        block = data["organization"]["repositories"]
        for n in block["nodes"]:
            repos.append(n["name"])
        if not block["pageInfo"]["hasNextPage"]:
            break
        cursor = block["pageInfo"]["endCursor"]
    repos.sort()
    c._cache_put(cache_key, repos)
    return repos


# --- issues + PRs opened ------------------------------------------------

ISSUES_QUERY = """
query($org: String!, $repo: String!, $cursor: String) {
  repository(owner: $org, name: $repo) {
    issues(first: 100, after: $cursor, orderBy: {field: CREATED_AT, direction: DESC}) {
      pageInfo { endCursor hasNextPage }
      nodes { createdAt author { login } }
    }
  }
  rateLimit { remaining cost resetAt }
}
"""

PRS_QUERY = """
query($org: String!, $repo: String!, $cursor: String) {
  repository(owner: $org, name: $repo) {
    pullRequests(first: 100, after: $cursor, orderBy: {field: CREATED_AT, direction: DESC}) {
      pageInfo { endCursor hasNextPage }
      nodes { createdAt author { login } }
    }
  }
  rateLimit { remaining cost resetAt }
}
"""


async def _opened_in_range(
    c: Client, repo: str, since: datetime, until: datetime, kind: str, prog: Progress
) -> list[tuple[str, str]]:
    """Return list of (login, createdAt) for issues or PRs opened in [since, until)."""
    query = ISSUES_QUERY if kind == "issues" else PRS_QUERY
    task_name = "issues" if kind == "issues" else "prs"
    cache_key = f"{repo}/{kind}-{since.date()}-{until.date()}"
    cached = c._cache_get(cache_key)
    if cached is not None:
        prog.done(repo, task_name, cached=True)
        return cached
    prog.start(repo, task_name)
    out: list[tuple[str, str]] = []
    cursor: str | None = None
    done = False
    while not done:
        data = await c.graphql(query, {"org": ORG, "repo": repo, "cursor": cursor})
        prog.tick(repo, task_name)
        block = data["repository"][kind]
        for n in block["nodes"]:
            created = _parse_dt(n["createdAt"])
            if created < since:
                done = True
                continue
            if created >= until:
                continue
            login = (n["author"] or {}).get("login")
            out.append((login or "", n["createdAt"]))
        if done or not block["pageInfo"]["hasNextPage"]:
            break
        cursor = block["pageInfo"]["endCursor"]
    c._cache_put(cache_key, out)
    prog.done(repo, task_name)
    return out


# --- reviews ------------------------------------------------------------

REVIEWS_QUERY = """
query($org: String!, $repo: String!, $cursor: String) {
  repository(owner: $org, name: $repo) {
    pullRequests(first: 50, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { endCursor hasNextPage }
      nodes {
        number
        updatedAt
        reviews(first: 100) {
          totalCount
          nodes { submittedAt author { login } }
        }
      }
    }
  }
  rateLimit { remaining cost resetAt }
}
"""


async def _reviews_in_range(
    c: Client, repo: str, since: datetime, until: datetime, prog: Progress
) -> list[tuple[str, str]]:
    cache_key = f"{repo}/reviews-{since.date()}-{until.date()}"
    cached = c._cache_get(cache_key)
    if cached is not None:
        prog.done(repo, "reviews", cached=True)
        return cached
    prog.start(repo, "reviews")
    out: list[tuple[str, str]] = []
    cursor: str | None = None
    done = False
    while not done:
        data = await c.graphql(REVIEWS_QUERY, {"org": ORG, "repo": repo, "cursor": cursor})
        prog.tick(repo, "reviews")
        block = data["repository"]["pullRequests"]
        for pr in block["nodes"]:
            updated = _parse_dt(pr["updatedAt"])
            if updated < since:
                done = True
                continue
            reviews = pr.get("reviews") or {}
            total = reviews.get("totalCount", 0)
            nodes = reviews.get("nodes") or []
            if total > len(nodes):
                # Rare for tinkerbell-sized PRs; warn and continue with what we have.
                print(
                    f"  warn: {repo}#{pr['number']} has {total} reviews; only first {len(nodes)} captured",
                    file=sys.stderr,
                )
            for rv in nodes:
                if not rv.get("submittedAt"):
                    continue
                sub = _parse_dt(rv["submittedAt"])
                if sub < since or sub >= until:
                    continue
                login = (rv.get("author") or {}).get("login")
                out.append((login or "", rv["submittedAt"]))
        if done or not block["pageInfo"]["hasNextPage"]:
            break
        cursor = block["pageInfo"]["endCursor"]
    c._cache_put(cache_key, out)
    prog.done(repo, "reviews")
    return out


# --- comments (REST, all three flavors) ---------------------------------

_ENDPOINT_TASK = {
    "issues/comments": "icomments",
    "pulls/comments": "prcomments",
    "comments": "ccomments",
}


async def _comments_in_range(
    c: Client, repo: str, since: datetime, until: datetime, endpoint: str, prog: Progress
) -> list[tuple[str, str]]:
    """endpoint is one of 'issues/comments', 'pulls/comments', 'comments' (commit comments).

    Strategy: page through with sort=created direction=desc and stop as soon
    as we see an item older than `since`. We deliberately do NOT pass the REST
    `since` query parameter, because that endpoint filters by `updated_at`,
    not `created_at`; on a busy repo (label edits, reactions, bot mentions)
    that returns vast amounts of old data we'd just discard.
    """
    task_name = _ENDPOINT_TASK[endpoint]
    cache_key = f"{repo}/{endpoint.replace('/', '_')}-{since.date()}-{until.date()}"
    cached = c._cache_get(cache_key)
    if cached is not None:
        prog.done(repo, task_name, cached=True)
        return cached
    prog.start(repo, task_name)
    url = f"{GH_REST}/repos/{ORG}/{repo}/{endpoint}"
    out: list[tuple[str, str]] = []
    page = 1
    stop = False
    while not stop:
        r = await c._send(
            "GET",
            url,
            params={"per_page": 100, "page": page, "sort": "created", "direction": "desc"},
        )
        prog.tick(repo, task_name)
        if r.status_code == 404:
            break
        if r.status_code != 200:
            raise RuntimeError(f"REST HTTP {r.status_code} for {url}: {r.text[:300]}")
        items = r.json()
        if not isinstance(items, list) or not items:
            break
        for it in items:
            created_s = it.get("created_at")
            if not created_s:
                continue
            ts = _parse_dt(created_s)
            if ts < since:
                stop = True
                continue
            if ts >= until:
                continue
            login = ((it.get("user") or {}).get("login")) or ""
            out.append((login, created_s))
        if stop:
            break
        if not _next_link(r.headers.get("Link")):
            break
        page += 1
    c._cache_put(cache_key, out)
    prog.done(repo, task_name)
    return out


# --- commits ------------------------------------------------------------

COMMITS_QUERY = """
query($org: String!, $repo: String!, $since: GitTimestamp!, $until: GitTimestamp!, $cursor: String) {
  repository(owner: $org, name: $repo) {
    defaultBranchRef {
      target {
        ... on Commit {
          history(first: 100, since: $since, until: $until, after: $cursor) {
            pageInfo { endCursor hasNextPage }
            nodes {
              committedDate
              parents { totalCount }
              author { user { login } }
            }
          }
        }
      }
    }
  }
  rateLimit { remaining cost resetAt }
}
"""


async def _commits_in_range(
    c: Client, repo: str, since: datetime, until: datetime, no_merges: bool, prog: Progress
) -> list[tuple[str, str]]:
    cache_key = f"{repo}/commits-{since.date()}-{until.date()}-{int(no_merges)}"
    cached = c._cache_get(cache_key)
    if cached is not None:
        prog.done(repo, "commits", cached=True)
        return cached
    prog.start(repo, "commits")
    out: list[tuple[str, str]] = []
    cursor: str | None = None
    while True:
        data = await c.graphql(
            COMMITS_QUERY,
            {
                "org": ORG,
                "repo": repo,
                "since": _to_iso(since),
                "until": _to_iso(until),
                "cursor": cursor,
            },
        )
        prog.tick(repo, "commits")
        ref = (data.get("repository") or {}).get("defaultBranchRef")
        if not ref:
            # Empty repo or no default branch.
            break
        hist = ((ref.get("target") or {}).get("history")) or {}
        for n in hist.get("nodes") or []:
            if no_merges and (n.get("parents") or {}).get("totalCount", 0) > 1:
                continue
            user = ((n.get("author") or {}).get("user")) or {}
            login = user.get("login") or ""
            out.append((login, n["committedDate"]))
        page = hist.get("pageInfo") or {}
        if not page.get("hasNextPage"):
            break
        cursor = page.get("endCursor")
    c._cache_put(cache_key, out)
    prog.done(repo, "commits")
    return out


# ---------------------------------------------------------------------------
# Orchestration
# ---------------------------------------------------------------------------


def _parse_dt(s: str) -> datetime:
    # GitHub returns ISO-8601 with 'Z'.
    if s.endswith("Z"):
        s = s[:-1] + "+00:00"
    return datetime.fromisoformat(s)


def _to_iso(dt: datetime) -> str:
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _parse_date_arg(s: str, *, end: bool = False) -> datetime:
    # Accept YYYY-MM-DD; treat as UTC midnight (start) or next-day midnight (exclusive end).
    if re.match(r"^\d{4}-\d{2}-\d{2}$", s):
        d = datetime.fromisoformat(s).replace(tzinfo=timezone.utc)
        return d
    return _parse_dt(s)


async def _gather_repo(
    c: Client,
    repo: str,
    since: datetime,
    until: datetime,
    no_merges: bool,
    sem: asyncio.Semaphore,
    prog: Progress,
) -> tuple[dict[str, Counts], dict[str, str]]:
    async with sem:
        # Fetch in parallel within the repo too, but bounded by the global semaphore
        # holding our slot.
        results = await asyncio.gather(
            _opened_in_range(c, repo, since, until, "issues", prog),
            _opened_in_range(c, repo, since, until, "pullRequests", prog),
            _reviews_in_range(c, repo, since, until, prog),
            _comments_in_range(c, repo, since, until, "issues/comments", prog),
            _comments_in_range(c, repo, since, until, "pulls/comments", prog),
            _comments_in_range(c, repo, since, until, "comments", prog),
            _commits_in_range(c, repo, since, until, no_merges, prog),
        )
    issues, prs, reviews, ic, prc, cc, commits = results

    bucket: dict[str, Counts] = defaultdict(Counts)
    # Original-case login captured from API responses, keyed by lowercased login.
    display: dict[str, str] = {}

    def _capture(login: str) -> str:
        key = login.lower()
        display.setdefault(key, login)
        return key

    for login, _ in issues:
        bucket[_capture(login)].issues += 1
    for login, _ in prs:
        bucket[_capture(login)].prs += 1
    for login, _ in reviews:
        bucket[_capture(login)].reviews += 1
    for login, _ in ic:
        bucket[_capture(login)].comments += 1
    for login, _ in prc:
        bucket[_capture(login)].comments += 1
    for login, _ in cc:
        bucket[_capture(login)].comments += 1
    for login, _ in commits:
        bucket[_capture(login)].commits += 1
    return bucket, display


def _merge(
    into: dict[str, Counts],
    display_into: dict[str, str],
    bucket: dict[str, Counts],
    display: dict[str, str],
) -> None:
    for login, c in bucket.items():
        t = into[login]
        t.commits += c.commits
        t.issues += c.issues
        t.prs += c.prs
        t.reviews += c.reviews
        t.comments += c.comments
    for k, v in display.items():
        display_into.setdefault(k, v)


# ---------------------------------------------------------------------------
# Render
# ---------------------------------------------------------------------------


# Row tuple: (rank, display_login, Counts)
Row = tuple[int, str, "Counts"]


def _spark_bar(value: int, top: int, width: int = 8) -> str:
    """Unicode block-spark of `width` chars, scaled so `top` fills it."""
    if top <= 0:
        return " " * width
    blocks = "▁▂▃▄▅▆▇█"
    # Map value/top into [0, width]; partial-block at the end.
    units = (value / top) * width
    full = int(units)
    frac = units - full
    out = "█" * min(full, width)
    if full < width:
        # Add a partial block proportional to the fractional remainder.
        idx = max(0, min(len(blocks) - 1, int(frac * len(blocks))))
        if frac > 0:
            out += blocks[idx]
    # Pad to width.
    return out.ljust(width)


def _totals(rows: list[Row]) -> "Counts":
    t = Counts()
    for _, _, c in rows:
        t.commits += c.commits
        t.issues += c.issues
        t.prs += c.prs
        t.reviews += c.reviews
        t.comments += c.comments
    return t


def _render_table(
    rows: list[Row],
    *,
    since: datetime,
    until: datetime,
    n_repos: int,
    n_total: int,
) -> None:
    """Pretty Rich table to stdout."""
    try:
        from rich.console import Console
        from rich.table import Table
        from rich import box
    except ImportError:
        sys.stderr.write(
            "error: --format table needs the `rich` package. "
            "Re-run with `uv run --with rich,httpx ./tink_contributions.py ...`\n"
        )
        sys.exit(2)

    console = Console()
    title = (
        f"[bold]Tinkerbell contributors[/bold]  "
        f"{since.date()} → {until.date()}  "
        f"([cyan]{n_repos}[/cyan] repos)"
    )
    table = Table(title=title, box=box.SIMPLE_HEAVY, title_justify="left", show_footer=True)

    top = max((c.contributions for _, _, c in rows), default=0)
    totals = _totals(rows)

    table.add_column("#", justify="right", style="dim", footer="")
    table.add_column("login", style="bold cyan", footer=f"[dim]{len(rows)} of {n_total}[/dim]")
    table.add_column("contribs", justify="right", style="bold", footer=str(totals.contributions))
    table.add_column("", justify="left")  # spark bar
    table.add_column("commits", justify="right", footer=str(totals.commits))
    table.add_column("issues", justify="right", footer=str(totals.issues))
    table.add_column("prs", justify="right", footer=str(totals.prs))
    table.add_column("reviews", justify="right", footer=str(totals.reviews))
    table.add_column("comments", justify="right", footer=str(totals.comments))

    for rank, login, c in rows:
        bar = _spark_bar(c.contributions, top)
        table.add_row(
            str(rank),
            login,
            str(c.contributions),
            f"[green]{bar}[/green]",
            str(c.commits) if c.commits else "[dim]·[/dim]",
            str(c.issues) if c.issues else "[dim]·[/dim]",
            str(c.prs) if c.prs else "[dim]·[/dim]",
            str(c.reviews) if c.reviews else "[dim]·[/dim]",
            str(c.comments) if c.comments else "[dim]·[/dim]",
        )

    console.print(table)


def _render_md(
    rows: list[Row],
    *,
    since: datetime,
    until: datetime,
    n_repos: int,
    n_total: int,
    breakdown: bool,
) -> str:
    out: list[str] = []
    out.append(f"# Tinkerbell contributors")
    out.append("")
    out.append(
        f"_{since.date()} → {until.date()} · {n_repos} repos · "
        f"{len(rows)} of {n_total} contributors shown_"
    )
    out.append("")
    totals = _totals(rows)
    if breakdown:
        out.append("| # | login | contributions | commits | issues | prs | reviews | comments |")
        out.append("|--:|---|--:|--:|--:|--:|--:|--:|")
        for rank, login, c in rows:
            out.append(
                f"| {rank} | [{login}](https://github.com/{login}) | {c.contributions} | "
                f"{c.commits} | {c.issues} | {c.prs} | {c.reviews} | {c.comments} |"
            )
        out.append(
            f"| | **total** | **{totals.contributions}** | **{totals.commits}** | "
            f"**{totals.issues}** | **{totals.prs}** | **{totals.reviews}** | "
            f"**{totals.comments}** |"
        )
    else:
        out.append("| # | login | contributions |")
        out.append("|--:|---|--:|")
        for rank, login, c in rows:
            out.append(
                f"| {rank} | [{login}](https://github.com/{login}) | {c.contributions} |"
            )
        out.append(f"| | **total** | **{totals.contributions}** |")
    out.append("")
    return "\n".join(out)


def _render_csv(rows: list[Row], breakdown: bool, fp) -> None:
    w = csv.writer(fp)
    if breakdown:
        w.writerow(["rank", "login", "contributions", "commits", "issues", "prs", "reviews", "comments"])
        for rank, login, c in rows:
            w.writerow([rank, login, c.contributions, c.commits, c.issues, c.prs, c.reviews, c.comments])
    else:
        w.writerow(["rank", "login", "contributions"])
        for rank, login, c in rows:
            w.writerow([rank, login, c.contributions])


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


async def main_async(args: argparse.Namespace) -> int:
    since = _parse_date_arg(args.since)
    until = _parse_date_arg(args.until, end=True)
    if until <= since:
        sys.exit("error: --until must be after --since")

    cache_dir = Path(args.cache).expanduser().resolve()
    cache_dir.mkdir(parents=True, exist_ok=True)

    token = get_token()
    client = Client(token, cache_dir, no_cache=args.no_cache)
    try:
        repos = await list_repos(client, ORG, refresh=args.refresh_repos)
        if args.only_repos:
            wanted = {r.strip() for r in args.only_repos.split(",") if r.strip()}
            repos = [r for r in repos if r in wanted]
        print(f"Scanning {len(repos)} repos in `{ORG}` from {since.date()} to {until.date()}", file=sys.stderr)

        sem = asyncio.Semaphore(args.concurrency)
        totals: dict[str, Counts] = defaultdict(Counts)
        display_totals: dict[str, str] = {}
        prog = Progress(total_repos=len(repos), interval=args.heartbeat)
        for r in repos:
            prog.queue(r)
        prog.start_heartbeat()
        tasks = [
            _gather_repo(client, r, since, until, args.no_merges, sem, prog) for r in repos
        ]
        try:
            for coro in asyncio.as_completed(tasks):
                bucket, disp = await coro
                _merge(totals, display_totals, bucket, disp)
                prog.repo_done()
                print(f"  [{prog.repos_done}/{len(repos)}] repos done", file=sys.stderr, flush=True)
        finally:
            await prog.stop_heartbeat()

        # Filter
        ranked: list[tuple[str, Counts]] = []
        for login, c in totals.items():
            if not login:
                continue  # commits/comments with no linked GitHub user
            if args.exclude_bots and is_bot(login):
                continue
            if c.contributions <= 0:
                continue
            ranked.append((login, c))
        ranked.sort(key=lambda x: (-x[1].contributions, x[0]))
        n_total = len(ranked)
        if args.top:
            ranked = ranked[: args.top]

        # Materialize rows with display login (and optional anonymization).
        rows: list[Row] = []
        for i, (login, c) in enumerate(ranked, start=1):
            disp = display_totals.get(login, login)
            if args.anonymize:
                disp = f"contributor-{i}"
            rows.append((i, disp, c))

        # Choose default format based on TTY.
        fmt = args.format
        if fmt is None:
            fmt = "table" if sys.stdout.isatty() else "md"

        if fmt == "table":
            _render_table(
                rows,
                since=since,
                until=until,
                n_repos=len(repos),
                n_total=n_total,
            )
        elif fmt == "md":
            sys.stdout.write(
                _render_md(
                    rows,
                    since=since,
                    until=until,
                    n_repos=len(repos),
                    n_total=n_total,
                    breakdown=args.breakdown,
                )
            )
        else:
            _render_csv(rows, args.breakdown, sys.stdout)

        # Rate-limit summary to stderr. GitHub gives 5000/hour for each of the
        # REST and GraphQL APIs; `cost_total` is the sum of GraphQL points this
        # run reported via `rateLimit.cost`.
        rl = client.rate
        rest = rl.rest_remaining if rl.rest_remaining is not None else "?"
        gql = rl.graphql_remaining if rl.graphql_remaining is not None else "?"
        print(
            f"\nGitHub API budget remaining this hour: "
            f"REST {rest}/5000 · GraphQL {gql}/5000 "
            f"(this run cost {rl.graphql_cost_total} GraphQL points)",
            file=sys.stderr,
        )
    finally:
        await client.aclose()
    return 0


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--since", required=True, help="Start of range (YYYY-MM-DD, inclusive, UTC)")
    p.add_argument("--until", required=True, help="End of range (YYYY-MM-DD, exclusive, UTC)")
    p.add_argument(
        "--format",
        choices=["table", "md", "csv"],
        default=None,
        help="Output format (default: table when TTY, md when piped)",
    )
    p.add_argument("--breakdown", action="store_true", help="Markdown/CSV: include per-component columns (table always shows them)")
    p.add_argument("--top", type=int, default=0, help="Show only top N (default: all)")
    p.add_argument("--no-merges", action="store_true", help="Exclude merge commits from the commit count")
    p.add_argument("--exclude-bots", action="store_true", help="Filter out bot accounts (default: bots included)")
    p.add_argument("--anonymize", action="store_true", help="Replace logins with `contributor-N`")
    p.add_argument("--cache", default="./cache", help="Cache directory (default: ./cache)")
    p.add_argument("--no-cache", action="store_true", help="Disable read+write of cache")
    p.add_argument("--refresh-repos", action="store_true", help="Re-fetch repo list from GitHub")
    p.add_argument("--only-repos", default="", help="Comma-separated subset of repo names to scan")
    p.add_argument("--concurrency", type=int, default=4, help="Concurrent repos (default: 4)")
    p.add_argument("--heartbeat", type=float, default=5.0, help="Heartbeat interval in seconds (default: 5)")
    return p


def main() -> None:
    args = build_parser().parse_args()
    try:
        sys.exit(asyncio.run(main_async(args)))
    except KeyboardInterrupt:
        sys.exit(130)


if __name__ == "__main__":
    main()
