// Package whisparrimport is a one-time, human-triggered importer that reads a
// still-live Whisparr V3 instance and populates internal/library's Scene
// table from what Whisparr already tracks — the migration path off Whisparr
// for an existing Adult library (see the plan this was built from, Stage 3).
//
// This package makes ZERO write calls to Whisparr — it only reads
// (AllTracked) — and is meant to be run once during migration, then never
// again. It's safe to re-run anyway: every write here goes through
// UpsertScene (idempotent, ON CONFLICT(box, scene_id)), so a second run just
// re-confirms the same rows rather than duplicating anything.
//
// The bridge is simpler than Sonarr's in one way — Whisparr's ForeignID is
// already the stash-box UUID identity space SAK's library uses, so there's no
// cross-namespace ID translation — and harder in another: a bare UUID alone
// doesn't say which box produced it (StashDB and FansDB both yield raw UUIDs
// in the same shape). So for a bare UUID this probes StashDB first, then
// FansDB; whichever resolves it both attributes the box AND supplies the
// Title/Studio/Date metadata. A "tpdbId:<id>"-prefixed ForeignID (Whisparr's
// own encoding for a TPDB-only match, see identify.MatchResult.WhisparrForeignID)
// is already unambiguous — box "tpdb", no probe needed.
//
// Deliberate MVP limitation, not a silent gap: a UUID neither box resolves is
// still stored, with box="" — give-back won't work for that scene until a
// later Rename scan re-identifies it. Acceptable for a one-time migration
// tool. FilePath is taken directly from the tracked item's on-disk path; if
// that path no longer exists, the item is skipped and recorded rather than
// importing a library row that points at nothing.
package whisparrimport

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
)

// SceneResult reports what happened importing one of Whisparr's tracked
// scenes.
type SceneResult struct {
	Title string `json:"title"`
	// Imported is false if this scene was skipped entirely (see Reason) —
	// still not an error for the whole run, since every other scene gets its
	// own independent chance (see Import's doc comment).
	Imported bool   `json:"imported"`
	Reason   string `json:"reason,omitempty"` // populated when Imported is false
}

// Result is the full summary of one Import run.
type Result struct {
	Scenes []SceneResult `json:"scenes"`
}

// Import reads every scene Whisparr currently tracks, resolves each to a
// (box, scene_id) identity, and records it in libStore. A failure on one
// scene (a box API error, the file having vanished from disk) is recorded in
// that scene's SceneResult and does NOT stop the run — every other scene
// still gets a chance, the same fault-isolation sonarrimport uses per series.
//
// boxes probes StashDB/FansDB for a bare UUID's box attribution + metadata; a
// nil-configured box degrades to "not resolved" (box=""), never an error.
func Import(ctx context.Context, whisparr *servarr.Client, boxes *identify.BoxSearcher, libStore *library.Store) (Result, error) {
	tracked, err := whisparr.AllTracked(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("loading Whisparr's tracked scenes: %w", err)
	}

	var result Result
	for _, t := range tracked {
		result.Scenes = append(result.Scenes, importOne(ctx, boxes, libStore, t))
	}
	return result, nil
}

func importOne(ctx context.Context, boxes *identify.BoxSearcher, libStore *library.Store, t servarr.TrackedItem) SceneResult {
	sr := SceneResult{Title: t.Title}

	// An item Whisparr tracks with no ForeignID has no stash-box identity to
	// key on — and box="" + scene_id="" is the one pair that would collide
	// under UNIQUE(box, scene_id), collapsing every such item onto one row.
	// Skip it rather than corrupt the table.
	if t.ForeignID == "" {
		sr.Reason = "Whisparr tracked this item with no ForeignID — no stash-box identity to import"
		return sr
	}

	// FilePath is whatever on-disk path Whisparr recorded (the item folder for
	// a Whisparr V3 / Radarr-fork item). A library row that points at a file
	// that's gone is worse than not importing it, so skip-and-record here
	// rather than abort the whole run.
	if _, err := os.Stat(t.Path); err != nil {
		sr.Reason = "file no longer exists on disk"
		return sr
	}

	scene := library.Scene{
		Title:          t.Title,
		FilePath:       t.Path,
		RootFolderPath: t.RootFolderPath,
	}

	if id, ok := strings.CutPrefix(t.ForeignID, "tpdbId:"); ok {
		// Already unambiguous — Whisparr's own encoding for a TPDB-only match.
		// No box probe: box="tpdb", metadata stays whatever Whisparr carried.
		scene.Box = "tpdb"
		scene.SceneID = id
	} else {
		// Bare UUID — probe StashDB first, then FansDB (buildIdentifier's own
		// order). SceneByID returns (nil,nil) on both a real miss and an
		// unconfigured box, so an unresolved UUID falls through to box="".
		scene.SceneID = t.ForeignID
		for _, box := range []string{"stashdb", "fansdb"} {
			res, err := boxes.SceneByID(ctx, box, t.ForeignID)
			if err != nil {
				sr.Reason = fmt.Sprintf("looking up %s for scene %q: %v", box, t.ForeignID, err)
				return sr
			}
			if res != nil {
				scene.Box = res.Box
				scene.SceneID = res.SceneID
				scene.Title = res.Title
				scene.Studio = res.Studio
				scene.Date = res.Date
				break
			}
		}
	}

	if _, err := libStore.UpsertScene(ctx, scene); err != nil {
		sr.Reason = fmt.Sprintf("recording the scene in the library failed: %v", err)
		return sr
	}

	sr.Title = scene.Title
	sr.Imported = true
	return sr
}
