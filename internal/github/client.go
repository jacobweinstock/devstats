// Package github is the repo-walk fetch layer. It walks every repository in an
// org and counts contributions via GraphQL (githubv4) for issues, PRs, reviews
// and commit history, and via REST (go-github) for the three comment endpoints.
package github

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v75/github"
	"github.com/shurcooL/githubv4"
)

const userAgent = "tink-contributions/0.2"

// Token resolves a GitHub token from GITHUB_TOKEN/GH_TOKEN, falling back to the
// `gh` CLI.
func Token() (string, error) {
	for _, env := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(env)); t != "" {
			return t, nil
		}
	}
	path, err := exec.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("no GITHUB_TOKEN/GH_TOKEN set and `gh` CLI not found; run `gh auth login`")
	}
	out, err := exec.Command(path, "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("`gh auth token` failed: %w", err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("`gh auth token` returned empty; run `gh auth login`")
	}
	return tok, nil
}

// Client bundles the REST and GraphQL clients over one authed, retrying transport.
type Client struct {
	REST    *github.Client
	GraphQL *githubv4.Client
}

// New builds a Client authenticated with token.
func New(token string) *Client {
	hc := &http.Client{
		Timeout:   90 * time.Second,
		Transport: &authTransport{token: token, base: http.DefaultTransport},
	}
	return &Client{
		REST:    github.NewClient(hc),
		GraphQL: githubv4.NewClient(hc),
	}
}

// authTransport injects auth headers and retries on rate-limit and 5xx responses.
type authTransport struct {
	token string
	base  http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("User-Agent", userAgent)

	// Buffer the body so POST (GraphQL) requests can be replayed on retry.
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}

	const (
		maxBackoff = 60 * time.Second
		maxReset   = 65 * time.Minute
	)
	backoff := 2 * time.Second
	var resp *http.Response
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		if body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		resp, err = t.base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests || isRateLimited(resp):
			wait, primaryReset := waitDuration(resp, backoff, maxReset)
			drain(resp)
			if err := sleep(req.Context(), wait); err != nil {
				return nil, err
			}
			// A primary-limit reset already waited a full window; don't also grow backoff.
			if !primaryReset {
				backoff = min(backoff*2, maxBackoff)
			}
		case resp.StatusCode >= 500:
			drain(resp)
			if err := sleep(req.Context(), backoff); err != nil {
				return nil, err
			}
			backoff = min(backoff*2, maxBackoff)
		default:
			return resp, nil
		}
	}
	return resp, err
}

// isRateLimited distinguishes a secondary-rate-limit 403 from a genuine 403.
func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden {
		return false
	}
	if resp.Header.Get("Retry-After") != "" {
		return true
	}
	return resp.Header.Get("X-RateLimit-Remaining") == "0"
}

// waitDuration returns how long to wait before retrying a rate-limited response
// and whether the wait is for a primary-limit reset. Secondary limits carry a
// Retry-After header; a primary limit (X-RateLimit-Remaining: 0) is honored by
// waiting until X-RateLimit-Reset (capped by maxReset) so a full-backfill run
// self-throttles across the hourly window instead of failing. The reset flag
// tells the caller not to also grow its backoff.
func waitDuration(resp *http.Response, fallback, maxReset time.Duration) (time.Duration, bool) {
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			return time.Duration(secs) * time.Second, false
		}
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if rs := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); rs != "" {
			if epoch, err := strconv.ParseInt(rs, 10, 64); err == nil {
				d := time.Until(time.Unix(epoch, 0)) + time.Second
				if d <= 0 {
					return fallback, false
				}
				if d > maxReset {
					d = maxReset
				}
				return d, true
			}
		}
	}
	return fallback, false
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
