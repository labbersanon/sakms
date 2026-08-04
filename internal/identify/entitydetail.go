package identify

import "context"

// SceneDescription fetches ONE scene's catalog description from exactly ONE box —
// the box the caller's entity_source names (AC4). Never a fan-out, never a merge:
// "the entity's own primary source wins" collapses to "only ask that source",
// which is also what makes the SCENE path's one-upstream-call bound structural
// rather than reviewed. This file's two functions do NOT share one bound: the
// entity path (EntityBio below) costs TWO upstream calls (an exact-name resolve,
// then a detail fetch), never one — see EntityBio's own doc comment for why.
// Best-effort: every failure path returns "" and no error.
func (id *Identifier) SceneDescription(ctx context.Context, box, sceneID string) (string, error) {
	// The same guard EnrichNewestScenes opens with (enrichnewest.go:55) —
	// sess.Identify is explicitly nil-able (internal/mode/mode.go:190).
	if id == nil || id.Boxes == nil || box == "" || sceneID == "" {
		return "", nil
	}

	switch box {
	case "tpdb":
		if id.Boxes.tpdb == nil {
			return "", nil
		}
		if err := id.Throttle.Wait(ctx, "tpdb"); err != nil {
			return "", nil
		}
		scene, err := id.Boxes.tpdb.GetSceneByID(ctx, sceneID)
		if err != nil || scene == nil {
			return "", nil
		}
		return scene.Description, nil

	// Claude 2026-08-04: was `case "stashdb", "fansdb":` (Stage 5 Wave 3).
	// Reason: any configured database name can reach here now, so the branch
	// is selected by "is there a stash-box client under this name" rather than
	// by a closed list of two. The nil-client guard already gave the right
	// answer for an unrecognised name, so widening the case changes nothing
	// for the routinely-reachable "prowlarr" a Show More drill-down carries —
	// it has no client, and still returns ("", nil) with no call made.
	// default:   // ← was: case "stashdb", "fansdb":
	default:
		client := id.Boxes.stashBoxes[box]
		if client == nil {
			return "", nil
		}
		if err := id.Throttle.Wait(ctx, box); err != nil {
			return "", nil
		}
		scene, err := client.FindScene(ctx, sceneID)
		if err != nil || scene == nil {
			return "", nil
		}
		return scene.Details, nil
	}
}

// EntityBio fetches a performer's bio or a studio's description from TPDB.
//
// STASH-BOX HAS NO SUCH FIELD. Live GraphQL introspection against stashdb.org and
// fansdb.cc (2026-08-02, unauthenticated) enumerates every field on Performer and
// Studio; neither type has a bio/details/description field, and neither has tags.
// This is proven absence, not an unverified assumption.
//
// So `box` is COERCED to "tpdb" unconditionally rather than honoured: a performer
// or studio's bio is always resolved via TPDB regardless of what catalog the
// drill-down's own entity_source names. This is a deliberate, Wade-approved
// exception to AC4 for the entity path only (2026-08-02, GATE-0 Option A), made
// because the strict reading — ask only the entity's own source — can never
// produce a banner for a stash-box-sourced performer or studio, there being no
// field to query. Scene descriptions are unaffected and still strictly follow
// entity_source (see SceneDescription above). Do not "fix" this by adding a
// stash-box query; there is nothing to query.
//
// Cost: at most TWO throttled upstream calls (an exact-name resolve, then a
// detail fetch) and never any fan-out across boxes. Best-effort throughout —
// every failure path returns "" and no error.
//
// Unstated precondition, worth knowing before diagnosing a missing banner
// (Critic round 2, R2-5): the caller resolves the entity's box from the newest
// pool BEFORE reaching here, and returns 200-empty on an empty box — which
// short-circuits ahead of this coercion. All four drill-down entry points are
// safe today (stash-box rows send source explicitly; newest rows are pool-backed
// by construction), but a pruned or absent pool row silently defeats Option A
// for that entity. Not special-cased on purpose.
func (id *Identifier) EntityBio(ctx context.Context, kind, name, box string) (string, error) {
	if id == nil || id.Boxes == nil || name == "" {
		return "", nil
	}
	box = "tpdb"

	// resolveEntityInBox is reused verbatim (enrichnewest.go:82) — it already
	// throttles, handles a nil tpdb client, and exact-name-matches both kinds.
	entityID := id.resolveEntityInBox(ctx, box, kind, name)
	if entityID == "" {
		return "", nil
	}
	if err := id.Throttle.Wait(ctx, box); err != nil {
		return "", nil
	}

	switch kind {
	case "studio":
		site, err := id.Boxes.tpdb.GetSiteByID(ctx, entityID)
		if err != nil {
			return "", nil
		}
		return site.Description, nil
	case "performer":
		performer, err := id.Boxes.tpdb.GetPerformerByID(ctx, entityID)
		if err != nil {
			return "", nil
		}
		return performer.Bio, nil
	}
	return "", nil
}
