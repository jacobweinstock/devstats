package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jacobweinstock/devstats/internal/webexport"
)

// Serve serves the static site directory over HTTP at addr until ctx is
// cancelled. It never touches GitHub or a date range: the leaderboard data at
// /data/events.json is built from the full cache on disk (every cached month,
// all history), so --serve always shows whatever is cached regardless of any
// --since. Everything else (index.html, app.js, ...) is served from siteDir.
func Serve(ctx context.Context, addr, siteDir, cacheDir, org string) error {
	if fi, err := os.Stat(siteDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("--site %q is not a directory", siteDir)
	}

	mux := http.NewServeMux()

	// Build events.json from the full cache and serve it in memory, overriding
	// any (possibly stale/range-limited) file committed under siteDir/data.
	data, err := webexport.BuildFromCache(cacheDir, org)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: reading cache %q: %v (serving static data instead)\n", cacheDir, err)
	} else if len(data.Events) == 0 {
		fmt.Fprintf(os.Stderr, "warning: no cached events in %q (serving static data instead)\n", cacheDir)
	} else {
		payload, merr := json.Marshal(data)
		if merr != nil {
			return merr
		}
		mux.HandleFunc("/data/events.json", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(payload)
		})
		fmt.Fprintf(os.Stderr, "Serving %d cached events (%d contributors, %d repos, full history)\n",
			len(data.Events), len(data.Logins), len(data.Repos))
	}

	mux.Handle("/", http.FileServer(http.Dir(siteDir)))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	shown := addr
	if strings.HasPrefix(shown, ":") {
		shown = "localhost" + shown
	}
	fmt.Fprintf(os.Stderr, "Serving %s at http://%s (Ctrl-C to stop)\n", siteDir, shown)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
