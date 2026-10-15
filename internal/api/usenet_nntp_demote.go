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
	demoteNativeGroupFromURL(ctx, deps.UsenetSearch, g.Mode, g.DownloadURL, failure)
}
