// Package tag implements Tidyarr's native tag management: browsing a mode's
// existing tag vocabulary and assigning/removing tags on tracked items,
// always through the app's own Tags resource — Radarr, Sonarr, and Whisparr
// V3 all expose the identical shape (see internal/servarr's Tag/Tags/
// CreateTag/UpdateItemTags), so one small package covers every mode instead
// of a bespoke tag-mutation client per app.
//
// Unlike Rename/Purge/Dedup, assigning or removing a tag is not staged
// through the proposals review queue: it's already a single, deliberate,
// atomic action a human takes (pick a tag, click it), the same shape as
// Settings' own Save/Delete actions — there's no automatic decision here
// that needs surfacing for approval first. AI-suggested tags (a genuinely
// automatic decision, and the design spec's "most new backend work" item
// for this section) aren't implemented yet: Tidyarr has no per-mode AI-
// provider/model configuration to draw on today (Settings' "AI providers"
// group is still mockup-only), and building a real suggestion feature on
// top of a guessed model name would be exactly the kind of half-built
// capability this project avoids. Kids/general classification (porting
// sonarr-radarr-sort's internal/classify) is also deferred, for the same
// reason: it's a separate, real chunk of work, not a one-line addition.
package tag

import (
	"context"
	"fmt"
	"strings"

	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
	"github.com/curtiswtaylorjr/tidyarr/internal/servarr"
)

// Vocabulary returns every tag sess's app currently has defined — the
// "existing tags" list a UI would autocomplete against, imported live
// rather than mirrored into Tidyarr's own storage.
func Vocabulary(ctx context.Context, sess *mode.Session) ([]servarr.Tag, error) {
	return sess.Servarr.Tags(ctx)
}

// ensureTag returns the ID of an existing tag matching label
// (case-insensitively), creating it upstream via CreateTag if no match
// exists yet — the "tags imported, not duplicated" principle: a genuinely
// new tag is pushed to the app immediately, never cached Tidyarr-side only.
func ensureTag(ctx context.Context, client *servarr.Client, label string) (int, error) {
	tags, err := client.Tags(ctx)
	if err != nil {
		return 0, fmt.Errorf("loading existing tags: %w", err)
	}
	for _, t := range tags {
		if strings.EqualFold(t.Label, label) {
			return t.ID, nil
		}
	}
	created, err := client.CreateTag(ctx, label)
	if err != nil {
		return 0, fmt.Errorf("creating tag %q: %w", label, err)
	}
	return created.ID, nil
}

// Add assigns label to the tracked item itemID, creating the tag upstream
// first if it doesn't already exist. A no-op (not an error) if the item
// already carries that tag.
func Add(ctx context.Context, sess *mode.Session, itemID int, label string) error {
	client := sess.Servarr
	tagID, err := ensureTag(ctx, client, label)
	if err != nil {
		return err
	}

	item, err := client.GetTracked(ctx, itemID)
	if err != nil {
		return fmt.Errorf("loading item %d: %w", itemID, err)
	}
	for _, id := range item.TagIDs {
		if id == tagID {
			return nil // already tagged
		}
	}
	return client.UpdateItemTags(ctx, itemID, append(item.TagIDs, tagID))
}

// Remove unassigns tagID from the tracked item itemID. A no-op (not an
// error) if the item doesn't carry that tag.
func Remove(ctx context.Context, sess *mode.Session, itemID, tagID int) error {
	client := sess.Servarr
	item, err := client.GetTracked(ctx, itemID)
	if err != nil {
		return fmt.Errorf("loading item %d: %w", itemID, err)
	}

	kept := make([]int, 0, len(item.TagIDs))
	found := false
	for _, id := range item.TagIDs {
		if id == tagID {
			found = true
			continue
		}
		kept = append(kept, id)
	}
	if !found {
		return nil
	}
	return client.UpdateItemTags(ctx, itemID, kept)
}
