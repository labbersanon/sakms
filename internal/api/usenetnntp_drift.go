package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

// Claude 2026-10-16: wire ChatJSON + HTTP fetch into NNTP collection drift.
// Reason: usenetsearch must not import identify/mode; api owns settings
//   and secrets. BuildAIClient is nil unless AI fallback is on.
// Troubleshooting: drift pass no-ops → ai_fallback_enabled false or no model.
// Review if: Settings grows a collection-drift toggle beyond the KV kill switch.
// Related files: internal/usenetsearch/drift.go, cmd/sakms/main.go

const maxSeederBytes = 8 << 20

// AttachNNTPCollectionDrift injects the AI client factory and seeder GET.
func AttachNNTPCollectionDrift(svc *usenetsearch.Service, connStore *connections.Store, settingsStore *settings.Store, httpClient *http.Client) {
	if svc == nil {
		return
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	svc.SetDriftAI(func(ctx context.Context) usenetsearch.DriftAI {
		ai, err := mode.BuildAIClient(ctx, connStore, settingsStore, httpClient)
		if err != nil || ai == nil {
			return nil
		}
		return ai
	})
	svc.SetDriftFetch(func(ctx context.Context, rawURL string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		res, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("seeder fetch HTTP %d", res.StatusCode)
		}
		return io.ReadAll(io.LimitReader(res.Body, maxSeederBytes))
	})
}
