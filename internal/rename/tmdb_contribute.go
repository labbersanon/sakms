package rename

import (
	"context"
	"fmt"
	"strings"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// Claude 2026-08-06: mainstream TMDB contribute (soft-fail)
// Reason: deep-interview-rename-apply-all-giveback-settings — authenticated
//   contribution when web-authority identifies a title.
// Troubleshooting: always errors until TMDB exposes create-title; session still required.
// Review if: TMDB ships a public contribution write API.

// ContributeTMDBTitle attempts to submit a web-identified title to TMDB.
// TMDB's public API has no create-movie/create-tv contribution endpoint for
// API keys + session_id (only ratings/lists/favorites). This soft-fails with
// a clear error so Scan never blocks; session_id is validated as present so
// Settings login is still required when the toggle is on.
func ContributeTMDBTitle(ctx context.Context, sess *mode.Session, sessionID string, p proposals.Proposal) error {
	_ = ctx
	_ = sess
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("tmdb session not configured — sign in under Settings")
	}
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("empty title")
	}
	return fmt.Errorf("tmdb public API has no create-title contribution endpoint for session writes (title %q year %d); session is stored for when/if TMDB exposes one", p.Title, p.Year)
}
