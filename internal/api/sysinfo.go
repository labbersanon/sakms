package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/sysinfo"
)

// sysinfoStreamHandler streams live resource metrics as server-sent events.
// It takes the first sample on connect, then every tick (2s by default)
// samples again and emits the rate snapshot computed against the previous
// sample. The response is always 200 with Content-Type text/event-stream;
// individual sample errors are sent as named SSE "sampleError" events (not
// HTTP errors, and deliberately NOT the reserved "error" event name so they
// don't collide with EventSource's transport-level onerror handler) so the
// client can display a transient warning without losing the connection.
//
// tickInterval is variadic purely so tests can inject a short interval; a
// production call (handler.go) passes only sampleFn and gets the 2s default,
// keeping the registration a clean one-arg call.
func sysinfoStreamHandler(
	sampleFn func([]sysinfo.MountSpec) (sysinfo.RawSample, error),
	mountsFn func(context.Context) []sysinfo.MountSpec,
	tickInterval ...time.Duration,
) http.HandlerFunc {
	interval := 2 * time.Second
	if len(tickInterval) > 0 && tickInterval[0] > 0 {
		interval = tickInterval[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ctx := r.Context()
		prev, err := sampleFn(mountsFn(ctx))
		if err != nil {
			fmt.Fprintf(w, "event: sampleError\ndata: %s\n\n", err.Error())
			flusher.Flush()
			return
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				curr, err := sampleFn(mountsFn(ctx))
				if err != nil {
					fmt.Fprintf(w, "event: sampleError\ndata: %s\n\n", err.Error())
					flusher.Flush()
					continue
				}
				snap := sysinfo.ComputeRates(prev, curr)
				prev = curr
				data, _ := json.Marshal(snap)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

// buildMountsFromSettings resolves the storage mounts the dashboard measures:
// the fixed "/data" App data volume plus each mode's configured library root
// folder. A mode whose root folder is unset (settings.ErrNotFound) or errors
// is reported with an empty Path, which sampleFromPaths renders as
// Configured: false rather than dropping it — the card still shows, marked
// "Not configured". Resolved on every tick so a root-folder settings change
// takes effect live without reconnecting.
func buildMountsFromSettings(ctx context.Context, s *settings.Store) []sysinfo.MountSpec {
	mounts := []sysinfo.MountSpec{{Name: "App data", Path: "/data"}}
	for _, m := range []mode.Mode{mode.Movies, mode.Series, mode.Adult} {
		key, ok := libraryRootFolderKey(m)
		if !ok {
			continue
		}
		path, err := s.Get(ctx, key)
		if err != nil {
			path = "" // ErrNotFound or any other error → not configured
		}
		label := string(m)                             // "movies"
		label = strings.ToUpper(label[:1]) + label[1:] // "Movies", "Series", "Adult"
		mounts = append(mounts, sysinfo.MountSpec{Name: label, Path: path})
	}
	return mounts
}
