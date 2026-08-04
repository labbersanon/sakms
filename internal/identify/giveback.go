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

	// Claude 2026-08-04: added Order (Stage 5 Wave 3, plan
	// .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md §3.2).
	// Reason: SubmitDraft's non-TPDB fallback was the literal
	// Boxes["stashdb"], which silently stops working on an install whose
	// first-priority database is something else. Boxes is a MAP and so has no
	// order of its own; this is the cascade order alongside it.
	// Troubleshooting: EMPTY MEANS LEGACY — an empty Order falls back to
	// "stashdb", so every hand-built GiveBack (all of them in tests) behaves
	// exactly as before.
	// Review if: GiveBack ever gains a second order-dependent operation, at
	// which point this should probably become []DatabaseRef like
	// Identifier.StashBoxes rather than a bare name list.

	// Order is the configured databases' cascade order, TPDB excluded — used
	// to pick SubmitDraft's fallback target. Client lookup still goes through
	// Boxes; this only decides which name to try first.
	Order []string

	mu                    sync.Mutex
	draftSubmissionBroken bool // latched true after a "not authorized" response — for this run only
}

func NewGiveBack(boxes map[string]*stashbox.Client) *GiveBack {
	return &GiveBack{Boxes: boxes}
}

// draftOrder is Order with the pre-Stage-5 default applied. See Order's
// comment for why empty means "stashdb" rather than "nothing".
func (g *GiveBack) draftOrder() []string {
	if len(g.Order) == 0 {
		return []string{"stashdb"}
	}
	return g.Order
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

	// Claude 2026-08-04: the fallback walks Order instead of reading the
	// literal Boxes["stashdb"] (Stage 5 Wave 3). On a default install Order is
	// ["stashdb","fansdb"], so the first configured client IS stashdb and the
	// behaviour is unchanged; the walk only matters once an operator has
	// reordered or removed it.
	// client, ok = g.Boxes["stashdb"]   // ← was: the single hardcoded fallback
	client, ok := g.Boxes["tpdb"]
	if !ok || client == nil {
		for _, name := range g.draftOrder() {
			if c := g.Boxes[name]; c != nil {
				client, ok = c, true
				break
			}
		}
	}
	if !ok || client == nil {
		return "", fmt.Errorf("neither tpdb nor any configured stash-box database is available — cannot submit a draft")
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
