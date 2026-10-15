package api

import (
	"context"
	"errors"
	"log"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

// Claude 2026-10-15: passive NNTP group demotion on article failures.
// Reason: grade starts at 1; +1 per failed grab (GID); grade>ceiling deletes index.
// Troubleshooting: only sakms-nntp locators; transport/password/no-video skip.
// Review if: content unpack failures should also demote (rejected — not group signal).
// Related files: internal/usenetsearch/grades.go, usenetretry.go, search.go

func shouldDemoteNativeGroup(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, usenet.ErrTransport) {
		return false
	}
	return errors.Is(err, usenet.ErrArticlesUnavailable) ||
		errors.Is(err, usenet.ErrArticleNotFound) ||
		errors.Is(err, usenet.ErrArticleRemoved)
}

func demoteNativeGroupFromURL(ctx context.Context, svc *usenetsearch.Service, m mode.Mode, downloadURL string, failure error) {
	if svc == nil || !shouldDemoteNativeGroup(failure) || !usenetsearch.IsLocator(downloadURL) {
		return
	}
	parts, ok, err := usenetsearch.DecodeLocator(downloadURL)
	if err != nil || !ok || parts.Group == "" {
		return
	}
	log.Printf("usenetsearch: demote from failure mode=%s group=%s err=%v", m, parts.Group, failure)
	svc.DemoteForFailure(ctx, string(m), parts.Group)
}

func demoteNativeGroupFromGrab(ctx context.Context, deps AutoGrabDeps, g grabs.Grab, failure error) {
	// Claude 2026-10-15: UsenetErrorHandler/retry sweep omit UsenetSearch on deps.
	// Reason: demote was a no-op for mid-download 430/451 without this fallback.
	// Troubleshooting: grade stuck at 1 after live failures — check WithUsenetSearch.
	// Review if: AutoGrabDeps always carries UsenetSearch from those callers.
	demoteNativeGroupFromURL(ctx, WithUsenetSearch(deps).UsenetSearch, g.Mode, g.DownloadURL, failure)
}

// promoteNativeGroupFromGrab notes a successful import for sakms-nntp grabs.
func promoteNativeGroupFromGrab(ctx context.Context, g grabs.Grab) {
	if !usenetsearch.IsLocator(g.DownloadURL) {
		return
	}
	parts, ok, err := usenetsearch.DecodeLocator(g.DownloadURL)
	if err != nil || !ok || parts.Group == "" {
		return
	}
	svc := getNNTPNativeService()
	if svc == nil {
		return
	}
	svc.PromoteForSuccess(ctx, string(g.Mode), parts.Group)
}
