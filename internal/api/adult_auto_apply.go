package api

// Claude 2026-08-12: background auto-apply for Adult alternates after scan
// Reason: web-identified rows approved via Review mint local identities; duplicate
//   downloads must fold as alternates without resurfacing in the Rename queue.
// Troubleshooting: same phash kept appearing as web-identified after Review confirm.
// Review if: operators need a toggle to disable unattended Adult alternate apply.

import (
	"context"
	"log"
	"strings"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/settings"
)

// maybeAutoApplyAdultLibrary applies Pending Adult Rename proposals that are
// already-tracked alternates (including local identities confirmed via Review).
// Soft-fail per row — never changes HTTP success of Scan.
func maybeAutoApplyAdultLibrary(
	ctx context.Context,
	sess *mode.Session,
	settingsStore *settings.Store,
	propStore *proposals.Store,
	libStore *library.Store,
	prober deduprober,
	saved []proposals.Proposal,
) {
	if sess == nil || libStore == nil || len(saved) == 0 {
		return
	}

	proposals.DefaultGate.BeginApply(mode.Adult, proposals.Rename)
	defer proposals.DefaultGate.EndApply(mode.Adult, proposals.Rename)

	var changes []mode.PathChange
	for _, p := range saved {
		if p.Mode != mode.Adult || p.Workflow != proposals.Rename || p.Status != proposals.Pending {
			continue
		}
		if !strings.Contains(p.Reason, "already in library") {
			continue
		}
		ch, err := applyByWorkflow(ctx, settingsStore, propStore, libStore, sess, p, applyProposalRequest{}, false, prober)
		if err != nil {
			log.Printf("rename auto-apply (adult): proposal %d soft-fail: %v", p.ID, err)
			continue
		}
		changes = append(changes, ch...)
	}
	if len(changes) > 0 {
		sess.NotifyPlayers(ctx, changes)
	}
}
