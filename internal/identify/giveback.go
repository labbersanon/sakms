package identify

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/labbersanon/sakms/internal/stashbox"
)

var (
	ErrNoValidDuration         = errors.New("no valid duration — skipping fingerprint submission")
	ErrDraftSubmissionDisabled = errors.New("draft submission disabled for this run (account not authorized)")
)

// GiveBack submits identification results back to the community databases: a
// pHash fingerprint for an existing scene match, or a new scene draft when
// nothing existed anywhere.
//
// Boxes must include "stashdb"/"fansdb" and MAY include "tpdb" — TPDB's
// GraphQL endpoint is ALSO stash-box-protocol-compatible, so it uses the same
// stashbox.Client type here. This is a SEPARATE client from BoxSearcher's
// TPDB REST client, which is used for text search only.
type GiveBack struct {
	Boxes map[string]*stashbox.Client

	mu                    sync.Mutex
	draftSubmissionBroken bool // latched true after a "not authorized" response — for this run only
}

func NewGiveBack(boxes map[string]*stashbox.Client) *GiveBack {
	return &GiveBack{Boxes: boxes}
}

// SubmitFingerprint submits a pHash for an existing scene. Requires a valid
// (non-zero) duration — stash-boxes cross-check submitted durations against
// the scene's known runtime, so a 0/missing duration is worse than not
// submitting at all.
func (g *GiveBack) SubmitFingerprint(ctx context.Context, box, sceneID, phash string, durationSeconds int) error {
	if durationSeconds <= 0 {
		return ErrNoValidDuration
	}
	client, ok := g.Boxes[box]
	if !ok || client == nil {
		return fmt.Errorf("box %q not configured — cannot submit fingerprint", box)
	}
	return client.SubmitFingerprint(ctx, sceneID, phash, durationSeconds)
}

// SubmitDraft submits a new scene draft for community review, when
// AI+web-search confidently identified a file but it matches NO existing
// scene anywhere. TPDB is preferred when configured (it's the box the
// original CLI ultimately submitted unknown titles to); StashDB is the
// fallback when TPDB isn't configured.
//
// Auto-disables draft submission for the REST of this run's lifetime (this
// GiveBack instance) once a "not authorized" response is seen — the current
// API key's account may lack submission privilege. This avoids logging the
// same warning once per file for the rest of the run. Fingerprint submission
// uses a different permission and is unaffected.
func (g *GiveBack) SubmitDraft(ctx context.Context, title, studio, date string) (string, error) {
	g.mu.Lock()
	broken := g.draftSubmissionBroken
	g.mu.Unlock()
	if broken {
		return "", ErrDraftSubmissionDisabled
	}

	client, ok := g.Boxes["tpdb"]
	if !ok || client == nil {
		client, ok = g.Boxes["stashdb"]
	}
	if !ok || client == nil {
		return "", fmt.Errorf("neither tpdb nor stashdb configured — cannot submit a draft")
	}

	draftID, err := client.SubmitSceneDraft(ctx, title, studio, date)
	if err != nil {
		if stashbox.IsNotAuthorized(err) {
			g.mu.Lock()
			g.draftSubmissionBroken = true
			g.mu.Unlock()
		}
		return "", err
	}
	return draftID, nil
}

// DraftSubmissionBroken reports whether draft submission has been latched off
// for this run (for logging/status purposes).
func (g *GiveBack) DraftSubmissionBroken() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.draftSubmissionBroken
}
