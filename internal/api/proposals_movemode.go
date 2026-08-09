package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/settings"
)

// moveModeRequest mirrors apidto.MoveModeRequest byte-for-byte on the wire —
// see that type's doc comment (internal/apidto/dto.go) for the full design
// rationale (why it is NOT an extension of repickProposalRequest, why
// SeasonNumber/EpisodeNumber are pointers, etc).
type moveModeRequest struct {
	TargetMode string `json:"targetMode"`

	// --- Movies / Series (TMDB) ---
	TMDBID int    `json:"tmdbId,omitempty"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`

	SeasonNumber  *int `json:"seasonNumber,omitempty"`
	EpisodeNumber *int `json:"episodeNumber,omitempty"`

	// --- Adult (stash-box / TPDB) ---
	Box     string `json:"box,omitempty"`
	SceneID string `json:"sceneId,omitempty"`
	Studio  string `json:"studio,omitempty"`
	Date    string `json:"date,omitempty"`
}

// moveProposalModeHandler implements POST /api/proposals/{id}/move-mode — the
// cross-mode "move to another section" commit endpoint (AC1/AC2). See
// .omc/plans/autopilot-impl.md §4.4 for the full 12-step flow this follows in
// order, and D-1a for why the Adult section-lock gate (step 8 below) exists
// and must never be skipped or reordered.
//
// This endpoint is deliberately UNGATED for Movies<->Series moves (D-1) — the
// gate only fires when the source OR target mode is Adult. Because of that,
// no 4xx response body written by this handler may ever contain a filesystem
// path, a library root, a title, or a studio name (§4.3's error-message
// rule) — every message below states what the caller did wrong, never what
// the server holds.
func moveProposalModeHandler(propStore *proposals.Store, settingsStore *settings.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Step 1: parse the proposal id.
		id, ok := parseProposalID(w, r)
		if !ok {
			return
		}
		ctx := r.Context()

		// Step 2: decode the body.
		var req moveModeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Step 3: validate TargetMode.
		var target mode.Mode
		switch req.TargetMode {
		case string(mode.Movies):
			target = mode.Movies
		case string(mode.Series):
			target = mode.Series
		case string(mode.Adult):
			target = mode.Adult
		default:
			http.Error(w, `targetMode must be "movies", "series", or "adult"`, http.StatusBadRequest)
			return
		}

		// Step 4: load the proposal.
		p, err := propStore.Get(ctx, id)
		if err != nil {
			proposalNotFoundOr500(w, err)
			return
		}

		// Step 5: workflow gate — Rename and Dedup are eligible; Purge has no
		// identification to move.
		if p.Workflow == proposals.Purge {
			http.Error(w, "purge proposals have no identification to move", http.StatusBadRequest)
			return
		}

		// Step 6: status gate — Pending or Unmatched only, same message shape
		// as repickProposalHandler's equivalent check.
		if p.Status != proposals.Pending && p.Status != proposals.Unmatched {
			http.Error(w, fmt.Sprintf("proposal %d is %q — only pending or unmatched proposals can be moved", id, p.Status), http.StatusBadRequest)
			return
		}

		// Step 7: same-mode gate.
		if p.Mode == target {
			http.Error(w, "targetMode must differ from the proposal's current mode", http.StatusBadRequest)
			return
		}

		// Step 8 — D-1a. LOAD-BEARING SECURITY GATE. Runs after step 4's
		// propStore.Get (needs p.Mode) and before any write or destructive
		// action, exactly as the plan requires. Do not skip or reorder.
		//
		// Claude 2026-08-08: gate cross-mode moves that touch Adult, in BOTH directions.
		// Reason: MoveMode preserves source_name/source_path, and an Adult filename is
		//   "Studio - Title (Date) [phash-...]". classifyModes keys the Adult gate on the
		//   PATH (routes.go:156-158), so a moved-to-Movies row is readable from the
		//   ungated Movies queue. Ungated, this endpoint is an enumerable Adult-library
		//   exfiltration primitive — see .omc/plans/autopilot-impl.md D-1a.
		// Troubleshooting: PIN-locked session can list Adult titles via the Movies queue.
		// Review if: source_name/source_path ever stop carrying the original filename.
		if (p.Mode == mode.Adult || target == mode.Adult) && denyIfAdultLocked(w, r) {
			return
		}

		// Step 9: per-target-mode dispatch and validation (AC2). No catalog
		// verification against TMDB/stash-box is performed — same trust model
		// repickProposalHandler already uses for an operator-picked search
		// result (see plan §4.4 step 9's closing note).
		t := proposals.MoveTarget{Mode: target}
		switch target {
		case mode.Movies:
			if req.TMDBID <= 0 || req.Title == "" {
				http.Error(w, "tmdbId and title are required to move to movies", http.StatusBadRequest)
				return
			}
			if req.SeasonNumber != nil || req.EpisodeNumber != nil {
				http.Error(w, "season/episode assignment is only supported for series", http.StatusBadRequest)
				return
			}
			t.Title, t.TMDBID, t.Year = req.Title, req.TMDBID, req.Year

		case mode.Series:
			if req.TMDBID <= 0 || req.Title == "" {
				http.Error(w, "tmdbId and title are required to move to series", http.StatusBadRequest)
				return
			}
			hasSeason, hasEpisode := req.SeasonNumber != nil, req.EpisodeNumber != nil
			switch {
			case !hasSeason && !hasEpisode:
				// show-level move — both stay 0.
			case hasSeason != hasEpisode:
				http.Error(w, "seasonNumber and episodeNumber must be supplied together", http.StatusBadRequest)
				return
			case *req.SeasonNumber < 0 || *req.EpisodeNumber < 1:
				// season 0 IS valid (Specials); episode 0 is not.
				http.Error(w, "seasonNumber must be >= 0 and episodeNumber must be >= 1", http.StatusBadRequest)
				return
			}
			t.Title, t.TMDBID, t.Year = req.Title, req.TMDBID, req.Year
			if hasSeason {
				t.SeasonNumber = *req.SeasonNumber
			}
			if hasEpisode {
				t.EpisodeNumber = *req.EpisodeNumber
			}

		case mode.Adult:
			if req.Box == "" || req.SceneID == "" || req.Title == "" {
				http.Error(w, "box, sceneId, and title are required to move to adult", http.StatusBadRequest)
				return
			}
			switch req.Box {
			case "stashdb", "fansdb", "tpdb":
			default:
				http.Error(w, `box must be "stashdb", "fansdb", or "tpdb"`, http.StatusBadRequest)
				return
			}
			foreignID, ok := (identify.MatchResult{SceneID: req.SceneID, Box: req.Box}).WhisparrForeignID()
			if !ok {
				http.Error(w, "box and sceneId must resolve to a valid identifier", http.StatusBadRequest)
				return
			}
			// D-2: refuse a move-to-Adult whose (box, sceneId) is already
			// tracked, mirroring the scan path's demotion check. Deliberately
			// a BARE 409 — no title or path echoed, unlike the scan path's
			// message, because this endpoint is ungated for Movies<->Series
			// and a caller-supplied (box, sceneId) here must never become an
			// Adult-library lookup oracle (see plan D-2).
			if _, err := libStore.GetScene(ctx, req.Box, req.SceneID); err == nil {
				http.Error(w, "this scene is already tracked in the library", http.StatusConflict)
				return
			} else if !errors.Is(err, library.ErrNotFound) {
				http.Error(w, "checking whether this scene is already tracked", http.StatusInternalServerError)
				return
			}
			t.Title, t.Studio, t.Date = req.Title, req.Studio, req.Date
			t.GiveBackBox, t.GiveBackSceneID = req.Box, req.SceneID
			t.ForeignID, t.ItemType = foreignID, "scene"
		}

		// Step 10: root-folder resolution (§4.3) — Rename only. A Dedup
		// proposal's files have not moved (Dedup Apply never relocates), so
		// its existing root_folder_path is preserved instead of being
		// rewritten to the target root.
		if p.Workflow == proposals.Dedup {
			t.RootFolderPath = p.RootFolderPath
		} else {
			key, ok := libraryRootFolderKey(target)
			if !ok {
				http.Error(w, "unknown target mode", http.StatusBadRequest)
				return
			}
			// A missing key is not an error here — it means "not configured
			// yet", handled by the empty-string check below. Any OTHER error
			// is a real storage failure.
			rootPath, err := settingsStore.Get(ctx, key)
			if err != nil && !errors.Is(err, settings.ErrNotFound) {
				http.Error(w, "reading the target mode's library root folder", http.StatusInternalServerError)
				return
			}
			if rootPath == "" {
				// Generic on purpose: this endpoint is ungated for
				// Movies<->Series moves, so the message must not disclose the
				// configured root path, nor which modes have one.
				http.Error(w, "the target mode's library root folder is not configured — set it in Settings first", http.StatusBadRequest)
				return
			}
			t.RootFolderPath = rootPath
		}

		// Step 10.5: fast-path conflict pre-check — a better error, not the
		// correctness guarantee (that's step 11's transaction / the unique
		// index it maps to ErrModeMoveConflict). TOCTOU-racy by nature.
		if p.Workflow == proposals.Rename && p.SourcePath != "" {
			if _, err := propStore.GetLiveBySourcePath(ctx, target, p.Workflow, p.SourcePath); err == nil {
				http.Error(w, "a live proposal for this file already exists in the target mode", http.StatusConflict)
				return
			} else if !errors.Is(err, proposals.ErrNotFound) {
				http.Error(w, "checking for a conflicting proposal", http.StatusInternalServerError)
				return
			}
		}

		// Step 10.6: build (do not execute) the retirement closure (§4.2b).
		// Every candidate's TrackedID is zeroed on the copy handed to
		// MoveMode (§4.2a — Apply must never delete an unrelated file), and
		// every non-zero TrackedID (plus the proposal's own, if non-zero) is
		// collected so its SOURCE-mode tracking row is deleted inside
		// MoveMode's own transaction.
		var retiredIDs []int
		if len(p.Candidates) > 0 {
			t.Candidates = make([]proposals.Candidate, len(p.Candidates))
			for i, c := range p.Candidates {
				if c.TrackedID != 0 {
					retiredIDs = append(retiredIDs, c.TrackedID)
				}
				c.TrackedID = 0
				t.Candidates[i] = c
			}
		}
		if p.TrackedID != 0 {
			retiredIDs = append(retiredIDs, p.TrackedID)
		}

		var retire proposals.RetireFunc
		if len(retiredIDs) > 0 {
			sourceMode := p.Mode
			retire = func(ctx context.Context, tx *sql.Tx) error {
				for _, tid := range retiredIDs {
					var err error
					switch sourceMode {
					case mode.Movies:
						err = libStore.DeleteItemTx(ctx, tx, int64(tid))
					case mode.Series:
						err = libStore.DeleteEpisodeTx(ctx, tx, int64(tid))
					case mode.Adult:
						err = libStore.DeleteSceneTx(ctx, tx, int64(tid))
					}
					if err != nil {
						return err
					}
				}
				return nil
			}
		}

		// Step 11: one transaction covering both the retirement and the mode
		// UPDATE — a conflict rolls both back together (§4.2b).
		if err := propStore.MoveMode(ctx, id, t, retire); err != nil {
			switch {
			case errors.Is(err, proposals.ErrModeMoveConflict):
				http.Error(w, "a live proposal for this file already exists in the target mode", http.StatusConflict)
			case errors.Is(err, proposals.ErrNotFound):
				http.Error(w, err.Error(), http.StatusNotFound)
			default:
				http.Error(w, "moving the proposal to the target mode", http.StatusInternalServerError)
			}
			return
		}

		// Step 12: 204, no body. Deliberate — see plan §4.4 step 12: the
		// UPDATE preserves source_name/source_path, and echoing the row on a
		// move out of Adult would hand Adult scene metadata to a PIN-locked
		// session through this (Movies<->Series-)ungated endpoint. Callers
		// refetch through the gated list endpoint instead.
		w.WriteHeader(http.StatusNoContent)
	}
}
